---
name: go-migrations-and-repositories
description: Use when changing a table, a migration, a repository, or an entity field in TaskTrackerServer. Covers the versioned per-driver migrations in internal/db/migrations, Postgres vs SQLite differences, optimistic locking, and time representation.
---

# Migrations and repositories

Read `docs/ARCHITECTURE.md` for the design context, then verify current
repository and migration behavior in code. Change the DDL and repository
mapping **in the same commit**; a column added to one and not the other is a
recurring failure mode.

## Where the schema lives

`internal/db/migrations/` holds the versioned schema, one file per driver sharing
a version number:

```
0001_initial.postgres.sql
0001_initial.sqlite.sql
```

`internal/db/migrate.go` embeds them with `go:embed`, loads the files for the
driver on `DB.Driver` in version order, and applies each unapplied version in its
own transaction, recording it in `schema_migrations` inside the same transaction.
Because the version is recorded with the statements, a failed migration leaves
nothing half-applied and is retried cleanly on the next start.

To add a migration:

1. Create the next number for **both** drivers. A version with only one dialect
   file fails `TestMigrationsCoverBothDialects` — that is the guardrail, not a
   nuisance to work around.
2. Keep every statement `IF NOT EXISTS` (or write a guarded `ALTER`). A database
   created before the runner existed has the tables but no `schema_migrations`
   row, so `0001` is re-run over it and must be re-runnable.
3. Statements are split on top-level semicolons by `splitStatements`, which
   ignores semicolons inside string literals and line comments. You do not need
   one statement per run, and you do not need to escape anything.

## Two drivers, one schema

Each driver has its own file, so there is no string rewriting and no probing the
server for its type. Write the DDL that driver actually accepts:

- SQLite spells UUID columns `TEXT`; Postgres uses `UUID` (and, for a client
  that generates IDs offline, does **not** need `DEFAULT gen_random_uuid()`).
- `JSONB` is Postgres-only; SQLite stores the same values as `TEXT`.
- SQLite's `INTEGER PRIMARY KEY AUTOINCREMENT` is what Postgres' `BIGSERIAL`
  means. Do not copy one into the other.
- Table creation order matters on Postgres: a `REFERENCES` to a table created
  later is an error there, though SQLite defers it. Create referenced tables
  first.
- Keep `ON DELETE CASCADE` / `SET NULL` on both. SQLite enforces them (the
  driver sets `_fk=1` and `PRAGMA foreign_keys = ON`), and dropping them silently
  changes delete behaviour.

Rule: a schema change is verified only when it has been run against **both**
drivers. SQLite passing proves the SQLite path, nothing more. Run the suite with
`TASKTRACKER_TEST_POSTGRES_URL` set (see *Verifying* in `README.md`).

## Repositories

A repository is the only place that knows SQL. It takes a `*sql.DB`, uses
`$1`-style placeholders (both drivers accept them), and returns entities or
`nil, nil` for a missing row.

### Placeholders must ascend in text order

SQLite numbers `$N` placeholders by **order of appearance**, not by name: in
`SELECT $2, $1`, `$2` binds to the first argument. Postgres numbers them by the
written index. The two agree only when the placeholders appear in ascending
order, so write every statement that way — a `SET` list before a `WHERE id = $1`
is the classic trap, and it fails silently by matching zero rows.

```go
// Wrong on SQLite: $1 appears last, so every value binds to the wrong column.
`UPDATE projects SET name=$2, version=$3 WHERE id=$1`

// Right on both drivers.
`UPDATE projects SET name=$1, version=$2 WHERE id=$3`
```

```go
func (r *ProjectRepository) GetByID(ctx context.Context, id ProjectID) (*Project, error) {
	row := r.db.QueryRowContext(ctx, `SELECT ... FROM projects WHERE id = $1`, id)
	// scan; sql.ErrNoRows → nil, nil
}
```

## Optimistic locking

Every entity carries a `version` string. The rule is: capture the version you
read, put that **old** version in the `WHERE`, and write a **new** version into
`SET`.

```go
func (r *ProjectRepository) Update(ctx context.Context, p *Project) error {
	previous := p.Version                 // the version the caller read
	p.Version = uuid.New().String()       // the version to store
	p.UpdatedAt = time.Now().UnixMilli()

	res, err := r.db.ExecContext(ctx, `
		UPDATE projects SET ..., version=$2, updated_at=$3
		WHERE id=$1 AND version=$4`,
		p.ID, p.Version, p.UpdatedAt, previous)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrVersionConflict
	}
	return nil
}
```

Filtering on the *new* version instead makes every update match nothing. The
repositories do this correctly; keep the pattern when you add or touch one.

## Time representation

Settled: models store epoch milliseconds as `int64` and the DDL declares `BIGINT`
(`TIMESTAMP` in Postgres rejects an `int64` in both directions; SQLite tolerates
it only because its types are loose). Read and write millis through the helpers
the repositories already use, and do not add a field that mixes the two.

## Adding a table or column

1. Add a new numbered migration under `internal/db/migrations/`, for both
   drivers, in an order that works on Postgres (referenced tables first).
2. Add the field to the entity in `internal/entities/models.go`.
3. Update every `SELECT`/`INSERT`/`UPDATE` in the repository that maps it.
4. If it needs a version, follow the optimistic-locking pattern above.
5. Run against both drivers before claiming it works.
