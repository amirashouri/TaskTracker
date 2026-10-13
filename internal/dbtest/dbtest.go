// Package dbtest opens a migrated, empty database for tests. It uses a
// temporary SQLite file by default, or the Postgres server named by
// TASKTRACKER_TEST_POSTGRES_URL, so one suite covers both drivers.
package dbtest

import (
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/db"
)

// PostgresURLEnv names the environment variable holding the Postgres URL to test
// against. It must point at a throwaway server: each test creates and drops its
// own schema there.
const PostgresURLEnv = "TASKTRACKER_TEST_POSTGRES_URL"

// NewDatabase returns a migrated database with no rows, cleaned up when the test
// finishes. Postgres isolation is one schema per test, so tests in different
// packages can run in parallel without seeing each other's rows.
func NewDatabase(t *testing.T) *db.DB {
	t.Helper()

	raw := os.Getenv(PostgresURLEnv)
	if raw == "" {
		database := open(t, "sqlite://"+filepath.Join(t.TempDir(), "test.db"))
		if err := db.Migrate(database); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		t.Cleanup(func() { database.Close() })
		return database
	}

	// The schema must exist before connecting with it selected, or an
	// unqualified CREATE TABLE has no schema to go in.
	schema := newSchemaName(t)
	database := open(t, withSearchPath(t, raw, schema))
	if _, err := database.Exec("CREATE SCHEMA IF NOT EXISTS " + schema); err != nil {
		t.Fatalf("create schema %s: %v", schema, err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		if _, err := database.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
		database.Close()
	})
	return database
}

func open(t *testing.T, databaseURL string) *db.DB {
	t.Helper()
	database, err := db.New(databaseURL)
	if err != nil {
		t.Fatalf("open %s: %v", databaseURL, err)
	}
	return database
}

func newSchemaName(t *testing.T) string {
	t.Helper()
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("random schema name: %v", err)
	}
	return "tt_test_" + hex.EncodeToString(buf[:])
}

func withSearchPath(t *testing.T, raw, schema string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", PostgresURLEnv, err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
