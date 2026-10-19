# Task specs

One file per unit of work. Read the selected task's spec, not the whole folder.

## Why this exists

`docs/PLAN.md` is the roadmap and finished log: a task spec avoids rereading
that history to understand one task. It is also the **portable handoff unit**:
task state survives a change of agent, tool, or session.

## The documents, and what each one is authoritative for

| Document | Holds | Authoritative for |
| --- | --- | --- |
| `docs/PLAN.md` | roadmap tasks and their status, in landing order | *what* the planned work is and its priority |
| `specs/<id>-<slug>.md` | one task's scope, contract, validation, and handoff | *how that task is done, checked, and resumed* |
| `docs/API.md`, `docs/ARCHITECTURE.md`, `README.md`, and other `docs/` guides | system contracts, design, setup, and operations | *the system*, not any one task |

A spec does **not** restate a roadmap entry — it links to it and adds the
checkable scope, acceptance criteria, validation, and `## Handoff`. Some
standalone or cross-repository work may have no server roadmap entry; record
that context in the spec rather than inventing a roadmap link.

## Lifecycle

1. For roadmap work, select the task from `docs/PLAN.md`. For approved
   standalone work, capture its context in the spec.
2. Copy `_template.md` to `<task-id>-<slug>.md` (or `<slug>.md` when there is
   no task ID) and fill it in. Set `status: pending`.
3. Before implementation, confirm the **Scope**, **Files**, **Contract**, and
   **Validation** sections against the workflow in `AGENTS.md`; set
   `status: in-progress`.
4. Implement and run the checks in **Validation**.
5. At the end of each session, update `## Handoff` with verified progress and
   the next step; commit it on the task branch.
6. When the work lands, set `status: done` and update the PLAN entry when there
   is one. Keep the spec as the record of how the task was verified.

## Reading budget

When choosing roadmap work, read `AGENTS.md` and `docs/PLAN.md`. When working on
task *X*, read `AGENTS.md`, the spec for *X*, and the source/docs that spec names.
Do not read unrelated specs; consult the PLAN only when choosing or updating a
roadmap task.
