# refactor-sync-apply-helper — deduplicate the change-feed upsert path

- **status:** in-progress
- **priority:** P1
- **branch:** `refactor/sync-apply-helper`
- **blocked by:** none
- **context:** Duplicate-code audit of 2026-11-22. `applyWorkItem`, `applySprint`,
  `applySprintMember`, `applyComment` and `applyAttachment` are the same
  delete/decode/load/create/version-check/update algorithm five times
  (`internal/sync/engine.go:526–697`). It is a behaviour-preserving internal
  refactor: no wire key, no endpoint, and no schema changes.

## Scope

- `internal/sync/engine.go` — the five generic `apply*` functions and one shared
  upsert helper.
- `internal/sync/engine_test.go` — add direct coverage if a contract line has no
  existing test.

The helper may not change the version compare-and-swap semantics, the
error wrapping (`decode project: …` style), or anything `applyProject` does with
`ProjectRepoURL`. Do not touch the change-feed envelope, `docs/API.md`, or
`cmd/server/contract_test.go`.

## Files

- `internal/sync/engine.go` — add `syncOps[T]` / `applyUpsert[T]`; reduce
  `applyWorkItem`, `applySprint`, `applySprintMember`, `applyComment`,
  `applyAttachment` to their type-specific wiring.
- `internal/sync/engine_test.go` — pin create/update/delete/version-conflict for
  at least one generic entity if the existing push tests do not already.

## Contract

- [x] `applyWorkItem`, `applySprint`, `applyComment` and `applyAttachment` share
      one upsert implementation; only their decode/repo/field wiring differs.
- [x] A missing row is created with `change.EntityID` as its id and returns the
      created version (`TestPushCreates*`).
- [x] An existing row with a non-empty `change.Version` that differs is still
      rejected with `versionConflict` and `StatusConflict`
      (`TestPushUpdateRespectsVersion`, `TestConflictVerdictCarriesTheServerVersion`).
- [x] An existing row keeps its server `ID`, `CreatedAt` and current `Version`
      (not the payload's) before the update commits (`TestPushWithoutAVersionOverwrites`).
- [x] `applySprintMember` still defaults a zero `CreatedAt` on create
      (`TestSprintMembersPushAndPullOnTheirOwnScope`), and `applyAttachment`
      still removes the blob on delete
      (`TestPushDeleteRemovesTheAttachmentBlob`, mutation-checked: fails when the
      `onDelete` hook is removed).
- [x] `applyProject` keeps its bespoke repo-URL rules and is unchanged
      (`TestPushStoresARepositoryURLFromACreate`, `TestPushCannotChangeARepositoryURLAfterCreation`).
- [x] `go test ./internal/sync ./cmd/server` passes; the contract key sets are
      untouched.

## Non-goal

- No change to `applyChange`'s authorization, transaction, notification snapshot,
  or log-row ordering.
- No new entity type, no repository signature change, no generic helper exported
  outside `internal/sync`.

## Validation

- **Iteration:** `go test ./internal/sync`
- **Before handoff:** `go test ./internal/sync ./cmd/server` (the contract suite
  decodes the client DTOs); `go vet ./internal/sync`
- **Stays pending:** a real push against Postgres; the focused suite runs SQLite
  unless `TASKTRACKER_TEST_POSTGRES_URL` is set.

## Handoff

- **Done:** `syncOps[T]`/`applyUpsert[T]` added to `engine.go`; the five generic
  `apply*` functions are now wiring only. `applyProject` untouched. Added
  `TestPushDeleteRemovesTheAttachmentBlob` and mutation-checked it. Verified:
  `go test ./internal/sync ./cmd/server`, `go vet ./internal/sync`, `gofmt -l`.
- **Pending:** diff review and merge; the task branch is not merged.
- **Next step:** review the diff against `main` and merge per `AGENTS.md`, then
  delete this spec in the final branch commit.
- **Gotchas:** repository methods take typed ids (`WorkItemID`), so each call
  site wraps `GetByID`/`Delete`; keep those closures local. The version rule
  reads `change.Version != ""`, not the payload's version — several entities
  omit it from the payload on purpose. Net line count grew by ~14 because the
  wiring table is explicit; the duplicated *logic* is what was removed.

## Promote

- **To `docs/ARCHITECTURE.md`:** one line under the sync section: "Every
  entity in the change feed shares one upsert path; new entities add decode/repo
  wiring only, never a second copy of the version rule."
