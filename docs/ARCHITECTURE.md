# Architecture

Review of the current scaffold, the shape it should take for self-hosted
deployments, and what is still missing. Written October 2026 against the initial
shell of `TaskTrackerServer`.

## Deployment shape, and why it is a monolith

The client is `TaskTrackerKMP`; the two ship together and companies host their
own copy. That single constraint decides the architecture:

- **One binary, one database.** An operator runs `server` with a
  `DATABASE_URL` and a `JWT_SECRET`. Nothing else. A microservice split would
  turn one `docker run` into a compose file, a service mesh, and a set of
  independent migrations — cost with no payoff at this scale.
- **Modular *within* the process.** `internal/` packages are the module
  boundaries; Go's compiler enforces them because `internal/` is importable only
  by this module. That gives the separation microservices would, without the
  network between the pieces.
- **Both databases, chosen at deploy time.** Postgres for a company that already
  runs one, SQLite for a small single-node install.

The scaffold follows this. The problems below are all local to the code, not to
the choice of shape.

## Intended module map

```
cmd/server/main.go        composition root: config → db → migrate → repos → sync → http → shutdown
internal/config           environment configuration
internal/db               driver selection (pgx / sqlite3) + migrations
internal/entities         models and repositories
internal/auth             JWT manager, sign-in/refresh/sign-out handlers, refresh-token store
internal/middleware       Chain, Auth, Logging, CORS, Recover
internal/sync             sync engine and push/pull handlers
internal/dbtest           SQLite- or Postgres-backed database for tests
internal/db/migrations    versioned SQL, one file per driver
```

## Intended request flow

| Endpoint | Method | Middleware | Notes |
| --- | --- | --- | --- |
| `/healthz` | GET | logging, CORS, recover | returns `ok`; does not check the database |
| `/v1/auth/signin` | POST | logging, CORS, recover | email + password → token pair; starts a token family |
| `/v1/auth/refresh` | POST | logging, CORS, recover | rotates the refresh token and returns a new pair |
| `/v1/auth/signout` | POST | logging, CORS, recover | revokes the refresh token's whole family; `204` |
| `/v1/sync/push` | POST | + Auth | apply client changes, return per-entry verdicts |
| `/v1/sync/changes/project` | GET | + Auth | cursor-paged pull |
| `/v1/sync/changes/workItem` | GET | + Auth | cursor-paged pull |

The offline-first sync design is the strongest part of the scaffold and matches
what the client already implements: an append-only `sync_log` keyed by
`(entity, created_at, id)`, opaque JSON payloads, and a `version` string per
entity for optimistic concurrency.

## Verification status

| Check | Result |
| --- | --- |
| `go.mod` parses, `go.sum` complete | yes |
| `gofmt -l .` | clean |
| `go build ./...`, `go vet ./...` | pass |
| `go test ./...` | passes (config only) |
| server on SQLite | boots; signs in; push/pull round-trips a project and work item |

The blockers below were fixed in Milestone 0 and the runtime gaps in Milestone 1
of `docs/PLAN.md`; they are kept here as the record of what was wrong. C1 and C2
in particular are worth reading before writing new SQL.

## Findings

Severity is about what blocks a working server.

### Blockers

- **B1 — `go.mod` was malformed.** `github.com/jackc/pgx/v5/stdlib` was listed
  as its own module; `stdlib` is a package *inside* `pgx/v5`. Go refused to
  parse the file at all (`should be v0 or v1, not v5`). Repaired, `go 1.27`, and
  an indirect requirement pinned to a non-existent `pgservicefile` revision was
  replaced with a real one. `go.sum` now exists.
- **B2 — three packages are imported but empty.** `cmd/server/main.go` imports
  `internal/auth`, `internal/middleware` and `internal/sync`; none contains a
  `.go` file. This alone makes the build fail.
- **B3 — compile errors in the code that does exist.**
  - `internal/entities/project_repository.go` reads and writes
    `Project.Version`, which `models.go` never declares (the other three
    entities do declare it).
  - Unused imports: `time` in `models.go`, `encoding/json` in
    `sprint_repository.go`, `uuid` in `user_repository.go`.
  - `internal/db/db.go` uses `sqlite3.SQLiteDriver` but imports
    `mattn/go-sqlite3` as a blank import.
  - `cmd/server/main.go` imports `mattn/go-sqlite3` twice (once named, once
    blank), which leaves the named import unused, and imports `pgx/v5/stdlib`
    without using it.
- **B4 — composition-root wiring will not type-check.** Repository and engine
  constructors take `*sql.DB`, but `db.New` returns `*db.DB`; `main.go` passes
  `database`, not `database.DB`.

### Correctness

- **C1 — optimistic locking is inverted in every repository.**
  `Update` assigns the *new* version (`p.Version = uuid.New()`) and then filters
  `WHERE id = $1 AND version = $10`, passing the same new version. The stored row
  still holds the *old* version, so the predicate never matches and every update
  fails with "not found or version conflict". The pattern needs the old version
  in  the `WHERE` and the new one in `SET`. Affects project, work item, sprint and
  comment repositories. A second, independent cause hid behind the same symptom:
  SQLite assigns `$N` placeholders by order of appearance, not by name, so
  `UPDATE ... SET x=$2 ... WHERE id=$1` bound the wrong values. Both are fixed;
  SQL must now list placeholders in ascending text order.
- **C2 — timestamps are epoch millis in Go and `TIMESTAMP` in SQL.** Models carry
  `int64` millis; inserts pass those directly into `created_at`/`updated_at`/
  `due_date`, and reads scan a `TIMESTAMP` into `sql.NullInt64`. SQLite tolerates
  this because its types are loose; Postgres rejects both directions. Pick one
  representation and make the DDL agree.
- **C3 — `Project.NextKey` is wrong past 9.** It builds with
  `string(rune(n+'0'))`, so item 10 produces `KEY-:` and 11 produces `KEY-;`
  instead of `KEY-10`. Use `strconv.Itoa`.
- **C4 — `isSQLite` returns true for every driver.**
  `db.QueryRow("SELECT 'sqlite'")` succeeds on Postgres, and the function treats
  any success as "this is SQLite". Postgres therefore receives DDL rewritten for
  SQLite, including `INTEGER PRIMARY KEY AUTOINCREMENT`, which Postgres does not
  accept — so migrations fail on the production database while appearing fine on
  SQLite.

### Database portability and migrations

- **D1 — SQLite loses referential actions.** `adjustForSQLite` strips every
  `ON DELETE CASCADE` / `ON DELETE SET NULL`. SQLite supports both; only the type
  substitutions were needed. Deleting a project now orphans its work items and
  comments instead of cascading.
- **D2 — table creation order.** `work_items` references `sprints(id)` but is
  created before `sprints`. Deferred in SQLite, an error in Postgres.
- **D3 — fixed.** DDL was an inline slice of `CREATE ... IF NOT EXISTS`
  statements, run one at a time outside a transaction, on every start, with no
  version table, no `ALTER`, and a failure midway left a partially built schema.
  The schema now lives in `internal/db/migrations/`, one file per driver sharing
  a version, embedded in the binary and applied by `internal/db/migrate.go`:
  versions are recorded in `schema_migrations`, each runs in its own transaction,
  and statements are split on top-level semicolons rather than guessed. `ALTER`
  migrations can now be added as new numbered versions.
- **D4 — repositories and schema disagree.** Columns read or written but never
  created: `projects.member_ids`, `projects.version`, `comments.version`, and
  `work_items.recurrence` (the DDL has `recurrence_type`/`recurrence_day`/
  `recurrence_until` instead). Membership is modelled twice — the
  `project_members` table and `Project.MemberIDs`.
- **D5 — `sql.Register` inside `New`.** The `sqlite3_custom` registration is
  global; a second call to `db.New` panics with "Register called twice".
- **D6 — one pool setting for both drivers.** `SetMaxOpenConns(25)` is
  reasonable for Postgres and wrong for SQLite with WAL, which wants a single
  writer; concurrent writers here will surface `SQLITE_BUSY`.

### Configuration and operations

- **E1 — fixed.** Config previously defaulted `JWT_SECRET` to a placeholder
  string and never validated it. It now loads an optional `.env` (never
  overriding real environment variables) and refuses to start without a
  ≥32-byte secret.
- **E2 — `/healthz` does not check the database**, so it cannot be used for a
  readiness probe.
- **E3 — fixed.** A refresh token used to stay valid for its full
  `JWT_REFRESH_TTL` (30 days by default) with no way to revoke it. `internal/auth`
  now records refresh tokens in `refresh_tokens` and rotates on every use: the
  presented token is revoked and replaced in one transaction, and presenting an
  already-rotated token revokes its whole family (`POST /v1/auth/signout` does the
  same deliberately). Only a SHA-256 hash of each token's JWT id is stored, so a
  database read does not yield a replayable token. Access tokens stay stateless
  and are not revoked; their short TTL is what bounds them.
- **E4 — nothing to deploy with.** No `Dockerfile`, systemd unit, or
  self-hosting guide, which is the whole point of the project.

## Recommended order of work

1. **C1** — fixes every write path; nothing is testable until updates succeed.
2. **C2 + D4** — settle the time representation and align schema with
   repositories; this is the largest single source of runtime failure.
3. **B3 + B4** — get `internal/entities` and `internal/db` compiling and wire
   `main.go` correctly.
4. **C4 + D1 + D2** — make the Postgres path real. Done: the driver comes from
   the URL, and the suite runs against Postgres in CI.
5. **D3** — replace inline DDL with a versioned migration runner. Done in
   Milestone 2 (T2.1–T2.3).
6. **B2** — implement `auth`, `middleware` and `sync`.
7. **E2/E3/E4** — readiness probe, refresh-token rotation, deploy artifacts.

Steps 1–3 are the blocking Milestone 1 of `docs/PLAN.md`. The build is green
today, so a red CI run is a real regression; the runtime failures Milestone 1
tracks are known and are not build failures.
