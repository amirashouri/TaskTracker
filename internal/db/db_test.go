package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseURL(t *testing.T) {
	cases := []struct {
		in     string
		driver string
		dsn    string
	}{
		{"sqlite:///tmp/x.db", DriverSQLite, "/tmp/x.db"},
		{"postgres://u:p@h/db", DriverPostgres, "postgres://u:p@h/db"},
		{"postgresql://u:p@h/db", DriverPostgres, "postgresql://u:p@h/db"},
	}
	for _, tc := range cases {
		driver, dsn, err := parseURL(tc.in)
		if err != nil {
			t.Fatalf("parseURL(%q): %v", tc.in, err)
		}
		if driver != tc.driver || dsn != tc.dsn {
			t.Errorf("parseURL(%q) = (%q, %q), want (%q, %q)", tc.in, driver, dsn, tc.driver, tc.dsn)
		}
	}

	if _, _, err := parseURL("mysql://host/db"); err == nil {
		t.Error("expected an error for an unsupported URL")
	}
}

func TestMigrationsForDriver(t *testing.T) {
	sqlite := strings.Join(migrationsFor(DriverSQLite), "\n")
	if !strings.Contains(sqlite, "INTEGER PRIMARY KEY AUTOINCREMENT") {
		t.Error("SQLite DDL should turn BIGSERIAL into an autoincrementing key")
	}
	if strings.Contains(sqlite, "JSONB") {
		t.Error("SQLite DDL must not contain JSONB")
	}
	if strings.Contains(sqlite, "gen_random_uuid") {
		t.Error("SQLite DDL must not call gen_random_uuid")
	}
	if !strings.Contains(sqlite, "ON DELETE CASCADE") {
		t.Error("SQLite supports ON DELETE CASCADE and it must be preserved")
	}

	postgres := strings.Join(migrationsFor(DriverPostgres), "\n")
	if strings.Contains(postgres, "AUTOINCREMENT") {
		t.Error("Postgres must not receive SQLite-only DDL")
	}
	if !strings.Contains(postgres, "BIGSERIAL PRIMARY KEY") {
		t.Error("Postgres DDL should keep BIGSERIAL")
	}

	// work_items references sprints, so sprints must be created first on Postgres.
	sprints := strings.Index(postgres, "CREATE TABLE IF NOT EXISTS sprints")
	workItems := strings.Index(postgres, "CREATE TABLE IF NOT EXISTS work_items")
	if sprints == -1 || workItems == -1 || sprints > workItems {
		t.Errorf("sprints must be created before work_items (sprints=%d work_items=%d)", sprints, workItems)
	}
}

func TestMigrateIsIdempotentAndCreatesSchema(t *testing.T) {
	database, err := New("sqlite://" + filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer database.Close()

	if err := Migrate(database); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := Migrate(database); err != nil {
		t.Fatalf("second Migrate should be a no-op: %v", err)
	}

	for _, table := range []string{"users", "projects", "sprints", "project_members", "work_items", "comments", "sync_log"} {
		if !hasTable(t, database.DB, table) {
			t.Errorf("table %q was not created", table)
		}
	}

	for _, column := range []struct{ table, column string }{
		{"projects", "version"},
		{"comments", "version"},
		{"work_items", "recurrence"},
		{"work_items", "blocked_by_ids"},
	} {
		if !hasColumn(t, database.DB, column.table, column.column) {
			t.Errorf("%s.%s is missing", column.table, column.column)
		}
	}

	// Membership lives in project_members; the column was removed.
	if hasColumn(t, database.DB, "projects", "member_ids") {
		t.Error("projects.member_ids should no longer exist")
	}
}

func TestNewCanBeCalledTwice(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.db", "b.db"} {
		database, err := New("sqlite://" + filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("New(%s): %v", name, err)
		}
		if err := Migrate(database); err != nil {
			t.Fatalf("Migrate(%s): %v", name, err)
		}
		database.Close()
	}
}

func hasTable(t *testing.T, database *sql.DB, table string) bool {
	t.Helper()
	var name string
	err := database.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
	return err == nil
}

func hasColumn(t *testing.T, database *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := database.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column name: %v", err)
		}
		if name == column {
			return true
		}
	}
	return false
}
