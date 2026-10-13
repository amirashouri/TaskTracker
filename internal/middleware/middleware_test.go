package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/auth"
	"github.com/amirashouri/tasktracker-server/internal/entities"
)

func serve(t *testing.T, mw Middleware, inner http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mw(inner).ServeHTTP(rec, r)
	return rec
}

func TestSecurityHeadersSetsNosniff(t *testing.T) {
	rec := serve(t, SecurityHeaders(), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

// The header must survive an error response, so a failure body is not sniffed
// either.
func TestSecurityHeadersCoversErrorResponses(t *testing.T) {
	rec := serve(t, SecurityHeaders(), func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestNoStoreSetsCacheControl(t *testing.T) {
	rec := serve(t, NoStore(), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil))

	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func testLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

func TestLoggingRecordsSuccessfulRequestsAtInfo(t *testing.T) {
	logger, output := testLogger()

	serve(t, Logging(logger), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	logged := output.String()
	if !strings.Contains(logged, "level=INFO") {
		t.Errorf("a 200 was not logged at info: %s", logged)
	}
	if strings.Contains(logged, "level=ERROR") {
		t.Errorf("a 200 was logged at error: %s", logged)
	}
	if !strings.Contains(logged, "status=200") {
		t.Errorf("the log line has no status: %s", logged)
	}
}

// A filter on level must find real failures without hiding the rest.
func TestLoggingRecordsServerErrorsAtError(t *testing.T) {
	logger, output := testLogger()

	serve(t, Logging(logger), func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}, httptest.NewRequest(http.MethodGet, "/", nil))

	if logged := output.String(); !strings.Contains(logged, "level=ERROR") {
		t.Errorf("a 500 was not logged at error: %s", logged)
	}
}

func TestRecoverTurnsAPanicIntoAServerError(t *testing.T) {
	logger, output := testLogger()

	rec := serve(t, Recover(logger), func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("panic status = %d, want 500", rec.Code)
	}
	if logged := output.String(); !strings.Contains(logged, "level=ERROR") {
		t.Errorf("the panic was not logged at error: %s", logged)
	}
}

func TestRequestIDEchoesAClientSuppliedID(t *testing.T) {
	var seen string
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, "abc-123")
	rec := httptest.NewRecorder()
	RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFrom(r.Context())
	})).ServeHTTP(rec, req)

	if seen != "abc-123" {
		t.Errorf("context id = %q, want the supplied abc-123", seen)
	}
	if got := rec.Header().Get(RequestIDHeader); got != "abc-123" {
		t.Errorf("response id = %q, want the supplied abc-123", got)
	}
}

func TestRequestIDGeneratesWhenAbsent(t *testing.T) {
	var seen string
	rec := httptest.NewRecorder()
	RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFrom(r.Context())
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if seen == "" {
		t.Fatal("no id was put on the context")
	}
	if got := rec.Header().Get(RequestIDHeader); got != seen {
		t.Errorf("response id = %q, want the context id %q", got, seen)
	}
}

// A client-supplied id must not be able to inject a header or bloat a log line.
func TestRequestIDRejectsAnUnsafeValue(t *testing.T) {
	for _, unsafe := range []string{
		"has space",
		"line\nbreak",
		strings.Repeat("x", maxRequestIDLength+1),
		"tab\there",
	} {
		var seen string
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(RequestIDHeader, unsafe)
		rec := httptest.NewRecorder()
		RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = RequestIDFrom(r.Context())
		})).ServeHTTP(rec, req)

		if seen == unsafe {
			t.Errorf("unsafe id %q was accepted", unsafe)
		}
		if seen == "" {
			t.Errorf("unsafe id %q produced no replacement", unsafe)
		}
	}
}

// The request log must carry the id, so a response can be traced to it.
func TestLoggingIncludesTheRequestID(t *testing.T) {
	logger, output := testLogger()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set(RequestIDHeader, "trace-me")
	rec := httptest.NewRecorder()
	Chain(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }),
		RequestID(),
		Logging(logger),
	).ServeHTTP(rec, req)

	if logged := output.String(); !strings.Contains(logged, "request_id=trace-me") {
		t.Errorf("the log line has no request id: %s", logged)
	}
}

// --- Auth ---

// newJWT builds a manager with a fixed secret. Tests that need an already
// expired token pass a negative access TTL.
func newJWT(accessTTL time.Duration) *auth.JWTManager {
	return auth.NewJWTManager(strings.Repeat("s", 32), accessTTL, time.Hour)
}

// probe records whether the protected handler ran and what it read off the
// context, so a rejection can be told apart from a pass-through.
type probe struct {
	called bool
	userID string
	role   string
}

func (p *probe) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p.called = true
		p.userID, _ = auth.UserFrom(r.Context())
		p.role, _ = auth.RoleFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}
}

func bearerRequest(method, path, header string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	return req
}

func TestAuthRejectsAMissingToken(t *testing.T) {
	inner := &probe{}
	rec := serve(t, Auth(newJWT(time.Hour)), inner.handler(),
		bearerRequest(http.MethodPost, "/v1/sync/push", ""))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if inner.called {
		t.Error("the protected handler ran without a token")
	}
}

// Anything that is not exactly "Bearer <token>" is a 401, not a pass-through
// and not a panic.
func TestAuthRejectsAMalformedAuthorizationHeader(t *testing.T) {
	jwt := newJWT(time.Hour)

	for _, header := range []string{
		"Bearer",                   // scheme with no token
		"Bearer ",                  // scheme and whitespace only
		"Basic dXNlcjpwYXNz",       // the wrong scheme
		"eyJhbGciOiJIUzI1NiJ9.abc", // a token with no scheme at all
		"Bearer a.b.c extra",       // trailing garbage
	} {
		inner := &probe{}
		rec := serve(t, Auth(jwt), inner.handler(),
			bearerRequest(http.MethodGet, "/", header))

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status = %d, want 401", header, rec.Code)
		}
		if inner.called {
			t.Errorf("Authorization %q: the protected handler ran", header)
		}
	}
}

func TestAuthRejectsAnUnparsableToken(t *testing.T) {
	inner := &probe{}
	rec := serve(t, Auth(newJWT(time.Hour)), inner.handler(),
		bearerRequest(http.MethodGet, "/", "Bearer not-a-jwt"))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if inner.called {
		t.Error("the protected handler ran on an unparsable token")
	}
}

// A token signed with a different key must not authenticate, even though it is
// well formed.
func TestAuthRejectsATokenSignedWithAnotherSecret(t *testing.T) {
	foreign := auth.NewJWTManager(strings.Repeat("x", 32), time.Hour, time.Hour)
	token, err := foreign.IssueAccess("u1", "a@example.com", entities.RoleAdmin)
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}

	inner := &probe{}
	rec := serve(t, Auth(newJWT(time.Hour)), inner.handler(),
		bearerRequest(http.MethodGet, "/", "Bearer "+token))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a foreign signature", rec.Code)
	}
	if inner.called {
		t.Error("the protected handler ran on a foreign token")
	}
}

func TestAuthRejectsAnExpiredToken(t *testing.T) {
	expired := newJWT(-time.Minute)
	token, err := expired.IssueAccess("u1", "a@example.com", entities.RoleMember)
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}

	inner := &probe{}
	rec := serve(t, Auth(expired), inner.handler(),
		bearerRequest(http.MethodGet, "/", "Bearer "+token))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for an expired token", rec.Code)
	}
	if inner.called {
		t.Error("the protected handler ran on an expired token")
	}
}

// A refresh token is validly signed, so only the typ claim stops it being
// replayed as an access token. This is the check that makes that true.
func TestAuthRejectsARefreshToken(t *testing.T) {
	jwt := newJWT(time.Hour)
	token, err := jwt.IssueRefresh("u1", "a@example.com", entities.RoleAdmin)
	if err != nil {
		t.Fatalf("issue refresh: %v", err)
	}

	inner := &probe{}
	rec := serve(t, Auth(jwt), inner.handler(),
		bearerRequest(http.MethodGet, "/v1/sync/push", "Bearer "+token))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a refresh token", rec.Code)
	}
	if inner.called {
		t.Error("a refresh token was accepted on a protected route")
	}
}

func TestAuthPassesTheUserAndRoleThrough(t *testing.T) {
	jwt := newJWT(time.Hour)
	token, err := jwt.IssueAccess("user-1", "a@example.com", entities.RoleAdmin)
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}

	inner := &probe{}
	rec := serve(t, Auth(jwt), inner.handler(),
		bearerRequest(http.MethodGet, "/", "Bearer "+token))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !inner.called {
		t.Fatal("the protected handler did not run")
	}
	if inner.userID != "user-1" {
		t.Errorf("user id = %q, want user-1", inner.userID)
	}
	if inner.role != entities.RoleAdmin {
		t.Errorf("role = %q, want %q", inner.role, entities.RoleAdmin)
	}
}

// RFC 7235 makes the scheme case-insensitive.
func TestAuthAcceptsALowercaseScheme(t *testing.T) {
	jwt := newJWT(time.Hour)
	token, err := jwt.IssueAccess("user-1", "a@example.com", entities.RoleMember)
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}

	inner := &probe{}
	rec := serve(t, Auth(jwt), inner.handler(),
		bearerRequest(http.MethodGet, "/", "bearer "+token))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !inner.called {
		t.Error("a lowercase bearer scheme was rejected")
	}
}

// --- AdminOnly ---

func TestAdminOnlyAllowsAnAdmin(t *testing.T) {
	inner := &probe{}
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/users", nil)
	req = req.WithContext(auth.WithUser(req.Context(), "u1", entities.RoleAdmin))

	rec := serve(t, AdminOnly(), inner.handler(), req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for an admin", rec.Code)
	}
	if !inner.called {
		t.Error("an admin was refused")
	}
}

func TestAdminOnlyRejectsAMember(t *testing.T) {
	inner := &probe{}
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/users", nil)
	req = req.WithContext(auth.WithUser(req.Context(), "u1", entities.RoleMember))

	rec := serve(t, AdminOnly(), inner.handler(), req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a member", rec.Code)
	}
	if inner.called {
		t.Error("a member reached an admin route")
	}
}

// If Auth did not run, or a token carried no role, AdminOnly must deny rather
// than treat an empty role as a pass.
func TestAdminOnlyDeniesWithNoUserOnTheContext(t *testing.T) {
	inner := &probe{}
	rec := serve(t, AdminOnly(), inner.handler(),
		httptest.NewRequest(http.MethodGet, "/v1/admin/users", nil))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 without a user on the context", rec.Code)
	}
	if inner.called {
		t.Error("the handler ran without a user on the context")
	}
}

// The admin route in main.go is Auth then AdminOnly; a member's valid access
// token must get past Auth and then be stopped by AdminOnly.
func TestAuthThenAdminOnlyStopsAMember(t *testing.T) {
	jwt := newJWT(time.Hour)
	token, err := jwt.IssueAccess("u1", "member@example.com", entities.RoleMember)
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}

	inner := &probe{}
	rec := httptest.NewRecorder()
	Chain(inner.handler(), Auth(jwt), AdminOnly()).ServeHTTP(rec,
		bearerRequest(http.MethodPost, "/v1/admin/users", "Bearer "+token))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if inner.called {
		t.Error("a member reached an admin route")
	}
}

// --- CORS ---

const testOrigin = "https://app.example.com"

func TestCORSPreflightIsAnsweredWithoutReachingTheHandler(t *testing.T) {
	inner := &probe{}
	req := httptest.NewRequest(http.MethodOptions, "/v1/sync/push", nil)
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Access-Control-Request-Method", "POST")

	rec := serve(t, CORS([]string{testOrigin}), inner.handler(), req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", rec.Code)
	}
	if inner.called {
		t.Error("the preflight reached the handler")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != testOrigin {
		t.Errorf("Allow-Origin = %q, want %q", got, testOrigin)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
		t.Errorf("Allow-Methods = %q, want POST listed", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Authorization") {
		t.Errorf("Allow-Headers = %q, want Authorization listed", got)
	}
	if got := rec.Header().Get("Access-Control-Max-Age"); got != "600" {
		t.Errorf("Max-Age = %q, want 600", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin", got)
	}
}

func TestCORSReflectsAnAllowedOriginOnANormalRequest(t *testing.T) {
	inner := &probe{}
	req := httptest.NewRequest(http.MethodGet, "/v1/sync/changes/project", nil)
	req.Header.Set("Origin", testOrigin)

	rec := serve(t, CORS([]string{testOrigin}), inner.handler(), req)

	if !inner.called {
		t.Fatal("a normal request with an Origin did not reach the handler")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != testOrigin {
		t.Errorf("Allow-Origin = %q, want %q", got, testOrigin)
	}
}

// An origin that is not on the list gets no CORS header, which is what makes
// the browser block the response.
func TestCORSIgnoresADisallowedOrigin(t *testing.T) {
	inner := &probe{}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.example.com")

	rec := serve(t, CORS([]string{testOrigin}), inner.handler(), req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q, want none for a disallowed origin", got)
	}
	if !inner.called {
		t.Error("a non-preflight request should still reach the handler")
	}
}

// A preflight from a disallowed origin is answered, but without the header the
// browser needs, so it is still refused.
func TestCORSPreflightFromADisallowedOriginCarriesNoHeader(t *testing.T) {
	inner := &probe{}
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://evil.example.com")

	rec := serve(t, CORS([]string{testOrigin}), inner.handler(), req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q, want none", got)
	}
}

func TestCORSWildcardAllowsAnyOrigin(t *testing.T) {
	inner := &probe{}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://anywhere.example.com")

	rec := serve(t, CORS([]string{"*"}), inner.handler(), req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin = %q, want *", got)
	}
}

// Mobile clients send no Origin, and an empty allow-list is how a deployment
// turns the browser headers off entirely.
func TestCORSWithNoAllowedOriginsSetsNoHeaders(t *testing.T) {
	inner := &probe{}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", testOrigin)

	rec := serve(t, CORS(nil), inner.handler(), req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q, want none with an empty allow-list", got)
	}
	if !inner.called {
		t.Error("the request did not reach the handler")
	}
}

// A mobile client sends no Origin at all and must be unaffected.
func TestCORSLeavesARequestWithoutAnOriginAlone(t *testing.T) {
	inner := &probe{}
	rec := serve(t, CORS([]string{testOrigin}), inner.handler(),
		httptest.NewRequest(http.MethodPost, "/v1/sync/push", nil))

	if !inner.called {
		t.Fatal("a request with no Origin did not reach the handler")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Allow-Origin = %q, want none when no Origin was sent", got)
	}
}

// --- Chain ---

// The first middleware listed must be the outermost, so it wraps the others.
func TestChainAppliesTheFirstMiddlewareOutermost(t *testing.T) {
	var order []string
	mark := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}

	rec := httptest.NewRecorder()
	Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		mark("outer"), mark("inner")).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if len(order) != 2 || order[0] != "outer" || order[1] != "inner" {
		t.Errorf("middleware order = %v, want [outer inner]", order)
	}
}
