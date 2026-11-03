package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// modelRequestTimeout bounds one model call. It is generous because a local model
// on a laptop can take minutes for a first token, and the worker's heartbeat keeps
// the run's lease alive while it waits.
const modelRequestTimeout = 5 * time.Minute

// maxModelResponse caps what is read from a model endpoint. A misconfigured base
// URL can answer with a whole page; the decoder needs a limit, not a guess.
const maxModelResponse = 4 << 20

// postModel sends one JSON request and decodes the reply into out. It reports the
// endpoint's status and a readable slice of its body, because that is the only
// diagnostic a user has when a local server is the wrong port.
func postModel(ctx context.Context, endpoint string, headers map[string]string, payload any, out any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	client := &http.Client{Timeout: modelRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxModelResponse))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("model endpoint answered %d: %s", resp.StatusCode, bodySnippet(data))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode model response: %w", err)
	}
	return nil
}

// bodySnippet collapses whitespace: a gateway's HTML error page otherwise fills
// the event log with markup.
func bodySnippet(data []byte) string {
	return truncate(strings.Join(strings.Fields(string(data)), " "), 300)
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
