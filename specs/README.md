# Task specs

One file per unit of work in progress. Read the selected task's spec, not the whole folder.

## Why this exists

`docs/PLAN.md` is the concise index of active roadmap work. A task spec is the
**portable handoff unit**: it records one task's execution contract and state so
another agent, tool, or session can resume it without loading the whole roadmap.
Completed specs are deleted; only durable, non-obvious decisions are promoted
into [`docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md#decisions-retained-from-completed-task-specs).

## The documents, and what each one is authoritative for

| Document | Holds | Authoritative for |
| --- | --- | --- |
| `docs/PLAN.md` | active roadmap index, task state, and dependencies | *what* to work on next and in what order |
| `specs/<id>-<slug>.md` | one in-progress task's scope, contract, validation, and handoff | *how that task is done, checked, and resumed* |
| `docs/ARCHITECTURE.md` | system design, boundaries, and decisions retained from completed tasks | *why the system is the way it is* |
| `docs/API.md`, `README.md`, and other `docs/` guides | wire contract, setup, and operations | *the system*, not any one task |

A spec does **not** restate a roadmap entry — it links to it and adds the
checkable scope, acceptance criteria, validation, and `## Handoff`. Some
standalone or cross-repository work may have no server roadmap entry; record
that context in the spec rather than inventing a roadmap link. A completed task
has no spec: the diff and commit history are its verification record.

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
6. Keep `status: in-progress` while implementation, validation, or review
   remains. Once the contract and checks pass and the diff review is approved,
   update the active PLAN entry when there is one, promote any durable
   non-obvious decisions into `docs/ARCHITECTURE.md` (or the relevant guide),
   and **delete the spec** in the final task-branch commit before merge. Do not
   leave a `status: done` spec behind.

## Reading budget

When choosing roadmap work, read `AGENTS.md` and `docs/PLAN.md`. When working on
task *X*, read `AGENTS.md`, the spec for *X*, and the source/docs that spec names.
If no spec exists, create one before implementation. Do not read unrelated specs
or completed-task history.
