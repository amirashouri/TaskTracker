# AGENTS.md

Working agreement for `TaskTrackerServer`. Read this before changing anything.

> **Cross-project rules:** if your task touches both repos, read `../AGENTS.md`
> first — it governs wire format contracts, the dual-repo ACK protocol, and
> build isolation boundaries. This file is the contract for Server-only work.

## Agent workflow

Follow this sequence for each task. Keep one task on one feature branch and in
one merge request.

1. **Orient.** Read this file first. Use `docs/PLAN.md` as the concise task
   index. Read the selected task's spec in `specs/`; if none exists, consult
   only that task's section in `docs/PLAN-ARCHIVE.md` and create a spec before
   implementation. For cross-repository work, read the other repository's
   `AGENTS.md` before planning.
2. **Plan the scope.** Identify the files, observable contract, and validation
   before editing. A roadmap task's spec records these. If the change needs
   files or behavior outside that scope, stop and re-plan before expanding it.
3. **Implement.** Read any applicable skill in `.agents/skills/`. Follow the
   architecture and compatibility rules below; add tests with behavior changes.
4. **Verify and review.** Run the focused tests for the packages you touched —
   `go test ./internal/<pkg>` and `go test -run <Test> ./...` finish in seconds,
   and that is the development loop. Then review the diff for scope,
   correctness, and tests. `scripts/check.sh` is the **release gate**, not a
   per-task check: run it when a release is cut, with `race` for anything
   concurrent.
5. **Update status and hand off.** Keep the spec `in-progress` while work or
   validation remains. Once its contract and checks pass and the diff review is
   approved, set it to `done` as the final task-branch update before merging.
   Update `## Handoff` with verified progress, remaining work, the next step,
   and gotchas; if nothing remains, say so. For standalone work, create a spec
   rather than an untracked handoff note. Commit the status and handoff on the
   task branch. Do not push without authorization.

## Project documentation

- `docs/PLAN.md` — concise active roadmap and task index.
- `docs/PLAN-ARCHIVE.md` — completed-work history and detailed planning notes
  for tasks not yet captured in specs; read only the selected task section.
- `specs/<task>.md` — one task's scope, acceptance contract, validation, and
  handoff; authoritative for how that task is done and resumed.
- `docs/API.md` — HTTP wire contract. Read it before changing a request or
  response shape.
- `docs/ARCHITECTURE.md` — architecture decisions, boundaries, and known gaps.
- `README.md` — build, configuration, and run instructions.
- `docs/SELF-HOSTING.md` and `docs/USER-MANAGEMENT.md` — operator and user
  management guidance.

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

## Verification: a fast development loop, and a release gate

Development does **not** run `scripts/check.sh`. The script builds and tests
every package; requiring that sweep per task is what makes small changes
unfinishable. The loop for a task is the packages it touched:

```bash
go test ./internal/<pkg>               # seconds
go test -run TestName ./internal/...   # one test across the tree
```

- A red focused run goes back to the code. Never make it green by deleting a
test, stubbing a proof, or weakening an assertion.
- Concurrency is the exception that cannot wait for a release: run
  `go test -race ./internal/<pkg>` on the package you made concurrent.
- Cross-database work (SQLite and Postgres) needs both runs; a SQLite-only pass
  is not the same claim.

**`scripts/check.sh` is the release gate** — `all` (gofmt, build, vet, tests) or
`race` for the full race sweep — run when a release is cut. It is not a per-push
check and not what a task is judged done by. All scopes pass on `main`; keep it
that way at release time.

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

1. Are the focused tests for every touched package green?
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

## Specs & continuity

A task's *how* lives in one spec under `specs/` (copy `specs/_template.md`);
roadmap tasks use their plan ID in the filename, while standalone tasks may use
a descriptive slug. The spec is the **portable handoff unit**: it preserves
task state when the agent, tool, or session changes. End each session by
updating its `## Handoff` section (done / pending / next step / gotchas) and
committing it on the branch. When resuming, read the task's spec and
`git diff main...HEAD`; do not load unrelated specs. Cross-repository handoffs
also follow `../AGENTS.md` §6.

## Skills

Portable, on-demand skills live in `.agents/skills/<name>/SKILL.md`. Read a
skill when its description matches the task; do not load unrelated skills.

- `go-server-architecture` — where code belongs, composition root and handler
  wiring, what not to break in the monolith.
- `go-config-and-secrets` — adding configuration, reading it safely, keeping
  secrets out of the repository.
- `go-migrations-and-repositories` — schema changes, repository patterns,
  optimistic locking, Postgres/SQLite differences.
