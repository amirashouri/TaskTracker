# custom-fields — per-project custom fields on work items (server)

- **status:** pending
- **priority:** P1
- **context:** Server half of the cross-repo custom-fields feature. Read the client spec
  `TaskTrackerKMP/specs/custom-fields.md` and `../AGENTS.md` §1/§2 first: this is a wire/DTO
  change, it needs a Phase 1 `ACK`, and the **server lands first**. The installed client is
  the fixed point — never send a new field before the client ships one that declares it.

## Scope

`internal/entities` (models + repositories), `internal/db/migrations` (both dialects),
`cmd/server` (handlers and route table), `docs/API.md`, and `cmd/server/contract_test.go`.
No auth, rate-limiting, or middleware changes.

## Files

`to confirm` before implementation.

- `internal/entities/*.go` — `CustomFieldDefinition` storage on the project and
  `field_values` on the work item; repository read/write + optimistic locking.
- `internal/db/migrations/000X_custom_fields.postgres.sql` and `.sqlite.sql` — one version,
  both dialects; `IF NOT EXISTS`.
- `cmd/server/*.go` — serde for the project and work-item payloads; `mount` route table if a
  catalog endpoint is added.
- `docs/API.md` — the response shape, with the client's declared keys pinned.
- `cmd/server/contract_test.go` — key-name assertions on every path that decodes a project or
  work item.

## Contract

- [ ] Project responses carry the field catalog; work-item responses carry values, in Go
  `json` tags that map **exactly** to the client's `@SerialName` (case-sensitive).
- [ ] The server does not emit a new key until the client declares it with a default; adding
  a key is a decoding failure for every installed build, not a skipped value.
- [ ] Deleting a field definition leaves no dangling values (or they are ignored on read),
  matching the client's stale-tag behavior.
- [ ] Migrations pass on **both** SQLite and Postgres; `go test ./internal/db` proves a
  dialect file is not missing.
- [ ] Reverting the production change fails a focused test.

## Non-goal

- [ ] No cross-project/shared field catalog.
- [ ] No field-level permissions.
- [ ] No server-side validation beyond type/cardinality, until the client needs it.

## Validation

- Run `go test ./internal/<pkg>` for each touched package (`entities`, `db`, `sync` if the
  payload rides sync); `go test ./cmd/server -run <Test>` for the contract test. Fast loop,
  seconds.
- Cross-database: SQLite by default, Postgres via `TASKTRACKER_TEST_POSTGRES_URL`. A schema
  change is verified only once it passes on both.
- `scripts/check.sh all` is the release gate, not a per-task check.
- Never make a check green by weakening the contract test or deleting an assertion.

## Handoff

- **Done:** Scope and cross-repo split agreed (planning only).
- **Pending:** Phase 1 `ACK`; column-vs-table storage decision; API shape; migration version.
- **Next step:** Write the dual-contract Phase 1 plan (Go struct + migration ↔ KMP DTO) with
  the client spec, then request `ACK`.
- **Gotchas:** A response may not gain a field before the client declares it. `docs/API.md`
  and `contract_test.go` must move together; a decode-only test cannot see a case-only rename.

## Risks / notes

- Go matches JSON keys case-insensitively; Kotlin does not. Pin exact key names in the
  contract test, not just decodability.
- Timestamps inside field values must use the one wire date format
  (`internal/entities/timestamp.go`) so Kotlin `Instant` parsing stays exact.