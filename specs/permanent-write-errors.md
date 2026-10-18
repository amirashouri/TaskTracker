# T4.6 — Permanent push failures are refused

- **status:** done
- **priority:** P1
- **plan:** [`docs/PLAN.md`](../docs/PLAN.md) §Milestone 4 — Sync engine, entry `T4.6` — the
  *what* and the rationale live there; this file is the *how*.

## Scope

`internal/entities` (the classifier) and `internal/sync` (the verdict). No schema
change, no wire-format change: the push response keeps its four verdicts and its key set,
and only which verdict a write failure earns moves.

## Files

- `internal/entities/write_error.go` *(new)* — `PermanentWriteError(error) bool`, reading
  the driver's error code. It lives in `entities` because the code is SQL and that package
  owns SQL; `internal/sync` only asks it.
- `internal/sync/engine.go` — the entity-write `default:` arm answers `rejected` when the
  error is permanent, `retry_later` otherwise. The `appendLog` and `Commit` arms do not move.
- `internal/entities/write_error_test.go` *(new)* — the policy, code by code, both drivers.
- `internal/sync/engine_test.go` — `TestPushRejectsAWorkItemTheDatabaseRefuses`.
- `docs/PLAN.md`, `docs/ARCHITECTURE.md` — the record.

## Contract

- [ ] A work item whose `status` no `CHECK` accepts is answered `rejected`, not
      `retry_later`, and is not stored. (`TestPushRejectsAWorkItemTheDatabaseRefuses`)
- [ ] With the classifier bypassed, that test fails and names the failing constraint.
- [ ] A work item whose payload merely fails to decode still earns `retry_later`; a
      `sync_log` failure does too. (`TestPushRollsBackWhenTheLogRowFails` is the guard —
      it must stay green and untouched.)
- [ ] Postgres `23503`/`23505`/`40001`/`40P01` and SQLite
      `ErrConstraintForeignKey`/`ErrConstraintUnique` are **not** permanent.

## Validation

- Run: `scripts/check.sh` and `scripts/check.sh race` — this is the sync engine.
- Counts: no host matrix to move; the server has no baseline file.
- Both drivers wanted: `TestPushRejectsAWorkItemTheDatabaseRefuses` runs against whatever
  `dbtest.NewDatabase` selects, so setting `TASKTRACKER_TEST_POSTGRES_URL` exercises the
  `23514` path on Postgres and the default run exercises `ErrConstraintCheck` on SQLite.

## Handoff

- **Done:** classifier, engine arm, both tests, and the docs; merged to `main` locally (the
  repo has no working remote, so the `AGENTS.md` no-remote path — `git merge --ff-only` — is
  the one taken). Green on `scripts/check.sh`, `scripts/check.sh race` and `scripts/check.sh
  docs`, and both tests mutation-checked: bypassing the classifier reports `status =
  retry_later (CHECK constraint failed: status IN (...))`, and narrowing the SQLite case fails
  `sqlite_check_violation`.
- **Pending:** nothing.
- **Next step:** none for this task. The mirror-image gap below is the only related work left,
  and it needs its own task.
- **Gotchas:** `sqlite3.ErrConstraintDatatype` does not exist in `mattn/go-sqlite3`
  v1.14.22 — that was a wrong detail in the approved plan. SQLite's datatype error is the
  primary code `ErrMismatch`, which is a driver-level type mismatch rather than a bad
  payload value, so it is deliberately **not** classified permanent. `go-sqlite3` returns its
  `Error` by value, so `errors.As` needs the concrete type and not a pointer.
  Do not widen the permanent set to foreign-key or unique violations, and do not classify
  the `appendLog`/`Commit` arms — the reasons are in `docs/ARCHITECTURE.md`.

## Risks / notes

- Easy to get wrong: treating "permanent" as "any error the database raises". The narrow set
  is the whole point; `rejected` cannot be taken back on the client, and a wrongly-dead
  lettered change is silent data loss.
- The mirror-image gap is left alone on purpose: a decode failure in `resolveProject` still
  answers `retry_later`, the same "version skew a later release understands" trade the client
  makes. Widening this task to cover it would be a second policy change in one diff.
