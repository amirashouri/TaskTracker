# specs/

One file per unit of work, read one at a time.

## Why this exists

`docs/PLAN.md` is the roadmap and finished log: an agent picking up one task
paid for the entire history to find its own entry. A per-task spec removes that
cost and is also the **portable handoff unit** between agent providers — chat
context dies on a provider switch, a committed spec does not.

## The documents, and what each one is authoritative for

| Document | Holds | Authoritative for |
| --- | --- | --- |
| `docs/PLAN.md` | every task, pending and done, in landing order | *what* the work is, and the priority order |
| `specs/<id>-<slug>.md` | one pending task: scope, contract, validation, handoff | *how one task is done, checked, and resumed* |
| `README.md` | architecture, run config, the wire contract | *the system*, not any one task |

A spec does **not** restate the PLAN entry — it links to it and adds the part a
machine can check (scope, acceptance, validation) plus a `## Handoff` section
for cross-provider resume. Nothing is written twice, so nothing drifts.

## Lifecycle

1. The task is picked from `docs/PLAN.md`.
2. Copy `_template.md` to `<plan-id>-<slug>.md` and fill it in. `status: pending`.
3. Plan the change (Phase 1 in `AGENTS.md`) — that becomes the spec's **Files**
   section. `status: in-progress`.
4. Do the work. The **Validation** section says what "done" runs.
5. End each session with a `## Handoff` section; commit it on the branch.
6. On merge, `status: done` and the PLAN entry is ticked. The spec stays as the
   record of how that task was verified.

## Reading budget

An agent working task *X* reads: `AGENTS.md` (always), the spec for *X*, and the
files the spec names. It does not read `PLAN.md` or any other spec.
