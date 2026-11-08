# agent-execution — Coordinate project-scoped agent runs

- **status:** in-progress
- **priority:** P1
- **context:** Plan for project repository metadata, agent setup from the client,
  and task execution by agents running on an admin workstation or VPS.

## Scope

The Go server owns project repository metadata, agent pairing and registration,
agent health, run authorization and lifecycle, context snapshot generation,
dispatch coordination, and authenticated agent callbacks. The agent—not the
server—clones or opens the repository and edits code on the machine where the
agent runs.

This is one server deployable. Do not add a graph database, message bus, or
server-side repository execution. Prefer existing database and server patterns.

## Files

Confirmed on `feat/agent-execution`:

- `internal/entities/models.go` — `Project.RepoURL` (emitted as
  `repoUrl,omitempty`) and the `PushChange.ProjectRepoURL` carrier.
- `internal/entities/project_repository.go` — persist and load `repo_url`.
- `internal/entities/repo_url.go` — URL validation (http/https/ssh, no password).
- `internal/entities/agent_repository.go` — project assignment, idempotent
  create, project-scoped claim.
- `internal/entities/agent_pairing.go` — hashed, expiring, single-use codes and
  atomic pairing.
- `internal/entities/workitem_repository.go` — bounded `GetByIDs` for graph
  dependencies.
- `internal/entities/comment_repository.go` — recent-comment page for the graph.
- `internal/admin/projects.go` — admin-only `repoUrl` input and preservation.
- `internal/sync/engine.go` — a membership push cannot change repository
  metadata.
- `internal/agent/app_handler.go` — idempotent dispatch, pairing-code issue,
  `agentEnabled` enforcement.
- `internal/agent/handler.go` — unauthenticated pair exchange.
- `internal/agent/context_graph.go` — versioned bounded graph with stable IDs
  and directional edges.
- `internal/db/migrations/0003_agent_execution.{sqlite,postgres}.sql`.
- `cmd/server/main.go` — pairing routes.
- `docs/API.md` and `cmd/server/contract_test.go` — documented and pinned
  contracts, including the `repoUrl` compatibility gate.

Tests live beside the code: `internal/entities/{agent_repository,repo_url}_test.go`,
`internal/agent/app_handler_test.go`, `internal/db/migrate_test.go`, and
`cmd/server/contract_test.go`. The KMP half remains in its own spec
(`TaskTrackerKMP/specs/agent-management.md`); this branch does not edit the client.

## Contract

- [ ] A project can store an optional repository URL and return it through
  supported project APIs without breaking installed clients. The wire name is
  `repoUrl`; rollout of any response field follows the documented compatibility
  plan and does not assume clients ignore unknown keys.
  **Server side done:** the value is stored, validated and carried in the agent
  brief. **Pending:** the project-response key, which stays `json:"-"` until the
  KMP client ships `repoUrl` with a default. The contract test pins the absence.
- [x] Agent setup can pair an agent to a project using a short-lived,
  single-use credential. The server stores only what is needed to validate
  future agent authentication; pairing secrets are not logged or returned
  after use.
- [x] The server records agent identity, project assignment, connection
  heartbeat/health, and agent-run state durably.
- [x] An authorized run request creates a stable `runId` and a durable queued
  state before dispatch. Duplicate requests with the same idempotency key do
  not create duplicate runs.
- [x] Agents can obtain work through an outbound connection or polling flow, so
  agents on NAT/firewalled workstations and VPS hosts do not require inbound
  reachability. Server push may be used only where reachability is available.
- [x] The server creates a bounded, task-focused context graph with a versioned
  schema, stable node IDs, explicit edge direction, and permission-filtered
  project/task/dependency/context data. It does not expose unrelated project
  history or unrestricted comments/logs.
- [x] The run payload includes project repository metadata, task and target
  branch information when available, graph schema version, and run ID. Private
  repository credentials are not embedded in project metadata or the payload.
  `targetBranch` is omitted because no branch metadata exists yet.
- [x] Authenticated, idempotent callbacks update only the matching run and
  allowed state transitions; callbacks cannot change a run belonging to another
  project or agent.
- [x] Delivery failures, timeouts, retries, cancellation, and terminal failure
  are represented explicitly; a task is never shown as started solely because
  a dispatch was attempted.
- [x] SQLite and PostgreSQL migrations and repository behavior remain aligned.
  SQLite is tested; PostgreSQL is pending unless `TASKTRACKER_TEST_POSTGRES_URL`
  is available.
- [x] Focused tests pin the new behavior and fail if its implementation is
  removed (verified by mutation for the response gate, project-scoped claim and
  single-use pairing).

## Subtasks and dependencies

1. **Add project repository metadata.** Add optional `repoUrl` to the Go project
   entity, migrations, repository reads/writes, validation, and supported API
   paths. Coordinate its response rollout with the KMP spec; never expose a new
   response key to installed clients without a compatibility strategy.
2. **Add durable agent and run records.** Define project-scoped agent identity,
   pairing/secret lifecycle, health, run IDs, idempotency, and state transitions.
   This depends on the persistence conventions from subtask 1.
3. **Implement secure pairing and agent access.** Provide client-facing pairing
   APIs and agent-facing authentication/claim or poll APIs. Pairing credentials
   must expire, be single-use, and be stored safely. Validate registered
   callback or connection destinations according to the chosen agent
   connectivity model.
4. **Implement run creation and dispatch.** Authorize the client request, create
   the run durably, and deliver it using a retryable database-backed mechanism
   consistent with the single-binary architecture. Support agents that connect
   outward; do not assume the server can reach an admin workstation.
5. **Build the bounded context graph.** Assemble a versioned task subgraph from
   project, task, dependencies, and permitted relevant context. Define
   directionality (for example, task → prerequisite for `DEPENDS_ON`), limits,
   recency, and metadata before exposing the payload.
6. **Implement callback and status APIs.** Authenticate callbacks, enforce
   ownership and legal transitions, and expose run/agent status for the client.
   This depends on subtasks 2–4.
7. **Document, test, and verify.** Update API docs and contract tests; test
   migrations and behavior on SQLite and PostgreSQL. Run focused server tests
   for touched packages; record any release-gate checks not run.

## Non-goal

- [ ] The Go server does not clone repositories, execute agent tools, or store
  repository access credentials.
- [ ] This work does not require a graph database or a second deployable service.
- [ ] Agent installation/distribution is not performed by the server; the CLI
  remains an optional bootstrap path for headless or automated setup.

## Validation

- Run focused `go test` commands for changed server packages, including
  `internal/entities`, `internal/db`, and the new/changed handler packages.
- Run the relevant contract tests in `cmd/server`.
- Run migration and repository tests against SQLite and, when
  `TASKTRACKER_TEST_POSTGRES_URL` is available, PostgreSQL.
- Run `go test -race` on any package where concurrent run claiming, delivery,
  or callback processing is implemented.
- `scripts/check.sh` is a release gate, not a per-task check; leave it pending
  unless this work is part of a release.

## Handoff

- **Done:** Server backend on `feat/agent-execution`: `repo_url` storage and
  admin-only validation; `0003` migrations for both drivers; project-scoped
  runner assignment and single-use pairing codes; idempotent dispatch; a
  versioned bounded context graph carried in `taskDocument`; routes, API docs,
  and contract tests. The KMP DTO now declares `repoUrl` and the runner
  `projectId` with defaults, so project responses emit `repoUrl,omitempty` and
  the runner list names its project; the contract key sets were updated. Focused
  `go test` passes for `internal/entities`, `internal/admin`, `internal/sync`,
  `internal/db`, `internal/agent` and `cmd/server`.
- **Pending:** PostgreSQL migration/behavior run when a test URL is available;
  `scripts/check.sh` remains a release gate. Optional follow-ups: `tt agent
  connect --pairing-code`, a project default branch for `targetBranch`, and a
  project-scoped runner list on the client.
- **Next step:** Finish `TaskTrackerKMP/specs/agent-management.md`: render the
  project's paired machines with health and host labels, add the CLI pairing
  flag, and run the KMP full matrix gate.
- **Gotchas:** The client declares both fields with defaults, which is the
  rollout condition for emitting them; an older installed client still fails on
  a project that *has* a `repoUrl`, so deployment order still matters until the
  clients in the field ship the defaulted DTO. `repoUrl` is admin-only; the sync
  `applyProject` preserves it on membership pushes. `agentEnabled` refuses
  dispatch with `409`. Pairing codes are `ttpc_`, runner tokens are `ttag_`, and
  both are stored as SHA-256 hashes only. The graph's `DEPENDS_ON` direction is
  task → prerequisite.

## Risks / notes

- A repository URL does not grant access to a private repository. Use agent-side
  credentials or a narrowly scoped, short-lived credential mechanism that is
  not stored in graph context.
- Local agent endpoints can create server-side request forgery risk if the
  server makes arbitrary outbound requests. Prefer outbound agent connections;
  validate destinations if callback-based push is retained.
- Context comments and logs can contain secrets or data the requesting user
  cannot see. Apply authorization, filtering, and size/recency limits before
  constructing the graph.
- Define which project member/admin roles may pair agents, start runs, and view
  callbacks/results.
