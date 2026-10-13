package entities

import (
	"context"
	"database/sql"
)

// Querier is satisfied by both *sql.DB and *sql.Tx. A repository holds one so
// the same code runs on the pool or inside a caller's transaction — which is
// what lets the sync engine commit an entity write and its log row together.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// inTx runs fn inside a transaction. When q is already a transaction it reuses
// it, so a repository called from the sync engine joins that transaction rather
// than opening a second one — a nested BeginTx would deadlock SQLite's single
// writer. When q is a pool, fn gets its own transaction.
func inTx(ctx context.Context, q Querier, fn func(Querier) error) error {
	if tx, ok := q.(*sql.Tx); ok {
		return fn(tx)
	}
	db, ok := q.(*sql.DB)
	if !ok {
		// Not a pool or a transaction: run directly, with no atomicity to offer.
		return fn(q)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
