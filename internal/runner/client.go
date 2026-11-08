package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// StatusError is a non-2xx answer from the server, carrying the status so a
// caller can tell "this token is no longer accepted" from "the server is down".
type StatusError struct {
	StatusCode int
	Message    string
}

func (e *StatusError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("server answered %d", e.StatusCode)
	}
	return e.Message
}

// IsUnauthorized reports whether err means the credential is gone: a token the
// server does not know, or one it has revoked.
func IsUnauthorized(err error) bool {
	var status *StatusError
	return errors.As(err, &status) && (status.StatusCode == http.StatusUnauthorized || status.StatusCode == http.StatusNotFound)
}

// Client talks to the runner routes with an opaque machine token.
//
// The timeout outlasts the longest claim wait (30s), because a long poll that the
// client aborts is a wasted round trip and a confusing 204.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewClient(serverURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(serverURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 45 * time.Second},
	}
}

// Run is the part of a claimed run the worker uses. The task document is opaque.
type Run struct {
	ID           string `json:"id"`
	WorkItemID   string `json:"workItemId"`
	ProjectID    string `json:"projectId"`
	Status       string `json:"status"`
	TaskDocument string `json:"taskDocument"`
}

// Event is one progress line. Payload is an opaque JSON string.
type Event struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Payload string `json:"payload"`
}

// Registration is what pairing a machine returns: the server shows the token
// once, and the CLI writes it to agent.json immediately.
type Registration struct {
	RunnerID   string `json:"-"`
	Name       string `json:"-"`
	Provider   string `json:"-"`
	Token      string `json:"-"`
	ModelLabel string `json:"-"`
}

// Claim leases the next queued run, waiting up to waitSeconds. A nil run means
// the server answered 204: nothing is queued.
func (c *Client) Claim(ctx context.Context, waitSeconds int) (*Run, error) {
	data, err := c.post(ctx, "/v1/agent/runs/claim", map[string]int{"waitSeconds": waitSeconds})
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var body struct {
		Run Run `json:"run"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, fmt.Errorf("decode claim: %w", err)
	}
	if body.Run.ID == "" {
		return nil, errors.New("claim returned no run id")
	}
	return &body.Run, nil
}

// PostEvents appends a batch and returns the seq of the last event.
func (c *Client) PostEvents(ctx context.Context, runID string, events []Event) (int64, error) {
	data, err := c.post(ctx, "/v1/agent/runs/"+url.PathEscape(runID)+"/events", map[string]any{"events": events})
	if err != nil {
		return 0, err
	}
	var body struct {
		NextSeq int64 `json:"nextSeq"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return 0, fmt.Errorf("decode events: %w", err)
	}
	return body.NextSeq, nil
}

// Heartbeat renews the lease and reports whether the user asked to cancel.
func (c *Client) Heartbeat(ctx context.Context, runID string) (bool, error) {
	data, err := c.post(ctx, "/v1/agent/runs/"+url.PathEscape(runID)+"/heartbeat", nil)
	if err != nil {
		return false, err
	}
	var body struct {
		CancelRequested bool `json:"cancelRequested"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return false, fmt.Errorf("decode heartbeat: %w", err)
	}
	return body.CancelRequested, nil
}

// Finish records the terminal outcome. It is idempotent server-side, so a retry
// after a lost response succeeds.
func (c *Client) Finish(ctx context.Context, runID, outcome, summary, detail, diff string) error {
	_, err := c.post(ctx, "/v1/agent/runs/"+url.PathEscape(runID)+"/finish", map[string]string{
		"outcome": outcome,
		"summary": summary,
		"error":   detail,
		"diff":    diff,
	})
	return err
}

// RevokeSelf retires this machine with its own token. The app route revokes by id
// with a user JWT; this is the one the CLI can call, because the CLI stores a
// runner token rather than a password. A machine can only revoke itself.
func (c *Client) RevokeSelf(ctx context.Context) error {
	_, err := c.post(ctx, "/v1/agent/runners/self/revoke", nil)
	return err
}

// Probe checks that a server is reachable and serving. It is what `tt agent
// status` reports, so it never needs a credential.
func Probe(ctx context.Context, serverURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(serverURL, "/")+"/healthz", nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check answered %d", resp.StatusCode)
	}
	return nil
}

// Login exchanges a member's credentials for an access token. The password is
// used once and never written anywhere; only the issued runner token is stored.
func Login(ctx context.Context, serverURL, email, password string) (string, error) {
	data, err := postJSON(ctx, strings.TrimRight(serverURL, "/")+"/v1/auth/login", "", map[string]string{
		"email":    email,
		"password": password,
	})
	if err != nil {
		return "", err
	}
	var body struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return "", fmt.Errorf("decode login: %w", err)
	}
	if body.AccessToken == "" {
		return "", errors.New("login returned no access token")
	}
	return body.AccessToken, nil
}

// Register pairs a machine and returns the runner token exactly once.
func Register(ctx context.Context, serverURL, accessToken, name, provider, modelLabel string) (Registration, error) {
	data, err := postJSON(ctx, strings.TrimRight(serverURL, "/")+"/v1/agent/runners", accessToken, map[string]string{
		"name":       name,
		"provider":   provider,
		"modelLabel": modelLabel,
	})
	if err != nil {
		return Registration{}, err
	}
	var body struct {
		Runner struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Provider   string `json:"provider"`
			ModelLabel string `json:"modelLabel"`
		} `json:"runner"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return Registration{}, fmt.Errorf("decode registration: %w", err)
	}
	if body.Runner.ID == "" || body.Token == "" {
		return Registration{}, errors.New("registration returned no runner id or token")
	}
	return Registration{
		RunnerID:   body.Runner.ID,
		Name:       body.Runner.Name,
		Provider:   body.Runner.Provider,
		ModelLabel: body.Runner.ModelLabel,
		Token:      body.Token,
	}, nil
}

func (c *Client) post(ctx context.Context, path string, payload any) ([]byte, error) {
	return postJSON(ctx, c.baseURL+path, c.token, payload)
}

func postJSON(ctx context.Context, endpoint, token string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &StatusError{StatusCode: resp.StatusCode, Message: errorMessage(data, resp.StatusCode)}
	}
	return data, nil
}

// errorMessage prefers the server's own sentence, which is the only one written
// with the failing rule in mind.
func errorMessage(data []byte, status int) string {
	var envelope struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err == nil && envelope.Error != "" {
		return envelope.Error
	}
	return fmt.Sprintf("server answered %d", status)
}
