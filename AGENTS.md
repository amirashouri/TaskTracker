# AGENTS.md

Working agreement for `TaskTrackerServer`. Read this before changing anything.

- `docs/ARCHITECTURE.md` — the design record and review: what exists, what is
  broken, and the order to fix it in.
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

`gofmt -l .` printing nothing is the only part that is green today. `go build`
and `go test` are **red for known reasons** — the empty `internal/auth`,
`internal/middleware` and `internal/sync` packages, plus the compile errors
listed in `docs/ARCHITECTURE.md`. Do not report that as a regression you caused,
and do not make it green by deleting code or stubbing a check.

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
git push -u origin feat/<kebab-case-slug>
```

CI (`.gitlab-ci.yml`) runs `lint`, `build` and `test`; review the MR diff before
merging.

## Skills

Repository skills live in `.opencode/skills/*/SKILL.md`. Read each skill whose
description matches the task before starting.

- `go-server-architecture` — where code belongs, how the composition root and
  handlers are wired, and what not to break in the monolith.
- `go-config-and-secrets` — adding configuration, reading it safely, and keeping
  secrets out of the repository.
- `go-migrations-and-repositories` — schema changes, repository patterns,
  optimistic locking, and the Postgres/SQLite differences.
