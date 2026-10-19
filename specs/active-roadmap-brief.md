# Active roadmap — separate current work from archived detail

- **status:** done
- **priority:** P2
- **plan:** none — standalone documentation maintenance.

## Scope

Make the active roadmap cheap to read while retaining completed work history
and detailed plans for pending tasks that do not yet have specs. No behavior,
API, or roadmap decisions change.

## Files

- `docs/PLAN.md` — concise active milestone/task index with current statuses,
  dependencies, and spec/detail links.
- `docs/PLAN-ARCHIVE.md` — preserve the previous roadmap detail and explain its
  limited use.
- `AGENTS.md` and `specs/README.md` — guide agents to read only the active
  index, selected spec, or selected archived task section.
- `README.md`, `docs/ARCHITECTURE.md`, `docs/SELF-HOSTING.md`, and relevant
  specs — update directly affected roadmap references.

## Contract

- [x] The active roadmap is substantially shorter than the previous 1,553-line
  file and lists all current Milestone 9 tasks, statuses, dependencies, and
  detail locations.
- [x] Previous detailed task notes are preserved in the archive.
- [x] Guidance says to read only the selected archive section when no task spec
  exists, then create a spec before implementation.
- [x] Existing task specs and repository docs link to the appropriate active or
  archived roadmap.

## Validation

- Run `scripts/check.sh docs`.
- Run `git diff --check`.
- Check changed Markdown links and search for stale claims that the active
  roadmap contains completed history or detailed plans.

## Handoff

- **Done:** Split the roadmap index from the preserved detailed archive, kept
  all Milestone 9 task information available, added task states/dependencies,
  and updated agent, README, architecture, self-hosting, and spec guidance.
- **Pending:** Nothing.
- **Next step:** Merge the reviewed branch to `main`.
- **Gotchas:** Pending Milestone 9 details for tasks without specs remain in
  `docs/PLAN-ARCHIVE.md`; consult only the selected task's section. Existing
  specs remain authoritative for T9.3 and T9.4.

## Risks / notes

- Keep `docs/PLAN.md` as the small active index. Do not move pending task
  details out of the archive until a corresponding task spec captures them.
