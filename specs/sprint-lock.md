# Sprint lock — the explicit flag that replaces the start-date edit rule

- **status:** done
- **priority:** P2
- **plan:** no `docs/PLAN.md` entry — standalone cross-repo feature. The client
  half is [`../../TaskTrackerKMP/specs/sprint-lock.md`](../../TaskTrackerKMP/specs/sprint-lock.md).

## Scope

- `internal/db/migrations/0008_sprint_lock.{sqlite,postgres}.sql` — one additive
  column, `is_locked BOOLEAN NOT NULL DEFAULT FALSE`.
- `internal/entities/models.go` — `Sprint.IsLocked` with `json:"isLocked"`.
- `internal/entities/sprint_repository.go` — the column in `sprintColumns`, in
  `scanSprint`, and in the `Insert`/`Update` statements.
- `internal/entities/repository_test.go` — the round trip.
- `internal/entities/timestamp_test.go` — the flag in the JSON round trip.

No new endpoint, no new sync entity, no sync-engine branch: the lock is a field
on a sprint, and the existing sprint upsert carries it like any other field.

## Contract

- [x] A sprint created without the flag stores `is_locked = false`, so every
      sprint that predates the column keeps accepting edits.
- [x] `Update` writes `is_locked`, so a push that flips it reaches every device
      through the ordinary pull feed.
- [x] The migration is idempotent-by-runner on SQLite (no `IF NOT EXISTS`) and
      `IF NOT EXISTS` on Postgres, matching the other additive migrations.
- [x] `isLocked` is a response field the installed client can decode: the client
      declares it with a default, so an old build ignores it and a new one
      reads it. Nothing else in the sprint payload changed.
- [x] The server invents no lock policy. `sync` routes opaque payloads, and
      whether a locked sprint may take an edit is the client's call — a server
      rule here would be a second owner of a domain decision it cannot see the
      UI for.

## Validation

- `go build ./...`
- `go test -count=1 ./internal/entities/ ./internal/sync/ ./internal/db/ ./cmd/server/`
- `scripts/check.sh` was not run for this change; the targeted packages above
  cover the migration, the repository and the contract test.

## Handoff

- **Done:** migration, entity field, repository column/scan/write, tests.
- **Pending:** none in this repo.
- **Next step:** merge after the client half, because the client is the fixed
  point and already declares the field with a default; the two can land in
  either order once the client branch is available.
- **Gotchas:**
  - `sprintColumns` is used by `SELECT`, `INSERT` and `UPDATE`; adding a column
    means the placeholder count in `Create` moves with it (`$15`), and the
    positional arguments in both writers must match the new order.
  - Do not add a "reject an update to a locked sprint" rule in `sync`. The
    lock is reversible by design and the client enforces it at the use case; a
    server refusal would turn an offline-queued unlock into a stuck outbox row.
