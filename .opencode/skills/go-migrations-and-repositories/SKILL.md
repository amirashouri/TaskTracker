---
name: go-migrations-and-repositories
description: Use when changing a table, a migration, a repository, or an entity field in TaskTrackerServer. Covers the inline DDL in internal/db, Postgres vs SQLite differences, optimistic locking, and time representation.
related:
  - go-server-architecture
  - go-config-and-secrets
---

# Migrations and repositories

Read `docs/ARCHITECTURE.md` first: the current DDL and repositories do not agree,
and the schema is not versioned yet. Confirm what you are changing against that
list before you edit.

## Where the schema lives

DDL is an inline slice in `internal/db/db.go`, executed as
`CREATE ... IF NOT EXISTS` on every startup. The `migrations/` directory is
empty and is the intended home for a real, versioned runner. Until that exists:

- Change the DDL and the repository mapping **in the same commit**. A column
  added to one and not the other is the current default failure mode.
- Migrations run one statement at a time, outside a transaction, so a failure
  partway leaves a partial schema. Keep a change to a single statement where you
  can, and never assume a prior migration in the same run succeeded.
- There is no `ALTER` path yet. Adding a column to an existing database will not
  happen on startup; note it explicitly for the operator.

## Two drivers, one schema

The same DDL runs on Postgres and SQLite. `adjustForSQLite` rewrites the Postgres
text for SQLite. Treat that rewrite as dangerous:

- `isSQLite` is currently broken — it runs `SELECT 'sqlite'`, which succeeds on
  Postgres, so Postgres is rewritten with SQLite DDL (`INTEGER PRIMARY KEY
  AUTOINCREMENT`, which Postgres rejects). Verify the detection before trusting
  any "it works on SQLite" result for the Postgres path.
- Stripping `ON DELETE CASCADE` / `SET NULL` is not required for SQLite and
  silently changes delete behaviour.
- Table creation order matters on Postgres: a `REFERENCES` to a table created
  later is an error there, though SQLite defers it.
- `UUID[]` and `JSONB` are Postgres-only; SQLite stores them as `TEXT`.

Rule: a schema change is verified only when it has been run against **both**
drivers. SQLite passing proves the SQLite path, nothing more.

## Repositories

A repository is the only place that knows SQL. It takes a `*sql.DB`, uses
`$1`-style placeholders (both drivers accept them), and returns entities or
`nil, nil` for a missing row.

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

The scaffold gets this backwards in every repository — it filters on the *new*
version, so no update ever matches. Do not copy the existing `Update` methods;
fix the one you touch.

## Time representation

Models store epoch milliseconds as `int64`; the DDL declares `TIMESTAMP`. This
works on SQLite (loose types) and fails on Postgres in both directions. Pick one:

- **Recommended:** keep `int64` millis and use `BIGINT` columns, or
- move the models to `time.Time` and keep `TIMESTAMP`.

Do not add a new field that mixes the two.

## Adding a table or column

1. Add the DDL to the slice in `internal/db/db.go`, in an order that works on
   Postgres (referenced tables first).
2. Add the field to the entity in `internal/entities/models.go`.
3. Update every `SELECT`/`INSERT`/`UPDATE` in the repository that maps it.
4. If it needs a version, follow the optimistic-locking pattern above.
5. Run against both drivers before claiming it works.
