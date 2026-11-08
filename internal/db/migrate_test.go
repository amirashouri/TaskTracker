package db

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSplitStatements(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{
			name: "two statements",
			sql:  "CREATE TABLE a (id TEXT);\nCREATE TABLE b (id TEXT);",
			want: []string{"CREATE TABLE a (id TEXT)", "CREATE TABLE b (id TEXT)"},
		},
		{
			name: "trailing statement without a semicolon",
			sql:  "SELECT 1;\nSELECT 2",
			want: []string{"SELECT 1", "SELECT 2"},
		},
		{
			name: "blank and comment-only input",
			sql:  "-- just a comment\n\n   \n",
			want: nil,
		},
		{
			// A semicolon inside a literal must not end the statement.
			name: "semicolon inside a string literal",
			sql:  "INSERT INTO t (b) VALUES ('a;b');\nSELECT 1;",
			want: []string{"INSERT INTO t (b) VALUES ('a;b')", "SELECT 1"},
		},
		{
			// A doubled quote escapes a quote; the literal continues.
			name: "escaped quote inside a string literal",
			sql:  "INSERT INTO t (b) VALUES ('it''s;ok');",
			want: []string{"INSERT INTO t (b) VALUES ('it''s;ok')"},
		},
		{
			name: "semicolon inside a line comment",
			sql:  "-- drop these; later\nSELECT 1;",
			want: []string{"SELECT 1"},
		},
		{
			name: "semicolon then a trailing comment",
			sql:  "SELECT 1; -- trailing; comment",
			want: []string{"SELECT 1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := splitStatements(tc.sql)
			if err != nil {
				t.Fatalf("splitStatements: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitStatements = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestSplitStatementsRejectsUnterminatedLiteral(t *testing.T) {
	if _, err := splitStatements("SELECT 'oops;"); err == nil {
		t.Fatal("expected an error for an unterminated string literal")
	}
}

func TestMigrationsCoverBothDialects(t *testing.T) {
	postgres, err := loadMigrations(DriverPostgres)
	if err != nil {
		t.Fatalf("load postgres migrations: %v", err)
	}
	sqlite, err := loadMigrations(DriverSQLite)
	if err != nil {
		t.Fatalf("load sqlite migrations: %v", err)
	}

	if len(postgres) == 0 {
		t.Fatal("no migrations were found")
	}

	postgresVersions := versionsOf(postgres)
	sqliteVersions := versionsOf(sqlite)
	if !reflect.DeepEqual(postgresVersions, sqliteVersions) {
		t.Errorf("dialects disagree: postgres has %v, sqlite has %v — every version needs a file for both", postgresVersions, sqliteVersions)
	}

	for i := 1; i < len(postgresVersions); i++ {
		if postgresVersions[i-1] >= postgresVersions[i] {
			t.Errorf("versions are not ascending: %v", postgresVersions)
		}
	}
}

func TestLoadMigrationsRejectsUnknownDriver(t *testing.T) {
	if _, err := loadMigrations("mysql"); err == nil {
		t.Fatal("expected an error for an unsupported driver")
	}
}

func TestVersionFromName(t *testing.T) {
	version, err := versionFromName("0002_add_thing.postgres.sql")
	if err != nil {
		t.Fatalf("versionFromName: %v", err)
	}
	if version != "0002" {
		t.Errorf("version = %q, want 0002", version)
	}

	for _, name := range []string{"initial.sql", "abc_name.sqlite.sql"} {
		if _, err := versionFromName(name); err == nil {
			t.Errorf("versionFromName(%q) should fail", name)
		}
	}
}

func TestMigrateRecordsAppliedVersions(t *testing.T) {
	database, err := New("sqlite://" + filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer database.Close()

	if err := Migrate(database); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	expected, err := loadMigrations(DriverSQLite)
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if got := countRows(t, database.DB, "schema_migrations"); got != len(expected) {
		t.Fatalf("recorded %d versions, want %d", got, len(expected))
	}

	// A second run must not re-apply anything.
	if err := Migrate(database); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if got := countRows(t, database.DB, "schema_migrations"); got != len(expected) {
		t.Errorf("second run recorded %d versions, want %d", got, len(expected))
	}
}

// A database created by the pre-runner inline DDL has the tables but no
// schema_migrations row, so every statement has to be re-runnable.
func TestMigrateUpgradesDatabaseWithoutVersionTable(t *testing.T) {
	database, err := New("sqlite://" + filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer database.Close()

	if err := Migrate(database); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := database.Exec(`DROP TABLE schema_migrations`); err != nil {
		t.Fatalf("drop schema_migrations: %v", err)
	}

	if err := Migrate(database); err != nil {
		t.Fatalf("Migrate over an existing schema: %v", err)
	}

	expected, err := loadMigrations(DriverSQLite)
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if got := countRows(t, database.DB, "schema_migrations"); got != len(expected) {
		t.Errorf("recorded %d versions, want %d", got, len(expected))
	}
}

// The migration runner records versions, not checksums of the SQL. A 0004 file
// that exists but forgets an ALTER still "applies" cleanly, so pin the resulting
// columns rather than the file's presence. The defaults matter too: a legacy row
// has no host or diff, and a scan that meets NULL where the repository expects a
// string fails long after the migration looked successful.
func TestAgentTargetingMigrationAddsItsColumns(t *testing.T) {
	database, err := New("sqlite://" + filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer database.Close()

	if err := Migrate(database); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	type wantColumn struct {
		kind    string
		notNull int
		dflt    sql.NullString
	}
	emptyDefault := sql.NullString{String: "''", Valid: true}
	wanted := map[string]map[string]wantColumn{
		"agent_runners": {
			"host": {kind: "TEXT", notNull: 1, dflt: emptyDefault},
		},
		"agent_pairing_codes": {
			"host": {kind: "TEXT", notNull: 1, dflt: emptyDefault},
		},
		"agent_runs": {
			"requested_runner_id": {kind: "TEXT", notNull: 0},
			"result_diff":         {kind: "TEXT", notNull: 1, dflt: emptyDefault},
		},
	}

	for table, columns := range wanted {
		rows, err := database.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			t.Fatalf("describe %s: %v", table, err)
		}
		found := map[string]wantColumn{}
		for rows.Next() {
			var cid, notNull, pk int
			var name, kind string
			var dflt sql.NullString
			if err := rows.Scan(&cid, &name, &kind, &notNull, &dflt, &pk); err != nil {
				t.Fatalf("scan %s column: %v", table, err)
			}
			found[name] = wantColumn{kind: kind, notNull: notNull, dflt: dflt}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate %s columns: %v", table, err)
		}
		rows.Close()

		for name, want := range columns {
			got, ok := found[name]
			if !ok {
				t.Errorf("%s has no %q column; it has %d columns", table, name, len(found))
				continue
			}
			if got.kind != want.kind || got.notNull != want.notNull || got.dflt != want.dflt {
				t.Errorf("%s.%s = %+v, want %+v", table, name, got, want)
			}
		}
	}

	// Both drivers must declare the same four columns. SQLite uses a plain
	// ALTER and PostgreSQL an IF NOT EXISTS one, so a driver-specific edit can
	// drift without either migration test noticing.
	for _, driver := range []string{DriverSQLite, DriverPostgres} {
		migrations, err := loadMigrations(driver)
		if err != nil {
			t.Fatalf("load %s migrations: %v", driver, err)
		}
		var sql0004 string
		for _, m := range migrations {
			if m.version == "0004" {
				sql0004 = m.sql
			}
		}
		if sql0004 == "" {
			t.Fatalf("%s has no 0004 migration", driver)
		}
		for _, column := range []string{"agent_runners", "agent_pairing_codes", "requested_runner_id", "result_diff"} {
			if !strings.Contains(sql0004, column) {
				t.Errorf("%s 0004 does not mention %q", driver, column)
			}
		}
	}
}

func versionsOf(migrations []migration) []string {
	versions := make([]string, len(migrations))
	for i, m := range migrations {
		versions[i] = m.version
	}
	return versions
}

func countRows(t *testing.T, database *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}
