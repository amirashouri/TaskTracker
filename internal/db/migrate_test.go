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

// migrateBeforeSQLite returns a freshly migrated SQLite database whose schema
// stops just short of target. It is how a test can insert the legacy row a
// backfill is supposed to see: a fully migrated database has no such row to
// write.
func migrateBeforeSQLite(t *testing.T, target string) *DB {
	t.Helper()
	database, err := New("sqlite://" + filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at BIGINT NOT NULL
	)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	migrations, err := loadMigrations(DriverSQLite)
	if err != nil {
		t.Fatalf("load sqlite migrations: %v", err)
	}
	for _, m := range migrations {
		if m.version >= target {
			break
		}
		if err := applyMigration(database, m); err != nil {
			t.Fatalf("apply %s: %v", m.version, err)
		}
	}
	return database
}

func applyNamedMigration(t *testing.T, database *DB, version string) {
	t.Helper()
	migrations, err := loadMigrations(database.Driver)
	if err != nil {
		t.Fatalf("load %s migrations: %v", database.Driver, err)
	}
	for _, m := range migrations {
		if m.version != version {
			continue
		}
		if err := applyMigration(database, m); err != nil {
			t.Fatalf("apply %s: %v", version, err)
		}
		return
	}
	t.Fatalf("%s has no migration %s", database.Driver, version)
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

// 0008 adds the epic tables and replaces sync_log's single project column with
// the visibility join. Pin the end state on SQLite and check both dialect files
// mention every object, because a driver-specific edit can drift unnoticed.
func TestEpicMigrationAddsItsObjects(t *testing.T) {
	database, err := New("sqlite://" + filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer database.Close()

	if err := Migrate(database); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	for _, table := range []string{"epics", "epic_projects", "sync_log_projects"} {
		var name string
		if err := database.QueryRow(
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = $1`, table).Scan(&name); err != nil {
			t.Errorf("%s table is missing: %v", table, err)
		}
	}

	// Selecting each column is what proves it exists, rather than the file
	// mentioning it somewhere.
	for _, query := range []string{
		`SELECT epic_id FROM work_items LIMIT 0`,
		`SELECT log_id, project_id FROM sync_log_projects LIMIT 0`,
	} {
		rows, err := database.Query(query)
		if err != nil {
			t.Errorf("%s: %v", query, err)
			continue
		}
		rows.Close()
	}

	// The single project column is replaced, not kept beside the join: two
	// answers to "who may see this row" can disagree.
	rows, err := database.Query(`PRAGMA table_info(sync_log)`)
	if err != nil {
		t.Fatalf("describe sync_log: %v", err)
	}
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan sync_log column: %v", err)
		}
		if name == "project_id" {
			t.Error("sync_log still has project_id; visibility must come only from sync_log_projects")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sync_log columns: %v", err)
	}
	rows.Close()

	for _, driver := range []string{DriverSQLite, DriverPostgres} {
		migrations, err := loadMigrations(driver)
		if err != nil {
			t.Fatalf("load %s migrations: %v", driver, err)
		}
		var sql0008 string
		for _, m := range migrations {
			if m.version == "0008" {
				sql0008 = m.sql
			}
		}
		if sql0008 == "" {
			t.Fatalf("%s has no 0008 migration", driver)
		}
		for _, object := range []string{"epics", "epic_projects", "sync_log_projects", "epic_id"} {
			if !strings.Contains(sql0008, object) {
				t.Errorf("%s 0008 does not mention %q", driver, object)
			}
		}
	}
}

// 0009 must adopt an existing epics table: add the column, then give every
// ownerless epic the oldest non-deleted admin. A deleted admin created earlier
// must not win, because it can no longer act as an owner.
func TestEpicOwnerMigrationBackfillsAdmins(t *testing.T) {
	database := migrateBeforeSQLite(t, "0009")

	legacy := "11111111-1111-1111-1111-111111111111"
	deletedAdmin := "22222222-2222-2222-2222-222222222222"
	activeAdmin := "33333333-3333-3333-3333-333333333333"
	for _, user := range []struct {
		id, email, role, status string
		createdAt               int64
	}{
		{deletedAdmin, "gone@example.com", "admin", "deleted", 1},
		{activeAdmin, "first@example.com", "admin", "active", 2},
	} {
		if _, err := database.Exec(`INSERT INTO users
			(id, email, password_hash, role, status, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			user.id, user.email, "x", user.role, user.status, user.createdAt); err != nil {
			t.Fatalf("insert user %s: %v", user.id, err)
		}
	}
	if _, err := database.Exec(`INSERT INTO epics
		(id, title, details, is_archived, created_at, updated_at, version)
		VALUES ($1, 'Legacy', '', 0, 1, 1, '')`, legacy); err != nil {
		t.Fatalf("insert legacy epic: %v", err)
	}

	applyNamedMigration(t, database, "0009")

	var owner sql.NullString
	if err := database.QueryRow(`SELECT owner_id FROM epics WHERE id = $1`, legacy).Scan(&owner); err != nil {
		t.Fatalf("read owner_id: %v", err)
	}
	if !owner.Valid || owner.String != activeAdmin {
		t.Errorf("backfilled owner = %+v, want %s", owner, activeAdmin)
	}

	assertEpicOwnerMigrationMentionsBothDialects(t, "owner_id")
}

func TestEpicOwnerMigrationLeavesOwnerNullWithoutAdmin(t *testing.T) {
	database := migrateBeforeSQLite(t, "0009")
	legacy := "44444444-4444-4444-4444-444444444444"
	if _, err := database.Exec(`INSERT INTO epics
		(id, title, details, is_archived, created_at, updated_at, version)
		VALUES ($1, 'Legacy', '', 0, 1, 1, '')`, legacy); err != nil {
		t.Fatalf("insert legacy epic: %v", err)
	}

	applyNamedMigration(t, database, "0009")

	var owner sql.NullString
	if err := database.QueryRow(`SELECT owner_id FROM epics WHERE id = $1`, legacy).Scan(&owner); err != nil {
		t.Fatalf("read owner_id: %v", err)
	}
	if owner.Valid {
		t.Errorf("owner = %q with no admin in the directory, want NULL", owner.String)
	}
}

func assertEpicOwnerMigrationMentionsBothDialects(t *testing.T, object string) {
	t.Helper()
	for _, driver := range []string{DriverSQLite, DriverPostgres} {
		migrations, err := loadMigrations(driver)
		if err != nil {
			t.Fatalf("load %s migrations: %v", driver, err)
		}
		var sql0009 string
		for _, m := range migrations {
			if m.version == "0009" {
				sql0009 = m.sql
			}
		}
		if sql0009 == "" {
			t.Fatalf("%s has no 0009 migration", driver)
		}
		if !strings.Contains(sql0009, object) {
			t.Errorf("%s 0009 does not mention %q", driver, object)
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
