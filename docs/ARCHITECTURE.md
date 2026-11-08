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
| `/healthz` | GET | security, request-id, logging, CORS, recover | liveness; returns `ok`; does not check the database |
| `/readyz` | GET | security, request-id, logging, CORS, recover | readiness; pings the database, `503` when it fails |
| `/v1/auth/login` | POST | + no-store | email + password → token pair; starts a token family; `429` when rate-limited |
| `/v1/auth/refresh` | POST | + no-store | rotates the refresh token and returns a new pair |
| `/v1/auth/signout` | POST | + no-store | revokes the refresh token's whole family; `204` |
| `/v1/sync/push` | POST | + Auth | apply client changes, return per-entry verdicts |
| `/v1/sync/changes/projects` | GET | + Auth | cursor-paged pull; the scope the client asks for |
| `/v1/sync/changes/workItems` | GET | + Auth | cursor-paged pull; carries project changes too |
| `/v1/sync/changes/project` | GET | + Auth | alias for `/projects`, kept for a build that shipped against it |
| `/v1/sync/changes/workItem` | GET | + Auth | alias for `/workItems` |
| `/v1/admin/users` | POST, GET | + Auth + AdminOnly | admin creates users and lists them in the `active`/`deleted` buckets; no self-signup |
| `/v1/admin/users/{id}` | PATCH, DELETE | + Auth + AdminOnly | admin updates, soft-deletes or reactivates a user |
| `/v1/admin/projects` | GET | + Auth + AdminOnly | admin lists every project in the `active`/`archived` buckets |
| `/v1/admin/projects/{id}` | GET | + Auth + AdminOnly | admin reads any project, to seed the edit screen for one they are not in |
| `/v1/admin/projects/{id}` | PATCH | + Auth + AdminOnly | admin edits any project, applied through the sync engine |
| `/v1/users/search` | GET | + Auth | directory search; any authenticated user |

The offline-first sync design is the strongest part of the scaffold and matches
what the client already implements: an append-only `sync_log` keyed by
`(entity, created_at, id)`, opaque JSON payloads, and a `version` string per
entity for optimistic concurrency.

## Conflict policy

The client is offline-first, so two devices can edit one entity before either
syncs. The server's rule is **server-wins, client re-pulls**:

- Every entity carries a `version` string. A push may name the version the
  client last saw, and the server compares it with the stored row inside the
  same transaction.
- A push naming a stale version is answered `conflict`, and the verdict's
  `version` field carries the version the server now holds. The client drops
  its local change for that entity, re-pulls it and replays its intent on top.
- A push naming no version is last-write-wins. The engine has already read the
  row, so the compare-and-swap runs against the stored version rather than
  failing on the payload's empty one — the client is saying it does not track
  versions for this entity.
- Timestamps are never consulted.

Last-write-wins by timestamp and field-level merge were both rejected: the
first trusts client clocks, so a device with a skewed clock would win every
race; the second needs the server to understand a payload it deliberately
treats as opaque. The verdict vocabulary is `accepted`, `conflict`,
`rejected` and `retry_later`; `conflict` and `retry_later` are the two the
client is expected to act on.

`rejected` is terminal on the client, so the server spends it only where a
retry could never help: a write the row's own values cannot satisfy, which
`entities.PermanentWriteError` recognises from the driver's code (Postgres
SQLSTATE class `22` and `23502`/`23514`/`23P01`; SQLite `ErrConstraintCheck`
and `ErrConstraintNotNull`). A foreign-key violation stays `retry_later`
because it can be a child that arrived before its parent, and a unique
violation because it can be a re-push after a lost response. A failure
writing the `sync_log` row or committing keeps `retry_later` too even when
the database error is permanent: that is the server's own fault, and
dead-lettering client work over it is the failure mode `docs/API.md` warns
against.

## Change feed retention

The cursor is the numeric `sync_log.id`, which is monotonic. Rows are pruned
after `SYNC_LOG_RETENTION` (default 30 days; `0` keeps them forever), swept
hourly, so the feed does not grow without bound on a long-lived install.

Pruning is what makes a cursor able to go stale, so `Pull` compares the
caller's cursor with the oldest row still retained and sets
`PullPage.ResyncRequired` when the cursor points before it. A client that sees
the flag drops its cursor and pulls from the start — there is no separate
"resync" endpoint, because pulling with no cursor is already that operation. A
client that has never synced is not flagged: it is already pulling everything
that survived.

## Wire compatibility

The client is an installed mobile app that syncs offline and cannot be updated on
this server's schedule. That makes **the installed client the fixed point**: when
the two disagree, the server changes. Four mismatches have already shipped on that
basis — `entityId` against `entityID`, an undeclared `resyncRequired`, ISO-8601
timestamps against `int64` epoch millis, and `token_type`/`expires_in` on the
token pair — and the first two were invisible from the server side alone, because
Go decodes JSON keys case-insensitively and because the pull path re-serves stored
bytes rather than re-encoding them.

The fourth was invisible for a different reason, and a worse one: the executable
contract had drifted *with* the server. `cmd/server/contract_test.go`'s
`clientTokenPair` carried all seven keys the server sent, including the two no
client version has ever declared (`git log -S'expires_in' --all` in
`TaskTrackerKMP` returns nothing), so the test **required** the drift it existed to
catch. A contract test that restates this server's own struct is a mirror; it has
to pin the client's declared key set to be a check at all.

The client leaves `ignoreUnknownKeys` off on purpose, which gives two rules:

- **A response may not gain a field.** An undeclared key is a decoding failure,
  not a value the client skips, so adding one breaks every installed build
  exactly as removing one does.
- **A response may not lose or rename a field the client declares without a
  default.** Kotlin treats a property with a default as optional and one without
  as required — `hasMore` is always sent, `resyncRequired` and `nextCursor` may
  be omitted.

A new response field therefore ships **client first**: a client that declares it
with a default works against both servers, and only then may the server send it.
Anything that cannot follow that order is a new `/v1` version and a migration
path.

`docs/API.md` is the written contract; `cmd/server/contract_test.go` is the
executable one, and it compares key names rather than decoded values because a
case-insensitive decode cannot see a case-only rename.

## Verification status

| Check | Result |
| --- | --- |
| `go.mod` parses, `go.sum` complete | yes |
| `gofmt -l .` | clean |
| `go build ./...`, `go vet ./...` | pass |
| `go test ./...` | passes, 8 packages |
| `go test -race ./...` | passes; also run in CI |
| server on SQLite | boots; signs in; push/pull round-trips every entity type |
| server on Postgres | covered by the CI `test-postgres` job |

The blockers below were fixed in Milestones 0 and 1; they are kept here as the
record of what was wrong. C1 and C2 in particular are worth reading before
writing new SQL.

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
- **C3 — fixed.** `Project.NextKey` built the key with
  `string(rune(n+'0'))`, so item 10 produced `KEY-:` and 11 produced `KEY-;`
  instead of `KEY-10`, and it read and incremented the counter in memory.
  `Project.Key` is now a pure `strconv.Itoa` formatter and
  `ProjectRepository.AllocateKey` reserves a number with a single
  `UPDATE ... RETURNING`, so concurrent callers cannot collide.
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
- **E4 — fixed.** There was no `Dockerfile`, systemd unit, or self-hosting
  guide. A multi-stage Docker build (`golang:1.27-alpine` → `alpine:3.21`) now
  produces a 44 MB image with CGO enabled, a non-root `app` user, and a `/data`
  volume for SQLite. `docker-compose.yml` provides `postgres` and `sqlite`
  profiles. See [Docker](../README.md#docker) in the README.
- **E5 — fixed.** There was no way to create the first user, so sign-in could
  never succeed on a fresh install. The decision: **no self-signup** — the
  admin creates users, and the first admin is bootstrapped via the CLI
  (`cmd/server user-create --admin`). After that, admin-only `POST
  /v1/admin/users` provisions the rest. A `role` column (`admin`/`member`)
  on `users` gates the admin routes via the `AdminOnly` middleware, which
  reads the `role` claim from the JWT. Directory search
  (`GET /v1/users/search`) is open to any authenticated user so the KMP
  client's `TeamPort` can find teammates without admin privileges. See
  [`docs/USER-MANAGEMENT.md`](USER-MANAGEMENT.md).
- **E6 — fixed.** Admin management could create and deactivate users but not
  remove access. `DELETE /v1/admin/users/{id}` used to set `status` to
  `"inactive"`, which still permits sign-in, and left every token alive, so a
  "deleted" member stayed signed in. Deletion is now its own `status` value,
  `"deleted"`, which refuses sign-in and refresh, and the member's live refresh
  tokens are revoked through a new `RevokeAllForUser` (the existing `RevokeFamily`
  closes one sign-in; a member may hold several). Revoking *before* the status
  write closes the window in which a refresh in flight mints a token the revoke
  never sees. `PATCH` is refused the value `"deleted"` so that removing access
  cannot bypass the revocation. Because access tokens are stateless and checked
  only for `exp`, one already issued stays usable for up to its TTL — the same
  bound E3 accepts for refresh-token abuse.
- **E7 — fixed.** An admin could not see or edit a project they were not a member
  of, because projects reach a client only through the sync feed, which is scoped
  to membership. `GET /v1/admin/projects`, `GET /v1/admin/projects/{id}` and
  `PATCH /v1/admin/projects/{id}` now expose the whole server to an admin — the
  list to find the row, the by-id read to seed the edit screen, the `PATCH` to
  save it. The edit is applied through the sync engine rather than by writing the
  repository directly: the row and its `sync_log` entry commit in one
  transaction, so the project's members pull the edit as they would one of their
  own. `ApplyAdminProject` shares `apply`'s body and skips only the membership
  check, which the admin route performs instead. The stored payload stays the
  JSON *string* a client sends, not a nested object — the client's DTO declares
  `payload` as an opaque `String` and rejects an unknown shape.

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

Steps 1–3 were the blocking Milestone 1 work. The build is green today, so a red
CI run is a real regression.

## Decisions retained from completed task specs

The per-task specs that produced these decisions were removed before the first
release. Only constraints a future change could silently violate are kept here;
validation runs, counts, handoffs and per-task file lists were dropped.

### Wire contract

- **The installed client is the fixed point.** A response may not gain, lose or
  rename a key the client declares without a default, because it strict-decodes
  (`ignoreUnknownKeys` off). Add a field client-first, with a default, then send
  it; anything else is a new `/v1` version. `docs/API.md` is the written
  contract, `cmd/server/contract_test.go` the executable one — it must pin key
  names, since Go matches JSON keys case-insensitively and a decode-only check
  cannot see a case-only rename.
- **One JSON convention.** Response keys are `camelCase`, timestamps ISO-8601 UTC
  whole seconds via `internal/entities/timestamp.go`. SQL column names, the
  agent runner's `last_seen_at` and third-party LLM request keys are not wire
  keys and stay `snake_case`.
- **`/v1/me` identity comes from the request context**, never a body, query or
  path value. `UserRepository.Update` (admin) and `UpdateProfile` keep disjoint
  `SET` lists, so neither route can erase the other's field. `presence` is not
  the account-lifecycle `status`; a profile write must never revive an
  `inactive`/`deleted` account.
- **Blob bytes never enter the sync payload.** The `attachment` sync entity is
  metadata-only JSON and the two blob routes use `http.MaxBytesReader`, not
  `httpx.DecodeJSON`, so they bypass the 1 MiB JSON body cap. `tagIDs` must be a
  non-null array on scan; `color`/`icon` are opaque client strings, never
  validated against a server enum.
- **Blank `q` is a different query.** `GET /v1/users/search` with a blank `q`
  lists non-deleted accounts (the addable set); a non-empty `q` deliberately
  includes deleted authors (the historical directory). Do not "fix" the
  asymmetry or add a route — the client already calls this one.

### Database and schema

- **`0001` is a pre-release baseline.** `internal/db/migrations/` holds one
  version for both drivers, squashed before release when no installed database
  existed. Never edit it after release; add the next version for *both* drivers,
  keep statements `IF NOT EXISTS`, and verify on SQLite **and** Postgres — a
  SQLite-only pass proves nothing about Postgres.
- **Entity `version` columns are optimistic locks, not schema versions.** The
  sync engine uses them for compare-and-swap conflict detection; removing them
  breaks offline-first sync. `StateVersion` and `ConfigVersion` are separate
  guards on `install.json` and `agent.json`.
- **Blob path invariant.** `${root}/${id}.blob` is shared by
  `engine.removeAttachmentBlob` and `attachments.BlobStore.path`. Uploading a
  blob must not bump the attachment version (no rebroadcast through sync), and
  a failed file delete must never fail the metadata delete.
- **Permanent write errors are narrow.** Only CHECK-constraint violations earn
  `rejected`; FK, unique and serialization failures are retryable. `rejected`
  is terminal on the client, so a wrongly dead-lettered change is silent data
  loss. `sqlite3.ErrConstraintDatatype` does not exist in the pinned driver.
- **Domain rules stay out of the sync engine.** It routes opaque payloads;
  whether a locked sprint may take an edit is the client's call. The sprint
  overlap check lives in the repository (inside the push transaction), because
  only that layer sees the project's other sprints.

### Install and exposure

- **The project owns no infrastructure.** No domain, relay, provisioning
  endpoint, hosted service, static IP, port forwarding or DDNS; `tt` never
  phones home. `lan` is the default provider and serves desktop, Android and iOS
  over plain HTTP on the customer's network; `tailscale` and `cloudflare` are
  additive behind one flag. Exposure is a provider the customer holds an account
  with, or nothing — the project earns nothing and cannot carry a recurring cost.
  A static IP solves addressing only, and CGNAT makes port forwarding impossible
  for most home and small-office connections; `lan` needs no account, third party
  or beta dependency.
- **`SERVER_BIND` defaults to `127.0.0.1`** and is validated as an IP literal.
  Inside a container, loopback is the container's own, so the compose service
  must set `0.0.0.0` explicitly and only the *published* side moves to loopback.
- **The installer is a pure state machine.** `Next` is pure; a second `install`
  is a `repair`, never `init`, and repair defaults come from existing state. The
  JWT secret is generated once and survives byte-for-byte, and the rendered
  image tag is the CLI's own version, so a repair cannot pull a newer server
  than the binary that planned it. State and compose are written atomically at
  `0600`; a corrupt state is refused, not replaced.
- **`tt expose` is not persisted.** It writes the publish into the rendered
  compose; `tt install` re-renders loopback, so a repair resets exposure (docs
  tell the operator to re-run `expose`). Verification GETs `/readyz` at the
  detected LAN address — never `127.0.0.1` — restores the previous compose on
  failure, and prints the pairing link and QR only after success.
- **Pairing link grammar.** `tasktracker://connect?url=<QueryEscape(the WHOLE
  URL)>`; escape `&`, `#`, `%` too, because the client decodes exactly once, so
  an existing `%2F` goes on the wire as `%252F`. Render a QR only for a verified
  URL and refuse links over 2000 bytes, since the QR library ignores the encode
  error and would panic.
- **Trusted proxies key on the peer, not the forwarded value.** An untrusted
  peer's `X-Forwarded-For`/`Forwarded` is never read; walk right-to-left to the
  first untrusted hop. Reject `0.0.0.0/0`, `::/0` and bare addresses, and reject
  the whole header on one bad hop rather than skipping it.
- **Tailscale keeps the server on loopback** with
  `TRUSTED_PROXIES=127.0.0.1/32`; funnel is beta, Personal is non-commercial,
  and ACLs may disable it. Cloudflare uses a `cloudflared` sidecar, derives the
  trusted CIDR from the compose network, and keeps the API token in the OS
  keychain or a `0600` file — never in compose or state. Its Free plan's 100 MiB
  request cap is why `ATTACHMENT_MAX_BYTES` is validated at startup.
- **Only the blob routes get longer deadlines.** Global timeouts stay 15 s read /
  15 s write / 60 s idle for slowloris protection; the two blob routes use
  `http.ResponseController` with `max(15s, cap / 1 MiB/s)`.
- **`status` and `doctor` are read-only.** They print an instruction, never run
  a repair, and never print an install secret. `doctor` fails only on a failed
  check; warnings and skips do not fail. A check that cannot be proven stays a
  documented `skip` — a permanently yellow line trains operators to ignore it.
- **Docker bootstrap is the only host-changing step.** The prompt defaults to
  `No`, `--dry-run` executes nothing, non-interactive `--yes` fails closed, and
  there is no `curl | sh`. Docker-group membership (root-equivalent) needs its
  own explicit yes. SIGINT maps to exit `130`, and any future blocking read must
  take the run context because `signal.NotifyContext` disables the default.
- **Distribution assets.** The license is MIT + Commons Clause + no-rebranding
  (source-available, not OSI) and the Docker image is the only distribution
  channel, so `/app/LICENSE` must be copied into the runtime stage. The embedded
  template is `docker-compose.tmpl` while the rendered customer file must stay
  `docker-compose.yml`; `State.ComposePath` and every repair depend on that name.

### Testing traps

- **Both dialects, and both parser shapes.** Postgres coverage needs
  `TASKTRACKER_TEST_POSTGRES_URL` pointed at a throwaway server.
  `compose ps --format json` is an array on newer Compose and one object per
  line on older releases — keep both paths or it works only on the author's
  machine. Fake-Docker e2e must put an executable `docker` on `PATH` and isolate
  `HOME`, or it writes to the developer's real install state.

### Agent execution

- **Agents reach out; the server never dials in.** A runner long-polls
  `POST /v1/agent/runs/claim` and reports progress through `events`,
  `heartbeat` and `finish`, authenticated by its `ttag_…` machine token — no
  inbound connectivity to an admin workstation is assumed. Pairing codes
  (`ttpc_…`) are short-lived, single-use and project-scoped; both kinds of
  credential are stored only as SHA-256 hashes, which is sufficient for 32
  random bytes.
- **Dispatch is not a start.** A run is persisted `queued` before delivery and
  only a claim moves it onward; the unique
  `(work_item_id, requested_by, idempotency_key)` index makes a replay return the
  stored run. A claim is a *lease*, not a lock: `RunLeaseReaper` fails runs whose
  heartbeats stop, so a crashed worker cannot leave a task looking started.
  `agentEnabled` on the project refuses dispatch with 409.
- **`agent_runners.project_id` is nullable on purpose.** NULL is a legacy
  machine-wide registration that claims across its owner's projects; a paired
  runner sees only its own project. Claim, events and finish enforce both
  project scope and the claiming-runner check.
- **`repoUrl` is admin-only metadata, not a credential.** It is emitted only
  when non-empty, and only after the client shipped a DTO declaring the key with
  a default — an older installed client still fails on a project that has one,
  so deployment order matters. An omitted `repoUrl` on `PATCH` preserves the
  stored value and a membership sync push cannot write it. Private repository
  access belongs to the agent's own credential store.
- **`taskDocument` is a bounded, versioned graph, not a history dump.** Stable
  namespaced node IDs and directional edges (`DEPENDS_ON` is task →
  prerequisite), explicit node/tag/dependency/comment/rune limits, and
  membership-filtered content. The worker receives the stored document
  verbatim, so a new `schemaVersion` is additive; `targetBranch` stays omitted
  until project metadata carries a branch.
- **The agent wire names are the ones that shipped, not the ones a spec proposed.**
  `host` (pairing-code request/response and runner objects), dispatch request
  `runnerId` → run response `requestedRunnerId`, and `diff` (finish request → run
  response) are the contract. `requestedRunnerID` and `resultDiff` were never on
  the wire and are not being adopted: Go decodes case-insensitively but Kotlin does
  not, so a case-only rename is a hard break for installed builds. This is why
  `cmd/server/contract_test.go` pins closed key sets from raw JSON maps rather than
  decode-based assertions, and `internal/db/migrate_test.go` pins the `0004`
  columns on both dialects. All three response keys are `omitempty` and defaulted
  in the client DTO, so an untargeted run with no diff gains no new key at all.
- **`host` is a stated fact; `--pairing-code` is a credential, not a bypass.**
  `desktop` and `remote` are the only kinds; empty or null means "not stated" for a
  legacy machine or an older client's pairing code, and reading it as `desktop`
  would mislabel every legacy machine. Redeeming a code copies its `host` and
  `projectId` onto the runner, so the machine cannot name its own project.
  `tt agent connect --pairing-code` spends the one-use `ttpc_…` code at
  `POST /v1/agent/pair`, asks for no email or password, and saves the returned
  `ttag_…` token into the same `runner.Config` as a legacy registration — the
  runner then authenticates and claims exactly like any other.
