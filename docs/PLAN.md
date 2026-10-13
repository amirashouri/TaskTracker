# Plan

The implementation roadmap. Each task is self-contained, ordered, and sized for
one branch and one merge request. `docs/ARCHITECTURE.md` explains *why* things
are the way they are; this file is *what to do next*.

## How to use this file

- Tasks are numbered `T<milestone>.<n>` and are meant to be done **in order**
  within a milestone. A task's `Depends on` line names anything that must land
  first.
- One task = one `feat/` branch = one MR (see `AGENTS.md`).
- Every task ends with the same command and it must pass before you open the MR:

  ```bash
  gofmt -l . && go build ./... && go vet ./... && go test ./...
  ```

  `gofmt -l .` must print nothing. If a task's change is not covered by a test,
  say so in the MR — do not leave it unverified silently.
- Do not bundle tasks. Do not fix a neighbour's bug inside a task ("no drive-by
  cleanup"); record it here or in the MR description instead.
- When a task changes the database, change the DDL **and** every repository that
  maps the affected columns in the same commit.

## Status

| Milestone | State |
| --- | --- |
| 0 — Compiling baseline | **Done** |
| 1 — Data-layer integrity | Not started (blocking everything below) |
| 2 — Versioned migrations | Not started |
| 3 — Auth and security | Not started |
| 4 — Sync engine | Not started |
| 5 — Observability and ops | Not started |
| 6 — Testing | Partially (config only) |
| 7 — Client contract | Not started |

---

## Milestone 0 — Compiling baseline (done)

Shipped:

- `go.mod` on Go 1.27 with a valid, complete `go.sum`.
- `internal/config` — environment config seeded from `.env`, fail-fast
  `JWT_SECRET` validation, with tests.
- `internal/httpx` — `WriteJSON`, `WriteError`, `DecodeJSON`, one error envelope.
- `internal/auth` — `JWTManager` (`IssueAccess`, `IssueRefresh`, `Parse`),
  token-type separation, `WithUser`/`UserFrom`, and the `SignIn`/`Refresh`
  handlers.
- `internal/middleware` — `Middleware`, `Chain`, `Logging`, `Recover`, `CORS`,
  `Auth`.
- `internal/sync` — `Engine` (`Push`, `Pull`) and `Handler` (`Push`,
  `PullProject`, `PullWorkItem`) over the existing repositories and `sync_log`.
- `cmd/server` — wires all of the above; repositories now receive `database.DB`.
- Two portability repairs in `internal/db/db.go`: the sqlite3 import was blank
  but referenced by name, and `adjustForSQLite` now maps `DEFAULT NOW()` to
  `DEFAULT CURRENT_TIMESTAMP` (without it, SQLite migrations failed on the very
  first statement).

Verified by hand:

- `gofmt -l .` clean; `go build ./...`, `go vet ./...`, `go test ./...` all exit 0.
- The binary boots on SQLite, `/healthz` returns `ok`, `POST /v1/auth/signin`
  returns `400` on a bad body and `401` on bad credentials, protected routes
  return `401` without or with a bad token, and SIGTERM shuts down cleanly.

**Not working yet — this is what Milestone 1 fixes.** Reads of any `TIMESTAMP`
column fail on SQLite:

```
sql: Scan error on column index 8, name "created_at":
converting driver.Value type time.Time ("2026-10-13 10:00:22.935 +0000 UTC")
to a int64: invalid syntax
```

Because of that, sign-in can never succeed even for a correctly seeded user.
Writes to `projects` and `work_items` also fail, because the repositories name
columns the DDL never creates.

---

## Milestone 1 — Data-layer integrity (blocking)

Nothing above this milestone can be trusted until it is done. Every task here
touches `internal/db/db.go` and/or `internal/entities/*`.

### T1.1 — Settle the time representation

**Goal:** one representation for every time column, matching between Go and SQL.

**Files:** `internal/entities/models.go`, `internal/db/db.go`,
every `internal/entities/*_repository.go`.

**Why it is broken:** models carry epoch milliseconds as `int64`, the DDL
declares `TIMESTAMP`. On SQLite the driver sees the declared type and converts
the stored integer back to a `time.Time` on read, which cannot scan into
`int64`; on Postgres the insert (`bigint` → `timestamp`) is rejected outright.
`TestLoad...`/sign-in fail on the read path for exactly this reason.

**Steps:**

1. Choose millis as canonical. Keep `int64` millis in the models (they are
   already part of the wire format the client uses — do not change the JSON).
2. In `db.go`, change every time column from `TIMESTAMP` to `BIGINT`:
   `users.last_seen`, `users.created_at`, `projects.created_at`,
   `projects.updated_at`, `work_items.due_date`, `work_items.recurrence_until`,
   `work_items.resolved_at`, `work_items.created_at`, `work_items.updated_at`,
   `sprints.start_date`, `sprints.end_date`, `sprints.finished_at`,
   `sprints.created_at`, `sprints.updated_at`, `comments.created_at`,
   `sync_log.created_at`.
3. In `adjustForSQLite`, `BIGINT` is already valid SQLite; confirm no rewrite is
   needed for these columns.
4. Leave the Go structs and JSON tags unchanged.

**Done when:** a user can be created and read back, and `created_at` round-trips
as the same millisecond value. Add a test (T6.2) that fails without this change.

**Depends on:** nothing. **Blocks:** T1.2, T1.3, and every runtime test after it.

### T1.2 — Reconcile the schema with the repositories

**Goal:** every column a repository names exists in the DDL.

**Files:** `internal/db/db.go`, `internal/entities/{project,comment,workitem}_repository.go`.

**The drift, precisely:**

| Repository uses | DDL currently has | Fix |
| --- | --- | --- |
| `projects.member_ids` | nothing (membership lives in `project_members`) | drop the column and stop writing it; read members via `GetMembers` |
| `projects.version` | nothing | add `version TEXT NOT NULL` |
| `work_items.recurrence` | `recurrence_type`, `recurrence_day`, `recurrence_until` | replace the three columns with `recurrence TEXT` (the model stores one JSON `Recurrence`) |
| `comments.version` | nothing | add `version TEXT NOT NULL` |

**Steps:**

1. Add `version TEXT NOT NULL DEFAULT ''` to `projects` and `comments`.
2. Replace the three `recurrence_*` columns with a single `recurrence TEXT`.
3. Decide `member_ids`: the clean answer is to remove `Project.MemberIDs` from
   the SQL path entirely and let `project_members` be the only source of truth.
   If you keep the field for the wire format, populate it from `GetMembers`
   rather than from a column. Do not keep two writable copies.
4. Update the `SELECT`/`INSERT`/`UPDATE` statement text to match.

**Done when:** creating, reading, updating and deleting a project, work item,
sprint and comment each succeed on SQLite, and a round-trip preserves every
field.

**Depends on:** T1.1.

### T1.3 — Fix optimistic locking in all four repositories

**Goal:** a stale write is rejected, a fresh write succeeds.

**Files:** `internal/entities/{project,workitem,sprint,comment}_repository.go`.

**Why it is broken:** each `Update` assigns the new version and then filters
`WHERE id = $1 AND version = <same new version>`. The stored row holds the old
version, so the predicate never matches and *every* update fails. Follow the
correct pattern in `.opencode/skills/go-migrations-and-repositories/SKILL.md`:
capture the caller's version, write a new one, compare against the captured one,
and return a typed conflict error when `RowsAffected() == 0`.

**Steps:**

1. Capture `previous := p.Version` before overwriting.
2. Use `previous` in the `WHERE`, the new version in `SET`.
3. Return a shared sentinel (e.g. `entities.ErrVersionConflict`) instead of a
   formatted string, so `internal/sync` can distinguish a conflict from a
   transport failure.

**Done when:** two updates from the same starting version — the first succeeds,
the second returns the conflict error.

**Depends on:** T1.1, T1.2.

### T1.4 — Preserve client-supplied IDs

**Goal:** an entity created from a client payload keeps the client's UUID.

**Files:** all `Create` methods in `internal/entities/*_repository.go`.

**Why it matters:** the client generates IDs offline. `Create` currently
overwrites `ID` with a fresh UUID, so the row's primary key will not match the
`entityId` in its own `sync_log` entry — the next pull has nothing to resolve.

**Steps:** assign a new ID only when the caller did not supply one:
`if w.ID == "" { w.ID = NewWorkItemID() }`. Apply to project, work item, sprint,
comment and user.

**Done when:** `Create` with a pre-set ID stores that exact ID.

**Depends on:** T1.2.

### T1.5 — Fix driver detection

**Goal:** `isSQLite` answers correctly, so Postgres is never rewritten as SQLite.

**Files:** `internal/db/db.go`.

**Why it is broken:** it runs `SELECT 'sqlite'`, which succeeds on *every*
driver, and returns true. Postgres therefore receives SQLite DDL including
`INTEGER PRIMARY KEY AUTOINCREMENT`, which Postgres rejects — migrations fail on
the production database.

**Steps:** the driver name is already known in `New` (`"pgx"` or `"sqlite3"`);
carry it on `DB.Driver` and branch on it in `Migrate` instead of probing the
connection. Change `Migrate` to take the `*DB` (or a driver string) rather than a
bare `*sql.DB`.

**Done when:** a Postgres run applies the Postgres DDL unmodified; add a unit
test for the branch.

**Depends on:** T1.1.

### T1.6 — Fix the SQLite migration rewrite

**Goal:** SQLite keeps real referential actions and creates tables in a valid
order.

**Files:** `internal/db/db.go`.

**Steps:**

1. Stop stripping `ON DELETE CASCADE` / `ON DELETE SET NULL` for SQLite — SQLite
   supports both. Removing them currently orphans child rows.
2. Reorder the DDL so `sprints` is created before `work_items`, which references
   it. SQLite defers this, Postgres does not.
3. Keep the `DEFAULT NOW()` mapping added in M0, and add a test that the rewritten
   DDL executes on an in-memory SQLite database.

**Done when:** deleting a project removes its work items on SQLite, and a fresh
migration run succeeds on both drivers.

**Depends on:** T1.5.

### T1.7 — Make `sql.Register` idempotent

**Goal:** calling `db.New` more than once does not panic.

**Files:** `internal/db/db.go`.

**Why:** the `sqlite3_custom` registration is global and happens inside `New`, so
a second call panics with *"Register called twice"* — which any test that opens a
second database will hit.

**Steps:** register once at package init (guard with `sync.Once`), or use the
plain `sqlite3` driver and move the PRAGMAs into the DSN.

**Done when:** two `db.New` calls in one process both succeed.

**Depends on:** T1.5.

### T1.8 — Driver-aware connection pool

**Goal:** pool settings suit the driver.

**Files:** `internal/db/db.go`.

**Why:** `SetMaxOpenConns(25)` is reasonable for Postgres and wrong for SQLite
with WAL, which wants a single writer; concurrent writers surface `SQLITE_BUSY`.

**Steps:** for SQLite set `SetMaxOpenConns(1)` (and keep WAL + a busy timeout);
for Postgres keep the current values. Add a comment with the reason once.

**Done when:** concurrent writes on SQLite do not return `SQLITE_BUSY`.

**Depends on:** T1.5.

---

## Milestone 2 — Versioned migrations

### T2.1 — Migration runner

**Goal:** ordered, recorded, single-transaction migrations.

**Files:** new `internal/db/migrate.go`; `migrations/*.sql`.

**Steps:**

1. Add a `schema_migrations (version TEXT PRIMARY KEY, applied_at BIGINT)` table.
2. Load `migrations/NNNN_name.sql` in lexical order, skip versions already
   recorded, apply each in its own transaction, and record it.
3. Keep an in-process `dev` path that runs the same files.
4. Migrate the M0 DDL into `0001_initial.sql`. Do **not** keep a second copy of
   the DDL in `db.go`.

**Done when:** a fresh database reaches the latest version, and re-running
applies nothing.

**Depends on:** Milestone 1.

### T2.2 — Per-driver DDL

**Goal:** Postgres and SQLite each get valid DDL without string surgery.

**Files:** `migrations/` (e.g. `0001_initial.sql` plus `0001_initial.sqlite.sql`).

**Steps:** split the DDL per driver rather than rewriting one file with
`ReplaceAll`. Either a dialect suffix or a `-- +dialect` marker read by the
runner. Delete `adjustForSQLite` once nothing uses it.

**Done when:** both drivers reach the same schema from the same runner.

**Depends on:** T2.1, T1.1, T1.2, T1.6.

### T2.3 — Migration tests

**Goal:** the schema is exercised, not assumed.

**Files:** `internal/db/migrate_test.go`.

**Steps:** run migrations on an in-memory SQLite database, assert the expected
tables and columns exist, and assert a second run is a no-op.

**Depends on:** T2.1.

---

## Milestone 3 — Auth and security

### T3.1 — Refresh-token rotation and revocation

**Files:** `internal/auth/auth.go`, new `internal/auth/token_store.go`,
`internal/db/db.go`.

**Steps:** add a `refresh_tokens` table (hashed token id, user id, expiry,
revoked_at). Rotate on every refresh, revoke the presented token, and revoke the
whole family on reuse. `JWT_REFRESH_TTL` is 30 days by default — say so in
`.env.example`.

**Done when:** a used refresh token cannot be used twice, and signing out
invalidates a live refresh token.

**Depends on:** Milestone 1.

### T3.2 — Rate-limit sign-in

**Files:** `internal/auth/auth.go` or new `internal/middleware/ratelimit.go`.

**Steps:** add a per-IP and per-email limiter in front of `SignIn`, returning
`429` with `Retry-After`. Keep it in-process (the deployment is one binary) and
document that it is per-instance.

**Done when:** repeated failures from one IP are throttled.

### T3.3 — User provisioning

**Files:** `internal/auth/`, `cmd/server/main.go`.

**Steps:** there is no way to create the first user, so `SignIn` can never
succeed on a fresh install. Add either an explicit admin-only
`POST /v1/users` route or a documented one-shot bootstrap command
(`cmd/server --create-user`). Whichever you pick, record the decision in
`docs/ARCHITECTURE.md`.

**Done when:** a fresh install can produce a working sign-in without hand-editing
the database.

**Depends on:** Milestone 1.

### T3.4 — Security headers and CORS review

**Files:** `internal/middleware/middleware.go`.

**Steps:** decide whether the desktop/web client needs CORS at all; if not, keep
the default empty and document it. Add `X-Content-Type-Options: nosniff` and
`Cache-Control: no-store` on auth responses.

---

## Milestone 4 — Sync engine

### T4.1 — Transactional push

**Goal:** the entity write and its `sync_log` row commit together.

**Files:** `internal/sync/engine.go`, `internal/entities/*_repository.go`.

**Why:** `Engine.apply` writes the entity through a repository bound to the pool
and *then* inserts the log row. A crash between the two strands the change with
nothing queued — the exact failure the client's design record warns about.

**Steps:** introduce a querier interface (`ExecContext`/`QueryContext`/
`QueryRowContext`) that both `*sql.DB` and `*sql.Tx` satisfy; make the repository
structs hold it. In `apply`, open one transaction, build the repositories from
it, apply the change and insert the log row, then commit.

**Done when:** an induced failure between the two writes leaves neither.

**Depends on:** T1.3, T1.4.

### T4.2 — Project-scoped authorization

**Files:** `internal/sync/engine.go`, `internal/sync/handler.go`.

**Why:** `Push` and `Pull` are authenticated but not authorized — any signed-in
user can currently read or rewrite every project's changes.

**Steps:** filter `Pull` by the projects the user is a member of (join
`project_members`), and reject a `Push` whose `projectId` the user cannot access
with a `rejected` verdict. Decide what a deleted project's changes do to the
feed.

**Depends on:** T1.2.

### T4.3 — Atomic work-item key generation

**Files:** `internal/entities/models.go`, `internal/db/db.go`.

**Why:** `Project.NextKey` builds the key with `string(rune(n+'0'))`, so item 10
becomes `KEY-:` . It also reads and increments a counter without a lock.

**Steps:** use `strconv.Itoa`, and allocate the number with a single
`UPDATE projects SET next_item_number = next_item_number + 1 ... RETURNING`.

**Depends on:** T1.2.

### T4.4 — Conflict policy

**Files:** `internal/sync/engine.go`, `docs/ARCHITECTURE.md`.

**Steps:** define and document what the client does with `conflict` (last-write-
wins, server-wins, or field merge) and make the verdict payload carry the server
version so the client can re-pull. Today the statuses exist but nothing consumes
them.

**Depends on:** T1.3, T4.1.

### T4.5 — Cursor durability and retention

**Files:** `internal/sync/engine.go`, `internal/db/`.

**Steps:** the cursor is the numeric `sync_log.id`, which is monotonic and fine;
decide what happens when rows are pruned or a client has been away longer than
the retention window. Add a way for a client to detect a too-old cursor and
request a full resync.

---

## Milestone 5 — Observability and operations

### T5.1 — Readiness probe

**Files:** `cmd/server/main.go`.

**Steps:** keep `/healthz` as liveness and add `/readyz` that pings the database
and returns `503` when it fails.

### T5.2 — Structured logging and `LOG_LEVEL`

**Files:** `internal/middleware/middleware.go`, `internal/config/config.go`,
`cmd/server/main.go`.

**Why:** `LOG_LEVEL` is read but never used; logging is `log.Printf` text.

**Steps:** use `log/slog`, wire the level from config, and replace `log.Printf`
calls. Keep request logs at info and panics/5xx at error.

### T5.3 — Request IDs

**Files:** `internal/middleware/middleware.go`.

**Steps:** generate or accept `X-Request-ID`, put it on the context, echo it in
the response and include it in the request log.

### T5.4 — Container image

**Files:** new `Dockerfile`, `docker-compose.yml`, `.dockerignore`.

**Steps:** multi-stage build (CGO on, static-ish binary), non-root user,
`/data` volume for SQLite, healthcheck against `/readyz`.

### T5.5 — Self-hosting guide

**Files:** new `docs/SELF-HOSTING.md`, `deploy/tasktracker.service`.

**Steps:** document the systemd unit, the `EnvironmentFile` permissions, Postgres
setup, TLS termination, and backup/restore for SQLite and Postgres.

---

## Milestone 6 — Testing

### T6.1 — Test harness

**Files:** new `internal/db/testdb.go` (`//go:build test` or a helper exported
for tests).

**Steps:** a helper that opens an in-memory SQLite database, runs migrations, and
returns it with `t.Cleanup` closing it. Requires T1.7.

### T6.2 — Repository tests

**Files:** `internal/entities/*_test.go`.

**Steps:** create/read/update/delete round-trips, the version-conflict path from
T1.3, and the millisecond round-trip from T1.1.

### T6.3 — Auth and middleware tests

**Files:** `internal/auth/auth_test.go`, `internal/middleware/middleware_test.go`.

**Steps:** token issue/parse, expiry, wrong token type rejected, tampered
signature rejected, `Auth` returns `401` without a header, CORS preflight.

### T6.4 — Sync engine tests

**Files:** `internal/sync/engine_test.go`.

**Steps:** push creates, updates and deletes; the log row matches; pull pages and
the cursor advances; a stale version yields `conflict`; unknown entity yields
`rejected`.

### T6.5 — CI hardening

**Files:** `.gitlab-ci.yml`.

**Steps:** once the tree is green, add `go test -race ./...` and a coverage
report. Do not add `-race` before T6.1 exists or the pipeline stays red.

---

## Milestone 7 — Client contract

### T7.1 — Freeze the wire format

**Files:** new `docs/API.md`.

**Steps:** document every endpoint, request/response body, status code and the
push verdict vocabulary, and cross-check it against the client's actual JSON.

### T7.2 — Compatibility policy

**Files:** `AGENTS.md`, `docs/ARCHITECTURE.md`.

**Steps:** the client is an installed mobile app that syncs offline and cannot be
updated on the server's schedule. Write down the rule: additive changes only
within a `/v1` route; a breaking change is a new version and a migration path.

### T7.3 — Contract tests

**Steps:** capture real client request/response pairs and assert the server
round-trips them, so a model rename cannot silently break the app.
