# epic-owner — give an epic an owner

- **status:** in-progress
- **priority:** P1
- **branch:** `feat/epics` — the existing epic feature branch
- **blocked by:** none — cross-repo client spec is `../TaskTrackerKMP/specs/epics.md`;
  the server half lands first, then the client network/data/UI half
- **context:** Requested 2026-11-24: an epic has no owner field. Add one, expose a single-select
  member picker on the client, and give existing ownerless epics a deterministic admin default.
  This is a coordinated pre-release wire change on two unmerged `feat/epics` branches.

## Scope

Server schema, entity, repository, contract documentation and focused tests for an epic's
`ownerId`. It must not change sprint ownership, work-item assignment, user roles, or the
sync scope/visibility rule: an epic is still delivered to members of any linked project.

## Files

- `internal/db/migrations/0009_epic_owner.postgres.sql` — nullable `epics.owner_id` FK to
  `users(id)` with `ON DELETE SET NULL`, and a backfill of ownerless rows to the oldest
  non-deleted admin.
- `internal/db/migrations/0009_epic_owner.sqlite.sql` — the same change in SQLite spelling.
- `internal/entities/models.go` — `Epic.OwnerID *UserID` with the `ownerId` JSON key.
- `internal/entities/epic_repository.go` — select/insert/update the column; default a nil
  owner to the oldest active admin on create and update.
- `internal/entities/user_repository.go` — `FirstByRole` lookup used for the default.
- `internal/db/migrate_test.go` — pin the column and backfill behaviour.
- `internal/entities/epic_repository_test.go` — round-trip and default-owner tests.
- `internal/sync/epics_test.go` — a pushed owner survives the sync upsert.
- `cmd/server/contract_test.go` — pin `ownerId` in the epic push/pull key set.
- `docs/API.md` — document the epic payload's `ownerId`.
- `docs/ARCHITECTURE.md` — retain the deterministic admin-default rule when this spec is removed.

## Contract

- [ ] An epic row has a nullable `owner_id` referencing `users(id)`; deleting the user sets it
      to null (`TestEpicOwnerMigrationAddsItsColumn`).
- [ ] Existing rows with no owner are backfilled with the oldest non-deleted admin; with no
      admin the column stays null rather than inventing a user
      (`TestEpicOwnerMigrationBackfillsAdmins`).
- [ ] A create without an owner gets the admin default, an update without one keeps the stored
      owner, and an explicit owner is preserved (`TestEpicOwnerRoundTripAndDefault`).
- [ ] The epic change payload carries the exact `ownerId` key the client declares
      (`TestTheEpicScopeIsWhatTheClientDecodes`).
- [ ] Reverting the repository default or the backfill turns a named test red.

**Wire Constraint:** the key is `ownerId`. The client declares it with a null default and the
contract test pins that key name; both halves ship together from the unmerged `feat/epics`
branches. No response key for any installed client is removed or renamed.

## Non-goal

- The member picker UI and the client's local storage; those live in TaskTrackerKMP.
- Changing who may see an epic or adding an owner-only permission.
- Adding a new sync scope or changing the epics cursor/fan-out rule.

## Validation

- **Iteration:** `go test ./internal/db`, `go test ./internal/entities`,
  `go test ./internal/sync`, `go test ./cmd/server`.
- **Before handoff:** the same packages with `TASKTRACKER_TEST_POSTGRES_URL` set to a throwaway
  server; `scripts/check.sh` remains the release gate, not a per-task check.
- **Stays pending:** a real client round-trip against a running server; the contract test proves
  the wire shape, not a device install.

## Handoff

- **Done:** migration `0009` for both drivers, `Epic.OwnerID`, repository persistence and the
  admin default, contract/API docs, and focused tests. `go test ./internal/db ./internal/entities
  ./internal/sync ./cmd/server` is green on SQLite. Mutation-checked: disabling the repository
  default turns `TestEpicOwnerRoundTripAndDefault` red, and disabling the SQL backfill turns
  `TestEpicOwnerMigrationBackfillsAdmins` red; both were restored green.
- **Pending:** the Postgres run with `TASKTRACKER_TEST_POSTGRES_URL` (no server URL is configured
  in this session), promotion to `docs/ARCHITECTURE.md`, spec deletion, and the client half.
- **Next step:** after the client half lands, run the focused packages against Postgres, promote
  the durable decision, delete this spec, and commit on `feat/epics`.
- **Gotchas:** SQLite numbers `$N` by appearance, so placeholders must ascend in SQL text order.
  Only `deleted` means gone; an inactive admin still counts as an admin. An update that omits
  `ownerId` keeps the stored owner; only a create defaults to the oldest active admin. The
  contract test pins key names, not just decode results, because Go's decoder is
  case-insensitive.

## Promote

- **To `docs/ARCHITECTURE.md`:** under durable decisions, the epic owner defaults to the oldest
  non-deleted admin when a write omits it, and the backfill is deterministic rather than
  caller-dependent.
