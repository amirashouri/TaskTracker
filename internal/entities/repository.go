package entities

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// scanner is satisfied by both *sql.Row and *sql.Rows. Sharing it is what lets
// one per-entity scan function serve the single-row and the list paths; keeping
// those on separate code is how their column order drifted apart.
type scanner interface {
	Scan(dest ...any) error
}

// errUserNotFound is what the user repository reports when a write matched no
// row. Users carry no version column, so it is not a conflict — the row simply
// is not there.
var errUserNotFound = errors.New("user not found")

// orNewID keeps the id a caller supplied, or mints one. Every entity id in this
// package is a string type, so one constraint covers them all.
func orNewID[T ~string](id T, generate func() T) T {
	if id == "" {
		return generate()
	}
	return id
}

// newVersion mints the version a write stamps on a row. It is a random string
// rather than a counter so a client cannot guess the value that would win the
// next compare-and-swap.
func newVersion() string {
	return uuid.New().String()
}

// createStamp returns the timestamps and version a new row starts with. A
// caller that already set CreatedAt keeps it — the sync engine replays entities
// that carry the time the client authored them.
func createStamp(createdAt Timestamp) (Timestamp, Timestamp, string) {
	now := Now()
	if createdAt == 0 {
		createdAt = now
	}
	return createdAt, now, newVersion()
}

// conflict is the error a compare-and-swap reports when the row moved under the
// caller, tagged so a log line names which write lost.
func conflict[T ~string](entity string, id T) error {
	return fmt.Errorf("%w: %s %s", ErrVersionConflict, entity, id)
}

// execOneRow runs a write that must affect exactly one row: the version
// compare-and-swap behind every entity Update, and the plain UPDATEs behind the
// user repository. missing is what the caller wants back when no row matched.
// It surfaces the RowsAffected error, which three user-repository methods used
// to discard.
func execOneRow(ctx context.Context, q Querier, missing error, query string, args ...any) error {
	result, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return missing
	}
	return nil
}

// oneOrNil scans a single row through scan and maps "no such row" to (nil, nil),
// the contract every GetByID in this package offers.
func oneOrNil[T any](row *sql.Row, scan func(scanner, *T) error) (*T, error) {
	var v T
	err := scan(row, &v)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// collectRows drains rows through scan, which reads one row at a time. A
// mid-iteration failure is reported rather than returned as a short list: for
// the sync engine a truncated change set is worse than an error.
func collectRows[T any](rows *sql.Rows, scan func(scanner, *T) error) ([]*T, error) {
	var out []*T
	for rows.Next() {
		var v T
		if err := scan(rows, &v); err != nil {
			return nil, err
		}
		out = append(out, &v)
	}
	return out, rows.Err()
}
