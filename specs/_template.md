# <Task ID or slug> — <imperative title>

- **status:** pending | in-progress — delete this file to finish the task; there is no `done` state
- **priority:** P0 | P1 | P2 | P3
- **branch:** `feat/<kebab-case-slug>` — blank until cut; never commit to `main`
- **blocked by:** none | `<spec path or release it waits on>`
- **context:** 1–4 lines on where the work came from (a sync log, a device finding, a follow-up
  spec). For cross-repo work, name the client spec in `../TaskTrackerKMP/specs/` and state the
  landing order — the server lands first. `specs/` is the only index; this file is self-contained
  and holds the task's scope, contract, validation and handoff.

## Scope

The packages and layers this task may touch. If a file outside this list has to change, that is
the drift case in `AGENTS.md` — abort, re-plan, get a review. Say in one sentence what the change
must not reach (auth, the sync engine, migrations, another entity).

## Files

Confirm this list before implementation. Until then: `to confirm`.

- `path/to/file.go` — what changes and why.

## Diagnosis

Bug specs only; delete the heading for feature work. The root cause, not the symptom — what is
actually wrong, and why the obvious fix is the wrong one. This is what stops the next agent from
re-diagnosing.

## Contract

What must be true when the task is done. Each line is falsifiable — a test or a check-script
result proves it, not good intentions. Name the test that proves a line when one exists.

- [ ] <an observable property a test pins> (`TestName`)
- [ ] <the property still holds when the production change is reverted>

**Wire Constraint:** if this touches a wire shape, `docs/API.md` and `cmd/server/contract_test.go`
are in **Files**, no response gains, loses, or renames a key the client declares, and a new key
ships client-first with a default.

## Non-goal

Plain bullets — a non-goal is a boundary, not a checklist item. Name the boundary so the task does
not become catch-all cleanup.

- <one explicit thing out of scope, and where it is tracked instead>

## Validation

The commands for *this* task, and what a focused run cannot prove. Scopes and the release gate
live in `AGENTS.md` §Verification; do not restate them here.

- **Iteration:** `go test ./internal/<pkg>` for each touched package.
- **Before handoff:** <`-race` for concurrency; both SQLite and Postgres when a migration or
  repository changes; the release gate only when a release is cut>
- **Stays pending:** <environment behaviour a focused run cannot prove — a real Docker pull, a
  real systemctl restart>

## Handoff

Filled at the end of each session. Carries state across agents, tools, and sessions; a committed
spec does not depend on chat context.

- **Done:** <what is finished and verified>
- **Pending:** <what remains>
- **Next step:** <the single next concrete action>
- **Gotchas:** <what is easy to get wrong here, and the constraints or decisions the next agent
  must not re-litigate — written before coding, corrected after>

## Promote

What survives this spec when it is deleted. The spec is removed in the final task-branch commit,
so anything durable moves first.

- **To `docs/ARCHITECTURE.md`:** <the section under "Decisions retained from completed task
  specs" and the rule in one sentence, or "nothing — this is plumbing, not a rule">
