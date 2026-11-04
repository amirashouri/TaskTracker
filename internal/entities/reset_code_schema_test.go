package entities

import (
	"context"
	"testing"
	"time"
)

// The baseline declares expires_at and created_at as BIGINT on both drivers.
// A Postgres draft once declared them `timestamp with time zone`, and every
// epoch-millisecond insert failed with a 500 from the admin endpoint. This
// asserts the schema a fresh install actually builds, on whichever driver the
// suite is pointed at, rather than the upgrade repair that used to fix a
// drifted database — no such database exists before the first release.
func TestBaselineResetCodeColumnsAcceptEpochMillis(t *testing.T) {
	database := newTestDB(t)

	repo := NewUserRepository(database.DB)
	user := mustCreateUser(t, repo, "reset@example.com")

	code, err := repo.GeneratePasswordResetCode(context.Background(), user.ID, time.Hour)
	if err != nil {
		t.Fatalf("GeneratePasswordResetCode on %s: %v", database.Driver, err)
	}
	ok, err := repo.VerifyResetCode(context.Background(), user.ID, code)
	if err != nil {
		t.Fatalf("VerifyResetCode on %s: %v", database.Driver, err)
	}
	if !ok {
		t.Error("a freshly minted code was not accepted")
	}
}
