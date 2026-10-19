# Agent documentation — organize harness-neutral guidance

- **status:** in-progress
- **priority:** P2
- **plan:** none — standalone documentation maintenance.

## Scope

Reorganize the server's agent entry point, portable Go skills, and task-spec
guidance. Do not change API, architecture, roadmap entries, or server behavior.
Correct only stale paths to the skills.

## Files

- `AGENTS.md` — define the entry flow and map authoritative project docs.
- `.agents/skills/` — use a portable home for the Go skills and remove stale
  claims.
- `specs/README.md` and `specs/_template.md` — clarify task lifecycle and
  provider-neutral handoff guidance.
- `specs/agent-docs-organization.md` — record this standalone task and its
  handoff.
- Related docs — update only references that point to old skill paths or name a
  specific agent provider.

## Contract

- [x] `AGENTS.md` presents an ordered orient / scope / implement / verify /
  handoff flow.
- [x] All three Go skills use `.agents/skills/` and valid matching skill names.
- [x] Task-spec guidance separates the roadmap, task execution details, and
  system documentation without requiring one agent provider.
- [x] Documentation checks pass, with no code or API changes.

## Validation

- Run: `scripts/check.sh docs`.
- Confirm no Markdown references remain to the old skill directory or specific
  agent tools.
- Review the complete diff for scope and stale links.

## Handoff

- **Done:** Reorganized agent workflow guidance, moved the Go skills to the
  portable skill directory, clarified task-spec lifecycle and handoffs, and
  updated the affected roadmap reference. `scripts/check.sh docs` passed.
- **Pending:** Review and merge this branch through the repository's normal
  process.
- **Next step:** Review the diff, then follow the branch and merge-request
  workflow.
- **Gotchas:** `.agents/skills/` is also discovered by the configured local
  adapter, so no harness-specific skill copy is needed. The existing tool
  adapter remains unchanged and still points to root `AGENTS.md`.

## Risks / notes

- Keep `AGENTS.md` as the single project entry point; task specs and skills
  supplement it rather than becoming competing instruction roots.
