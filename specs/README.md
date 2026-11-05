# Task specs

One file per unit of work in progress. Read the selected task's spec, not the
whole folder. **`specs/` is the only index of work** — there is no roadmap
document.

## Why this exists

A task spec is the **portable handoff unit**: it records one task's status,
execution contract and state so another agent, tool, or session can resume it.
Each spec carries its own `status:`; the set of specs in this directory *is* the
backlog. Completed specs are deleted; only durable, non-obvious decisions are
promoted into
[`docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md#decisions-retained-from-completed-task-specs).

## The documents, and what each one is authoritative for

| Document | Holds | Authoritative for |
| --- | --- | --- |
| `specs/<id>-<slug>.md` | one in-progress task's status, scope, contract, validation, and handoff | *what* is being worked on, and *how that task is done, checked, and resumed* |
| `docs/ARCHITECTURE.md` | system design, boundaries, and decisions retained from completed tasks | *why the system is the way it is* |
| `docs/API.md`, `README.md`, and other `docs/` guides | wire contract, setup, and operations | *the system*, not any one task |

A spec is self-contained: it states the *what* and the *how*, including the
checkable scope, acceptance criteria, validation, and `## Handoff`. A completed
task has no spec: the diff and commit history are its verification record.

## Lifecycle

1. Choose work by reading the `status:` lines of the specs in `specs/`. If
   nothing is active, create a spec before implementation and record the
   context the work needs in the spec itself.
2. Copy `_template.md` to `<task-id>-<slug>.md` (or `<slug>.md` when there is no
   task ID) and fill it in. Set `status: pending`.
3. Before implementation, confirm the **Scope**, **Files**, **Contract**, and
   **Validation** sections against the workflow in `AGENTS.md`; set
   `status: in-progress`.
4. Implement and run the checks in **Validation**.
5. At the end of each session, update `## Handoff` with verified progress and
   the next step; commit it on the task branch.
6. Keep `status: in-progress` while implementation, validation, or review
   remains. Once the contract and checks pass and the diff review is approved,
   promote any durable non-obvious decisions into `docs/ARCHITECTURE.md` (or the
   relevant guide) and **delete the spec** in the final task-branch commit before
   merge. Do not leave a `status: done` spec behind.

## Reading budget

When choosing work, read `AGENTS.md` and the `status:` lines in `specs/`. When
working on task *X*, read `AGENTS.md`, the spec for *X*, and the source/docs that
spec names. If no spec exists, create one before implementation. Do not read
unrelated specs or completed-task history.
