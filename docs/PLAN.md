# TaskTrackerServer — Active roadmap

This is the short index of current and upcoming work. `docs/ARCHITECTURE.md`
records system design and rationale — including the durable decisions retained
from completed tasks — while `specs/` holds the implementation contract for work
in progress. A completed task leaves no spec.

## How to use this roadmap

- Choose a pending task below, respecting its dependencies and the milestone
  ordering.
- Read that task's spec in `specs/` if one exists. If it does not, create one
  from `specs/_template.md` and review it before implementation.
- Keep one task on one feature branch and in one merge request. Do not bundle
  neighboring work.
- Verify a task with focused tests for the packages it touches
  (`go test ./internal/<pkg>`); `scripts/check.sh` is the release gate, run when
  a release is cut, not per task. Use `race` for anything concurrent.
- Database changes require a migration for both SQLite and Postgres, with the
  DDL and repository updates in the same change.

## Status

| Milestone | State |
| --- | --- |
| 0 — Compiling baseline | Complete |
| 1 — Data-layer integrity | Complete |
| 2 — Versioned migrations | Complete |
| 3 — Auth and security | Complete |
| 4 — Sync engine | Complete |
| 5 — Observability and operations | Complete; T5.6 moved to T9.1 |
| 6 — Testing | Complete |
| 7 — Client contract | Complete |
| 8 — Admin management | Complete |
| 9 — Customer install and exposure | Active; tasks below are pending or deferred |
| 10 — Agent runs | Complete; T10.1–T10.5 done |

## Milestone 9 — Customer install and exposure

Deliver a customer-run install and a supported way for teams to reach their own
server. The project owns no infrastructure, hosted relay, domain, or customer
credentials. `lan` is the default provider and serves every client — desktop,
Android, and iOS — over plain HTTP on the customer's network; remote access
uses a provider the customer controls. The client permits plain HTTP only for
local addresses and refuses a public `http://` root at save time.

| Task | State | Intent | Depends on | Execution details |
| --- | --- | --- | --- | --- |
| T9.1 | Complete | Trusted-proxy-aware client address | T3.2 | — |
| T9.2 | Complete | Per-route attachment-transfer deadlines | None | — |
| T9.3 | Complete | Bind and publish the server safely | None | — |
| T9.4 | Complete | `tt` CLI skeleton, state file, and idempotence | T9.3 | — |
| T9.5 | Complete | Interactive `tt install` wizard | T9.4 | — |
| T9.6 | Complete | `tt doctor` and `tt status` | T9.5 | — |
| T9.7 | Complete | Runtime server URL and `tasktracker://connect` pairing | Client first; CLI depends on T9.5 | — |
| T9.8 | Complete | `tt expose --provider=lan` (default) | T9.3, T9.5 | — |
| T9.9 | Complete | `tt expose --provider=tailscale` | T9.1, T9.7, T9.8 | — |
| T9.10 | Complete | `tt expose --provider=cloudflare` | T9.1, T9.7, T9.8 | — |
| T9.11 | Deferred | VPS + Caddy deployment guide (documented, not implemented) | T9.1–T9.3, if picked up | — (no spec yet) |
| T9.12 | Complete | Guided Docker bootstrap in `tt install` | T9.4, T9.5 | — |
| T9.13 | Complete | Customer installation and onboarding docs | T9.5, T9.7–T9.12 | — |

Completed tasks leave no spec. The decisions worth keeping are in
[`ARCHITECTURE.md`](ARCHITECTURE.md#decisions-retained-from-completed-task-specs).

### Milestone ordering

The minimum LAN install path is T9.3 → T9.4 → T9.5 → T9.8. T9.7's client
changes land before the CLI generates connect links. T9.1 and T9.2 are needed
for remote-provider behavior; T9.9 precedes T9.10. T9.12 is additive to the
install wizard, and T9.13 is last so it documents working commands. T9.11
remains explicitly deferred.

## Milestone 10 — Agent runs

A work item in progress can be handed to an LLM agent. The agent runs as a
worker on the machine that holds the code (`tt agent run`), so this server never
calls a model, never stores a provider key, and never holds a working copy. It
owns the queue, the runner credential, and the append-only progress log the app
polls. Agent state stays outside the sync envelope: it is server-owned,
time-sensitive, and not mergeable, and a new key on an existing response would
break installed clients.

| Task | State | Intent | Depends on | Execution details |
| --- | --- | --- | --- | --- |
| T10.1 | Complete | Agent-runs schema and lifecycle (schema baseline, repository, lease) | None | [`t10.1-agent-runs.md`](../specs/t10.1-agent-runs.md) |
| T10.2 | Complete | Runner API: claim, events, heartbeat, finish, token middleware | T10.1 | [`t10.1-agent-runs.md`](../specs/t10.1-agent-runs.md) |
| T10.3 | Complete | App API: dispatch, read, cancel, runner registration | T10.2 | [`t10.1-agent-runs.md`](../specs/t10.1-agent-runs.md) |
| T10.4 | Complete | `tt agent connect`, `status`, `disconnect`; `echo` worker | T10.3 | [`t10.1-agent-runs.md`](../specs/t10.1-agent-runs.md) |
| T10.5 | Complete | `tt agent run`: providers, tool loop, cancel, lease heartbeats | T10.4 | [`t10.1-agent-runs.md`](../specs/t10.1-agent-runs.md) |

### Milestone ordering

T10.1 → T10.2 → T10.3 is the server path; the client's `11.1` starts once T10.3's
contract test is green, because the client never decodes an unpinned server
stub. T10.4 and T10.5 are the CLI worker and can proceed alongside the client
work. Provider keys and model choice stay in the runner's own config on the
developer's machine; nothing in this milestone adds a server secret.
