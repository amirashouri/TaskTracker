# agent-wire-contracts — Finish the deferred agent wire contracts

- **status:** in-progress
- **priority:** P1
- **context:** Completes the agent-execution / agent-management slice by landing the
  wire contracts that were deliberately deferred: the runner host kind (desktop vs
  VPS), per-dispatch runner targeting, the run result diff, and the pairing-code CLI
  bootstrap. Implementation is complete; the remaining work is a green release
  gate before this spec can be retired.

## Current state (re-verified 2026-11-08)

This spec was written against a working tree that no longer exists. The `0004`
migration is committed on `main` for both dialects, the Go wiring for `host`,
`requested_runner_id` and `result_diff` is in place, dispatch already persists the
client's chosen `runnerId`, and `internal/runner/client.go` already sends `diff` on
finish. On the KMP side the run DTO already declares `diff` and
`requestedRunnerId` with defaults and maps them to `AgentRun`; both the runner and
pairing DTOs already declare `host`. The "untracked migration", "client diff gap"
and "silently dropped picker choice" were therefore already resolved before this
session — and the KMP UI already renders the host label from a nullable `host`.

The cross-repo ACK ratified the **shipped** names instead of the names this spec
originally proposed: `host`, dispatch request `runnerId` → run response
`requestedRunnerId`, and `diff`. `requestedRunnerID` and `resultDiff` were never on
the wire and are not being adopted. **No KMP source file was edited.**

## Scope

The server owns the runner host field, per-dispatch targeting, the result diff, the
`0004` migration, its contract tests, and `tt agent connect --pairing-code`. The KMP
half is tracked in its own spec (`TaskTrackerKMP/specs/agent-management.md`) and
needed no DTO work — only a reconciled handoff.

## Files (verified or touched on `main`)

- `internal/db/migrations/0004_agent_targeting.{sqlite,postgres}.sql` — committed
  and ordered after `0003`; `internal/db/migrate_test.go` now asserts the four
  columns for both dialects.
- `internal/entities/agent_repository.go`, `internal/entities/agent_pairing.go` —
  verified: `host`, `requested_runner_id` and `result_diff` round-trip, and the
  claim query honours the requested runner.
- `internal/agent/app_handler.go` — verified: `runnerId` → `requestedRunnerId`,
  `host` and `diff` emitted; its comment now names the defaulted client DTO.
- `cmd/server/contract_test.go` — closed key-set pins built from
  `map[string]json.RawMessage`, plus an end-to-end test walking pairing code →
  `/v1/agent/pair` → roster → targeted dispatch → claim → finish → run detail/list.
  A deliberate mutation to `requestedRunnerID` was confirmed to fail the pin and
  then reverted.
- `docs/API.md`, `docs/ARCHITECTURE.md` — the written wire contract, its rollout
  order, and the durable decisions.
- `cmd/tt/agent_connect.go`, `cmd/tt/agent.go`, `cmd/tt/main.go`,
  `internal/runner/client.go`, `cmd/tt/agent_test.go`,
  `internal/runner/client_test.go`, `docs/INSTALL.md` — `--pairing-code`, its
  `runner.Pair` helper, usage text, install docs and tests.
- `TaskTrackerKMP/specs/agent-management.md` — matching handoff; no code edits.

## Contract

- [x] A runner's host kind (`desktop` or `remote`) is stored on the runner and on
  the pairing code, returned to the client, and rendered as the host label in
  `ProjectAgentSection`. Empty/`null` means "not stated", not a third kind.
- [x] A dispatch names a specific machine via request `runnerId`; the server
  persists it as `requested_runner_id`, the claim query honours it, and any machine
  may still claim when it is `NULL`. The run reports it as `requestedRunnerId`.
- [x] A succeeded run returns its git diff as `diff` on the run response; the
  client maps it to `AgentRun.diff` and the diff sheet shows it. The key is additive,
  `omitempty` on the server and defaulted on the client DTO.
- [x] The `0004` migration is committed and ordered after `0003` for both SQLite
  and PostgreSQL; `migrate_test.go` asserts all four columns on both dialects.
- [x] `cmd/server/contract_test.go` pins the added response keys by exact name, so a
  renamed, gained or lost key is a hard failure. Verified by a case-only mutation.
- [x] `tt agent connect --pairing-code <code>` redeems a client-issued pairing code
  without asking for email or password, refuses an empty code and refuses
  `--pairing-code` together with `--email`.
- [x] Field names, casing and the compatibility rollout were ACKed across both
  repos. The client had already shipped `requestedRunnerId` and `diff` with
  defaults, so the server may emit them; `ignoreUnknownKeys` stays off and no new
  required key reaches an installed client.
- [x] Focused server tests pin the new behavior and fail if it is removed; the KMP
  agent scenarios and wire-DTO tests pass on the JVM.

## Subtasks and dependencies

All seven subtasks from the original plan are complete. The migration, host
round-trip, targeting/diff round-trip, key pinning, API docs and CLI flag are
committed on `main`; the "client diff gap" needed no edit because the DTO already
declared `diff`. What remains is the release gate, not implementation.

## Non-goal

- [x] No change to the agent connectivity model (outbound/poll is unchanged).
- [x] No graph database, message bus or second deployable.
- [x] The diff is not a required response field for installed clients that predate
  the defaulted DTO.

## Validation

- Server: focused `go test` on `./internal/entities/... ./internal/agent/...
  ./internal/db/... ./cmd/server/... ./cmd/tt/... ./internal/runner/...`;
  `go build ./...`, `go vet ./...`, `go test ./...`, focused `go test -race`, and
  `scripts/check.sh race`.
- Client: `scripts/check.sh quick` on the four `AgentManagementScenarios` entry
  points, `HttpAgentRunsRemoteTest`, `HttpAgentPairingRemoteTest` and
  `HttpAgentRunnersRemoteTest`; `scripts/check.sh full` as the release gate.
- PostgreSQL integration when `TASKTRACKER_TEST_POSTGRES_URL` is available.

**Results (2026-11-08):** all focused server tests, `go build`, `go vet`,
`go test ./...` and `scripts/check.sh race` pass. All focused KMP agent tests pass
on the JVM. `scripts/check.sh full` is **red** (see Handoff), and the server's
default `scripts/check.sh` is red on pre-existing `gofmt` drift. `test-baseline.json`
was **not** regenerated — a baseline generated from a red run would hide failures.

## Handoff

- **Done:** the shipped wire names were ratified by cross-repo ACK; `0004` is
  committed and asserted; `host`, `runnerId` → `requestedRunnerId` and `diff` are
  pinned by closed key sets in `cmd/server/contract_test.go` and documented in
  `docs/API.md`; `tt agent connect --pairing-code` redeems a one-use code via
  `runner.Pair` and writes the same `runner.Config` as the legacy flow. The durable
  decisions are promoted into `docs/ARCHITECTURE.md` and
  `TaskTrackerKMP/docs/DECISIONS.md`. No KMP DTO was edited.
- **Pending:** `scripts/check.sh full` reports six failures per host (18 total) on
  **pristine** KMP `main` — `SprintSummaryTest`, three `WorkItemGroupingTest`s, and
  two `TaskTrackerWide*Test`s. Only a markdown file was modified when the gate ran,
  and `WorkItemGroupingTest` was reproduced red by a focused run. These are not
  this slice; `test-known-failures.json` is empty and `test-baseline.json` is stale.
  The server's default `scripts/check.sh` fails `gofmt` on
  `internal/admin/handler_test.go` and `internal/install/plan.go`, both already
  unformatted at `origin/main`. PostgreSQL tests remain unrun: no
  `TASKTRACKER_TEST_POSTGRES_URL`.
- **Next step:** get a green KMP full gate (and decide whether to fix or explicitly
  waive the two server `gofmt` files) under their own work, regenerate the baseline
  only from a green run, then delete this spec and
  `TaskTrackerKMP/specs/agent-management.md` in their final commits. No further
  wire-contract work is outstanding.
- **Gotchas:** the spec's original names `requestedRunnerID` / `resultDiff` are
  wrong; a case-only rename breaks installed Kotlin builds because
  `ignoreUnknownKeys` is off, and a decode-based Go test cannot see it. The pairing
  CLI path is a bootstrap for a one-use credential, not a bypass — the runner still
  authenticates with its `ttag_…` token. Keep `host` to two kinds plus "not
  stated"; do not invent a third kind for legacy runners.

## Risks / notes

- Deployment order still matters: the client shipped the defaulted DTOs first, the
  server emitted second. `omitempty` means an untargeted run without a diff gains no
  new key at all.
- A runner connected via `--pairing-code` still authenticates and claims like any
  other runner; the flag is a bootstrap path, not a bypass of pairing security.
- The red release gates are pre-existing on `main` and must not be made green by
  editing `test-baseline.json`, `test-known-failures.json`, or by deleting
  assertions.
