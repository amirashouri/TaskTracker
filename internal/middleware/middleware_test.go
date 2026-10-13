package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
