# <Task ID or slug> — <imperative title>

- **status:** pending | in-progress | done (`done` means validated, reviewed, and ready to merge)
- **priority:** P0 | P1 | P2 | P3
- **plan:** [`docs/PLAN.md`](../docs/PLAN.md) §<section>, entry `<id>`, or
  `none` for standalone work — the *what* and rationale live there when
  applicable; this file is the *how*.

## Scope

The packages and layers this task may touch. If a file outside this list has to
change, that is the drift case in `AGENTS.md` — abort, re-plan, get a review.

## Files

Confirm this list before implementation. Until then: `to confirm`.

- `path/to/file.go` — what changes and why.

## Contract

What must be true when the task is done. Each line is falsifiable — a test or a
check-script result proves it, not good intentions.

- [ ] <an observable property a test pins>
- [ ] <the property still holds when the production change is reverted>

## Validation

- Run `go test ./internal/<pkg>` (or `-run <Test> ./...`) for the packages this
  task touched — that is the development loop and it finishes in seconds.
  `scripts/check.sh` is the release gate and is not run per task; name any check
  it still needs and leave it pending.
- Run `go test -race ./internal/<pkg>` for concurrent code — sync engine, rate
  limiter, or refresh-token rotation.
- Never make a check green by deleting code, stubbing a proof, or weakening an
  assertion. Keep the checks green.
- Tests added go next to the code they cover and ship with the behaviour change.

## Handoff

Filled at the end of each session. Carries state across agents, tools, and
sessions; a committed spec does not depend on chat context.

- **Done:** <what is finished and verified>
- **Pending:** <what remains>
- **Next step:** <the single next concrete action>
- **Gotchas:** <constraints or decisions the next agent must not re-litigate>

## Risks / notes

- <what is easy to get wrong here>
