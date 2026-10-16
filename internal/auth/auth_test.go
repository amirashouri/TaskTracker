package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/dbtest"
	"github.com/amirashouri/tasktracker-server/internal/entities"
	"github.com/amirashouri/tasktracker-server/internal/ratelimit"
)

const testSecret = "00000000000000000000000000000000"

type testEnv struct {
	handler *Handler
	users   *entities.UserRepository
	store   *TokenStore
	jwt     *JWTManager
	user    *entities.User
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	database := dbtest.NewDatabase(t)
	users := entities.NewUserRepository(database.DB)
	jwt := NewJWTManager(testSecret, time.Hour, 720*time.Hour)

	user := &entities.User{Email: "ada@example.com", DisplayName: "Ada", PasswordHash: "correct horse battery"}
	if err := users.Create(context.Background(), user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	return &testEnv{
		handler: NewHandler(jwt, users, NewTokenStore(database.DB), nil),
		users:   users,
		store:   NewTokenStore(database.DB),
		jwt:     jwt,
		user:    user,
	}
}

func (e *testEnv) signIn(t *testing.T) tokenResponse {
	t.Helper()
	rec := postJSON(t, e.handler.SignIn, map[string]string{"email": "ada@example.com", "password": "correct horse battery"})
	if rec.Code != http.StatusOK {
		t.Fatalf("sign-in status = %d, body = %s", rec.Code, rec.Body.String())
	}
	return decodeTokens(t, rec)
}

func (e *testEnv) refresh(t *testing.T, refreshToken string) *httptest.ResponseRecorder {
	t.Helper()
	return postJSON(t, e.handler.Refresh, map[string]string{"refresh_token": refreshToken})
}

func (e *testEnv) signOut(t *testing.T, refreshToken string) *httptest.ResponseRecorder {
	t.Helper()
	return postJSON(t, e.handler.SignOut, map[string]string{"refresh_token": refreshToken})
}

func (e *testEnv) stored(t *testing.T, token string) *RefreshToken {
	t.Helper()
	claims, err := e.jwt.Parse(token)
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	record, err := e.store.Get(context.Background(), HashTokenID(claims.ID))
	if err != nil {
		t.Fatalf("get token record: %v", err)
	}
	if record == nil {
		t.Fatal("token was not stored")
	}
	return record
}

func postJSON(t *testing.T, handler http.HandlerFunc, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload)))
	return rec
}

// postRaw sends a body the caller encoded by hand, so a test can hand a handler
// something json.Marshal would never produce.
func postRaw(t *testing.T, handler http.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	return rec
}

// postSignIn sets the peer address, so a test can vary the client without
// varying the account.
func postSignIn(t *testing.T, handler http.HandlerFunc, remoteAddr, email, password string) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"email": email, "password": password})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func decodeTokens(t *testing.T, rec *httptest.ResponseRecorder) tokenResponse {
	t.Helper()
	var tokens tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &tokens); err != nil {
		t.Fatalf("decode token response %q: %v", rec.Body.String(), err)
	}
	return tokens
}

// The stored hash must identify the token without holding it: a database read
// must not yield something that can be replayed.
func TestSignInStoresAHashedRefreshToken(t *testing.T) {
	env := newTestEnv(t)
	tokens := env.signIn(t)

	record := env.stored(t, tokens.RefreshToken)
	if record.UserID != string(env.user.ID) {
		t.Errorf("token user = %q, want %q", record.UserID, env.user.ID)
	}
	if record.FamilyID == "" {
		t.Error("token has no family id, so it could never be revoked as a session")
	}
	if record.Revoked() {
		t.Error("a freshly issued token is already revoked")
	}
	if record.TokenHash == tokens.RefreshToken {
		t.Error("the token itself was stored instead of a hash")
	}
	if len(record.TokenHash) != 64 {
		t.Errorf("token hash %q is not a SHA-256 hex digest", record.TokenHash)
	}

	// A refresh token's own expiry must match the configured TTL.
	want := time.Now().Add(720 * time.Hour).UnixMilli()
	if delta := record.ExpiresAt - want; delta < -time.Minute.Milliseconds() || delta > time.Minute.Milliseconds() {
		t.Errorf("expires_at = %d, want about %d", record.ExpiresAt, want)
	}
}

func TestRefreshRotatesTheTokenAndRevokesThePresentedOne(t *testing.T) {
	env := newTestEnv(t)
	first := env.signIn(t)

	rec := env.refresh(t, first.RefreshToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, body = %s", rec.Code, rec.Body.String())
	}
	second := decodeTokens(t, rec)

	if second.RefreshToken == first.RefreshToken {
		t.Fatal("refresh returned the same token; it was not rotated")
	}

	if old := env.stored(t, first.RefreshToken); !old.Revoked() {
		t.Error("the presented token is still live after rotation")
	}
	next := env.stored(t, second.RefreshToken)
	if next.Revoked() {
		t.Error("the replacement token is revoked")
	}
	if next.FamilyID != env.stored(t, first.RefreshToken).FamilyID {
		t.Error("rotation changed the family; the session is no longer revocable as a unit")
	}
}

// The headline requirement: a used refresh token cannot be used twice, and
// presenting it again burns the session.
func TestRefreshRejectsAUsedTokenAndRevokesTheFamily(t *testing.T) {
	env := newTestEnv(t)
	first := env.signIn(t)

	rotated := env.refresh(t, first.RefreshToken)
	if rotated.Code != http.StatusOK {
		t.Fatalf("first refresh status = %d", rotated.Code)
	}
	second := decodeTokens(t, rotated)

	reuse := env.refresh(t, first.RefreshToken)
	if reuse.Code != http.StatusUnauthorized {
		t.Fatalf("reusing a rotated token = %d, want 401", reuse.Code)
	}

	if live := env.stored(t, second.RefreshToken); !live.Revoked() {
		t.Error("reuse did not revoke the family, so the replacement token still works")
	}
	if again := env.refresh(t, second.RefreshToken); again.Code != http.StatusUnauthorized {
		t.Errorf("the replacement token survived reuse detection: status = %d", again.Code)
	}
}

// A second rotation of the same presented token is the race between two clients
// using one token, and must not produce two live tokens.
func TestConcurrentRotationLeavesOneLiveToken(t *testing.T) {
	env := newTestEnv(t)
	tokens := env.signIn(t)

	first := env.refresh(t, tokens.RefreshToken)
	if first.Code != http.StatusOK {
		t.Fatalf("first refresh status = %d", first.Code)
	}
	second := env.refresh(t, tokens.RefreshToken)
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("second refresh of the same token = %d, want 401", second.Code)
	}
	if live := env.refresh(t, decodeTokens(t, first).RefreshToken); live.Code != http.StatusUnauthorized {
		t.Errorf("the family was not revoked after the losing race: status = %d", live.Code)
	}
}

func TestSignOutRevokesTheSession(t *testing.T) {
	env := newTestEnv(t)
	tokens := env.signIn(t)

	if rec := env.signOut(t, tokens.RefreshToken); rec.Code != http.StatusNoContent {
		t.Fatalf("sign-out status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if after := env.refresh(t, tokens.RefreshToken); after.Code != http.StatusUnauthorized {
		t.Errorf("a signed-out refresh token still works: status = %d", after.Code)
	}
}

// Sign-out is idempotent and must not disclose whether the token existed.
func TestSignOutAcceptsAnUnknownToken(t *testing.T) {
	env := newTestEnv(t)

	if rec := env.signOut(t, "not-a-token"); rec.Code != http.StatusNoContent {
		t.Errorf("sign-out with an unknown token = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

// Refresh and SignOut read the same request body, so both must reject one that
// carries no usable token rather than falling through to the token lookup.
func TestRefreshAndSignOutRequireARefreshToken(t *testing.T) {
	env := newTestEnv(t)

	handlers := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"Refresh", env.handler.Refresh},
		{"SignOut", env.handler.SignOut},
	}
	bodies := []struct {
		name string
		body string
	}{
		{"an empty object", `{}`},
		{"a blank token", `{"refresh_token":""}`},
		{"a body that is not JSON", `{"refresh_token":`},
	}

	for _, h := range handlers {
		for _, b := range bodies {
			rec := postRaw(t, h.handler, b.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s with %s = %d, want %d (body %s)",
					h.name, b.name, rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		}
	}
}

func TestRefreshRejectsAccessTokens(t *testing.T) {
	env := newTestEnv(t)
	tokens := env.signIn(t)

	if rec := env.refresh(t, tokens.AccessToken); rec.Code != http.StatusUnauthorized {
		t.Errorf("refreshing with an access token = %d, want 401", rec.Code)
	}
}

func TestRefreshRejectsExpiredTokens(t *testing.T) {
	env := newTestEnv(t)
	tokens := env.signIn(t)

	// The JWT is still inside its lifetime, so this exercises the stored record's
	// expiry rather than the signature's.
	if _, err := env.store.db.ExecContext(context.Background(),
		`UPDATE refresh_tokens SET expires_at = $1 WHERE token_hash = $2`,
		time.Now().Add(-time.Hour).UnixMilli(), env.stored(t, tokens.RefreshToken).TokenHash); err != nil {
		t.Fatalf("expire token: %v", err)
	}

	if rec := env.refresh(t, tokens.RefreshToken); rec.Code != http.StatusUnauthorized {
		t.Errorf("refreshing with an expired record = %d, want 401", rec.Code)
	}
}

func TestRefreshRejectsUnknownAndForeignTokens(t *testing.T) {
	env := newTestEnv(t)

	if rec := env.refresh(t, "not-a-token"); rec.Code != http.StatusUnauthorized {
		t.Errorf("refresh with a malformed token = %d, want 401", rec.Code)
	}

	// Signed correctly, but not issued by this server.
	other := NewJWTManager(strings.Repeat("x", 32), time.Hour, 720*time.Hour)
	foreign, _, err := other.IssueRefreshWithID(string(env.user.ID), env.user.Email, env.user.Role)
	if err != nil {
		t.Fatalf("issue foreign token: %v", err)
	}
	if rec := env.refresh(t, foreign); rec.Code != http.StatusUnauthorized {
		t.Errorf("refresh with a foreign signature = %d, want 401", rec.Code)
	}

	// Valid token, but the stored record belongs to a different user.
	other2 := &entities.User{Email: "grace@example.com", PasswordHash: "hopper"}
	if err := env.users.Create(context.Background(), other2); err != nil {
		t.Fatalf("create second user: %v", err)
	}
	signed, id, err := env.jwt.IssueRefreshWithID(string(env.user.ID), env.user.Email, env.user.Role)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if err := env.store.Save(context.Background(), RefreshToken{
		TokenHash: HashTokenID(id),
		FamilyID:  "family",
		UserID:    string(other2.ID),
		IssuedAt:  time.Now().UnixMilli(),
		ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("save mismatched record: %v", err)
	}
	if rec := env.refresh(t, signed); rec.Code != http.StatusUnauthorized {
		t.Errorf("refresh with a record owned by another user = %d, want 401", rec.Code)
	}
}

func TestRotateReportsReuseAndRevokesTheFamily(t *testing.T) {
	env := newTestEnv(t)
	tokens := env.signIn(t)
	first := env.stored(t, tokens.RefreshToken)

	next := RefreshToken{
		TokenHash: "next",
		FamilyID:  first.FamilyID,
		UserID:    first.UserID,
		IssuedAt:  time.Now().UnixMilli(),
		ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
	}
	if err := env.store.Rotate(context.Background(), first.TokenHash, next); err != nil {
		t.Fatalf("first rotate: %v", err)
	}
	if err := env.store.Rotate(context.Background(), first.TokenHash, next); err != ErrTokenReused {
		t.Fatalf("second rotate error = %v, want ErrTokenReused", err)
	}

	live, err := env.store.Get(context.Background(), "next")
	if err != nil {
		t.Fatalf("get rotated token: %v", err)
	}
	if live == nil || !live.Revoked() {
		t.Error("reuse did not revoke the family's live token")
	}
}

// Repeated guesses from one address must be throttled, not merely slowed by
// bcrypt.
// A deleted account keeps its row but must not be able to sign in again. The
// answer is the same 401 a wrong password gets, so the response cannot be used
// to tell a deleted account from a mistyped one.
func TestSignInRejectsADeletedUser(t *testing.T) {
	env := newTestEnv(t)
	if err := env.users.SetStatus(context.Background(), env.user.ID, entities.StatusDeleted); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	rec := postJSON(t, env.handler.SignIn, map[string]string{
		"email": "ada@example.com", "password": "correct horse battery",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("sign-in by a deleted user = %d, want 401 (body %s)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "invalid credentials") {
		t.Errorf("deleted-user sign-in body = %s, want the same message a wrong password gets", body)
	}
}

// A session that was live when the account was deleted must not be able to
// rotate — and the deletion must revoke the family, not merely refuse once.
func TestRefreshRejectsADeletedUserAndRevokesTheFamily(t *testing.T) {
	env := newTestEnv(t)
	tokens := env.signIn(t)

	if err := env.users.SetStatus(context.Background(), env.user.ID, entities.StatusDeleted); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	rec := env.refresh(t, tokens.RefreshToken)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("refresh after deletion = %d, want 401 (body %s)", rec.Code, rec.Body.String())
	}
	if live := env.stored(t, tokens.RefreshToken); !live.Revoked() {
		t.Error("refreshing a deleted user's token did not revoke the family, so the deletion can be outlived")
	}
	if again := env.refresh(t, tokens.RefreshToken); again.Code != http.StatusUnauthorized {
		t.Errorf("a revoked token after deletion = %d, want 401", again.Code)
	}
}

// An inactive account is not deleted, and must keep working: only `deleted`
// blocks a session.
func TestSignInStillAcceptsAnInactiveUser(t *testing.T) {
	env := newTestEnv(t)
	if err := env.users.SetStatus(context.Background(), env.user.ID, entities.StatusInactive); err != nil {
		t.Fatalf("set inactive: %v", err)
	}

	rec := postJSON(t, env.handler.SignIn, map[string]string{
		"email": "ada@example.com", "password": "correct horse battery",
	})
	if rec.Code != http.StatusOK {
		t.Errorf("sign-in by an inactive user = %d, want 200 (only `deleted` blocks)", rec.Code)
	}
}

func TestSignInThrottlesPerAddress(t *testing.T) {
	env := newTestEnv(t)
	env.handler.limiter = ratelimit.New(2, time.Minute)

	// Different accounts, one address: only the address counter can stop the
	// third attempt.
	for i, email := range []string{"a@example.com", "b@example.com"} {
		if rec := postSignIn(t, env.handler.SignIn, "10.0.0.9:1", email, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i+1, rec.Code)
		}
	}

	rec := postSignIn(t, env.handler.SignIn, "10.0.0.9:1", "c@example.com", "wrong")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third attempt from one address = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("a 429 response has no Retry-After header")
	}
}

// Spraying one account from many addresses must be throttled too.
func TestSignInThrottlesPerAccount(t *testing.T) {
	env := newTestEnv(t)
	env.handler.limiter = ratelimit.New(2, time.Minute)

	for i, addr := range []string{"10.0.0.1:1", "10.0.0.2:1"} {
		if rec := postSignIn(t, env.handler.SignIn, addr, "ada@example.com", "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i+1, rec.Code)
		}
	}

	rec := postSignIn(t, env.handler.SignIn, "10.0.0.3:1", "ada@example.com", "wrong")
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("third attempt against one account = %d, want 429", rec.Code)
	}
}
