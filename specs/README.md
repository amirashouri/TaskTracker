# Task specs

One file per unit of work. Read the selected task's spec, not the whole folder.

## Why this exists

`docs/PLAN.md` is the concise index of active roadmap work.
`docs/PLAN-ARCHIVE.md` preserves completed-work history and detailed notes for
pending tasks that do not yet have specs. A task spec is the **portable handoff
unit**: it records one task's execution contract and state so another agent,
tool, or session can resume it without loading the whole roadmap.

## The documents, and what each one is authoritative for

| Document | Holds | Authoritative for |
| --- | --- | --- |
| `docs/PLAN.md` | active roadmap index, task state, and dependencies | *what* to work on next and in what order |
| `docs/PLAN-ARCHIVE.md` | completed history and details for un-spec'd tasks | historical context and task detail until moved to a spec |
| `specs/<id>-<slug>.md` | one task's scope, contract, validation, and handoff | *how that task is done, checked, and resumed* |
| `docs/API.md`, `docs/ARCHITECTURE.md`, `README.md`, and other `docs/` guides | system contracts, design, setup, and operations | *the system*, not any one task |

A spec does **not** restate a roadmap entry — it links to it and adds the
checkable scope, acceptance criteria, validation, and `## Handoff`. When a task
has no spec yet, consult only its section in the archive and then create one.
Some standalone or cross-repository work may have no server roadmap entry;
record that context in the spec rather than inventing a roadmap link.

## Lifecycle

1. For roadmap work, select the task from `docs/PLAN.md`. If no spec exists,
   read only its detailed section in `docs/PLAN-ARCHIVE.md`. For approved
   standalone work, capture its context in the spec.
2. Copy `_template.md` to `<task-id>-<slug>.md` (or `<slug>.md` when there is
   no task ID) and fill it in. Set `status: pending`.
3. Before implementation, confirm the **Scope**, **Files**, **Contract**, and
   **Validation** sections against the workflow in `AGENTS.md`; set
   `status: in-progress`.
4. Implement and run the checks in **Validation**.
5. At the end of each session, update `## Handoff` with verified progress and
   the next step; commit it on the task branch.
6. Keep `status: in-progress` while implementation, validation, or review
   remains. After the contract and checks pass and the diff review is approved,
   set `status: done` in the final task-branch commit before merge. Update the
   active PLAN entry when there is one. Keep the spec as the record of how the
   task was verified.

## Reading budget

When choosing roadmap work, read `AGENTS.md` and `docs/PLAN.md`. When working on
task *X*, read `AGENTS.md`, the spec for *X*, and the source/docs that spec names.
If no spec exists, read only task *X*'s section in `docs/PLAN-ARCHIVE.md` and
create a spec before implementation. Do not read unrelated specs or archive
sections.
