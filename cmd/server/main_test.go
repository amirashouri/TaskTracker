package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/dbtest"
)

func TestHealthzDoesNotTouchTheDatabase(t *testing.T) {
	database := dbtest.NewDatabase(t)
	database.Close()

	rec := httptest.NewRecorder()
	healthz()(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("liveness with a dead database = %d, want 200", rec.Code)
	}
}

func TestReadyzReportsTheDatabaseState(t *testing.T) {
	database := dbtest.NewDatabase(t)
	handler := readyz(database.DB)

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("readiness with a live database = %d, want 200", rec.Code)
	}

	database.Close()

	rec = httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readiness with a dead database = %d, want 503", rec.Code)
	}
}
