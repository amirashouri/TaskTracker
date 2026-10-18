package entities

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mattn/go-sqlite3"
)

// PermanentWriteError reports whether a repository write failed in a way that
// re-sending the same payload can never get past.
//
// The sync engine answers `rejected` for these and `retry_later` for everything
// else, which makes this the classification that decides whether a client's
// change is dead-lettered — and `rejected` is terminal on the client. The set is
// therefore narrow on purpose: it holds only errors that are the *row's own
// values*, a value a CHECK refuses or a required column left null. Everything a
// retry could still fix stays out. A foreign-key violation may be a child that
// arrived before its parent, a unique violation may be a re-push after a lost
// response, and a serialization failure or a deadlock is transient by
// definition.
//
// The driver's error code is SQL, so it is read here rather than in the engine
// that acts on the verdict.
func PermanentWriteError(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		// Class 22 is "data exception": a value the column cannot hold, such as
		// invalid input syntax or a value too long for the type.
		case strings.HasPrefix(pgErr.Code, "22"):
			return true
		case pgErr.Code == "23502", // not_null_violation
			pgErr.Code == "23514", // check_violation
			pgErr.Code == "23P01": // exclusion_violation
			return true
		}
		return false
	}

	// go-sqlite3 returns its Error by value, not by pointer, so errors.As needs
	// the concrete type. Only the two constraints that describe the row's own
	// values are permanent; its foreign-key and unique violations are left to
	// the engine to retry, on the same reasoning as Postgres above.
	var liteErr sqlite3.Error
	if errors.As(err, &liteErr) {
		switch liteErr.ExtendedCode {
		case sqlite3.ErrConstraintCheck, sqlite3.ErrConstraintNotNull:
			return true
		}
	}
	return false
}
