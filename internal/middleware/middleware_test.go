package middleware

import (
	"net/http"
	"net/http/httptest"
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
