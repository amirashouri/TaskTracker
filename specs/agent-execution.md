# agent-execution — Coordinate project-scoped agent runs

- **status:** pending
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

Confirm the implementation file list in the implementation plan before editing.
Known project model and repository files:

- `internal/entities/models.go` — add optional project repository URL metadata.
- `internal/entities/project_repository.go` — persist and load the repository URL.
- `docs/API.md` and `cmd/server/contract_test.go` — document and pin API
  contracts, including compatibility expectations.
- `internal/db/migrations/` — add paired versioned SQLite and PostgreSQL schema
  changes for project metadata, agent registration, and agent runs as needed.

The exact route, handler, agent repository, run repository, tests, and wiring
files are to be confirmed in the implementation plan after inspecting existing
patterns. Do not expand the approved implementation scope without a secondary
ACK.

## Contract

- [ ] A project can store an optional repository URL and return it through
  supported project APIs without breaking installed clients. The wire name is
  `repoUrl`; rollout of any response field follows the documented compatibility
  plan and does not assume clients ignore unknown keys.
- [ ] Agent setup can pair an agent to a project using a short-lived,
  single-use credential. The server stores only what is needed to validate
  future agent authentication; pairing secrets are not logged or returned
  after use.
- [ ] The server records agent identity, project assignment, connection
  heartbeat/health, and agent-run state durably.
- [ ] An authorized run request creates a stable `runId` and a durable queued
  state before dispatch. Duplicate requests with the same idempotency key do
  not create duplicate runs.
- [ ] Agents can obtain work through an outbound connection or polling flow, so
  agents on NAT/firewalled workstations and VPS hosts do not require inbound
  reachability. Server push may be used only where reachability is available.
- [ ] The server creates a bounded, task-focused context graph with a versioned
  schema, stable node IDs, explicit edge direction, and permission-filtered
  project/task/dependency/context data. It does not expose unrelated project
  history or unrestricted comments/logs.
- [ ] The run payload includes project repository metadata, task and target
  branch information when available, graph schema version, and run ID. Private
  repository credentials are not embedded in project metadata or the payload.
- [ ] Authenticated, idempotent callbacks update only the matching run and
  allowed state transitions; callbacks cannot change a run belonging to another
  project or agent.
- [ ] Delivery failures, timeouts, retries, cancellation, and terminal failure
  are represented explicitly; a task is never shown as started solely because
  a dispatch was attempted.
- [ ] SQLite and PostgreSQL migrations and repository behavior remain aligned.
- [ ] Focused tests pin the new behavior and fail if its implementation is
  removed.

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

- **Done:** Specification only; no implementation or validation has been run.
- **Pending:** Agree and implement the detailed API, data model, and rollout
  contract with the KMP spec.
- **Next step:** In the implementation session, inspect existing server agent,
  project, migration, and routing patterns and submit the exact dual-repo file
  plan for ACK.
- **Gotchas:** Keep the server as coordinator, not execution host. Agents may
  run locally or on a VPS and must be able to connect outward. Client decoding
  rejects unknown response keys; align repository metadata rollout with the
  client before sending it.

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
