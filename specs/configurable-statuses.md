# configurable-statuses — project-defined workflow statuses (server)

- **status:** pending
- **priority:** P1
- **context:** Server half of the cross-repo configurable-statuses feature. Read the client
  spec `TaskTrackerKMP/specs/configurable-statuses.md` and `../AGENTS.md` §1/§2 first: wire
  change, needs a Phase 1 `ACK`, **server lands first**, and never emit a new field before
  the client declares it with a default.

## Scope

`internal/db/migrations` (both dialects), `internal/entities` (work items + projects),
`cmd/server` (payloads), `docs/API.md`, `cmd/server/contract_test.go`. No auth, sync-engine
algorithm, or middleware changes.

## Files

`to confirm` before implementation.

- `internal/db/migrations/000X_configurable_statuses.postgres.sql` and `.sqlite.sql` — one
  version, both dialects, `IF NOT EXISTS`: a project status catalog and a per-item reference,
  seeded per project with the current six statuses so nothing changes meaning.
- `internal/entities/project*.go` — persist/read the catalog.
- `internal/entities/workitem*.go` — persist/read the status reference; keep the category the
  terminal/board rules read.
- `internal/db/migrations/*` — the current `work_items_status_check` (`CHECK (status IN
  (...))`) is the hard wall; the additive shape keeps the coarse category in that column and
  adds the catalog reference, so the constraint is not loosened and no migration rewrites
  existing rows.
- `cmd/server/*.go` — serde for project and work-item payloads.
- `docs/API.md` + `cmd/server/contract_test.go` — pin the client's declared keys.

## Contract

- [ ] Project payloads carry the ordered status catalog; work-item payloads carry the status
  reference, with Go `json` tags mapping **exactly** to the client's `@SerialName`.
- [ ] The server seeds every existing project with the six current statuses on migration, so
  installed clients see identical boards before and after.
- [ ] The category (Backlog/Todo/InProgress/InReview/Done/Closed) stays in the existing column
  and CHECK constraint; renaming/adding a status does not alter overdue or archive behavior.
- [ ] The server does not emit a new key before the client declares it with a default.
- [ ] Migrations pass on **both** SQLite and Postgres (`go test ./internal/db` catches a
  missing dialect file). Reverting the production change fails a focused test.

## Non-goal

- [ ] No transition/guard rules per status.
- [ ] No per-role visibility.
- [ ] No change to the sync conflict-resolution algorithm.

## Validation

- Run `go test ./internal/<pkg>` for `db` and `entities` (and `sync` if the payload rides
  sync); `go test ./cmd/server -run <Test>` for the contract test. Seconds.
- Cross-database: SQLite by default, Postgres via `TASKTRACKER_TEST_POSTGRES_URL`; verified
  only once both pass.
- `scripts/check.sh all` is the release gate, not a per-task check.
- Never make a check green by loosening a constraint or weakening the contract test.

## Handoff

- **Done:** Scope and additive design direction agreed (planning only).
- **Pending:** Phase 1 `ACK`; catalog storage shape; seed migration; whether a catalog
  endpoint is needed or the catalog only rides the project payload.
- **Next step:** Write the dual-contract Phase 1 plan (migration + payload ↔ KMP `Project` /
  `WorkItem`) with the client spec, then request `ACK`.
- **Gotchas:** `work_items_status_check` is `CHECK (status IN
  ('backlog','todo','in_progress','in_review','done','closed'))` — a renamed status must not
  be written into that column. Keep the category there and add the reference beside it.

## Risks / notes

- The client is the fixed point; changing the `status` value space would break installed
  builds — the additive catalog reference is the safe plan, `/v1` only as a last resort.
- The catalog is project configuration (like tags) and must round-trip through sync; two
  offline clients defining statuses is the merge hazard, so the server owns the catalog.