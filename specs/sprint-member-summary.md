# sprint-member-summary — store sprint members and publish them through sync

- **status:** in-progress
- **priority:** P1
- **context:** Cross-repo feature approved in `ACK` on 2026-11-19. Companion spec in
  `../TaskTrackerKMP/specs/sprint-member-summary.md`. Server lands first: an older server
  rejects an unknown pushed entity, so the client may not push `sprintMember` until this
  branch exists on the server.

## Scope

- `internal/entities` — `SprintMember` value, JSON decode, repository.
- `internal/db/migrations` — new version `0005` for both dialects.
- `internal/sync` — accept `sprintMember` pushes; serve a new `sprintMembers` pull scope.
- `cmd/server` — composition root and executable contract test.
- `docs/API.md` — entity vocabulary and new scope.

No existing response gains, loses or renames a key. `sprintMember` is a new entity and a
new scope, so installed clients never request it and never decode its payload.

## Files

- `internal/entities/models.go` — `SprintMember`, `SprintMemberFromJSON`.
- `internal/entities/sprint_member_repository.go` — new repository (SQL only here).
- `internal/entities/sprint_member_repository_test.go` — behaviour tests.
- `internal/db/migrations/0005_sprint_members.sqlite.sql` — `IF NOT EXISTS` DDL.
- `internal/db/migrations/0005_sprint_members.postgres.sql` — same version, Postgres DDL.
- `internal/sync/engine.go` — entity allow-list, scope resolution, push and pull branches.
- `internal/sync/engine_test.go` — push/pull tests.
- `cmd/server/main.go` — wire the repository into the engine.
- `cmd/server/contract_test.go` — pin closed key sets for the new push/response shapes and
  prove existing key sets are unchanged.
- `docs/API.md` — vocabulary and scope.

## Contract

- [ ] `POST /v1/sync/push` accepts `entity: "sprintMember"` and persists one row per
      `(sprintID, userID)`, with `availableStoryPoints` floored at zero.
- [ ] A duplicate member for the same sprint is an update, not a second row.
- [ ] `GET /v1/sync/changes/sprintMembers` returns only that entity's rows and the unchanged
      envelope keys (`changes`, `nextCursor`, `hasMore`, `resyncRequired`).
- [ ] No existing scope's response gains a key; the contract test fails if one does.
- [ ] `sprint_members` exists on both SQLite and Postgres; `go test ./internal/db` passes on
      both dialects.
- [ ] Every behaviour change has a test that fails when the production change is removed.

## Non-goal

- No server-side lock enforcement: the client owns the `isLocked || isFinished` rule, and
  the sync engine stays a router of opaque payloads.
- No per-member task list on the wire; the client derives it from synced work items.

## Validation

- `go test ./internal/entities ./internal/sync ./internal/db ./cmd/server`
- `go test -race ./internal/sync` (the engine is shared state)
- `TASKTRACKER_TEST_POSTGRES_URL=<throwaway> go test ./internal/db ./internal/entities`
  when a throwaway Postgres is available.
- `scripts/check.sh all` at release time only.

## Handoff

- **Done:** branches and both specs created; design approved.
- **Pending:** all implementation and validation above.
- **Next step:** add `SprintMember` to `internal/entities/models.go`.
- **Gotchas:** `0001` is untouchable. Go `json.Unmarshal` ignores unknown keys, so a key-set
  assertion is the only check that can see a response gain. SQLite placeholder order must be
  ascending. SQL column names stay `snake_case`; wire keys stay `camelCase`.

## Risks / notes

- The new pull scope must not be folded into `workItems`: an installed client requests that
  scope and would receive rows with an entity it does not handle.
- Project membership scoping must match the other entities, or one project can read another
  project's roster.
