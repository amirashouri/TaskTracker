# agent-wire-contracts — Finish the deferred agent wire contracts

- **status:** in-progress
- **priority:** P1
- **context:** Completes the agent-execution / agent-management slice by landing the
  wire contracts that were deliberately deferred: the runner host kind (desktop vs
  VPS), per-dispatch runner targeting, the run result diff, and the pairing-code CLI
  bootstrap. The client UI for all four already exists and is structurally tested
  (`AgentManagementScenarios`), but it has no server backing yet, and one client DTO
  gap means the diff never reaches the screen.

## Current state (verified before writing this spec)

This is **not** a from-scratch task. Working-tree code already implements most of
the server side; what is missing is completion, commitment, contract pinning, and a
cross-repo ACK before any new key is emitted to installed clients.

Server (uncommitted `M` files on `feat/agent-execution`, not yet a clean commit):

- `internal/entities/agent_repository.go` — `agentRunColumns` already lists
  `requested_runner_id` and `result_diff`; the run scan maps `requestedRunnerID`,
  the claim query filters `AND (requested_runner_id IS NULL OR requested_runner_id =
  $1)`, and run completion writes `result_diff`. `agentRunnerColumns` already lists
  `host`.
- `internal/entities/agent_pairing.go` — `AgentPairingCode.Host` exists and
  `CreatePairingCode(..., host string, ...)` already accepts it.
- `internal/agent/app_handler.go` — dispatch derives `requestedRunnerID` from the
  chosen runner; pairing-code create/response encode `host`.
- `internal/db/migrations/0004_agent_targeting.{sqlite,postgres}.sql` — add
  `agent_runners.host`, `agent_pairing_codes.host`, `agent_runs.requested_runner_id`,
  `agent_runs.result_diff`. **Untracked (`??` in git):** not committed, so not part
  of a release. The migration runner discovers files from disk, so they apply
  locally, but they must be committed and ordered after `0003`.

Client KMP (`TaskTrackerKMP`, see `specs/agent-management.md`):

- `HttpAgentPairingRemote` already sends `host` on pairing-code creation and reads it
  back; `HttpAgentRunsRemote` already reads `runnerId` and `requestedRunnerId` into
  `AgentRun`. **Gap:** `HttpAgentRunsRemote`'s run DTO has no `diff`/`resultDiff`
  field, so `AgentRun.diff` is never populated — the diff sheet (structurally
  tested with a faked `AgentRun`) shows nothing real from the server.
- `HttpAgentRunsRemote.dispatch` sends `runnerId`; must be confirmed to align with
  the server's `requested_runner_id` semantics (the server currently derives
  `requestedRunnerID` from the claiming runner, not from the request).

Tooling:

- `cmd/tt/agent_connect.go` has **no `--pairing-code` flag** — it is an email-based
  connect. The pairing-code redemption path the client issues (`ttpc_…` codes) is
  not implemented as a CLI bootstrap.

Contract test:

- `cmd/server/contract_test.go` does **not** pin `host`, `requestedRunnerID`/
  `runnerId`, or `resultDiff`. The existing contract test pins response key sets by
  name, so the new keys must be added there to lock the wire contract.

## Scope

Server owns the runner host field, per-dispatch targeting, the result diff, the
`0004` migration and its contract tests, and the `tt agent connect --pairing-code`
flag. The KMP half owns the run-DTO `diff` mapping and the dispatch-field alignment,
and is tracked in its own spec (`TaskTrackerKMP/specs/agent-management.md`). This
branch edits the client only to the extent required to carry the four contracts.

## Files

Confirmed or to be touched:

- `internal/db/migrations/0004_agent_targeting.{sqlite,postgres}.sql` — commit;
  verify version order after `0003` and that both drivers are aligned.
- `internal/entities/agent_repository.go` — verify `requested_runner_id` and
  `result_diff` round-trip (write on callback, read on list/detail); verify
  `agent_runners.host` / `agent_pairing_codes.host` are persisted and read.
- `internal/entities/agent_pairing.go` — verify `CreatePairingCode` persists `host`.
- `internal/agent/app_handler.go` — align dispatch request `runnerId` with
  `requested_runner_id`; confirm `host` and `resultDiff` are emitted on the run
  response.
- `cmd/server/contract_test.go` — pin the new response keys (`host`,
  `requestedRunnerID`/`runnerId`, `resultDiff`) and the compatibility gate.
- `cmd/tt/agent_connect.go` — add `--pairing-code` to redeem a pairing code.
- `TaskTrackerKMP/.../HttpAgentRunsRemote.kt` — add a defaulted `diff` field to the
  run DTO and map it to `AgentRun.diff`; confirm dispatch `runnerId` maps to the
  server's `requested_runner_id`.

## Contract

- [ ] A runner's host kind (desktop or remote/VPS) is stored on the runner and on
  the pairing code, returned to the client, and rendered as the existing host label
  in `ProjectAgentSection`. Empty/`null` means "not stated", not a third kind.
- [ ] A dispatch can name a specific machine via `requested_runner_id`; the claim
  query already honors it, and any machine may still claim when it is `NULL`. The
  client's target picker sends the chosen runner and the server persists it as the
  requested runner.
- [ ] A succeeded run returns its git diff (`result_diff`) on the run response; the
  client maps it to `AgentRun.diff` and the diff sheet shows it. The field is
  additive and declared with a default on the client.
- [ ] The `0004` migration is committed and ordered after `0003` for both SQLite and
  PostgreSQL; migration and repository behavior stay aligned.
- [ ] `cmd/server/contract_test.go` pins the added response keys so a renamed,
  gained, or lost key is a hard failure (matching the existing key-set pins).
- [ ] `tt agent connect --pairing-code <code>` redeems a client-issued pairing code
  and pairs the local runner, as an alternative to the email flow.
- [ ] Field names/casing and compatibility rollout are ACKed with the KMP spec
  before emission: the client already declares `requestedRunnerID` and `diff` with
  defaults, so the server may emit them; `ignoreUnknownKeys` is off, so no new
  *required* key may be emitted to an installed client that lacks the defaulted DTO.
- [ ] Focused server and client tests pin the new behavior and fail if it is removed.

## Subtasks and dependencies

1. **Commit and verify the `0004` migration.** Stage the two `0004` files, confirm
   they sort after `0003`, and confirm the runner discovers them. Add a SQLite
   migration test; run PostgreSQL when `TASKTRACKER_TEST_POSTGRES_URL` is available.
2. **Verify the host round-trip.** Confirm `CreatePairingCode` persists `host`, the
   pairing-code and runner reads return it, and the run response emits it. The
   client already sends and renders it.
3. **Verify targeting and diff round-trip.** Confirm dispatch persists the requested
   runner as `requested_runner_id`, the claim honors it, and the runner callback
   writes `result_diff` that the response returns. Align the client dispatch
   `runnerId` with the server column if the names diverge.
4. **Close the client diff gap.** Add a defaulted `diff`/`resultDiff` field to the
   KMP run DTO and map it to `AgentRun.diff` so the diff sheet is populated from the
   server, not only from a faked `AgentRun`.
5. **Pin the contract.** Extend `cmd/server/contract_test.go` to lock the new keys
   and the compatibility gate; add/adjust KMP tests for the diff mapping.
6. **Add the CLI flag.** Implement `tt agent connect --pairing-code` to redeem a
   pairing code and pair the local runner.
7. **Cross-repo ACK and release gate.** Agree the contract with
   `agent-management.md`, then run `scripts/check.sh full` (KMP) and the server
   `go test` release gate.

## Non-goal

- [ ] This does not change the agent connectivity model (outbound/poll already
  supported); it only adds the host/targeting/diff fields and a CLI bootstrap.
- [ ] It does not add a graph database, message bus, or second deployable.
- [ ] It does not make the diff a required response field for installed clients that
  predate the defaulted DTO.

## Validation

- Server: `go test ./internal/entities/... ./internal/agent/... ./internal/db/...
  ./cmd/server/...`; `go test -race` on the claim/delivery path; PostgreSQL when a
  test URL is available; run `cmd/server/contract_test.go`.
- Client: `scripts/check.sh quick :packages:feature-projects-presentation
  '*.aTerminalRunWithADiffOpensTheDiffSheet'` (now backed by a real DTO) and the
  other `AgentManagementScenarios`; `scripts/check.sh full` as the release gate.
- Confirm the migration test passes on both drivers and that the contract test pins
  the new keys.

## Handoff

- **Done:** (none yet — this spec consolidates the deferred work.) The server working
  tree already contains most of the Go wiring for `host`, `requested_runner_id`, and
  `result_diff`; the client already renders the host label and the target picker and
  sends `host` on pairing.
- **Pending:** Commit + verify the untracked `0004` migration on both drivers; pin
  the new keys in `contract_test.go`; close the KMP run-DTO `diff` gap; add
  `tt agent connect --pairing-code`; align the dispatch `runnerId` →
  `requested_runner_id` contract; ACK the field names/casing and compatibility
  rollout with `agent-management.md`; run both release gates.
- **Next step:** Get the cross-repo ACK on the contract (names, casing, rollout),
  then commit `0004`, pin the contract test, close the KMP `diff` DTO gap, add the
  CLI flag, and run `scripts/check.sh full` plus the server `go test` gate.
- **Gotchas:** The migration runner discovers files from disk, so the untracked
  `0004` applies locally but is not release-clean until committed. The client
  declares `requestedRunnerID` and `diff` with defaults — that is the rollout
  condition for the server to emit them — but `ignoreUnknownKeys` is off, so a gained
  or renamed response key is a hard break for installed builds; the contract test
  must pin the key sets by name. The server currently derives `requestedRunnerID`
  from the claiming runner, so the client's requested-runner choice must be mapped to
  `requested_runner_id` on dispatch or the picker's choice is silently dropped.

## Risks / notes

- Emitting `host`/`requestedRunnerID`/`resultDiff` before the ACK and before the
  client ships the defaulted DTO breaks installed clients; deployment order still
  matters.
- A runner that connects via `--pairing-code` must still authenticate and claim like
  any other runner; the flag is a bootstrap path, not a bypass of pairing security.
- Keep the host model to the two kinds (desktop, remote) plus "not stated"; do not
  invent a third kind for legacy or older-client runners.
