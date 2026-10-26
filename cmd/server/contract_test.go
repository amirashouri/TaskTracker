package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/amirashouri/tasktracker-server/internal/me"
	"github.com/amirashouri/tasktracker-server/internal/profile"
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

// clientProfile mirrors the KMP `UserProfileResponse`, transcribed from
// specs/settings-user-backend-profile.md. Every field after the id has a Kotlin
// default, which is why the response may omit the optional ones; the key set is
// still closed, because `ignoreUnknownKeys` is off.
type clientProfile struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	DisplayName  string `json:"display_name"`
	Position     string `json:"position"`
	AvatarID     string `json:"avatar_id"`
	Presence     string `json:"presence"`
	PresenceNote string `json:"presence_note"`
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
	tokens := auth.NewTokenStore(database.DB)
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
			auth.NewHandler(jwtManager, users, tokens, ratelimit.New(100, time.Minute), nil),
			admin.NewHandler(users, tokens),
			profile.NewHandler(users),
			me.NewHandler(users),
			admin.NewProjectHandler(entities.NewProjectRepository(database.DB), sync.NewEngine(database.DB, "")),
			sync.NewHandler(sync.NewEngine(database.DB, ""), jwtManager),
			jwtManager,
			database.DB,
			sync.NewEngine(database.DB, ""),
			"",
			int64(25*1024*1024),
			30*time.Second,
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

// An update echoes the project back to the server, body including the `version`
// the client read from a GET. That version travels on the envelope
// (RemoteChange.Version), not inside the project payload: the client's Project
// DTO does not declare it, so including it breaks decoding on every pull.
func TestTheProjectUpdateOmitsVersionFromTheFeedPayload(t *testing.T) {
	env := newContractEnv(t)
	tokens := env.signIn(t)

	// Fetch the stored project so the body carries the server's version.
	getRec := env.do(t, http.MethodGet, "/v1/admin/projects/"+string(env.project.ID), tokens.AccessToken, nil)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get project = %d, body = %s", getRec.Code, getRec.Body.String())
	}
	var stored entities.Project
	if err := json.Unmarshal(getRec.Body.Bytes(), &stored); err != nil {
		t.Fatalf("decode stored project: %v", err)
	}

	// Echo it back with a change.
	stored.Name = "Demo renamed"
	rec := env.do(t, http.MethodPatch, "/v1/admin/projects/"+string(env.project.ID), tokens.AccessToken, stored)
	if rec.Code != http.StatusOK {
		t.Fatalf("update project = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Pull the project through the sync feed.
	pullRec := env.do(t, http.MethodGet, "/v1/sync/changes/projects?limit=50", tokens.AccessToken, nil)
	if pullRec.Code != http.StatusOK {
		t.Fatalf("pull projects = %d, body = %s", pullRec.Code, pullRec.Body.String())
	}

	page := decodeInto[clientChangesPage](t, pullRec)
	if len(page.Changes) == 0 {
		t.Fatalf("no project changes in the feed: %s", pullRec.Body.String())
	}

	// The payload is a JSON string (the client's encoding). Decode it as a
	// string, then as a map, and assert `version` is not a top-level key.
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(page.Changes[0].Payload), &payload); err != nil {
		t.Fatalf("decode project payload: %v", err)
	}
	if _, ok := payload["version"]; ok {
		t.Errorf("project payload carries a top-level `version` key; the client's Project DTO does not declare it, so it is a decoding failure on every pull: %s", page.Changes[0].Payload)
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

	// position and avatar_id are omitempty and empty on a fresh user, so they are
	// permitted rather than required. status is always present now that Create
	// defaults it to active, but the client declares it with a default, so the
	// contract still only promises it may be there.
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

// The self-profile routes are the client's Settings identity block. They must
// decode into the client's DTO, carry no key it does not declare, and leave the
// admin/directory user object unchanged — that shape is already installed on
// devices that reject an unknown key.
func TestTheSelfProfileIsWhatTheClientDecodes(t *testing.T) {
	env := newContractEnv(t)
	tokens := env.signIn(t)

	rec := env.do(t, http.MethodGet, "/v1/me", tokens.AccessToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/me = %d, body %s", rec.Code, rec.Body.String())
	}

	profile := decodeInto[clientProfile](t, rec)
	assertWireKeys(t, "the self profile", rec.Body.Bytes(),
		[]string{"id", "email", "presence"},
		[]string{"display_name", "position", "avatar_id", "presence_note"})

	if profile.ID != string(env.user.ID) || profile.Email != "ada@example.com" {
		t.Errorf("profile = %+v, want the signed-in account", profile)
	}
	if profile.Presence != entities.PresenceActive {
		t.Errorf("presence = %q, want the column default %q", profile.Presence, entities.PresenceActive)
	}

	rec = env.do(t, http.MethodPatch, "/v1/me", tokens.AccessToken, map[string]string{
		"display_name":  "Ada Lovelace",
		"position":      "Product owner",
		"avatar_id":     "7",
		"presence":      "away",
		"presence_note": "At the dentist",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH /v1/me = %d, body %s", rec.Code, rec.Body.String())
	}

	updated := decodeInto[clientProfile](t, rec)
	assertWireKeys(t, "the updated self profile", rec.Body.Bytes(),
		[]string{"id", "email", "presence"},
		[]string{"display_name", "position", "avatar_id", "presence_note"})

	want := clientProfile{
		ID:           string(env.user.ID),
		Email:        "ada@example.com",
		DisplayName:  "Ada Lovelace",
		Position:     "Product owner",
		AvatarID:     "7",
		Presence:     entities.PresenceAway,
		PresenceNote: "At the dentist",
	}
	if updated != want {
		t.Errorf("updated profile = %+v, want %+v", updated, want)
	}

	// The admin/directory object is a different, already-installed contract. A
	// presence key leaking into it would fail every client that decodes it.
	rec = env.do(t, http.MethodGet, "/v1/users/search?q=ada", tokens.AccessToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d, body %s", rec.Code, rec.Body.String())
	}
	var rawUsers []json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &rawUsers); err != nil {
		t.Fatalf("decode users: %v", err)
	}
	assertWireKeys(t, "the user object", rawUsers[0],
		[]string{"id", "email", "display_name", "role", "created_at"},
		[]string{"position", "avatar_id", "status", "last_seen"})
}

// Deleting a member is a soft delete: the row stays, but the account can no
// longer sign in and the sessions it already had stop working. That is the
// whole point of the story, so it is checked end to end, through the same
// routes the client calls.
func TestDeletingAMemberEndsTheirSessionsAndBlocksSignIn(t *testing.T) {
	env := newContractEnv(t)
	admin := env.signIn(t)

	created := env.do(t, http.MethodPost, "/v1/admin/users", admin.AccessToken, map[string]string{
		"email":        "grace@example.com",
		"password":     "grace-password",
		"display_name": "Grace",
		"role":         entities.RoleMember,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create member = %d, body = %s", created.Code, created.Body.String())
	}
	grace := decodeInto[clientUser](t, created)

	// Sign the member in, so there is a live session for the delete to end.
	signIn := env.do(t, http.MethodPost, "/v1/auth/login", "", map[string]string{
		"email": "grace@example.com", "password": "grace-password",
	})
	if signIn.Code != http.StatusOK {
		t.Fatalf("member sign-in = %d, body = %s", signIn.Code, signIn.Body.String())
	}
	graceTokens := decodeInto[clientTokenPair](t, signIn)

	deleted := env.do(t, http.MethodDelete, "/v1/admin/users/"+grace.ID, admin.AccessToken, nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete member = %d, body = %s", deleted.Code, deleted.Body.String())
	}
	if body := deleted.Body.String(); body != "" {
		t.Errorf("delete returned body %q, want none on a 204", body)
	}

	// The session that was open when the row changed must not rotate.
	rotated := env.do(t, http.MethodPost, "/v1/auth/refresh", "", map[string]string{
		"refresh_token": graceTokens.RefreshToken,
	})
	if rotated.Code != http.StatusUnauthorized {
		t.Errorf("a deleted member's refresh = %d, want 401", rotated.Code)
	}

	// And the account must not be able to start a new session.
	again := env.do(t, http.MethodPost, "/v1/auth/login", "", map[string]string{
		"email": "grace@example.com", "password": "grace-password",
	})
	if again.Code != http.StatusUnauthorized {
		t.Errorf("sign-in after deletion = %d, want 401", again.Code)
	}

	// The row is still there, but it left the live list.
	list := decodeInto[[]clientUser](t, env.do(t, http.MethodGet, "/v1/admin/users", admin.AccessToken, nil))
	for _, u := range list {
		if u.ID == grace.ID {
			t.Errorf("a deleted member (%s) is still in the live list", grace.Email)
		}
	}
}

// Deleting the only admin would leave nobody able to manage users, so it is
// refused. The caller's own role cannot decide this: the claim can outlive the
// role it names, which is exactly the case constructed here.
func TestDeletingTheLastAdminIsRefused(t *testing.T) {
	env := newContractEnv(t)
	admin := env.signIn(t)

	// Demote the caller's row while the access token still claims admin, then
	// promote a second account. The database now holds one admin, and the caller
	// is not it.
	demoted := env.do(t, http.MethodPatch, "/v1/admin/users/"+string(env.user.ID), admin.AccessToken,
		map[string]string{"role": entities.RoleMember})
	if demoted.Code != http.StatusOK {
		t.Fatalf("demote caller = %d, body = %s", demoted.Code, demoted.Body.String())
	}
	created := env.do(t, http.MethodPost, "/v1/admin/users", admin.AccessToken, map[string]string{
		"email": "root@example.com", "password": "root-password", "role": entities.RoleAdmin,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create the remaining admin = %d, body = %s", created.Code, created.Body.String())
	}
	root := decodeInto[clientUser](t, created)

	refused := env.do(t, http.MethodDelete, "/v1/admin/users/"+root.ID, admin.AccessToken, nil)
	if refused.Code != http.StatusConflict {
		t.Errorf("deleting the last admin = %d, want 409 (body %s)", refused.Code, refused.Body.String())
	}
}

// The admin cannot delete their own account out from under their session.
func TestDeletingYourselfIsRefused(t *testing.T) {
	env := newContractEnv(t)
	admin := env.signIn(t)

	refused := env.do(t, http.MethodDelete, "/v1/admin/users/"+string(env.user.ID), admin.AccessToken, nil)
	if refused.Code != http.StatusBadRequest {
		t.Errorf("deleting yourself = %d, want 400 (body %s)", refused.Code, refused.Body.String())
	}
}

// The members screen has two buckets behind a segmented control, and the live
// one is the default. A deleted member must appear in exactly one of them.
func TestMembersListSplitsActiveFromDeleted(t *testing.T) {
	env := newContractEnv(t)
	admin := env.signIn(t)

	created := env.do(t, http.MethodPost, "/v1/admin/users", admin.AccessToken, map[string]string{
		"email": "grace@example.com", "password": "grace-password",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create member = %d, body = %s", created.Code, created.Body.String())
	}
	grace := decodeInto[clientUser](t, created)

	if rec := env.do(t, http.MethodDelete, "/v1/admin/users/"+grace.ID, admin.AccessToken, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete member = %d, body = %s", rec.Code, rec.Body.String())
	}

	// No parameter means the live bucket, and grace is not in it.
	defaultList := decodeInto[[]clientUser](t, env.do(t, http.MethodGet, "/v1/admin/users", admin.AccessToken, nil))
	for _, u := range defaultList {
		if u.ID == grace.ID {
			t.Errorf("the default list contains the deleted member %s", u.Email)
		}
	}

	// ?status=deleted is the other bucket, and grace is in it.
	deletedList := decodeInto[[]clientUser](t, env.do(t, http.MethodGet, "/v1/admin/users?status=deleted", admin.AccessToken, nil))
	found := false
	for _, u := range deletedList {
		if u.ID == grace.ID {
			found = true
			if u.Status != entities.StatusDeleted {
				t.Errorf("deleted bucket row status = %q, want %q", u.Status, entities.StatusDeleted)
			}
		}
	}
	if !found {
		t.Errorf("the deleted bucket does not contain %s (got %+v)", grace.Email, deletedList)
	}

	// A bucket name the server does not know is a client bug, not an empty list.
	bad := env.do(t, http.MethodGet, "/v1/admin/users?status=archived", admin.AccessToken, nil)
	if bad.Code != http.StatusBadRequest {
		t.Errorf("listing an unknown bucket = %d, want 400 (body %s)", bad.Code, bad.Body.String())
	}
}

// Reactivating a deleted member is a status write, and it has to actually
// restore access -- sign-in was refused by the deleted status.
func TestReactivatingADeletedMemberLetsThemSignInAgain(t *testing.T) {
	env := newContractEnv(t)
	admin := env.signIn(t)

	created := env.do(t, http.MethodPost, "/v1/admin/users", admin.AccessToken, map[string]string{
		"email": "grace@example.com", "password": "grace-password",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create member = %d, body = %s", created.Code, created.Body.String())
	}
	grace := decodeInto[clientUser](t, created)

	if rec := env.do(t, http.MethodDelete, "/v1/admin/users/"+grace.ID, admin.AccessToken, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete member = %d, body = %s", rec.Code, rec.Body.String())
	}
	blocked := env.do(t, http.MethodPost, "/v1/auth/login", "", map[string]string{
		"email": "grace@example.com", "password": "grace-password",
	})
	if blocked.Code != http.StatusUnauthorized {
		t.Fatalf("sign-in while deleted = %d, want 401", blocked.Code)
	}

	reactivated := env.do(t, http.MethodPatch, "/v1/admin/users/"+grace.ID, admin.AccessToken,
		map[string]string{"status": entities.StatusActive})
	if reactivated.Code != http.StatusOK {
		t.Fatalf("reactivate = %d, body = %s", reactivated.Code, reactivated.Body.String())
	}

	allowed := env.do(t, http.MethodPost, "/v1/auth/login", "", map[string]string{
		"email": "grace@example.com", "password": "grace-password",
	})
	if allowed.Code != http.StatusOK {
		t.Errorf("sign-in after reactivation = %d, want 200 (body %s)", allowed.Code, allowed.Body.String())
	}
}

// Deleting through a status write would skip the session revocation DELETE
// performs, so it is refused and the member is told to use DELETE.
func TestStatusCannotBeSetToDeletedThroughUpdate(t *testing.T) {
	env := newContractEnv(t)
	admin := env.signIn(t)

	created := env.do(t, http.MethodPost, "/v1/admin/users", admin.AccessToken, map[string]string{
		"email": "grace@example.com", "password": "grace-password",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create member = %d, body = %s", created.Code, created.Body.String())
	}
	grace := decodeInto[clientUser](t, created)

	refused := env.do(t, http.MethodPatch, "/v1/admin/users/"+grace.ID, admin.AccessToken,
		map[string]string{"status": entities.StatusDeleted})
	if refused.Code != http.StatusBadRequest {
		t.Errorf("PATCH status=deleted = %d, want 400 (body %s)", refused.Code, refused.Body.String())
	}

	// And it must not have taken effect on the way to the error.
	list := decodeInto[[]clientUser](t, env.do(t, http.MethodGet, "/v1/admin/users", admin.AccessToken, nil))
	for _, u := range list {
		if u.ID == grace.ID && u.Status == entities.StatusDeleted {
			t.Error("the refused write still marked the member deleted")
		}
	}
}

// The admin edit page is seeded with a project and saves the whole thing back.
// Its response has to be the same project shape the client already decodes from
// the change feed, so the page can reuse the client's project model rather than
// a second one that would drift.
func TestAdminProjectEditSavesTheProjectAndReachesTheFeed(t *testing.T) {
	env := newContractEnv(t)
	admin := env.signIn(t)

	// Seed the change feed with an ordinary push, so there is a genuine sync
	// payload and version to work from rather than a shape restated here.
	seed := fmt.Sprintf(
		`{"id":%q,"keyPrefix":"TT","name":"Demo","details":"","tags":[],"memberIds":[%q],"isArchived":false,"nextItemNumber":1,"createdAt":"1970-01-01T00:00:00Z","updatedAt":"1970-01-01T00:00:00Z","version":""}`,
		string(env.project.ID), string(env.user.ID))
	seeded := env.do(t, http.MethodPost, "/v1/sync/push", admin.AccessToken, map[string]any{
		"changes": []map[string]any{{
			"id": "seed", "entity": "project", "entityId": string(env.project.ID),
			"operation": "upsert", "payload": seed,
		}},
	})
	if seeded.Code != http.StatusOK {
		t.Fatalf("seed push = %d, body = %s", seeded.Code, seeded.Body.String())
	}
	if results := decodeInto[clientPushResponse](t, seeded); len(results.Results) != 1 || results.Results[0].Status != "accepted" {
		t.Fatalf("seed push was not accepted: %s", seeded.Body.String())
	}

	pulled := env.do(t, http.MethodGet, "/v1/sync/changes/projects", admin.AccessToken, nil)
	page := decodeInto[clientChangesPage](t, pulled)
	if len(page.Changes) != 1 {
		t.Fatalf("the seeded push is not the only project change: %s", pulled.Body.String())
	}
	var feedPayload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(page.Changes[0].Payload), &feedPayload); err != nil {
		t.Fatalf("decode the feed payload: %v", err)
	}
	// The version lives in the change's own column, not in the payload: the
	// payload carries whatever the client sent, and a client does not mint it.
	if page.Changes[0].Version == nil || *page.Changes[0].Version == "" {
		t.Fatalf("the seeded change carries no version: %s", pulled.Body.String())
	}
	feedVersion := *page.Changes[0].Version

	rec := env.do(t, http.MethodPatch, "/v1/admin/projects/"+string(env.project.ID), admin.AccessToken, map[string]any{
		"id":             string(env.project.ID),
		"keyPrefix":      env.project.KeyPrefix,
		"name":           "Renamed",
		"details":        "Edited from the admin app",
		"memberIds":      []string{string(env.user.ID)},
		"isArchived":     true,
		"nextItemNumber": env.project.NextItemNumber,
		"version":        feedVersion,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("edit project = %d, body = %s", rec.Code, rec.Body.String())
	}

	var saved map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode the saved project: %v", err)
	}
	if got, want := strings.Join(keysOf(saved), ","), strings.Join(keysOf(feedPayload), ","); got != want {
		t.Errorf("the admin project keys are %s, want the sync payload's %s", got, want)
	}

	body := rec.Body.String()
	for _, want := range []string{`"name":"Renamed"`, `"details":"Edited from the admin app"`, `"isArchived":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("the saved project is missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, `"version":"`+feedVersion+`"`) {
		t.Errorf("the version did not move, so the client cannot save against it again: %s", body)
	}

	// The edit is on the feed, which is the only way the project's members learn
	// about it.
	after := decodeInto[clientChangesPage](t, env.do(t, http.MethodGet, "/v1/sync/changes/projects", admin.AccessToken, nil))
	if len(after.Changes) != 2 {
		t.Fatalf("the feed holds %d project changes, want the seed and the admin edit", len(after.Changes))
	}
	if last := after.Changes[len(after.Changes)-1]; !strings.Contains(last.Payload, `"name":"Renamed"`) {
		t.Errorf("the admin edit did not reach the change feed: %s", last.Payload)
	}

	// Saving against a version that has moved on is a conflict, not a silent
	// overwrite of whatever the other writer did.
	stale := env.do(t, http.MethodPatch, "/v1/admin/projects/"+string(env.project.ID), admin.AccessToken, map[string]any{
		"keyPrefix": "TT", "name": "Stale write", "version": feedVersion,
	})
	if stale.Code != http.StatusConflict {
		t.Errorf("editing with a stale version = %d, want 409 (body %s)", stale.Code, stale.Body.String())
	}

	// Editing a project that does not exist is a 404, not a create.
	missing := env.do(t, http.MethodPatch, "/v1/admin/projects/00000000-0000-0000-0000-000000000000", admin.AccessToken, map[string]any{
		"keyPrefix": "TT", "name": "Ghost",
	})
	if missing.Code != http.StatusNotFound {
		t.Errorf("editing an unknown project = %d, want 404 (body %s)", missing.Code, missing.Body.String())
	}
}

// The edit screen is seeded from this route and saved back through the PATCH above,
// so the client decodes both into one DTO. Before T8.9 the route did not exist and
// nothing answered the call the client already made.
func TestAdminProjectGetSeedsTheEditScreen(t *testing.T) {
	env := newContractEnv(t)
	admin := env.signIn(t)

	got := env.do(t, http.MethodGet, "/v1/admin/projects/"+string(env.project.ID), admin.AccessToken, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("get project = %d, body = %s", got.Code, got.Body.String())
	}

	// The keys the client declares — the same set the list and the PATCH answer
	// with. An extra key here is a decoding failure on every installed build, not
	// a field it skips.
	assertWireKeys(t, "the admin project", got.Body.Bytes(),
		[]string{
			"id", "keyPrefix", "name", "details", "tags", "memberIds", "isArchived",
			"nextItemNumber", "createdAt", "updatedAt", "version",
		}, nil)

	var stored map[string]json.RawMessage
	if err := json.Unmarshal(got.Body.Bytes(), &stored); err != nil {
		t.Fatalf("decode the project: %v", err)
	}
	if name := string(stored["name"]); name != `"Demo"` {
		t.Errorf("the project came back named %s, want %q", name, "Demo")
	}
	if id := string(stored["id"]); id != `"`+string(env.project.ID)+`"` {
		t.Errorf("the project came back with id %s, want %q", id, env.project.ID)
	}

	// An id the server does not hold is a 404, which the client turns into `null`
	// and shows as "not on the server". A 200 carrying a zero project would seed
	// the editor with a phantom row instead.
	missing := env.do(t, http.MethodGet, "/v1/admin/projects/00000000-0000-0000-0000-000000000000", admin.AccessToken, nil)
	if missing.Code != http.StatusNotFound {
		t.Errorf("getting an unknown project = %d, want 404 (body %s)", missing.Code, missing.Body.String())
	}
}

// Creating through the admin route is the sync path's project create authorized
// by the admin role rather than by membership, so it has to land the row and the
// change-feed entry exactly as a push would: the members the body names learn
// about the project from the feed and from nowhere else.
func TestAdminProjectCreateLandsTheProjectAndReachesTheFeed(t *testing.T) {
	env := newContractEnv(t)
	admin := env.signIn(t)

	type listedProject struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	// The id is the client's, as it is on the push path: the create screen mints
	// it, so a response lost in flight leaves a row the caller can name.
	const createdID = "22222222-2222-2222-2222-222222222222"
	rec := env.do(t, http.MethodPost, "/v1/admin/projects", admin.AccessToken, map[string]any{
		"id":        createdID,
		"keyPrefix": "NEW",
		"name":      "New work",
		"details":   "Started from the admin app",
		"memberIds": []string{string(env.user.ID)},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project = %d, body = %s", rec.Code, rec.Body.String())
	}

	// The keys the client declares: the create answers with the same project the
	// list, the seed and the PATCH do, because the client decodes all four into
	// one DTO.
	assertWireKeys(t, "the created project", rec.Body.Bytes(),
		[]string{
			"id", "keyPrefix", "name", "details", "tags", "memberIds", "isArchived",
			"nextItemNumber", "createdAt", "updatedAt", "version",
		}, nil)

	var created map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode the created project: %v", err)
	}
	// The server owns what the body does not send: a new project is active, its
	// first key is `-1`, and its version is what the first edit saves against.
	for key, want := range map[string]string{
		"id":             `"` + createdID + `"`,
		"keyPrefix":      `"NEW"`,
		"name":           `"New work"`,
		"details":        `"Started from the admin app"`,
		"memberIds":      `["` + string(env.user.ID) + `"]`,
		"isArchived":     `false`,
		"nextItemNumber": `1`,
	} {
		if got := string(created[key]); got != want {
			t.Errorf("the created project's %s = %s, want %s", key, got, want)
		}
	}
	if version := string(created["version"]); version == `""` || version == "" {
		t.Errorf("the created project carries no version, so the first edit cannot save against it: %s", rec.Body.String())
	}

	active := decodeInto[[]listedProject](t, env.do(t, http.MethodGet, "/v1/admin/projects", admin.AccessToken, nil))
	var found int
	for _, p := range active {
		if p.ID == createdID {
			found++
		}
	}
	if found != 1 {
		t.Errorf("the active list holds the created project %d times, want once: %+v", found, active)
	}

	// The feed is the only way the project's members learn about it.
	page := decodeInto[clientChangesPage](t, env.do(t, http.MethodGet, "/v1/sync/changes/projects", admin.AccessToken, nil))
	if len(page.Changes) != 1 {
		t.Fatalf("the feed holds %d project changes, want the create: %s", len(page.Changes), rec.Body.String())
	}
	if !strings.Contains(page.Changes[0].Payload, `"name":"New work"`) {
		t.Errorf("the created project did not reach the change feed: %s", page.Changes[0].Payload)
	}

	// A form with nothing in it is a client bug the server refuses, rather than a
	// nameless project on the list.
	for _, body := range []map[string]any{
		{"keyPrefix": "NEW", "name": "   "},
		{"keyPrefix": "  ", "name": "New work"},
	} {
		if rec := env.do(t, http.MethodPost, "/v1/admin/projects", admin.AccessToken, body); rec.Code != http.StatusBadRequest {
			t.Errorf("creating with %v = %d, want 400 (body %s)", body, rec.Code, rec.Body.String())
		}
	}

	// The same id twice is a request the client already made, not a replace:
	// overwriting would write this body's nextItemNumber over a row that may have
	// issued keys since, handing one number out to two work items.
	again := env.do(t, http.MethodPost, "/v1/admin/projects", admin.AccessToken, map[string]any{
		"id": createdID, "keyPrefix": "DUP", "name": "Duplicate", "details": "",
	})
	if again.Code != http.StatusConflict {
		t.Errorf("re-creating an id the server holds = %d, want 409 (body %s)", again.Code, again.Body.String())
	}

	// And the refused create left the row alone.
	after := decodeInto[[]listedProject](t, env.do(t, http.MethodGet, "/v1/admin/projects", admin.AccessToken, nil))
	var named int
	for _, p := range after {
		if p.Name == "Duplicate" {
			t.Errorf("the conflict wrote its body anyway: %+v", p)
		}
		if p.ID == createdID {
			named++
		}
	}
	if named != 1 {
		t.Errorf("the active list holds the created project %d times after the conflict, want once", named)
	}
}

// The projects screen has two buckets, and the default has to be the active one:
// an admin opening the screen should be looking at live work, not the archive.
func TestAdminProjectListSplitsActiveFromArchived(t *testing.T) {
	env := newContractEnv(t)
	admin := env.signIn(t)

	type listedProject struct {
		ID         string `json:"id"`
		IsArchived bool   `json:"isArchived"`
	}

	// An archived project, created the way a client would create one.
	const archivedID = "11111111-1111-1111-1111-111111111111"
	payload := fmt.Sprintf(
		`{"id":%q,"keyPrefix":"ARC","name":"Old work","details":"","memberIds":[%q],"isArchived":true,"nextItemNumber":1,"createdAt":"1970-01-01T00:00:00Z","updatedAt":"1970-01-01T00:00:00Z","version":""}`,
		archivedID, string(env.user.ID))
	pushed := env.do(t, http.MethodPost, "/v1/sync/push", admin.AccessToken, map[string]any{
		"changes": []map[string]any{{
			"id": "a1", "entity": "project", "entityId": archivedID,
			"operation": "upsert", "payload": payload,
		}},
	})
	if pushed.Code != http.StatusOK {
		t.Fatalf("seed archived project = %d, body = %s", pushed.Code, pushed.Body.String())
	}

	active := decodeInto[[]listedProject](t, env.do(t, http.MethodGet, "/v1/admin/projects", admin.AccessToken, nil))
	if len(active) != 1 || active[0].ID != string(env.project.ID) {
		t.Errorf("the default list = %+v, want just the active fixture project %s", active, env.project.ID)
	}

	archived := decodeInto[[]listedProject](t, env.do(t, http.MethodGet, "/v1/admin/projects?status=archived", admin.AccessToken, nil))
	if len(archived) != 1 || archived[0].ID != archivedID {
		t.Errorf("the archived bucket = %+v, want just the archived project %s", archived, archivedID)
	}
	if len(archived) == 1 && !archived[0].IsArchived {
		t.Error("the archived bucket holds a project that is not archived")
	}

	// A bucket name the server does not know is a client bug, not an empty list.
	bad := env.do(t, http.MethodGet, "/v1/admin/projects?status=deleted", admin.AccessToken, nil)
	if bad.Code != http.StatusBadRequest {
		t.Errorf("listing an unknown bucket = %d, want 400 (body %s)", bad.Code, bad.Body.String())
	}
}

// clientResetCodeResponse mirrors the KMP `ResetCodeResponse`, transcribed from
// TaskTrackerKMP's HttpMemberAdminRemote. The admin endpoint hands the code
// back rather than emailing it — the project owns no mail infrastructure
// (AGENTS.md §7) — so these two keys are the entire response.
type clientResetCodeResponse struct {
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expires_at"`
}

// clientChangePasswordResponse mirrors the KMP `ChangePasswordResponse`,
// transcribed from TaskTrackerKMP's HttpUserProfileGateway. Like the sign-in
// pair, every field is required and the key set is closed.
type clientChangePasswordResponse struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	DisplayName  string `json:"display_name"`
	PasswordHash string `json:"password_hash"`
}

// The whole forgot-password flow over the routes the client actually calls
// (specs/t8.1-change-password.md): the admin mints a code, the member spends it
// on a new password exactly once, and the new password is what signs in
// afterwards. It also pins the route table itself — both handlers existed
// before they were mounted, which is the exact failure mode a route-level test
// exists to catch.
func TestChangePasswordFlowOverTheMountedRoutes(t *testing.T) {
	env := newContractEnv(t)
	member := env.signIn(t)

	// The route is mounted and auth-gated: without a token it is 401, not 404.
	denied := env.do(t, http.MethodPost, "/v1/me/password", "", map[string]string{
		"old_password_or_code": "correct horse battery",
		"new_password":         "brand new secret",
	})
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("change-password without a token = %d, want 401 (body %s)", denied.Code, denied.Body.String())
	}

	// A wrong credential refuses with a 400 the client renders verbatim.
	wrong := env.do(t, http.MethodPost, "/v1/me/password", member.AccessToken, map[string]string{
		"old_password_or_code": "not-the-password",
		"new_password":         "brand new secret",
	})
	if wrong.Code != http.StatusBadRequest {
		t.Fatalf("change-password with a wrong credential = %d, want 400 (body %s)", wrong.Code, wrong.Body.String())
	}

	// The admin mints a code for the member. The fixture user is an admin, so
	// one account exercises both halves of the flow.
	minted := env.do(t, http.MethodPost, "/v1/admin/users/"+string(env.user.ID)+"/password-reset-code", member.AccessToken, nil)
	if minted.Code != http.StatusCreated {
		t.Fatalf("mint reset code = %d, want 201 (body %s)", minted.Code, minted.Body.String())
	}
	assertWireKeys(t, "the reset code", minted.Body.Bytes(), []string{"code", "expires_at"}, nil)
	code := decodeInto[clientResetCodeResponse](t, minted)
	if code.Code == "" {
		t.Fatal("the minted response carries no code, so there is nothing to hand over")
	}
	if code.ExpiresAt <= time.Now().UnixMilli() {
		t.Errorf("expires_at = %d, want a future epoch-millisecond instant", code.ExpiresAt)
	}

	// The member spends the code on a new password.
	changed := env.do(t, http.MethodPost, "/v1/me/password", member.AccessToken, map[string]string{
		"old_password_or_code": code.Code,
		"new_password":         "brand new secret",
	})
	if changed.Code != http.StatusOK {
		t.Fatalf("change-password with the code = %d, want 200 (body %s)", changed.Code, changed.Body.String())
	}
	assertWireKeys(t, "the change-password response", changed.Body.Bytes(),
		[]string{"id", "email", "display_name", "password_hash"}, nil)
	applied := decodeInto[clientChangePasswordResponse](t, changed)
	if applied.ID != string(env.user.ID) || applied.Email != env.user.Email {
		t.Errorf("change-password answered %+v, want the caller's own row", applied)
	}

	// Single use: the spent code cannot authorise a second change.
	replay := env.do(t, http.MethodPost, "/v1/me/password", member.AccessToken, map[string]string{
		"old_password_or_code": code.Code,
		"new_password":         "another secret 1",
	})
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("replaying the spent code = %d, want 400 (body %s)", replay.Code, replay.Body.String())
	}

	// The new password is the one that signs in, and the old one is gone.
	signIn := func(password string) int {
		rec := env.do(t, http.MethodPost, "/v1/auth/login", "", map[string]string{
			"email": env.user.Email, "password": password,
		})
		return rec.Code
	}
	if got := signIn("brand new secret"); got != http.StatusOK {
		t.Errorf("sign in with the new password = %d, want 200", got)
	}
	if got := signIn("correct horse battery"); got != http.StatusUnauthorized {
		t.Errorf("sign in with the old password = %d, want 401", got)
	}
}

// A sprint the app pushed arrives on the same scope the app reads.
//
// The client has no `sprints` scope and never asks for one: its `SyncScope` says sprints and
// comments ride `workItems`. This server, meanwhile, filtered that feed to
// `entity = 'workItem'`, so a sprint committed to the log was invisible to every other
// device — the reported symptom was a second sign-in that pulled all the cards and no
// sprints. This test is the end-to-end shape of that: push like the phone does, pull like
// the other phone does.
func TestAPushedSprintArrivesOnTheWorkItemsScope(t *testing.T) {
	env := newContractEnv(t)
	tokens := env.signIn(t)

	const sprintID = "d1d1d1d1-d1d1-d1d1-d1d1-d1d1d1d1d1d1"
	// Every key here is the spelling `JSONCoding.json.encodeToString(Sprint.serializer(), …)`
	// produces on the device, not the server struct's tags: `projectID`/`ownerID`, a null
	// `goal`/`ownerID`/`finishedAt`, and microsecond timestamps. A test that pushed the
	// server's own shape would pass while the real payload was rejected.
	sprint := `{"id":"` + sprintID + `","projectID":"` + string(env.project.ID) + `",` +
		`"name":"Sprint 1","goalStoryPoints":20,"goal":null,` +
		`"startDate":"2026-09-01T00:00:00Z","endDate":"2026-09-14T00:00:00Z",` +
		`"ownerID":null,"isFinished":false,"finishedAt":null,"isArchived":false,"isLocked":false,` +
		`"createdAt":"2026-08-30T10:00:00.123456Z","updatedAt":"2026-08-30T10:00:00.123456Z"}`

	pushed := env.do(t, http.MethodPost, "/v1/sync/push", tokens.AccessToken, clientPushBody{
		Changes: []clientPushChange{{
			ID: "sprint-1", Entity: "sprint", EntityID: sprintID, Operation: "upsert", Payload: sprint,
		}},
	})
	if pushed.Code != http.StatusOK {
		t.Fatalf("push = %d, body = %s", pushed.Code, pushed.Body.String())
	}
	var verdicts clientPushResponse
	if err := json.Unmarshal(pushed.Body.Bytes(), &verdicts); err != nil {
		t.Fatalf("decode verdicts: %v", err)
	}
	if len(verdicts.Results) != 1 || verdicts.Results[0].Status != "accepted" {
		t.Fatalf("sprint push = %+v, want one accepted change", verdicts.Results)
	}

	rec := env.do(t, http.MethodGet, "/v1/sync/changes/workItems?limit=50", tokens.AccessToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("pull = %d, body = %s", rec.Code, rec.Body.String())
	}
	page := decodeInto[clientChangesPage](t, rec)

	for _, change := range page.Changes {
		if change.Entity == "sprint" && change.EntityID == sprintID {
			if !strings.Contains(change.Payload, `"name":"Sprint 1"`) {
				t.Errorf("the sprint came back with payload %q, want the document the client sent",
					change.Payload)
			}
			return
		}
	}
	t.Fatalf("the sprint was not on the workItems feed, so a second device never sees it: %s",
		rec.Body.String())
}
