package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/admin"
	"github.com/amirashouri/tasktracker-server/internal/auth"
	"github.com/amirashouri/tasktracker-server/internal/dbtest"
	"github.com/amirashouri/tasktracker-server/internal/entities"
	"github.com/amirashouri/tasktracker-server/internal/ratelimit"
	"github.com/amirashouri/tasktracker-server/internal/sync"
)

// The types in this file are the *client's* DTOs, transcribed from
// TaskTrackerKMP's HttpAuthRemote and HttpSyncTransport with the same JSON
// names. Decoding a response into them is the assertion: rename a field on the
// server and the corresponding member here stays zero, which the checks catch.
//
// Hand-transcribing rather than reusing the server's own structs is the whole
// point. A test that decodes with the encoder that produced the bytes cannot
// tell whether the two sides agree, which is exactly how `entityId` vs
// `entityID` and the ISO-8601 timestamps survived a full test suite.

// clientTokenPair mirrors the client's token response. Its `@SerialName`s are
// snake_case, so these are too.
//
// Five fields, and the count is the assertion. This transcription previously
// carried `token_type` and `expires_in` as well, which the client has never
// declared in any commit — so the test required the server to send two keys the
// client rejects, and the pair of them stayed green while every real sign-in
// failed with "The server sent something unexpected." A hand-transcription is
// only a contract if it is transcribed from something; this one was written from
// what the server sent.
type clientTokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	UserID       string `json:"user_id"`
	Email        string `json:"email"`
	DisplayName  string `json:"display_name"`
}

// clientPushBody mirrors PushRequestBody. Note `payload` is a *string*: the
// client's payload is an opaque byte array it encodes with decodeToString, and
// `ignoreUnknownKeys` is off, so an object here fails the whole request.
type clientPushBody struct {
	Changes []clientPushChange `json:"changes"`
}

type clientPushChange struct {
	ID        string `json:"id"`
	Entity    string `json:"entity"`
	EntityID  string `json:"entityId"`
	Operation string `json:"operation"`
	Payload   string `json:"payload"`
}

// clientPushResponse mirrors PushResponseBody.
type clientPushResponse struct {
	Results []clientPushResult `json:"results"`
}

type clientPushResult struct {
	ID      string  `json:"id"`
	Status  string  `json:"status"`
	Version *string `json:"version"`
	Message *string `json:"message"`
}

// clientChangesPage mirrors ChangesResponseBody. `HasMore` has no default on
// the client — a server that omits it has not said whether the log continues,
// and guessing `false` would silently end a pull with pages still unread.
type clientChangesPage struct {
	Changes        []clientChange `json:"changes"`
	NextCursor     *string        `json:"nextCursor"`
	HasMore        bool           `json:"hasMore"`
	ResyncRequired bool           `json:"resyncRequired"`
}

type clientChange struct {
	Entity    string  `json:"entity"`
	EntityID  string  `json:"entityId"`
	Operation string  `json:"operation"`
	Payload   string  `json:"payload"`
	Version   *string `json:"version"`
}

// clientUser mirrors the user object the client's TeamPort reads.
type clientUser struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Position    string `json:"position"`
	AvatarID    string `json:"avatar_id"`
	Status      string `json:"status"`
	Role        string `json:"role"`
	CreatedAt   int64  `json:"created_at"`
}

type contractEnv struct {
	mux     *http.ServeMux
	project *entities.Project
	user    *entities.User
}

func newContractEnv(t *testing.T) *contractEnv {
	t.Helper()

	database := dbtest.NewDatabase(t)
	users := entities.NewUserRepository(database.DB)
	jwtManager := auth.NewJWTManager(strings.Repeat("s", 32), time.Hour, 720*time.Hour)

	user := &entities.User{
		Email:        "ada@example.com",
		PasswordHash: "correct horse battery",
		DisplayName:  "Ada",
		Role:         entities.RoleAdmin,
	}
	if err := users.Create(context.Background(), user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	project := &entities.Project{
		KeyPrefix:      "TT",
		Name:           "Demo",
		MemberIDs:      []entities.UserID{user.ID},
		NextItemNumber: 1,
	}
	if err := entities.NewProjectRepository(database.DB).Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	return &contractEnv{
		mux: mount(
			auth.NewHandler(jwtManager, users, auth.NewTokenStore(database.DB), ratelimit.New(100, time.Minute)),
			admin.NewHandler(users),
			sync.NewHandler(sync.NewEngine(database.DB), jwtManager),
			jwtManager,
			database.DB,
		),
		project: project,
		user:    user,
	}
}

// do sends a request the way the client does and returns the recorder.
func (e *contractEnv) do(t *testing.T, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode body: %v", err)
		}
		payload = encoded
	}

	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

func (e *contractEnv) signIn(t *testing.T) clientTokenPair {
	t.Helper()

	rec := e.do(t, http.MethodPost, "/v1/auth/login", "", map[string]string{
		"email":    "ada@example.com",
		"password": "correct horse battery",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d, body = %s", rec.Code, rec.Body.String())
	}

	var tokens clientTokenPair
	if err := json.Unmarshal(rec.Body.Bytes(), &tokens); err != nil {
		t.Fatalf("decode the client's token pair from %s: %v", rec.Body.String(), err)
	}

	// Every field the client declares is populated on a successful sign-in, so all
	// five are required — and the key set is closed, because the client's DTO is.
	assertWireKeys(t, "the token pair", rec.Body.Bytes(), []string{
		"access_token", "refresh_token", "user_id", "email", "display_name",
	}, nil)

	return tokens
}

// decodeInto unmarshals a response into one of the client's own types, which is
// what the client does and therefore the only decode that proves anything.
func decodeInto[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()

	var value T
	if err := json.Unmarshal(rec.Body.Bytes(), &value); err != nil {
		t.Fatalf("the client cannot decode %s: %v", rec.Body.String(), err)
	}
	return value
}

// assertWireKeys pins the key names a response actually carries.
//
// Decoding into the client's struct cannot do this. Go matches JSON keys
// case-insensitively, so a server that renamed `entityId` to `entityID` would
// still populate clientChange.EntityID and every decode-based check would pass —
// which is exactly how that mismatch survived a full test suite and shipped.
// The client's decoder is not case-insensitive, so the key set is the contract
// and it has to be read off the bytes.
//
// The unknown-key half is not pedantry either. The client leaves
// `ignoreUnknownKeys` off on purpose, so a key its DTO does not declare is a
// decoding failure rather than a field it skips: a field added to a response
// breaks every installed build. required lists what must be there, optional
// what may be, and anything outside both is an error.
func assertWireKeys(t *testing.T, what string, raw []byte, required, optional []string) {
	t.Helper()

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode %s: %v", what, err)
	}

	for _, key := range required {
		if _, ok := fields[key]; !ok {
			t.Errorf("%s carries no %q; it has %s", what, key, strings.Join(keysOf(fields), ", "))
		}
	}

	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, key := range append(append([]string{}, required...), optional...) {
		allowed[key] = struct{}{}
	}
	for key := range fields {
		if _, ok := allowed[key]; !ok {
			t.Errorf("%s carries %q, which the client's DTO does not declare — with "+
				"ignoreUnknownKeys off that is a decoding failure, not a field it skips",
				what, key)
		}
	}
}

func keysOf(fields map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestTheLoginResponseIsWhatTheClientDecodes(t *testing.T) {
	env := newContractEnv(t)
	tokens := env.signIn(t)

	if tokens.AccessToken == "" || tokens.RefreshToken == "" {
		t.Errorf("token pair = %+v, want both tokens populated", tokens)
	}
	if tokens.UserID != string(env.user.ID) {
		t.Errorf("user_id = %q, want %q", tokens.UserID, env.user.ID)
	}
	if tokens.Email != "ada@example.com" {
		t.Errorf("email = %q, want %q", tokens.Email, "ada@example.com")
	}
	if tokens.DisplayName != "Ada" {
		t.Errorf("display_name = %q, want %q", tokens.DisplayName, "Ada")
	}
}

func TestTheRefreshResponseIsWhatTheClientDecodes(t *testing.T) {
	env := newContractEnv(t)
	tokens := env.signIn(t)

	rec := env.do(t, http.MethodPost, "/v1/auth/refresh", "", map[string]string{
		"refresh_token": tokens.RefreshToken,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh = %d, body = %s", rec.Code, rec.Body.String())
	}

	rotated := decodeInto[clientTokenPair](t, rec)

	// The key set is pinned here too, and it is the assertion this test was missing. It
	// decoded into the transcription without checking keys, so while that transcription
	// carried two members the client does not declare, the refresh path could send them
	// and stay green — the same blind spot as the login pair, on the other call that
	// returns one.
	assertWireKeys(t, "the rotated token pair", rec.Body.Bytes(), []string{
		"access_token", "refresh_token", "user_id", "email", "display_name",
	}, nil)

	if rotated.RefreshToken == "" || rotated.RefreshToken == tokens.RefreshToken {
		t.Errorf("refresh_token = %q, want a new one", rotated.RefreshToken)
	}
	if rotated.AccessToken == "" {
		t.Error("the rotated pair carries no access_token")
	}
}

// A push built the way the client builds it: payload as a JSON string, entity
// ids in the server's spelling, timestamps in ISO-8601. The server used to
// reject this at the payload decode and answer `rejected`, which the client
// dead-letters — so a change that could never succeed was silently dropped.
func TestThePushRequestIsWhatTheClientSends(t *testing.T) {
	env := newContractEnv(t)
	tokens := env.signIn(t)

	const itemID = "44444444-4444-4444-4444-444444444444"
	workItem := `{
		"id": "` + itemID + `",
		"projectId": "` + string(env.project.ID) + `",
		"key": "TT-1",
		"title": "Fix the thing",
		"details": "",
		"status": "todo",
		"priority": "medium",
		"blockedByIds": [],
		"rank": 0,
		"createdAt": "2026-09-26T20:13:32Z",
		"updatedAt": "2026-09-26T20:13:32Z",
		"version": "v-1"
	}`

	rec := env.do(t, http.MethodPost, "/v1/sync/push", tokens.AccessToken, clientPushBody{
		Changes: []clientPushChange{{
			ID:        "entry-1",
			Entity:    "workItem",
			EntityID:  itemID,
			Operation: "upsert",
			Payload:   workItem,
		}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("push = %d, body = %s", rec.Code, rec.Body.String())
	}

	response := decodeInto[clientPushResponse](t, rec)
	if len(response.Results) != 1 {
		t.Fatalf("results = %+v, want one verdict", response.Results)
	}

	assertWireKeys(t, "the push response", rec.Body.Bytes(), []string{"results"}, nil)

	var envelope struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode results: %v", err)
	}
	assertWireKeys(t, "a push verdict", envelope.Results[0],
		[]string{"id", "status", "version"}, []string{"message"})

	result := response.Results[0]
	if result.ID != "entry-1" {
		t.Errorf("results[0].id = %q, want %q — the client matches verdicts to outbox rows by this",
			result.ID, "entry-1")
	}
	if result.Status != "accepted" {
		t.Fatalf("results[0].status = %q, want %q (message: %v)",
			result.Status, "accepted", result.Message)
	}
	if result.Version == nil || *result.Version == "" {
		t.Error("an accepted verdict carries no version, so the client cannot record what the server holds")
	}
}

// The pull the client makes, decoded into the client's own page type. The
// payload must come back as a *string* — an object fails this decode, which is
// the failure mode that made every pulled page unreadable.
func TestThePullResponseIsWhatTheClientDecodes(t *testing.T) {
	env := newContractEnv(t)
	tokens := env.signIn(t)

	const itemID = "55555555-5555-5555-5555-555555555555"
	workItem := `{"id":"` + itemID + `","projectId":"` + string(env.project.ID) + `",` +
		`"key":"TT-1","title":"Fix the thing","details":"","status":"todo","priority":"medium",` +
		`"blockedByIds":[],"rank":0,"createdAt":"2026-09-26T20:13:32Z",` +
		`"updatedAt":"2026-09-26T20:13:32Z","version":"v-1"}`

	pushed := env.do(t, http.MethodPost, "/v1/sync/push", tokens.AccessToken, clientPushBody{
		Changes: []clientPushChange{{
			ID: "entry-1", Entity: "workItem", EntityID: itemID, Operation: "upsert", Payload: workItem,
		}},
	})
	if pushed.Code != http.StatusOK {
		t.Fatalf("push = %d, body = %s", pushed.Code, pushed.Body.String())
	}

	// The plural scope, which is what the client asks for.
	rec := env.do(t, http.MethodGet, "/v1/sync/changes/workItems?limit=50", tokens.AccessToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("pull = %d, body = %s", rec.Code, rec.Body.String())
	}

	page := decodeInto[clientChangesPage](t, rec)
	if len(page.Changes) == 0 {
		t.Fatalf("the pushed change did not come back: %s", rec.Body.String())
	}

	assertWireKeys(t, "the pull page", rec.Body.Bytes(),
		[]string{"changes", "nextCursor", "hasMore"}, []string{"resyncRequired"})

	// The keys of the change inside the page. This is the assertion that catches
	// a case-only rename like `entityId` → `entityID`, which the decode above
	// cannot: Go matches JSON keys case-insensitively.
	var envelope struct {
		Changes []json.RawMessage `json:"changes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode changes: %v", err)
	}
	assertWireKeys(t, "a change", envelope.Changes[0],
		[]string{"entity", "entityId", "operation", "payload", "version"}, nil)

	change := page.Changes[0]
	if change.Entity != "workItem" {
		t.Errorf("changes[0].entity = %q, want %q", change.Entity, "workItem")
	}
	if change.EntityID != itemID {
		t.Errorf("changes[0].entityId = %q, want %q", change.EntityID, itemID)
	}
	if change.Operation != "upsert" {
		t.Errorf("changes[0].operation = %q, want %q", change.Operation, "upsert")
	}
	if !strings.Contains(change.Payload, `"title":"Fix the thing"`) {
		t.Errorf("changes[0].payload = %q, want the document the client sent", change.Payload)
	}
	if !strings.Contains(change.Payload, `"createdAt":"2026-09-26T20:13:32Z"`) {
		t.Errorf("changes[0].payload = %q, want the client's ISO-8601 timestamp echoed back", change.Payload)
	}
	if page.NextCursor == nil || *page.NextCursor == "" {
		t.Error("nextCursor is absent, so the client cannot resume the scope")
	}
}

// `hasMore` has no default on the client, so its presence is part of the
// contract rather than a detail of the value.
func TestThePullPageAlwaysStatesHasMore(t *testing.T) {
	env := newContractEnv(t)
	tokens := env.signIn(t)

	rec := env.do(t, http.MethodGet, "/v1/sync/changes/projects", tokens.AccessToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("pull = %d, body = %s", rec.Code, rec.Body.String())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if _, ok := raw["hasMore"]; !ok {
		t.Errorf("the page omits hasMore: %s", rec.Body.String())
	}

	// nextCursor is omitted on an empty page, and resyncRequired when false.
	assertWireKeys(t, "an empty pull page", rec.Body.Bytes(),
		[]string{"changes", "hasMore"}, []string{"nextCursor", "resyncRequired"})

	// resyncRequired is the one field that may be absent, and absent means false.
	page := decodeInto[clientChangesPage](t, rec)
	if page.ResyncRequired {
		t.Errorf("a first pull from no cursor asked for a resync: %s", rec.Body.String())
	}
}

// The client's TeamPort reads this shape from the directory search.
func TestTheUserObjectIsWhatTheClientDecodes(t *testing.T) {
	env := newContractEnv(t)
	tokens := env.signIn(t)

	rec := env.do(t, http.MethodGet, "/v1/users/search?q=ada", tokens.AccessToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d, body = %s", rec.Code, rec.Body.String())
	}

	users := decodeInto[[]clientUser](t, rec)
	if len(users) != 1 {
		t.Fatalf("search returned %d users, want 1: %s", len(users), rec.Body.String())
	}

	// position, avatar_id and status are omitempty and empty on a fresh user, so
	// they are permitted rather than required.
	var rawUsers []json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &rawUsers); err != nil {
		t.Fatalf("decode users: %v", err)
	}
	assertWireKeys(t, "the user object", rawUsers[0],
		[]string{"id", "email", "display_name", "role", "created_at"},
		[]string{"position", "avatar_id", "status", "last_seen"})

	user := users[0]
	if user.ID != string(env.user.ID) || user.Email != "ada@example.com" {
		t.Errorf("user = %+v, want ada with id %s", user, env.user.ID)
	}
	if user.Role != entities.RoleAdmin {
		t.Errorf("role = %q, want %q", user.Role, entities.RoleAdmin)
	}
	if user.CreatedAt <= 0 {
		t.Errorf("created_at = %d, want the epoch-millis the admin API uses", user.CreatedAt)
	}
}
