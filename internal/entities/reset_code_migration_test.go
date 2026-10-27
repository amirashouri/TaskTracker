package entities

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/db"
	"github.com/amirashouri/tasktracker-server/internal/dbtest"
)

// A database migrated by an earlier draft of 0009 kept `timestamp with time
// zone` columns for the reset code, because the runner records a version once
// and `CREATE TABLE IF NOT EXISTS` can never correct the table afterwards. On
// such a database every reset-code insert failed — "column expires_at is of
// type timestamp with time zone but expression is of type bigint" — and the
// admin endpoint answered 500 with no diagnostic. 0011 rebuilds those columns;
// this is the test that would have caught the drift, because the suite
// otherwise builds every database from the current files and cannot see it.
//
// Postgres only: SQLite stores the int64 regardless of the declared type, so
// there is nothing to repair there.
func TestMigrateRepairsDriftedResetCodeTimestamps(t *testing.T) {
	if os.Getenv(dbtest.PostgresURLEnv) == "" {
		t.Skipf("set %s to run: the drift this repairs is Postgres-only", dbtest.PostgresURLEnv)
	}

	database := newTestDB(t)

	// Put the table back the way the drifted draft left it, and forget the
	// repair so Migrate applies it again — the state of a real upgraded
	// install, where 0009 is recorded and 0011 has never run.
	if _, err := database.Exec(`DROP TABLE password_reset_codes`); err != nil {
		t.Fatalf("drop password_reset_codes: %v", err)
	}
	if _, err := database.Exec(`CREATE TABLE password_reset_codes (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		code TEXT NOT NULL,
		user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
		consumed BOOLEAN NOT NULL DEFAULT FALSE,
		created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now()
	)`); err != nil {
		t.Fatalf("recreate the drifted table: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM schema_migrations WHERE version = '0011'`); err != nil {
		t.Fatalf("forget the repair: %v", err)
	}

	if err := db.Migrate(database); err != nil {
		t.Fatalf("Migrate over a drifted password_reset_codes: %v", err)
	}

	// The two operations the admin endpoint and the change-password sheet
	// perform: write an epoch-millisecond expiry, then read it back as one.
	repo := NewUserRepository(database.DB)
	user := mustCreateUser(t, repo, "drift@example.com")

	code, err := repo.GeneratePasswordResetCode(context.Background(), user.ID, time.Hour)
	if err != nil {
		t.Fatalf("GeneratePasswordResetCode after the repair: %v", err)
	}
	ok, err := repo.VerifyResetCode(context.Background(), user.ID, code)
	if err != nil {
		t.Fatalf("VerifyResetCode after the repair: %v", err)
	}
	if !ok {
		t.Error("a freshly minted code was not accepted after the repair")
	}
}
