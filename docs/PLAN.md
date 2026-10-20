# TaskTrackerServer — Active roadmap

This is the short index of current and upcoming work. For completed-work
history, see [`PLAN-ARCHIVE.md`](PLAN-ARCHIVE.md). `docs/ARCHITECTURE.md`
records system design and rationale; task specs hold the implementation contract.

## How to use this roadmap

- Choose a pending task below, respecting its dependencies and the milestone
  ordering.
- Read that task's spec in `specs/` if one exists. If it does not, consult only
  the matching section in [`PLAN-ARCHIVE.md`](PLAN-ARCHIVE.md), then create and
  review a task spec before implementation.
- Keep one task on one feature branch and in one merge request. Do not bundle
  neighboring work.
- Run `scripts/check.sh` for completion, or the narrowest appropriate scope;
  use `scripts/check.sh race` for concurrent changes and
  `scripts/check.sh docs` for documentation-only work.
- Database changes require a migration for both SQLite and Postgres, with the
  DDL and repository updates in the same change.

## Status

| Milestone | State |
| --- | --- |
| 0 — Compiling baseline | Complete — details in the archive |
| 1 — Data-layer integrity | Complete — details in the archive |
| 2 — Versioned migrations | Complete — details in the archive |
| 3 — Auth and security | Complete — details in the archive |
| 4 — Sync engine | Complete — details in the archive |
| 5 — Observability and operations | Complete; T5.6 moved to T9.1 |
| 6 — Testing | Complete — details in the archive |
| 7 — Client contract | Complete — details in the archive |
| 8 — Admin management | Complete — details in the archive |
| 9 — Customer install and exposure | Active; tasks below are pending or deferred |

## Milestone 9 — Customer install and exposure

Deliver a customer-run install and a supported way for teams to reach their own
server. The project owns no infrastructure, hosted relay, domain, or customer
credentials. `lan` is the default provider; remote access uses a provider the
customer controls. The Android client's cleartext-HTTP restriction means phones
need a remote HTTPS provider rather than plain LAN access.

| Task | State | Intent | Depends on | Execution details |
| --- | --- | --- | --- | --- |
| T9.1 | Pending | Trusted-proxy-aware client address | T3.2 | [`PLAN-ARCHIVE.md`](PLAN-ARCHIVE.md), section T9.1 (promoted from T5.6) |
| T9.2 | Pending | Per-route attachment-transfer deadlines | None | [`PLAN-ARCHIVE.md`](PLAN-ARCHIVE.md), section T9.2 |
| T9.3 | Complete | Bind and publish the server safely | None | [`t9.3-server-bind.md`](../specs/t9.3-server-bind.md) |
| T9.4 | Complete | `tt` CLI skeleton, state file, and idempotence | T9.3 | [`t9.4-tt-cli-skeleton.md`](../specs/t9.4-tt-cli-skeleton.md) |
| T9.5 | Pending | Interactive `tt install` wizard | T9.4 | [`PLAN-ARCHIVE.md`](PLAN-ARCHIVE.md), section T9.5 |
| T9.6 | Pending | `tt doctor` and `tt status` | T9.5 | [`PLAN-ARCHIVE.md`](PLAN-ARCHIVE.md), section T9.6 |
| T9.7 | Pending | Runtime server URL and `tasktracker://connect` pairing | Client first; CLI depends on T9.5 | [`PLAN-ARCHIVE.md`](PLAN-ARCHIVE.md), section T9.7 |
| T9.8 | Pending | `tt expose --provider=lan` (default) | T9.3, T9.5 | [`PLAN-ARCHIVE.md`](PLAN-ARCHIVE.md), section T9.8 |
| T9.9 | Pending | `tt expose --provider=tailscale` | T9.1, T9.7, T9.8 | [`PLAN-ARCHIVE.md`](PLAN-ARCHIVE.md), section T9.9 |
| T9.10 | Pending | `tt expose --provider=cloudflare` | T9.1, T9.7, T9.8 | [`PLAN-ARCHIVE.md`](PLAN-ARCHIVE.md), section T9.10 |
| T9.11 | Deferred | VPS + Caddy deployment guide (documented, not implemented) | T9.1–T9.3, if picked up | [`PLAN-ARCHIVE.md`](PLAN-ARCHIVE.md), section T9.11 |
| T9.12 | Pending | Guided Docker bootstrap in `tt install` | T9.4, T9.5 | [`PLAN-ARCHIVE.md`](PLAN-ARCHIVE.md), section T9.12 |
| T9.13 | Pending | Customer installation and onboarding docs | T9.5, T9.7–T9.12 | [`PLAN-ARCHIVE.md`](PLAN-ARCHIVE.md), section T9.13 |

### Milestone ordering

The minimum LAN install path is T9.3 → T9.4 → T9.5 → T9.8. T9.7's client
changes land before the CLI generates connect links. T9.1 and T9.2 are needed
for remote-provider behavior; T9.9 precedes T9.10. T9.12 is additive to the
install wizard, and T9.13 is last so it documents working commands. T9.11
remains explicitly deferred.
