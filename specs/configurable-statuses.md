# configurable-statuses — project-defined workflow statuses (server)

- **status:** in-progress
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

- `internal/entities/models.go` — added `StatusDefinition{id,name,category,order,color}`,
  `Project.Statuses` (`json:"statuses,omitempty"`), `WorkItem.StatusKey`
  (`json:"statusKey,omitempty"`); `WorkItemStatus` stays the coarse category.
- `internal/db/migrations/0002_project_statuses.postgres.sql` + `.sqlite.sql` — add
  `projects.statuses TEXT` and `work_items.status_key TEXT`.
- `internal/entities/project_repository.go` — `statuses` column, `decodeStatuses`,
  `normalisedStatuses`, insert/update marshal.
- `internal/entities/workitem_repository.go` — `status_key` column, scan, insert/update.
- `cmd/server/contract_test.go` — allow `statuses` on the project key set; added
  `TestTheProjectStatusCatalogRoundTripsWithTheClientsKeys`.
- `internal/entities/repository_test.go` — added
  `TestStatusCatalogAndStatusKeyRoundTrip`.
- `docs/API.md` — `statuses`/`statusKey` documented, omit-when-empty noted.

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

- **Done:** Server layer implemented and verified. `StatusDefinition` +
  `Project.Statuses` + `WorkItem.StatusKey` on the model; `0002` migration for both drivers;
  project and work-item repositories read/write the new columns; contract and repository tests
  added; `docs/API.md` updated. `go test ./...` green.
- **Pending:** Client half (domain, database, mapping, remote DTO, board/editor, admin catalog
  UI) — the client spec `TaskTrackerKMP/specs/configurable-statuses.md` owns it. Not yet
  committed to any branch.
- **Next step:** Implement the client model + data layer against the now-verified server
  contract, then the presentation.
- **Gotchas:** `statuses`/`statusKey` use `omitempty`, so a catalog-free project adds no key
  and installed clients still decode. Once a project adopts a catalog, the key is emitted —
  the client build must reach users before that happens (client-first rule). `isTerminal`,
  the archive, and `work_items_status_check` still read the coarse `status` category.

## Risks / notes

- The client is the fixed point; changing the `status` value space would break installed
  builds — the additive catalog reference is the safe plan, `/v1` only as a last resort.
- The catalog is project configuration (like tags) and must round-trip through sync; two
  offline clients defining statuses is the merge hazard, so the server owns the catalog.