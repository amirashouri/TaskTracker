# AGENTS.md

Working agreement for `TaskTrackerServer`. Read this before changing anything.

- `docs/PLAN.md` — the ordered, task-by-task implementation roadmap. Work from
  this; one task, one branch, one MR.
- `docs/ARCHITECTURE.md` — the design record and review: what exists, what is
  broken, and why.
- `README.md` — how to configure and run the server.

## What this is

The self-hosted backend for the `TaskTrackerKMP` client. One Go binary and one
database, run by the customer on their own server. There are no microservices
here on purpose: an operator should need a `DATABASE_URL`, a `JWT_SECRET`, and
`./server`. Do not add a service, a message bus, or a second deployable without
a reason that survives that constraint.

## The command that decides whether you are done

```bash
gofmt -l . && go build ./... && go vet ./... && go test ./...
```

All four pass on `main`. Keep it that way: never make a check green by deleting
code, stubbing a proof, or weakening an assertion.

The server compiles and boots but cannot yet read or write data — Milestone 1 of
`docs/PLAN.md` is the blocking work. A runtime failure that Milestone 1 lists is
known, not a regression you caused.

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
  and wired here and nowhere else.
- `internal/entities` owns models and repositories. A repository is the only
  place that knows SQL.
- `internal/auth`, `internal/middleware`, `internal/sync` own their concerns;
  they are empty today.
- A handler does not reach into the database directly. It calls a repository or
  the sync engine.

Place new code in the closest existing package. Prefer a new file over growing a
320-line one. See the `go-server-architecture` skill.

## Database changes

The DDL lives inline in `internal/db/db.go` and is not versioned yet
(`docs/ARCHITECTURE.md`, D3). Schema changes therefore need extra care, and the
driver differences between Postgres and SQLite are real — `isSQLite` currently
misdetects Postgres, so a change verified on SQLite is not verified on Postgres.
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

1. Is the validation command green?
   `gofmt -l . && go build ./... && go vet ./... && go test ./...`
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
