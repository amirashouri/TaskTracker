# TaskTrackerServer — Plan

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
| 4 — Sync engine | **Done** |
| 5 — Observability and ops | T5.1–T5.5 done; T5.6 **promoted to T9.1** |
| 6 — Testing | **Done** |
| 7 — Client contract | **Done** |
| 8 — Admin management | **Done** — T8.1–T8.10; T8.7 closed as unnecessary (the role is already on the JWT claim the client reads) |
| 9 — Customer install and exposure | T9.1–T9.3 not started; T9.4–T9.13 not started |

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
correct pattern in `.agents/skills/go-migrations-and-repositories/SKILL.md`:
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

### T4.2 — Project-scoped authorization (done)

Done. `sync_log` gains a `project_id` column (migration `0004`, both
drivers) so a change can be attributed to a project and the feed scoped to
it. The column is deliberately **not** a foreign key: a row must outlive
the project it names, or a client would never learn the project was
deleted.

- **Push is authorized before it writes.** `resolveProject` works out which
  project a change belongs to — the entity id for a project, the payload's
  `projectId` for a work item or sprint, and the owning work item's project
  for a comment. For a delete it reads the row first, because the payload
  may be absent. `canWrite` then requires the caller to be a member.
- **A new project authorizes on its payload.** A project that does not
  exist yet has no membership rows, so creation is allowed when the payload
  lists the caller. An existing project is checked against the stored
  membership, not the payload — otherwise anyone could add themselves to a
  project they cannot see.
- **Pull is scoped to membership** by joining `project_members`, and a
  change with no resolvable project is not delivered.
- **Deleted projects:** a project's own `delete` row is delivered to every
  authenticated user, so a client that has been removed from a project — or
  whose project was deleted — still retires it locally instead of keeping
  it forever. That leaks the existence of a deleted project id to signed-in
  users, which is accepted for a single-organisation deployment.
  Membership *changes* are still not themselves sync events; a client that
  loses access without the project being deleted learns of it on the next
  full resync.

Rows written before this version are attributed for the `project` entity
only; older work-item, sprint and comment rows keep a NULL `project_id` and
are not delivered until a full resync.

**Files:** new
`internal/db/migrations/0004_sync_log_project.{postgres,sqlite}.sql`,
`internal/sync/engine.go`, `internal/sync/handler.go`.

**Tests:** `internal/sync/engine_test.go` — creation refused when the
caller is not listed, a non-member refused on an existing project (even
naming themselves in the payload), a non-member refused a work item, Pull
scoped to membership, and a project delete delivered beyond membership.

### T4.3 — Atomic work-item key generation (done)

Done. `Project.NextKey` is gone — it was dead code that built the key with
`string(rune(n+'0'))`, so item 10 became `TT-:` — and the counter no longer
lives in memory. `Project.Key(number)` is a pure formatter using
`strconv.Itoa`, and `ProjectRepository.AllocateKey` reserves a number with
one `UPDATE projects SET next_item_number = next_item_number + 1 ...
RETURNING key_prefix, next_item_number - 1`, so two concurrent callers
cannot be handed the same number. Both drivers support `RETURNING`.

**Files:** `internal/entities/models.go`,
`internal/entities/project_repository.go`.

**Tests:** `internal/entities/repository_test.go` — decimal counting past
9, 20 concurrent allocations with no duplicate, and a missing project
reported rather than silently returning a key.

### T4.4 — Conflict policy (done)

Done. The policy is **server-wins, client re-pulls**, recorded in
`docs/ARCHITECTURE.md` under "Conflict policy":

- A push naming a stale version is answered `conflict` and the verdict
  carries the version the server now holds.
- A push naming no version is last-write-wins.
- Timestamps are never consulted, because a skewed client clock would win
  every race; field merge is out of reach for a payload the server treats
  as opaque.

Two fixes were needed to make the verdict trustworthy:

1. The update path assigned the *payload's* version before calling
   `Update`, so the repository's compare-and-swap ran against the payload's
   version — an empty one when the client omitted it, which failed every
   versionless update. The engine now sets `X.Version = existing.Version`
   before the write, so the swap runs against the row it just read.
2. The `ErrVersionConflict` branch carried no version. It now re-reads the
   row (`currentVersion`) so both conflict paths return the same shape.

**Files:** `internal/sync/engine.go`, `internal/entities/models.go`,
`docs/ARCHITECTURE.md`.

**Tests:** `internal/sync/engine_test.go` —
`TestConflictVerdictCarriesTheServerVersion` and
`TestPushWithoutAVersionOverwrites`.

### T4.5 — Cursor durability and retention (done)

Done. The cursor stays the numeric `sync_log.id`, which is monotonic, and
the server now has both halves of the durability story:

- **Retention.** `Engine.Prune(before)` deletes change-feed rows older than
  a cutoff. `main.go` runs it hourly for `SYNC_LOG_RETENTION` (default
  `720h`; `0` keeps the feed forever). The interval is fixed on purpose —
  the window is what an operator tunes.
- **Detection.** `Pull` compares the caller's cursor with the oldest row
  still retained for that entity (served by the existing
  `(entity, created_at, id)` index) and sets `PullPage.ResyncRequired` when
  the cursor points before it. A client that sees the flag drops its cursor
  and pulls from the start; that is the whole "request a full resync"
  mechanism, and it needs no new endpoint.

A client that has never synced (`cursor` empty) is not flagged: it is
already pulling everything that survived.

**Files:** `internal/sync/engine.go`, `internal/entities/models.go`,
`internal/config/config.go`, `cmd/server/main.go`, `.env.example`.

**Tests:** `internal/sync/engine_test.go` —
`TestPullAsksForAResyncWhenTheCursorIsTooOld` and
`TestPruneKeepsRecentRows`.

---

### T4.6 — Permanent push failures are refused (done)

Done. A repository write that fails on the row's own values is now answered
`rejected` rather than `retry_later`.

`Engine.applyChange` had one `default:` arm that answered `retry_later` for
every write error that was not a version conflict, so a payload the schema
refuses — a `CHECK` violation on a value the column has no room for — was
reported as transient. The client then spends a retry budget it cannot
spend, and because it stops its push loop at the first retryable failure the
refused change wedges everything queued behind it. Seen live: a work item
pushed with `"priority":"Medium"` against a CHECK that allows only lowercase.

`entities.PermanentWriteError` classifies by driver code — Postgres SQLSTATE
class `22` and `23502`/`23514`/`23P01`, SQLite `ErrConstraintCheck` and
`ErrConstraintNotNull` — and the engine answers `rejected` for those. The set
is deliberately narrow, because `rejected` is terminal on the client: a
foreign-key violation can be a child that arrived before its parent and a
unique violation can be a re-push after a lost response, so both stay
retryable. The `sync_log` and commit failures keep answering `retry_later`
even on a permanent database error — a failure there is the server's own,
and dead-lettering client work over it is the error `docs/API.md` warns of.

**Files:** new `internal/entities/write_error.go`, `internal/sync/engine.go`,
`docs/ARCHITECTURE.md`.

**Tests:** `internal/entities/write_error_test.go` (the policy table, both
drivers' codes) and `internal/sync/engine_test.go` →
`TestPushRejectsAWorkItemTheDatabaseRefuses`.

---

## Milestone 5 — Observability and operations

### T5.1 — Readiness probe (done)

Done. `/healthz` stays liveness and deliberately never touches the
database, so a database outage does not get the process restarted.
`/readyz` pings the database with a 2s timeout and returns `503` with the
standard error envelope when it fails. Both Docker images and both compose
services now healthcheck `/readyz` instead of relying on the process being
alive.

**Files:** `cmd/server/main.go`, `Dockerfile`, `docker-compose.yml`,
`README.md`.

**Tests:** `cmd/server/main_test.go` — liveness survives a dead database,
readiness flips from `200` to `503` when it is closed.

### T5.2 — Structured logging and `LOG_LEVEL` (done)

Done. Everything now goes through `log/slog` with a text handler on stdout,
and `LOG_LEVEL` actually selects the level: `Config.SlogLevel` maps the
name and `Config.validate` refuses to start on an unknown one, so a typo
fails loudly instead of silently logging at info.

- `middleware.Logging` and `middleware.Recover` take the logger as a
  parameter, so a test can capture their output. Request logs are `info`,
  except a response that ended in 5xx, which is `error` — a filter on level
  finds real failures without hiding the rest. Panics are `error` with the
  stack.
- `main.go` builds the logger from config and sets it as the default.
  Startup failures go through a small `fatal` helper, because `slog` has no
  `Fatal`. The `user-create` subcommand keeps plain stderr messages: it is
  a command-line tool, not a service.

**Files:** `internal/middleware/middleware.go`,
`internal/config/config.go`, `cmd/server/main.go`, `.env.example`.

**Tests:** `internal/middleware/middleware_test.go` (a 200 logs at info, a
500 and a panic log at error) and `internal/config/config_test.go` (level
mapping, unknown level rejected).

### T5.3 — Request IDs (done)

Done. `middleware.RequestID` reuses a client-supplied `X-Request-ID` when
it is safe and generates one otherwise, puts it on the context
(`RequestIDFrom`), and echoes it back in the response header. `Logging`
includes it as `request_id`, so a response can be traced to its log line.

A supplied id must be printable ASCII and at most 128 characters; anything
else is replaced rather than passed through, so a client cannot inject a
header or bloat a log line. The middleware sits outside `Logging` in the
chain so the id exists before the request is logged.

**Files:** new `internal/middleware/requestid.go`,
`internal/middleware/middleware.go`, `cmd/server/main.go`.

**Tests:** `internal/middleware/middleware_test.go` — a supplied id is
echoed on the context and the response, an absent one is generated, an
unsafe one is replaced, and the request log carries it.

### T5.4 — Container image (done)

Done. A multi-stage Docker build (`golang:1.27-alpine` builder with
`gcc`/`musl-dev` for CGO → `alpine:3.21` runtime) produces a ~44 MB image
with a non-root `app` user and a `/data` volume for SQLite.
`docker-compose.yml` provides `postgres` and `sqlite` profiles; the Postgres
profile starts `postgres:16-alpine` with a healthcheck and the server waits
for it before booting. `.dockerignore` keeps the build context small.

The compose healthcheck now targets `/readyz` on the server services, and
the image carries the same check for a standalone `docker run`.

**Files:** `Dockerfile`, `docker-compose.yml`, `.dockerignore`.

See [Docker](../README.md#docker) in the README.

### T5.5 — Self-hosting guide (bare-metal) (done)

Done. `docs/SELF-HOSTING.md` is the bare-metal counterpart to the Docker path,
in twelve sections: prerequisites (a C toolchain is a *build-time* need only),
build and install, the service user and directories, configuration and secrets,
SQLite vs Postgres setup, installing the unit, creating the first admin, TLS
termination, backups and restore, upgrades, operations, and a troubleshooting
table.

`deploy/tasktracker.service` is the unit the guide installs: `EnvironmentFile`
at `/etc/tasktracker/secrets.env`, `StateDirectory=tasktracker`, and hardening
(`NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`,
`CapabilityBoundingSet=`, `SystemCallFilter=@system-service`).

Two things the guide records rather than hides:

- **The sign-in rate limiter does not read `X-Forwarded-For`.** Behind a
  reverse proxy every request appears to come from the proxy, so the
  per-address limit collapses to one global limit while the per-account limit
  keeps working. Trusting the header without knowing which proxies are yours
  would be worse — that is T5.6.
- **`user-create` is a separate process** and does not inherit the unit's
  `EnvironmentFile`; the guide passes `ENV_FILE` explicitly. Without it the
  command finds no `JWT_SECRET` and refuses to start.

**Files:** `docs/SELF-HOSTING.md`, `deploy/tasktracker.service`.

See [Self-Hosting](SELF-HOSTING.md), linked from the README.

### T5.6 — Trusted-proxy aware client address

> **Promoted to T9.1.** The text below is unchanged and remains authoritative; the task
> moved to Milestone 9 because a proxy is now a supported deployment shape rather than a
> hypothetical, which makes this a prerequisite instead of a follow-up. Work it there.

**Context:** the sign-in rate limiter keys on `r.RemoteAddr`. Behind the
reverse proxy that T5.5 recommends, every request carries the proxy's address,
so the per-address limit degrades into a single global limit. The per-account
limit is unaffected, which is why this is a follow-up and not a blocker: a
determined attacker cannot exceed the per-account budget, only spread across
accounts.

Reading `X-Forwarded-For` unconditionally would be worse than the status quo —
any client could forge the header and evade the per-address limit entirely.
The fix is to trust the header only when the peer is a configured proxy.

**Steps:**

1. Add `TRUSTED_PROXIES` (comma-separated CIDRs) to `internal/config`, parsed
   and validated; empty means "trust nothing", i.e. today's behaviour.
2. In `internal/auth`, take the client address from `X-Forwarded-For` (or
   `Forwarded`) only when `RemoteAddr` falls inside a trusted CIDR, walking the
   list right-to-left and stopping at the first untrusted hop. Cache the parsed
   prefixes at startup, not per request.
3. Reject an unparsable or oversized header by falling back to `RemoteAddr`
   rather than trusting it.
4. Test the forgery case: an untrusted peer sending `X-Forwarded-For` must be
   limited under its real address.
5. Document the variable in `.env.example` and in
   [`docs/SELF-HOSTING.md`](SELF-HOSTING.md) §8.

**Depends on:** T3.2.

---

## Milestone 6 — Testing

T6.1–T6.4 are done as part of milestones 1–3; T6.5 completes the milestone.

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

### T6.3 — Auth and middleware tests (done)

**Auth — done.** `internal/auth/auth_test.go` covers sign-in, refresh-token
rotation, reuse detection and family revocation, concurrent rotation,
sign-out (both valid and unknown tokens), access-token rejection on the
refresh endpoint, expiry, and foreign-token rejection. Runs on both drivers
via `internal/dbtest`.

**Middleware — done.** `internal/middleware/middleware_test.go` covers:

- `Auth` — `401` for a missing header, for a malformed one (a bare scheme,
  an empty token, the wrong scheme, a token with no scheme, trailing
  garbage), for an unparsable token, for a token signed with another secret,
  for an expired token, and for a refresh token replayed as an access token.
  The refresh case is the one that matters: the signature is valid, so only
  the `typ` claim stops it. The pass-through case asserts the user id and
  role land on the context, and a lowercase `bearer` scheme is accepted per
  RFC 7235.
- `AdminOnly` — allows an admin, refuses a member with `403`, and refuses
  when no user is on the context at all, so an empty role is not read as a
  pass. One test chains `Auth` then `AdminOnly` to match how `main.go`
  wires the admin routes.
- `CORS` — a preflight is answered `204` without reaching the handler and
  carries the allow-origin, methods, headers and max-age; a normal request
  reflects an allowed origin; a disallowed origin gets no header (on a
  preflight or otherwise); `*` allows anything; an empty allow-list sets no
  headers; and a request with no `Origin`, as a mobile client sends, is
  untouched.
- `Chain` — the first middleware listed is the outermost.

**Files:** `internal/auth/auth_test.go`, `internal/middleware/middleware_test.go`.

### T6.4 — Sync engine tests (done)

Done. `internal/sync/engine_test.go` covers push-creates-and-pulls-back,
push-update-respects-version (stale version → `conflict`), push-delete,
unknown entity → `rejected`, and work-item push. Runs on both drivers via
`internal/dbtest`.

**Files:** `internal/sync/engine_test.go`.

### T6.5 — CI hardening (done)

Done. The `test` job now runs `go test -race -covermode=atomic
-coverprofile=coverage.out ./...` and prints the module-wide total, so the
race detector guards the concurrent code — the sync engine, the rate
limiter, and refresh-token rotation — on every push. `-race` requires
`-covermode=atomic`, which is why the mode is stated rather than left to
default.

Coverage is reported, not enforced: GitLab reads the total out of the job
log into the MR widget, and `coverage.out` is kept as an artifact for a
week. A threshold was rejected — it would either be set low enough to be
meaningless or would block a legitimate change, and the useful signal is
the direction of the number across MRs.

`test-postgres` stays without `-race` on purpose: that job exists to prove
dialect parity, and the concurrency the race detector guards lives in Go
code the job above already runs. Adding it there would roughly double the
pipeline for no new signal.

The whole suite was verified race-clean locally before this landed, so the
pipeline is not left red.

**Files:** `.gitlab-ci.yml`.

---

## Milestone 7 — Client contract (done)

### T7.1 — Freeze the wire format (done)

**Files:** `docs/API.md`.

**Steps:** document the sync endpoints' request/response bodies, status codes,
and push verdict vocabulary. Merge with the auth/admin reference already in
USER-MANAGEMENT.md into one `docs/API.md`. Cross-check against the KMP
client's actual JSON shapes.

**Done.** `docs/API.md` is the consolidated contract: conventions, the two JSON
conventions and why, the two timestamp formats, auth, users, sync, and the
compatibility rules. `docs/USER-MANAGEMENT.md` keeps the operational content
(roles, CLI bootstrap, token lifecycle, flow) and points here for shapes, so the
endpoint reference exists in one place rather than two that drift.

Cross-checking against the client is what found the four mismatches this
milestone was written for — `entityId`/`entityID`, the undeclared
`resyncRequired`, the string-encoded payload, and ISO-8601 timestamps — plus the
plural pull paths. All five are fixed.

Two decisions the doc had to make explicitly:

- **Timestamps are ISO-8601 UTC at whole seconds** on synced entity payloads.
  The client's `DateCoding` already fixed this format and says why; the server's
  `int64` millis were simply out of step. The server now truncates to whole
  seconds on the way in, so a stored value is exactly a value it hands back.
  The auth/admin user object keeps epoch millis, because that half of the API is
  a separate contract.
- **The pull scopes are `projects` and `workItems`**, matching the client's
  `SyncScope`. A scope is not an entity type — `workItems` carries project
  changes too, because renaming a project has to reach a client that never asked
  for projects. The singular paths are kept as aliases.

A gap is recorded rather than fixed: `push` accepts `sprint` and `comment`
changes and the engine applies them, but no pull scope returns them, so those
changes are push-only today.

### T7.2 — Compatibility policy (done)

**Files:** `AGENTS.md`, `docs/ARCHITECTURE.md`.

**Steps:** the client is an installed mobile app that syncs offline and cannot be
updated on the server's schedule. Write down the rule: additive changes only
within a `/v1` route; a breaking change is a new version and a migration path.

**Done, with the rule corrected.** "Additive changes only" is wrong here. The
client leaves `ignoreUnknownKeys` off on purpose — its `JSONCoding` says a
renamed field has to fail loudly at the boundary rather than decode as null on
every device — and the same setting makes an *added* key a decoding failure
rather than a value it skips. So a response may not gain a field either.

The policy is therefore:

- A response may not gain a field.
- A response may not lose or rename a field the client declares without a
  default. Kotlin treats a property with a default as optional, one without as
  required — which is why `hasMore` is always sent and `resyncRequired` may be
  omitted.
- A new response field ships **client first**: a client that declares it with a
  default works against both servers, and only then may the server send it.
- Anything that cannot follow that order is a new `/v1` version and a migration
  path.

### T7.3 — Contract tests (done)

**Steps:** capture real client request/response pairs and assert the server
round-trips them, so a model rename cannot silently break the app.

**Files:** `cmd/server/contract_test.go`.

**Done.** The client's DTOs are transcribed into the test with the client's own
JSON names, and responses are decoded into them. The tests run against the real
route table (`mount`), so a missing or misspelled path fails too.

One thing they had to do differently from the obvious approach: **compare key
names, not decoded values.** Go matches JSON keys case-insensitively, so a
server that renamed `entityId` to `entityID` still populates a `json:"entityId"`
field and every decode-based assertion passes — which is precisely how that
mismatch shipped. `assertWireKeys` reads the key set off the bytes, and also
rejects a key the client's DTO does not declare, which is the T7.2 rule made
executable.

Reintroducing the `entityID` rename turns
`TestThePullResponseIsWhatTheClientDecodes` red; so does pointing the plural pull
routes elsewhere.

---

## Milestone 8 — Admin management

The story the admin surfaces grew out of: an admin manages members and projects
from the settings screen. Members could be created and deactivated but not
removed; projects could not be managed at all outside a membership. This
milestone makes deletion a real, session-ending operation and gives the admin
server-wide project screens, then teaches the client to consume them.

Everything here is on the server side of the contract in
`docs/ARCHITECTURE.md` (E5–E7) and `docs/API.md`.

### T8.1 — A status vocabulary with `deleted` as its own bucket (done)

Done. `StatusActive`, `StatusInactive` and `StatusDeleted` are named constants.
`UserRepository.Create` defaults `status` to `"active"`, `List` returns every
status *except* `deleted`, and a new `ListDeleted` returns exactly those. Only
`deleted` is withheld from the live list: an `inactive` member can still sign in
and be managed.

**Files:** `internal/entities/models.go`, `internal/entities/user_repository.go`.

**Tests:** `internal/entities/repository_test.go` →
`TestListSplitsDeletedFromEveryOtherStatus`.

### T8.2 — Refuse sign-in and refresh for a deleted user (done)

Done. `SignIn` checks the status *after* the password so the answer is the same
`401` a wrong password gets — the response cannot be used to tell a deleted
account from a mistyped one. `Refresh` revokes the presented token's family and
returns `401`. An `inactive` user is still accepted.

**Files:** `internal/auth/auth.go`.

**Tests:** `internal/auth/auth_test.go` — `TestSignInRejectsADeletedUser`,
`TestRefreshRejectsADeletedUserAndRevokesTheFamily`,
`TestSignInStillAcceptsAnInactiveUser`.

### T8.3 — `RevokeAllForUser` (done)

Done. `RevokeFamily` closes one sign-in; a member may hold several. The new store
method revokes every live token a user holds, scoped to that user, marking rows
revoked rather than deleting them so reuse detection keeps working.

**Files:** `internal/auth/token_store.go`.

**Tests:** `internal/auth/auth_test.go` →
`TestRevokeAllForUserEndsEverySessionForThatUserOnly`.

### T8.4 — `DELETE /v1/admin/users/{id}` ends the member's sessions (done)

Done. The route used to set `"inactive"` and leave every token alive. It now
marks the account `deleted` and revokes the member's tokens, revoking *before*
the status write to close the window in which a refresh in flight mints a token
the revoke never sees. Deleting yourself is `400`; deleting the last admin is
`409`, counted from admin rows rather than the caller's claim. `NewHandler` takes
the token store for this.

**Files:** `internal/admin/handler.go`, `internal/admin/handler_test.go`,
`internal/entities/user_repository.go` (`CountByRole`), `cmd/server/main.go`.

**Tests:** `cmd/server/contract_test.go` —
`TestDeletingAMemberEndsTheirSessionsAndBlocksSignIn`,
`TestDeletingTheLastAdminIsRefused`, `TestDeletingYourselfIsRefused`; and
`internal/admin/handler_test.go` → `TestDeleteRevokesTheTargetsLiveTokens`, which
reads the store directly because the status check alone would mask a missing
revoke.

### T8.5 — Members in two buckets, and reactivation (done)

Done. `GET /v1/admin/users?status=active|deleted` defaults to `active` (the whole
non-deleted set) and answers `400` for an unknown bucket. Reactivating a deleted
member is an ordinary `PATCH` of `"active"`. `PATCH` is refused the value
`"deleted"` — a status write that marked an account deleted would bypass the
revocation `DELETE` performs and leave the member signed in.

**Files:** `internal/admin/handler.go`.

**Tests:** `cmd/server/contract_test.go` — `TestMembersListSplitsActiveFromDeleted`,
`TestReactivatingADeletedMemberLetsThemSignInAgain`,
`TestStatusCannotBeSetToDeletedThroughUpdate`.

### T8.6 — Admin project screens (done)

Done. `GET /v1/admin/projects?status=active|archived` returns every project on
the server; `PATCH /v1/admin/projects/{id}` takes the whole project and applies it
through the sync engine, so the row and its change-feed entry commit together and
the members pull the edit as they would one of their own. `Engine.ApplyAdminProject`
shares `apply`'s body and skips only the membership check, which the admin route
performs instead. The stored payload is the JSON *string* a client sends, not a
nested object.

**Files:** new `internal/admin/projects.go`, `internal/sync/engine.go`,
`cmd/server/main.go`.

**Tests:** `internal/sync/engine_test.go` —
`TestApplyAdminProjectEditsAProjectTheAdminIsNotIn`,
`TestPushStillRefusesANonMemberAfterTheAdminPathExists`; and
`cmd/server/contract_test.go` — `TestAdminProjectEditSavesTheProjectAndReachesTheFeed`,
`TestAdminProjectListSplitsActiveFromArchived`.

### T8.7 — `role` on the auth token pair (closed: not needed)

The admin client decides whether to show the settings screen from the signed-in
user's role. This task assumed the role had to reach the client through the token
pair's envelope, because the envelope carries `user_id`, `email` and
`display_name`. That premise was wrong.

The role is already on the JWT the server issues — `internal/auth` puts a `role`
claim on both the access and the refresh token — and the client derives identity
from that token rather than from the envelope's `user_*` fields. It reads the
role from the claim, so there is nothing for the server to add.

Adding `role` to the envelope would in fact be **wrong**, not merely redundant.
The client leaves `ignoreUnknownKeys` off, so an undeclared key fails decoding —
the exact failure that removed `token_type` and `expires_in` in T7.1. The client
declares no `role` on its `AuthTokenResponse`, and `clientTokenPair` in
`cmd/server/contract_test.go` asserts five keys precisely because that count is
the contract.

**Resolved on the client.** KMP `CLI-1` reads the claim (`JWT.Claims.role`) into
`UserIdentity.role`, failing closed to `Member` for an absent or unknown value.
No server change, and the "client first" ordering this task described never
starts.

**Files:** none — this records why nothing ships.

### T8.8 — Document the admin surfaces (done)

Done. `docs/API.md` gains the members buckets, the delete/reactivate semantics,
the `PATCH` status rule, and the admin project endpoints; `docs/USER-MANAGEMENT.md`
replaces "deactivating" with deletion and the status table;
`docs/ARCHITECTURE.md` records E6 (deletion ends sessions) and E7 (admin projects
through the change feed).

**Files:** `docs/API.md`, `docs/USER-MANAGEMENT.md`, `docs/ARCHITECTURE.md`,
`docs/PLAN.md`.

### T8.9 — `GET /v1/admin/projects/{id}` (done)

Done. `mount` registers it under `adminMW` on `ProjectHandler`: it reads through
`ProjectRepository.GetByID`, 404s when the row is absent, and writes the same
`*entities.Project` the list and the `PATCH` write — the client decodes all three
into one DTO.

**Why it was needed.** T8.1–T8.8 left the two sides disagreeing about this path:
the client declares `AdminProjectsPort.project(id)` (implemented by
`HttpAdminProjectsRemote.project()`, with `HttpClientStatusExt.sendForStatus`
existing for exactly that 200/404 split) and nothing served it. The client turns a
404 into `null`, so an unmounted route read as "not on the server" rather than as
a bug.

**Why mounting was the answer rather than dropping the client's call.** The
client's admin project editor has to be port-backed (client plan `CLI-12`),
because a project the admin is not a member of is absent from that admin's Room
store and the sync push refuses a non-member — so the local editor cannot seed or
save it. This `GET` is its prerequisite, which is why the ordering is
server-then-client.

One correction to the premise this task was first recorded with: it said the
client "seeds its project edit screen" from this path. As of 2026-10-17 that was
intent, not behaviour — the client's destination ignored the selected project id
and nothing in its UI reached `project(id)`. `CLI-12` is where that gets wired.

**Files:** `internal/admin/projects.go`, `cmd/server/main.go`, `docs/API.md`,
`docs/ARCHITECTURE.md`.

**Tests:** `cmd/server/contract_test.go` — 200 with the sync project payload's
key set; unknown id → 404.

### T8.10 — `POST /v1/admin/projects` (done)

Done. `mount` registers the create under `adminMW` on `ProjectHandler`. The body
is the create form's fields plus the client-minted id; `isArchived`,
`nextItemNumber` and the stamps are the server's. It writes through
`Engine.ApplyAdminProject`, so the row, its member set and its change-feed entry
commit together — the create is the sync path's project push, authorized by the
admin route rather than by membership — and answers `201` with the same
`*entities.Project` the list, the seed and the `PATCH` write.

**Why it was needed.** The client's members section holds its create form in the
wide right pane (`CLI-15`), and the projects section is being given the same
arrangement (`CLI-16`). The projects screen's own create cannot serve it: that one
is offline-first, writing a local Room row plus an outbox row and letting the sync
engine push it, and the push authorizes creation by membership — so an admin
creating a project for other people would be pushed into its member list, and a
rejected push would land in the outbox where no admin screen looks. The admin
section reads server truth; its create has to land there.

**Why an existing id is a `409` rather than an upsert.** The engine's project
upsert takes `nextItemNumber` from the payload, so a create that overwrote the row
it collided with would rewind a project that has issued keys since and hand one
number out to two work items. A create is a create; the caller that lost a
response re-reads.

**Files:** `internal/admin/projects.go`, `cmd/server/main.go`, `docs/API.md`,
`docs/PLAN.md`.

**Tests:** `cmd/server/contract_test.go` — `201` with the client's key set, the
server-owned defaults (`isArchived` false, `nextItemNumber` 1, a live `version`),
the row on the active list and the change on the feed; blank name or prefix →
`400`; the same id twice → `409` with the row untouched.

---

## Milestone 9 — Customer install and exposure

The server has always been runnable — `docker compose up`, or the systemd unit — but
"runnable by the person who wrote it" is not the same as "installable by a customer".
This milestone is the gap between the two: one command that sets the stack up, and a
supported answer to "how do my colleagues reach this from outside the office".

### The constraint that shapes every task here

**The project must own no infrastructure and cost the customer nothing.** There is no
wildcard domain owned by this project, no provisioning endpoint, no hosted relay, and
the CLI never phones home. A customer's exposure is a provider *they* hold an account
with, or nothing at all. That rules out the two obvious designs — a vendor-owned
`*.tasktracker.app` wildcard (recurring cost, and every customer's ingress flows through
one account the project cannot support) and a hosted relay (a service to run).

It also rules out a static IP as the answer. A static IP solves *addressing* and nothing
else: a name, a certificate, and a routable path are three separate problems, and most
home and small-office connections are behind CGNAT, where a forwarded port cannot work
at all. Recorded here so the option is not re-proposed: **no static IP, no port
forwarding, no DDNS.**

### The providers, and why `lan` is the default

| Provider | Customer pays | Account needed | Stable HTTPS URL | Ships in |
| --- | --- | --- | --- | --- |
| `lan` | $0 | none | no (plain HTTP on the LAN) | **T9.8 — default** |
| `tailscale` | $0 | free Tailscale account | yes, `<host>.<tailnet>.ts.net` | T9.9 |
| `cloudflare` | $0 if they already own a domain | Cloudflare (free) | yes, their own hostname | T9.10 |
| VPS + Caddy | $5–10/month | — | yes | **documented only — T9.11** |

`lan` is the default because it is the only shape that needs **no account, no third
party, and no beta dependency**. A single-office team is productive with `tt install`
and nothing else, and "is this really free?" has a one-word answer. The two remote
providers are additive, behind one flag, so a customer is never locked into the choice
this project made for them.

`lan` has one honest limitation: the Android client blocks cleartext HTTP except for
`localhost` and `10.0.2.2` (`androidApp/src/main/res/xml/network_security_config.xml`),
so a phone reaches a LAN server only over HTTPS — which means `lan` serves the desktop
and iOS clients, and a phone needs a remote provider. That is a documentation fact, not
a defect to work around; adding a LAN host to the cleartext allow-list at build time
would put every customer's subnet in the shipped app.

`tailscale` is not free of caveats and the docs must say so: Funnel is still beta, its
bandwidth is capped at an unpublished number, and Tailscale describes the Personal plan
as non-commercial use. It is the right default *remote* answer for a hobby-scale install
and the wrong answer for a business that needs a commercial-safe path — which is what
`cloudflare` is for.

### T9.1 — Trusted-proxy aware client address

Promoted from **T5.6**, whose text is unchanged and still authoritative; it is
renumbered here because it stops being a follow-up and becomes a prerequisite. The
sign-in rate limiter keys on `r.RemoteAddr`, so behind *any* of the providers above
every request carries one address and the per-address budget (10 attempts / 15 min)
collapses into a single global budget for the whole team. The per-account limit keeps
working, which is why this was never a blocker before — but a customer whose five
colleagues trip a global sign-in limit on a Monday morning is a support ticket, not a
theoretical weakness.

The provider topology makes this cheaper than T5.6 assumed: with `cloudflared` or
`tailscale funnel` in front, the peer is a single address on the loopback or the compose
network, so `TRUSTED_PROXIES` is that one prefix and header spoofing is impossible —
nothing else can reach the bound port. No Cloudflare range list to maintain.

**Depends on:** T3.2. See T5.6 for the steps and tests.

### T9.2 — Per-route deadlines for attachment transfers

`cmd/server/main.go` sets `ReadTimeout` and `WriteTimeout` to 15 s globally, while
`ATTACHMENT_MAX_BYTES` defaults to 25 MiB. On a LAN that is invisible; over a home
uplink a 25 MiB upload cannot complete in 15 s, so the failure appears only once the
server leaves the building — exactly when a customer first tries it.

**Steps:**

1. Keep the 15 s global timeouts for ordinary requests; they are the right shape for a
   JSON API and they are what bounds a slowloris.
2. Give the two blob routes (`POST`/`GET .../attachments/{id}/blob`) their own deadlines
   through `http.ResponseController` — a read/write deadline sized from
   `ATTACHMENT_MAX_BYTES` and a conservative floor bandwidth, not an unbounded body.
3. Test with a body that arrives slower than 15 s but within the route's budget: the
   upload succeeds, and the same delay on a JSON route still fails. Both halves, or the
   test only proves the timeout was removed.

**Files:** `cmd/server/main.go`, `internal/attachments/handler.go`, `internal/config/config.go`.

**Depends on:** none.

### T9.3 — `SERVER_BIND`, and stop publishing `8080` on every interface

The server binds `":" + cfg.ServerPort`, i.e. every interface, and both compose
services publish `8080:8080` on the host's LAN address. For a tunnelled install the
proxy target is `http://127.0.0.1:8080` and the plain-HTTP port must not be reachable
from the network at all.

**Steps:**

1. Add `SERVER_BIND` (default `127.0.0.1`) to `internal/config`, validated as an IP
   literal; `0.0.0.0` is the explicit opt-in that `lan` mode sets. Default-loopback is
   the safe choice: an install that forgets the variable is unreachable rather than
   exposed.
2. Change the compose publish to `127.0.0.1:${SERVER_PORT:-8080}:8080`, and add an
   `expose` profile whose server service is not published at all (the sidecar reaches
   `http://server-pg:8080` over the compose network).
3. Document the variable in `.env.example` and in `docs/SELF-HOSTING.md`.

**Files:** `internal/config/config.go`, `cmd/server/main.go`, `docker-compose.yml`,
`.env.example`, `docs/SELF-HOSTING.md`.

**Tests:** `internal/config/config_test.go` — default is loopback, `0.0.0.0` accepted,
a hostname or empty value rejected.

**Depends on:** none.

### T9.4 — `tt` CLI: skeleton, state file, and idempotence

A second binary in the same module, `cmd/tt`, shipped as static builds for
darwin/linux/windows × amd64/arm64. Static because a customer installing a runtime first
is where the funnel dies; same module because the CLI and the server version must never
drift.

The CLI embeds the compose file, the systemd unit, and a `Caddyfile` with `go:embed`,
and pins the server image tag to its own build version — so `tt upgrade` cannot pull a
server newer than the CLI that is driving it.

**Commands (this task):** `install`, `status`, `doctor`, `logs`, `uninstall`.

**Rules that matter more than the command list:**

1. **Idempotent and resumable.** A state file (`/etc/tasktracker/install.json` on Linux,
   `~/.config/tasktracker/` elsewhere) records mode, provider, paths, and the image tag.
   Every step checks before acting; re-running `install` on an existing instance offers
   *repair*, never re-init.
2. **Wait on `/readyz`, not on compose.** `docker compose up -d` returns long before the
   healthcheck passes, and a CLI that prints "ready" early is the one bug every customer
   hits.
3. **Never recreate the JWT secret or the data volume.** Generated once with
   `crypto/rand`, written `0600`, printed once. `uninstall` requires the instance name
   typed back.
4. **`--dry-run` prints the rendered compose**, so a technical customer can audit what
   the wizard would do before it does it.

**Files:** new `cmd/tt/`, `internal/tt/…` (or `internal/install/…` if the state machine
is testable there), `go.mod` if a TUI/prompt dependency is taken, `Makefile`,
`.gitlab-ci.yml` (release job).

**Tests:** the state machine is a pure function of (state, command) and is tested
without Docker; the Docker-touching layer is a thin adapter over an interface.

**Depends on:** T9.3.

### T9.5 — `tt install`: the wizard

Wraps T9.4's state machine in the interactive path a non-technical customer follows:
pick a profile (sqlite by default — zero external dependencies), confirm the data
directory, generate the secret, start the stack, wait for `/readyz`, create the first
admin, then print the URL and the next step.

**Steps:**

1. Preflight: Docker present and the daemon reachable, port free, directory writable,
   enough disk. Each failure names the fix, not the symptom. When Docker is absent the
   fix is the offer in T9.12; this task ships with the instructions and declining is a
   supported answer, so the two can land in either order.
2. Profile choice, defaulting to `sqlite`; `postgres` offered for a multi-user install.
3. Admin bootstrap through the existing `server user-create` subcommand, not a new path.
4. End on a URL and a QR code — see T9.7.
5. If the chosen mode is `lan`, print the detected LAN address and warn when the host is
   on battery, Wi-Fi, or has a lid that closes: a laptop that sleeps is a server that is
   down, and it is the single most common failure for this deployment shape.

**Files:** `cmd/tt/`, `internal/install/…`.

**Depends on:** T9.4.

### T9.6 — `tt doctor` and `tt status`

`status` reports the facts an operator needs in one screen: container state, `/readyz`,
the bound address, the provider and its health, certificate/DNS expiry where applicable,
database size, and the age of the last backup. `doctor` is the same checks plus the
ones that only matter when something is wrong: DNS resolution of the public hostname,
TLS handshake, a request through the provider from outside, attachment-size versus the
provider's cap, and clock skew.

Both read from the same check functions, so a green `status` and a green `doctor` cannot
disagree.

**Files:** `cmd/tt/`, `internal/install/checks.go`.

**Depends on:** T9.5.

### T9.7 — Runtime server URL and `tasktracker://connect` pairing *(cross-repo)*

**This is the task that makes the rest usable, and the only one that touches both
repos.** Today the client's API root is build-time only — an Android Gradle property
into the manifest, an `Info.plist` key on iOS, `TASKTRACKER_API_URL` on the JVM. A
customer cannot rebuild the app, so every URL the CLI can print is one the app has no way
to accept. Until this lands, no provider is installable by a non-technical user.

**The client half is specified in the other repo**, not restated here:
`TaskTrackerKMP/docs/PLAN.md` §6, entries `6.1`–`6.4` (runtime override, Settings field,
validation, and the `connect` link). Its specs are
`TaskTrackerKMP/specs/6.1-runtime-api-root.md` and
`TaskTrackerKMP/specs/6.4-connect-deep-link.md`. Work those; do not re-derive them.

**The server half is small:** `tt install` renders
`tasktracker://connect?url=<the URL it just verified>` and a QR code of it. There is no
wire-format change — no response gains a field — so this is not a DTO contract change,
but it is still a dual-repo ACK because the link grammar is shared.

**Ordering:** client first (`6.1`–`6.4`), then the CLI renders the link. This is the
dual-engine "client is the fixed point" rule applied to a *configuration* surface rather
than a DTO. The client half has no server dependency, so it can land before T9.4 exists.

**Files:** `TaskTrackerServer` — `cmd/tt/`. The client files are listed in §6's specs.

**Depends on:** T9.5 (for the CLI side only).

### T9.8 — `tt expose --provider=lan` (default)

Finishes today's behaviour rather than adding a feature: set `SERVER_BIND=0.0.0.0`,
publish the port, detect and print the LAN address, and verify reachability from the
host's own LAN address (not loopback — the loopback check passes even when the bind is
wrong, which is the mistake this task exists to catch).

**Files:** `cmd/tt/`, `docker-compose.yml`, `docs/SELF-HOSTING.md`.

**Depends on:** T9.3, T9.5.

### T9.9 — `tt expose --provider=tailscale`

The $0 remote path: no domain, no port forwarding, no CGNAT problem, a valid certificate,
and TLS terminated on the customer's own machine — Tailscale's relay only carries
ciphertext, which makes it the cleanest of the remote options architecturally.

**Steps:**

1. Detect `tailscale` on the host; if absent, print the install command for the platform
   rather than installing it silently.
2. Require an authenticated tailnet and HTTPS enabled; guide through the two dashboard
   steps that cannot be automated without an API token, and do not pretend otherwise.
3. `tailscale funnel --bg 443 http://127.0.0.1:8080` (or `tailscale serve` when the
   customer wants tailnet-only access instead of public).
4. Verify with an HTTPS request to the `ts.net` name, then print it and a QR for T9.7.
5. Set `TRUSTED_PROXIES` to the loopback prefix, because Funnel connects to the target
   from `127.0.0.1`.

**Files:** `cmd/tt/`, `docs/SELF-HOSTING.md`.

**Depends on:** T9.1, T9.7, T9.8.

### T9.10 — `tt expose --provider=cloudflare`

For a customer who already owns a domain: `tt expose --provider=cloudflare
--domain tasks.theirco.com` adds a `cloudflared` sidecar, walks through creating a
tunnel and an API token, and verifies the public hostname end to end. Nothing is
provisioned by this project; the tunnel lives in the customer's own account, so the
project holds no credential and no shared failure domain.

**Steps:**

1. Refuse a hostname whose zone is not on Cloudflare, and say what to change at the
   registrar — this is the one step that cannot be automated and the docs must be
   honest that it takes minutes to hours to propagate.
2. `cloudflared tunnel` create/route through the customer's API token, token stored in
   the OS keychain or a `0600` file, never in the compose file.
3. Set `TRUSTED_PROXIES` to the compose subnet the sidecar sits on.
4. Validate `ATTACHMENT_MAX_BYTES` against Cloudflare's 100 MB request-body cap and
   refuse a larger value at startup with a message naming the provider — otherwise the
   customer gets a proxy 413 that never reaches this server's logs.

**Files:** `cmd/tt/`, `docker-compose.yml`, `internal/config/config.go`,
`docs/SELF-HOSTING.md`.

**Depends on:** T9.1, T9.7, T9.8.

### T9.11 — VPS + Caddy: documented, not implemented

The always-on option for a customer who does not want to run hardware. **Deferred on
purpose** — recorded so the shape is not re-derived later and so nobody half-builds it.

What it would be: the same compose stack on a VPS, Caddy in front with automatic ACME,
`80`/`443` open, `SERVER_BIND=127.0.0.1`, and `TRUSTED_PROXIES` set to the Caddy
container's address. The CLI addition is `tt install --vps` plus a cloud-init path; the
server-side prerequisites are all in T9.1–T9.3, which is why doing those first is
cheap even if this never ships.

**Files:** `docs/SELF-HOSTING.md` only, when written. No code in this milestone.

**Depends on:** T9.1–T9.3 (if it is ever picked up).

### T9.12 — Guided Docker bootstrap in `tt install`

A customer's first failure is `docker: command not found` on a host they cannot
administer, and the instructions T9.5 prints assume a command line they may not use. This
task turns that dead end into a question. It is the only place in the CLI that changes the
host's software, so the consent rules *are* the task:

1. **Never automatic, never default-yes.** The prompt defaults to `No`; a bare Enter
   declines. `--dry-run` prints the command and executes nothing. A non-interactive
   `tt install --yes` **fails closed** — an unattended root package install is the one
   thing this CLI must never do.
2. **No `curl | sh`.** Never pipe a remote script into a shell, Docker's convenience
   script included. Use the distribution's package manager, from the distribution's own
   repositories. Where the convenience script is the practical answer, the CLI *prints*
   it for the customer to run; it does not run it.
3. **Three distributions, then stop.** Debian/Ubuntu, Fedora/RHEL family, Arch. Anything
   else: print the official URL and exit rather than guess a package manager.
4. **Privilege is stated, not assumed.** The customer sees the exact command and the
   `sudo` it needs before it runs. If `sudo` is missing or wants a password the CLI cannot
   supply, print the command and stop. Joining the `docker` group is root-equivalent on
   the host, so it is a separate explicit yes/no — never a side effect of the install.
5. **Verify, then hand back.** Start the daemon, wait for `docker info`, and only then
   resume T9.5's preflight. A failed install relays the package manager's own output and
   rolls nothing back; the CLI does not uninstall a package the customer's OS owns.

**Files:** `cmd/tt/install.go`, `internal/install/docker.go` (detection and command
selection — a pure function of `os-release`, `docker` presence, and `sudo` availability),
`internal/install/exec.go` (the executor interface over `os/exec`).

**Tests:** the selector is table-tested per distribution with no process spawned; the
executor is faked. One test asserts the non-interactive path fails closed, one asserts a
declined prompt runs nothing.

**Depends on:** T9.4, T9.5.

### T9.13 — Customer install and onboarding documentation

**Deliberately the last task in the milestone.** Every command in it must already exist
and have been run once for real, because a doc written against stubs is the artifact that
ages silently: it describes a CLI nobody has, in a voice that reads as verified.

The repo README stays developer-facing (configuration, Docker, migrations, CI) *and* is
the scope table `scripts/check.sh` reads, so it keeps that job and gains a short
"Installing for a team" section pointing at the new doc. `docs/SELF-HOSTING.md` is
T9.11's and is left alone.

**`docs/INSTALL.md` covers, in order:**

- What the customer needs: a host, Docker (or T9.12's offer), and which provider wants
  which account — `lan` wants none.
- Install `tt`, then `tt install`, copied from a real terminal on a clean host.
- What the QR and the `tasktracker://connect` link do — carry the server URL (T9.7) — and
  what they do not: carry a password.
- The `lan` limitation stated plainly: the Android client blocks cleartext HTTP except
  `localhost`/`10.0.2.2`, so a phone needs `tailscale` or `cloudflare`.
- Day 2: `tt status`, `tt doctor`, `tt upgrade`, backup and restore (the SQLite file, a
  Postgres dump), and uninstall.
- A troubleshooting table where each row is the **actual** message → cause → fix,
  including `docker: command not found` (→ T9.12) and the Android cleartext refusal.
- The constraint, restated once so no customer expects otherwise: the project owns no
  domain, no relay, and no hosted service, and `tt` never phones home.

**Files:** `docs/INSTALL.md` (new), `README.md` (a pointer section only).

**Validation:** every command run on a clean host or container with its transcript in the
MR; the documented output matches `tt --help`; the README section is a pointer and not a
second copy; nothing appears in the doc that is not in `cmd/tt`.

**Depends on:** T9.5, T9.7 (client half), T9.8–T9.12.

### Milestone 9 ordering

`T9.3` → `T9.4` → `T9.5` → `T9.8` gives a working, honest, free install for a
single-office customer. `T9.12` turns the first failure they hit — `docker: command not
found` — from instructions into an offer, and is additive: `T9.5` ships without it. `T9.7`
is the client-side unlock and can land in parallel with any of them, but nothing remote is
*usable* until it does. `T9.1` and `T9.2` are prerequisites for the remote providers only,
and both are cheap. `T9.9` before `T9.10` because it needs no domain and therefore no
support call. `T9.13` is last by construction: it documents commands that must already
exist. `T9.11` stays unstarted.
