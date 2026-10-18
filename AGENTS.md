# AGENTS.md

Working agreement for `TaskTrackerServer`. Read this before changing anything.

> **Cross-project rules:** if your task touches both repos, read `../AGENTS.md`
> first — it governs wire format contracts, the dual-repo ACK protocol, and
> build isolation boundaries. This file is the contract for Server-only work.

- `docs/PLAN.md` — the ordered, task-by-task roadmap. One task, one branch, one MR.
- `docs/API.md` — the HTTP wire contract. Read it before changing a request or
  response shape.
- `docs/ARCHITECTURE.md` — the design record: what exists, what is broken, and why.
- `README.md` — how to configure and run the server.

## What this is

The self-hosted backend for the `TaskTrackerKMP` client. One Go binary, one
database, run by the customer on their own server. No microservices on purpose:
an operator needs `DATABASE_URL`, `JWT_SECRET`, and `./server`. Do not add a
service, message bus, or second deployable without a reason that survives that
constraint.

## The wire format is a contract with a client you cannot update

The client is an installed mobile app that syncs offline and cannot be updated
on this server's schedule. **The installed client is the fixed point**: when the
two disagree, the server changes.

**Two rules — the second is the one that gets missed:**

1. **A response may not gain a field.** The client leaves `ignoreUnknownKeys`
   off deliberately — an added key is a decoding failure, not a value it skips.
   A new response field breaks every installed build exactly as a removed one does.
2. **A response may not lose or rename a field the client declares without a
   default.** Kotlin treats a property with a default as optional; without one,
   required.

**Safe order for a new response field:** client first — ship a client that
declares it with a default (works against old and new server), then start
sending it. Anything else is a new `/v1` version and a migration path.

`docs/API.md` is the written contract; `cmd/server/contract_test.go` is the
executable one. It decodes responses into the client's DTOs **and** compares key
names, because Go matches JSON keys case-insensitively and a decode-based check
cannot see a case-only rename.

**The contract test can drift too.** A test that restates this server's own
struct is a mirror, not a check. When a response changes, pin the key set the
**client** declares, and add that key-name assertion to every path that decodes
one — a path that decodes without pinning is a path nobody is checking.

## The command that decides whether you are done

```bash
scripts/check.sh          # scopes: all (default), race, docs
```

All checks pass on `main`. Keep it that way: never make a check green by
deleting code, stubbing a proof, or weakening an assertion.

Run `scripts/check.sh race` before touching anything concurrent — the sync
engine, the rate limiter, and refresh-token rotation all are. CI runs it, so a
plain local run is not the same check.

## Toolchain

- **Go 1.27**, matching `go.mod`. Do not lower it.
- After changing imports: `go mod tidy`, then commit `go.mod` and `go.sum` together.
- Build prerequisites (CGO, Postgres/SQLite): `README.md` → Requirements.

## Comments

Brief and precise. A comment earns its place by explaining a non-obvious *why*;
the code already states the *what*. No restatement, no decorative banners, no
commented-out code.

## Configuration and secrets

- **A secret never enters this repository.** `.env` is git-ignored; only
  `.env.example` is tracked and holds no real values.
- Adding a variable means three places: `.env.example`, a read in
  `internal/config/config.go`, and a rule in `Config.validate` when a bad value
  should stop startup. See the `go-config-and-secrets` skill.

## Architecture boundaries

`internal/` is the module boundary — Go enforces that nothing outside can import
it, which is the whole point of keeping the server one module.

- `cmd/server/main.go` is the composition root. Concrete types are wired here
  and nowhere else; `mount` is the route table.
- `internal/entities` owns models and repositories. A repository is the only
  place that knows SQL; `internal/entities/timestamp.go` is the only place that
  knows the wire date format.
- `internal/auth`, `internal/middleware`, `internal/sync` own their concerns.
- A handler does not reach into the database directly — it calls a repository or
  the sync engine.

Place new code in the closest existing package. Prefer a new file over growing a
320-line one. See the `go-server-architecture` skill.

## Database changes

Schema is versioned SQL in `internal/db/migrations/`, one file per driver
sharing a version number (`0001_initial.postgres.sql` and
`0001_initial.sqlite.sql`), embedded and applied on startup by
`internal/db/migrate.go`. Rules:

- Add a new version for **both** drivers — `go test ./internal/db` fails if a
  version has only one dialect file.
- Statements stay `IF NOT EXISTS` so a pre-runner database is adopted on next start.
- Both drivers are tested: SQLite by default, Postgres when
  `TASKTRACKER_TEST_POSTGRES_URL` points at a throwaway server. A schema change
  is verified only once it passes on **both** — SQLite succeeding proves nothing
  about Postgres.
- Change the DDL and the repository in the same commit.

See the `go-migrations-and-repositories` skill before touching a table.

## Tests

- Ship a test with behaviour changes; put it next to the code it covers.
- `internal/config/config_test.go` is the pattern for env-reading tests: isolate
  with `t.Setenv` (see its `isolate` helper), never depend on the developer's `.env`.
- A test that has never failed is not yet evidence — confirm it fails when the
  behaviour it claims to protect is removed.

## Git workflow

One task, one branch, one MR to `main`. Never commit directly to `main`.

```bash
git switch -c feat/<kebab-case-slug>
# ... work ...
```

**The review is the merge gate.** When the task is done, read your own diff as a
stranger and answer three questions:

1. Is `scripts/check.sh` green?
2. Does the diff change only what the task called for — no drive-by cleanup, no
   bundled task?
3. Does every behaviour change ship with a test that fails without it?

All three yes → merge:
```bash
git switch main
git merge --ff-only feat/<kebab-case-slug>
git branch -d feat/<kebab-case-slug>
```

Any no → fix on the same branch and review again. A red or unreviewed branch is
never merged; a branch the review has approved is merged without asking again.
Where a remote is configured, push and merge the MR instead — push needs its own
authorisation.

## Specs & cross-harness continuity

A pending task's *how* lives in one spec, `specs/<plan-id>-<slug>.md` (copy
`specs/_template.md`). This file is also the **portable handoff unit** when you
switch agent providers: chat context does not transfer, but a committed spec
does. End every session by appending a `## Handoff` section (done / pending /
next step / gotchas) and committing it on the branch. On resume in a new harness,
read this file, the latest spec, and `git diff main...HEAD`. See `../AGENTS.md` §6.

## Skills

Skills live in `.opencode/skills/*/SKILL.md`. Read each skill whose description
matches the task before starting.

- `go-server-architecture` — where code belongs, composition root and handler
  wiring, what not to break in the monolith.
- `go-config-and-secrets` — adding configuration, reading it safely, keeping
  secrets out of the repository.
- `go-migrations-and-repositories` — schema changes, repository patterns,
  optimistic locking, Postgres/SQLite differences.
