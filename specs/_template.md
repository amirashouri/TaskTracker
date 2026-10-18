# <PLAN-id> — <imperative title>

- **status:** pending | in-progress | done
- **priority:** P0 | P1 | P2 | P3
- **plan:** [`docs/PLAN.md`](../docs/PLAN.md) §<section>, entry `<id>` — the *what* and the
  rationale live there; this file is the *how*.

## Scope

The packages and layers this task may touch. If a file outside this list has to
change, that is the drift case in `AGENTS.md` — abort, re-plan, get a review.

## Files

Filled in at Phase 1 (the plan). Until then: `to confirm at Phase 1`.

- `path/to/file.go` — what changes and why.

## Contract

What must be true when the task is done. Each line is falsifiable — a test or a
check-script result proves it, not good intentions.

- [ ] <an observable property a test pins>
- [ ] <the property still holds when the production change is reverted>

## Validation

- Run: `scripts/check.sh` (scopes: `all`, `race`, `docs`). For any concurrent
  code — sync engine, rate limiter, refresh-token rotation — run
  `scripts/check.sh race`.
- Never make a check green by deleting code, stubbing a proof, or weakening an
  assertion. All four checks pass on `main`; keep it that way.
- Tests added go next to the code they cover and ship with the behaviour change.

## Handoff

Filled at end of each session. Carries state across agent providers (OpenCode →
FreeBuf → WorkBuddy) — chat context dies on a switch, a committed spec does not.

- **Done:** <what is finished and verified>
- **Pending:** <what remains>
- **Next step:** <the single next concrete action>
- **Gotchas:** <constraints or decisions the next agent must not re-litigate>

## Risks / notes

- <what is easy to get wrong here>
