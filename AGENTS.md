# AGENTS.md

Working agreement for `TaskTrackerServer`. Read this before changing anything.

- `docs/PLAN.md` — the ordered, task-by-task implementation roadmap. Work from
  this; one task, one branch, one MR.
- `docs/API.md` — the HTTP wire contract. Read it before changing a request or
  response shape.
- `docs/ARCHITECTURE.md` — the design record and review: what exists, what is
  broken, and why.
- `README.md` — how to configure and run the server.

## What this is

The self-hosted backend for the `TaskTrackerKMP` client. One Go binary and one
database, run by the customer on their own server. There are no microservices
here on purpose: an operator should need a `DATABASE_URL`, a `JWT_SECRET`, and
`./server`. Do not add a service, a message bus, or a second deployable without
a reason that survives that constraint.

## The wire format is a contract with a client you cannot update

The client is an installed mobile app that syncs offline and cannot be updated on
this server's schedule. **The installed client is the fixed point**: when the two
disagree, the server changes. Three contract mismatches have already shipped and
been found this way — `entityId` vs `entityID`, an undeclared `resyncRequired`,
and ISO-8601 timestamps against `int64` millis — and the first two were invisible
from the server side alone.

Two rules follow, and the second is the one that gets missed.

**A response may not gain a field.** The client leaves `ignoreUnknownKeys` off,
deliberately: its `JSONCoding` says a field renamed on the server "has to fail
loudly at the boundary" rather than silently decode as null on every device. The
same setting makes an *added* key a decoding failure rather than a value it
skips, so a new response field breaks every installed build exactly as a removed
one does.

**A response may not lose or rename a field the client declares without a
default.** Kotlin treats a property with a default as optional and one without as
required.

The safe order for a new response field is therefore **client first**: ship a
client that declares it with a default — which works against both the old and the
new server — and only then start sending it. Anything that cannot follow that
order is a new `/v1` version and a migration path.

`docs/API.md` is the written contract; `cmd/server/contract_test.go` is the
executable one. It decodes responses into the client's own DTOs *and* compares the
key names, because Go matches JSON keys case-insensitively and a decode-based
check cannot see a case-only rename.

## The command that decides whether you are done

```bash
scripts/check.sh
```

That script is the one place the command lives:

```bash
gofmt -l . && go build ./... && go vet ./... && go test ./...
```

It runs `gofmt` as a **gate** — a bare `gofmt -l . && …` cannot fail on formatting, because
`gofmt -l` exits 0 even when it lists files, so the script exits non-zero on a non-empty list.
Scopes: `all` (default), `race`, `docs`.

All four pass on `main`. Keep it that way: never make a check green by deleting
code, stubbing a proof, or weakening an assertion.

Run `scripts/check.sh race` before touching anything concurrent — the sync engine,
the rate limiter and refresh-token rotation all are. CI runs it, so a plain local
run is not the same check.

## Toolchain

- **Go 1.27**, matching `go.mod`. Do not lower it.
- **CGO required** — `mattn/go-sqlite3` is a cgo driver. Build with
  `CGO_ENABLED=1` and a C compiler present.
- After changing imports run `go mod tidy` and commit `go.mod` and `go.sum`
  together.

## Comments

Brief and precise. A comment earns its place by explaining a non-obvious *why*;
the code already states the *what*. No restatement of the line below, no
decorative banners, no commented-out code.

## Configuration and secrets

- Configuration comes from the environment, optionally seeded from `.env`.
- **A secret never enters this repository.** `.env` is git-ignored; only
  `.env.example` is tracked and it holds no real values.
- Add a variable in three places: `.env.example`, a read in
  `internal/config/config.go`, and a rule in `Config.validate` when a bad value
  should stop startup. Details are in the `go-config-and-secrets` skill.

## Architecture boundaries

`internal/` is the module boundary — Go enforces that nothing outside this
module can import it, which is the whole point of keeping the server one module.

- `cmd/server/main.go` is the composition root. Concrete types are constructed
  and wired here and nowhere else; `mount` is the route table.
- `internal/entities` owns models and repositories. A repository is the only
  place that knows SQL, and `internal/entities/timestamp.go` is the only place
  that knows the wire date format.
- `internal/auth`, `internal/middleware`, `internal/sync` own their concerns.
- A handler does not reach into the database directly. It calls a repository or
  the sync engine.

Place new code in the closest existing package. Prefer a new file over growing a
320-line one. See the `go-server-architecture` skill.

## Database changes

The schema is versioned SQL in `internal/db/migrations/`, one file per driver
sharing a version number (`0001_initial.postgres.sql` and
`0001_initial.sqlite.sql`), embedded in the binary and applied on startup by
`internal/db/migrate.go`. Add a new version for **both** drivers: `go test
./internal/db` fails if a version has only one dialect file. Statements stay
`IF NOT EXISTS` so a database created before the runner existed is adopted the
next time the binary starts.

Both drivers are supported and both are tested: the suite runs on SQLite by default
and on Postgres when `TASKTRACKER_TEST_POSTGRES_URL` points at a throwaway
server (`internal/dbtest`, and the CI `test-postgres` job). A schema change is
verified only once it has passed on both — SQLite succeeding proves the SQLite
path and nothing more.

Read the `go-migrations-and-repositories` skill before touching a table or a
repository, and change the DDL and the repository in the same commit.

## Tests

- Ship a test with behaviour changes; put it next to the code it covers.
- `internal/config/config_test.go` is the pattern for anything that reads the
  environment: isolate the variable with `t.Setenv` (see its `isolate` helper)
  rather than depending on the developer's real `.env`.
- A test that has never failed is not yet evidence. Confirm it fails when the
  behaviour it claims to protect is removed.

## Git workflow

One task, one branch, one merge request to `main`. Never commit directly to
`main`.

```bash
git switch -c feat/<kebab-case-slug>
# ... work ...
```

**The review is the merge gate — do not wait for a human to approve it.** When
the task is done, read your own diff as a stranger would and answer three
questions:

1. Is the validation command green? `scripts/check.sh`
2. Does the diff change only what the task called for — no drive-by cleanup,
   no bundled task?
3. Does every behaviour change ship with a test that fails without it?

All three yes → the review approves, so merge:

```bash
git switch main
git merge --ff-only feat/<kebab-case-slug>
git branch -d feat/<kebab-case-slug>
```

Any no → fix it on the same branch and review again. A red or unreviewed branch
is never merged; a branch the review has approved is merged without asking
again.

Where a remote is configured, push the branch and merge the MR instead. Push
still needs its own authorisation — publishing to a remote is a separate action
from merging.

## Skills

Repository skills live in `.opencode/skills/*/SKILL.md`. Read each skill whose
description matches the task before starting.

- `go-server-architecture` — where code belongs, how the composition root and
  handlers are wired, and what not to break in the monolith.
- `go-config-and-secrets` — adding configuration, reading it safely, and keeping
  secrets out of the repository.
- `go-migrations-and-repositories` — schema changes, repository patterns,
  optimistic locking, and the Postgres/SQLite differences.
