package entities

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mattn/go-sqlite3"
)

// The verdict decides whether a client's change is dead-lettered, so the policy
// is pinned code by code rather than by one representative case: a value the row
// cannot hold is permanent, and anything a retry could still fix is not.
func TestPermanentWriteErrorClassifiesBothDrivers(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"postgres check violation", &pgconn.PgError{Code: "23514"}, true},
		{"postgres not-null violation", &pgconn.PgError{Code: "23502"}, true},
		{"postgres exclusion violation", &pgconn.PgError{Code: "23P01"}, true},
		{"postgres invalid text representation", &pgconn.PgError{Code: "22P02"}, true},
		{"postgres string too long", &pgconn.PgError{Code: "22001"}, true},
		{"postgres foreign key violation", &pgconn.PgError{Code: "23503"}, false},
		{"postgres unique violation", &pgconn.PgError{Code: "23505"}, false},
		{"postgres serialization failure", &pgconn.PgError{Code: "40001"}, false},
		{"postgres deadlock", &pgconn.PgError{Code: "40P01"}, false},
		{"postgres undefined column", &pgconn.PgError{Code: "42703"}, false},
		{"sqlite check violation", sqlite3.Error{Code: sqlite3.ErrConstraint, ExtendedCode: sqlite3.ErrConstraintCheck}, true},
		{"sqlite not-null violation", sqlite3.Error{Code: sqlite3.ErrConstraint, ExtendedCode: sqlite3.ErrConstraintNotNull}, true},
		{"sqlite foreign key violation", sqlite3.Error{Code: sqlite3.ErrConstraint, ExtendedCode: sqlite3.ErrConstraintForeignKey}, false},
		{"sqlite unique violation", sqlite3.Error{Code: sqlite3.ErrConstraint, ExtendedCode: sqlite3.ErrConstraintUnique}, false},
		{"sqlite database is locked", sqlite3.Error{Code: sqlite3.ErrLocked}, false},
		{"unclassified", errors.New("connection reset by peer"), false},
		{"wrapped permanent", fmt.Errorf("apply work item: %w", &pgconn.PgError{Code: "23514"}), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PermanentWriteError(tc.err); got != tc.want {
				t.Errorf("PermanentWriteError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
