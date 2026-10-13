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
- When a task changes the database, add the new version under
  `internal/db/migrations/` for **both** drivers **and** update every repository
  that maps the affected columns in the same commit.

## Status

| Milestone | State |
| --- | --- |
| 0 — Compiling baseline | **Done** |
| 1 — Data-layer integrity | **Done** |
| 2 — Versioned migrations | **Done** |
| 3 — Auth and security | **Done** |
| 4 — Sync engine | T4.1 done; T4.2–T4.5 not started |
| 5 — Observability and ops | T5.4 done; T5.1–T5.3, T5.5 not started |
| 6 — Testing | T6.1–T6.4 done; T6.3 middleware tests still missing; T6.5 not started |
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

**Resolved by Milestone 1.** Reads of `TIMESTAMP` columns used to fail on SQLite
(`converting driver.Value type time.Time ... to a int64`) and writes named
columns the DDL never created, so sign-in and sync could not complete. Milestone
1 fixed the representation, the schema drift and the placeholder ordering; an
end-to-end run now signs in and round-trips a project and a work item.

---

## Milestone 1 — Data-layer integrity (done)

Completed. Every task below lands in `internal/db/db.go` and
`internal/entities/*`, and is covered by `internal/db/db_test.go`,
`internal/entities/repository_test.go` and `internal/sync/engine_test.go`.

Two defects surfaced that the review had not found, both fixed here:

- **SQLite numbers placeholders by appearance, not by name.** `SELECT $2, $1`
  binds `$2` to the *first* argument, so every `UPDATE ... SET x=$2 ... WHERE
  id=$1` scrambled its values. All SQL now lists placeholders in ascending text
  order, which Postgres and SQLite agree on. Do not reintroduce a non-ascending
  list.
- **`Create` overwrote the caller's `CreatedAt`**, not only the `ID`, so client
  timestamps were dropped.

One guard was added: `Engine.apply` rejects a change with an empty `entityId`,
which would otherwise write a `sync_log` row pointing at nothing.

**Postgres is no longer unverified.** `internal/dbtest` runs the same suite
against Postgres when `TASKTRACKER_TEST_POSTGRES_URL` is set, giving each test
its own schema so packages can still run in parallel, and CI runs it as the
`test-postgres` job. Per-driver DDL (T2.2) is done — see Milestone 2.

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

## Milestone 2 — Versioned migrations (done)

Completed. `internal/db/migrate.go` owns the runner and
`internal/db/migrations/*.sql` the schema, embedded with `go:embed` so the binary
still ships as one file.

- **File per dialect, not string surgery.** `0001_initial.postgres.sql` and
  `0001_initial.sqlite.sql` carry the same version; `adjustForSQLite` and the
  inline `statements` slice are gone. A version missing a dialect file is a test
  failure (`TestMigrationsCoverBothDialects`), not a surprise at deploy time.
- **Recorded and transactional.** `schema_migrations (version, applied_at)` is
  the ledger; each version runs in one transaction and is recorded inside it, so
  a failed migration leaves nothing half-applied.
- **Statements are split, not guessed.** Postgres rejects several statements in
  one `Exec`, so `splitStatements` cuts on top-level semicolons while ignoring
  semicolons inside string literals and line comments.
- **Existing installs upgrade.** Every statement keeps `IF NOT EXISTS`, so a
  database built by the pre-runner inline DDL is recorded as `0001` on the next
  start. Verified on Postgres against the real binary, not just in a unit test.

### T2.1 — Migration runner

**Goal:** ordered, recorded, single-transaction migrations.

**Files:** new `internal/db/migrate.go`; `internal/db/migrations/*.sql`.

**Steps:**

1. Add a `schema_migrations (version TEXT PRIMARY KEY, applied_at BIGINT)` table.
2. Load `internal/db/migrations/NNNN_name.<driver>.sql` in lexical order, skip versions already
   recorded, apply each in its own transaction, and record it.
3. Keep an in-process `dev` path that runs the same files.
4. Migrate the M0 DDL into `0001_initial.sql`. Do **not** keep a second copy of
   the DDL in `db.go`.

**Done when:** a fresh database reaches the latest version, and re-running
applies nothing.

**Depends on:** Milestone 1.

### T2.2 — Per-driver DDL

**Goal:** Postgres and SQLite each get valid DDL without string surgery.

**Files:** `internal/db/migrations/` (`0001_initial.postgres.sql` plus `0001_initial.sqlite.sql`).

**Steps:** split the DDL per driver rather than rewriting one file with
`ReplaceAll`. Either a dialect suffix or a `-- +dialect` marker read by the
runner. Delete `adjustForSQLite` once nothing uses it.

**Done when:** both drivers reach the same schema from the same runner.

**Depends on:** T2.1, T1.1, T1.2, T1.6.

### T2.3 — Migration tests

**Goal:** the schema is exercised, not assumed.

**Files:** `internal/db/migrate_test.go`.

**Steps:** run migrations on a temporary SQLite database, assert the expected
tables and columns exist, and assert a second run is a no-op. Cover the other
driver too — `internal/dbtest` and `migrate_driver_test.go` run the same
migrations against Postgres when `TASKTRACKER_TEST_POSTGRES_URL` is set.

**Depends on:** T2.1.

---

## Milestone 3 — Auth and security

### T3.1 — Refresh-token rotation and revocation (done)

Done. `refresh_tokens` (migration `0002`, both drivers) records one row per
issued refresh token, and `internal/auth/token_store.go` owns it.

- **Rotated on every use.** `Rotate` revokes the presented token and inserts its
  replacement in one transaction. The revoke is a compare-and-swap
  (`WHERE token_hash = $ AND revoked_at IS NULL`), so two concurrent uses of one
  token cannot both succeed; the loser gets `ErrTokenReused`, which is handled the
  same way as theft.
- **A family ends together.** Each sign-in starts a `family_id`; reuse of a
  rotated token revokes every live token in that family, and `POST
  /v1/auth/signout` revokes it deliberately. The tradeoff is deliberate: a client
  that fires two refreshes concurrently ends its own session, which is the
  standard answer to a token appearing twice.
- **Only a hash is stored.** The token's JWT id is hashed with SHA-256, so the
  database never holds anything replayable while the signature still guards it.
- **The record, not just the signature, decides.** A refresh token must exist in
  the table, belong to the same user as its claims, and be unexpired; sign-out is
  idempotent and returns `204` whatever it is given.

Access tokens are deliberately still stateless and are not revoked by sign-out:
`docs/ARCHITECTURE.md` (E3) records why their short TTL is the bound.

**Files:** `internal/auth/auth.go`, new `internal/auth/token_store.go`,
`internal/db/migrations/0002_refresh_tokens.<driver>.sql`,
`cmd/server/main.go`.

**Tests:** `internal/auth/auth_test.go` — rotation, reuse and family revocation,
concurrent rotation, sign-out, stored-hash shape, expiry, access tokens, foreign
signatures and a record owned by another user. Run on both drivers via
`internal/dbtest`.

**Steps:** add a `refresh_tokens` table (hashed token id, user id, expiry,
revoked_at). Rotate on every refresh, revoke the presented token, and revoke the
whole family on reuse. `JWT_REFRESH_TTL` is 30 days by default — say so in
`.env.example`.

**Done when:** a used refresh token cannot be used twice, and signing out
invalidates a live refresh token.

**Depends on:** Milestone 1.

### T3.2 — Rate-limit sign-in (done)

Done. `internal/ratelimit` is a small fixed-window counter keyed by an
opaque string; `auth.Handler.SignIn` checks two keys before verifying the
password — `ip:<peer address>` and `email:<account>` — so one address cannot
spray many accounts and one account cannot be sprayed from many addresses.
A denied attempt answers `429` with `Retry-After`. A successful sign-in
clears the account's key so a few typos do not lock a user out; the address
key is left alone so spraying stays limited.

The limiter is in-process and per-instance: running several replicas
multiplies the effective allowance. That is a deliberate tradeoff for a
single-binary deployment — a shared store would be a second dependency.
`SIGNIN_MAX_ATTEMPTS` (default 10) and `SIGNIN_WINDOW` (default 15m)
configure it; `0` disables it.

**Files:** new `internal/ratelimit/ratelimit.go`, `internal/auth/auth.go`,
`internal/config/config.go`, `cmd/server/main.go`, `.env.example`.

**Tests:** `internal/ratelimit/ratelimit_test.go` (limit, per-key isolation,
window reset, reset, disabled/nil, eviction) and `internal/auth/auth_test.go`
(`TestSignInThrottlesPerAddress`, `TestSignInThrottlesPerAccount`).

### T3.3 — User provisioning (done)

Done. The server has no self-signup: the admin creates users, and the first
admin is bootstrapped from the CLI. Two entry points were implemented:

- **CLI bootstrap** (`cmd/server user-create`) — a one-shot subcommand that
  connects directly to `DATABASE_URL`, runs migrations, and creates a user
  with `--email`, `--password`, `--display-name`, `--position`, and `--admin`
  flags. This is how a fresh install gets its first working sign-in.
- **Admin API** (`/v1/admin/users`) — `POST` to create, `GET` to list,
  `PATCH /{id}` to update (display name, position, role, status), and
  `DELETE /{id}` to deactivate. All require an admin token (the `AdminOnly`
  middleware checks the `role` claim). A user cannot deactivate themselves.
- **Directory search** (`GET /v1/users/search?q=...`) — open to any
  authenticated user, used by the KMP client's `TeamPort` to find teammates.
  Case-insensitive LIKE on email and display name, limited to 50 results.

Migration `0003` adds `role TEXT NOT NULL DEFAULT 'member'` to the `users`
table for both drivers. The role is carried in the JWT `role` claim so the
middleware can gate admin routes without a database lookup on every request.

See [`docs/USER-MANAGEMENT.md`](USER-MANAGEMENT.md) for the full flow,
endpoint reference, and user object shape.

**Files:** `internal/admin/handler.go` (new), `internal/entities/models.go`,
`internal/entities/user_repository.go`, `internal/auth/auth.go`,
`internal/middleware/middleware.go`, `cmd/server/main.go`,
`internal/db/migrations/0003_user_roles.{postgres,sqlite}.sql`.

**Done when:** a fresh install can produce a working sign-in without
hand-editing the database.

**Depends on:** Milestone 1.

### T3.4 — Security headers (done)

Done. Two middlewares in `internal/middleware`:

- `SecurityHeaders` sets `X-Content-Type-Options: nosniff` on every
  response, so a browser cannot guess a content type and run a JSON body
  as script. It is the outermost middleware, so the header survives an
  error response too.
- `NoStore` sets `Cache-Control: no-store`, and wraps the three auth
  routes (`/v1/auth/login`, `/v1/auth/refresh`, `/v1/auth/signout`) so a
  proxy or the client's HTTP cache cannot retain a token.

CORS was already functional and is unchanged: the allow-list is
configurable and mobile clients send no `Origin`, so an empty list simply
disables the headers.

**Files:** `internal/middleware/middleware.go`, `cmd/server/main.go`.

**Tests:** `internal/middleware/middleware_test.go`.

---

## Milestone 4 — Sync engine

### T4.1 — Transactional push (done)

Done. Repositories now hold an `entities.Querier` (satisfied by `*sql.DB`
and `*sql.Tx`) instead of a `*sql.DB`, and `entities.inTx` reuses the
caller's transaction when there is one. `Engine.apply` opens one
transaction per change, binds a fresh repository set to it, applies the
change, writes the `sync_log` row and commits — so a crash between the two
writes can no longer strand a change with nothing queued. Each change
keeps its own transaction, so one failure never rolls back another.

`Engine` no longer carries pool-bound repositories; it holds only the pool
and builds transaction-bound ones inside `apply`. `NewEngine` now takes
just the `*sql.DB`.

**Files:** new `internal/entities/querier.go`, all
`internal/entities/*_repository.go`, `internal/sync/engine.go`,
`cmd/server/main.go`.

**Tests:** `internal/sync/engine_test.go` →
`TestPushRollsBackWhenTheLogRowFails` induces a failure after the entity
write (an invalid `sync_log.operation` violates its CHECK constraint) and
asserts the entity is not stored.

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

### T5.4 — Container image (done)

Done. A multi-stage Docker build (`golang:1.27-alpine` builder with
`gcc`/`musl-dev` for CGO → `alpine:3.21` runtime) produces a ~44 MB image
with a non-root `app` user and a `/data` volume for SQLite.
`docker-compose.yml` provides `postgres` and `sqlite` profiles; the Postgres
profile starts `postgres:16-alpine` with a healthcheck and the server waits
for it before booting. `.dockerignore` keeps the build context small.

The compose healthcheck currently targets the Postgres container, not the
server itself — once T5.1 adds `/readyz`, update the server service to
healthcheck against it.

**Files:** `Dockerfile`, `docker-compose.yml`, `.dockerignore`.

See [Docker](../README.md#docker) in the README.

### T5.5 — Self-hosting guide (bare-metal)

**Files:** new `docs/SELF-HOSTING.md`, `deploy/tasktracker.service`.

**Context:** Docker self-hosting is already documented in the
[README](../README.md#docker). What remains is the bare-metal/systemd path
for operators who do not use Docker.

**Steps:** document the systemd unit, the `EnvironmentFile` permissions,
Postgres setup, TLS termination, and backup/restore for SQLite and Postgres.

---

## Milestone 6 — Testing

T6.1–T6.4 are done as part of milestones 1–3. T6.3 is partially done (auth
tests exist; middleware tests are still missing). T6.5 (CI hardening) is the
remaining open task.

### T6.1 — Test harness (done)

Done. `internal/dbtest/dbtest.go` exposes `NewDatabase(t)`, which opens a
migrated SQLite or Postgres database, creates a per-test schema for Postgres
isolation, and registers cleanup with `t.Cleanup`. Every test package uses it.

**Files:** `internal/dbtest/dbtest.go`.

### T6.2 — Repository tests (done)

Done. `internal/entities/repository_test.go` covers user/project/work-item/
sprint/comment create-read-update-delete round-trips, the millisecond
round-trip from T1.1, the version-conflict path from T1.3, the
client-supplied ID from T1.4, and the cascade-delete from T1.6. Runs on
both drivers via `internal/dbtest`.

**Files:** `internal/entities/repository_test.go`.

### T6.3 — Auth and middleware tests (partially done)

**Auth — done.** `internal/auth/auth_test.go` covers sign-in, refresh-token
rotation, reuse detection and family revocation, concurrent rotation,
sign-out (both valid and unknown tokens), access-token rejection on the
refresh endpoint, expiry, and foreign-token rejection. Runs on both drivers
via `internal/dbtest`.

**Middleware — still missing.** There is no `internal/middleware/middleware_test.go`.
What remains to test: `Auth` returns `401` without a header or with a bad
token, `AdminOnly` returns `403` for a member, and CORS preflight returns
`204` with the right headers.

**Files:** `internal/auth/auth_test.go` (done); `internal/middleware/middleware_test.go` (to create).

### T6.4 — Sync engine tests (done)

Done. `internal/sync/engine_test.go` covers push-creates-and-pulls-back,
push-update-respects-version (stale version → `conflict`), push-delete,
unknown entity → `rejected`, and work-item push. Runs on both drivers via
`internal/dbtest`.

**Files:** `internal/sync/engine_test.go`.

### T6.5 — CI hardening

**Files:** `.gitlab-ci.yml`.

**Steps:** once the tree is green, add `go test -race ./...` and a coverage
report. Do not add `-race` before T6.1 exists or the pipeline stays red.

---

## Milestone 7 — Client contract

### T7.1 — Freeze the wire format

**Files:** new `docs/API.md`.

**Context:** the auth and admin endpoint contracts are already documented in
[`docs/USER-MANAGEMENT.md`](USER-MANAGEMENT.md) — request/response shapes,
status codes, and the snake_case JSON convention. The route table in
`docs/ARCHITECTURE.md` lists every mounted endpoint. What remains is a
single consolidated `docs/API.md` that also covers the sync endpoints
(push/pull request and response shapes, the verdict vocabulary, cursor
paging) and cross-checks every entry against the client's actual JSON.

**Steps:** document the sync endpoints' request/response bodies, status codes,
and push verdict vocabulary. Merge with the auth/admin reference already in
USER-MANAGEMENT.md into one `docs/API.md`. Cross-check against the KMP
client's actual JSON shapes.

### T7.2 — Compatibility policy

**Files:** `AGENTS.md`, `docs/ARCHITECTURE.md`.

**Steps:** the client is an installed mobile app that syncs offline and cannot be
updated on the server's schedule. Write down the rule: additive changes only
within a `/v1` route; a breaking change is a new version and a migration path.

### T7.3 — Contract tests

**Steps:** capture real client request/response pairs and assert the server
round-trips them, so a model rename cannot silently break the app.
