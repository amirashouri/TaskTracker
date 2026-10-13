// This file lives in the external db_test package so it can use internal/dbtest,
// which imports internal/db and would otherwise form a cycle with an internal
// test. It is what gives the Postgres dialect migration coverage: the package's
// own tests build their databases with sqlite:// directly.
package db_test

import (
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/db"
	"github.com/amirashouri/tasktracker-server/internal/dbtest"
)

func TestMigrateIsIdempotentOnEveryDriver(t *testing.T) {
	// dbtest has already migrated this database once.
	database := dbtest.NewDatabase(t)

	before := countVersions(t, database)
	if before == 0 {
		t.Fatal("dbtest did not record any migration version")
	}

	if err := db.Migrate(database); err != nil {
		t.Fatalf("re-running Migrate on %s: %v", database.Driver, err)
	}

	if after := countVersions(t, database); after != before {
		t.Errorf("recorded versions went from %d to %d; a second run re-applied a migration", before, after)
	}
}

func countVersions(t *testing.T, database *db.DB) int {
	t.Helper()
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	return count
}
