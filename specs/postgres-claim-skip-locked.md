# postgres-claim-skip-locked — make the claim race correct on Postgres

- **status:** in-progress
- **priority:** P1
- **branch:** `fix/postgres-claim-skip-locked`
- **blocked by:** none
- **context:** A whole-repo Postgres run (throwaway `postgres:16`) failed
  `TestClaimRunIsAtomicAcrossRunners`: six concurrent claims over six queued runs,
  one claim returned nil. `specs/sprint-member-summary.md` recorded the failure as
  pre-existing before this branch point. The same run also exposed two test-side
  defects now fixed here so the Postgres matrix can be green without a skip.

## Scope

`internal/entities` (the claim statement and its dialect branch), `internal/db` (a
dialect query the repository can assert), and two test files. It must not reach the
wire shape, migrations, auth, or the sync engine's routing rules.

## Files

- `internal/entities/agent_repository.go` — claim statement gains
  `FOR UPDATE SKIP LOCKED` on Postgres only.
- `internal/db/db.go` — `IsPostgres`, so the repository can choose the clause
  without importing `db` (a transaction carries no dialect).
- `internal/sync/engine_test.go` — the attachment-delete test sends the bare-id
  payload the client actually sends; an empty payload is invalid `JSONB`.
- `internal/dbtest/dbtest.go` — schema cleanup tolerates a test that closed the
  pool itself (the readiness probe does, to prove its 503 path).

## Diagnosis

`ClaimRun` is one statement:

```sql
UPDATE agent_runs SET … WHERE id = (SELECT id … ORDER BY created_at, id LIMIT 1)
  AND status = 'queued'
```

Under Postgres READ COMMITTED all six statements evaluate the sub-select on the same
snapshot, pick the same oldest row, and queue behind its lock. The winner commits;
the others re-evaluate the outer `AND status = 'queued'` against the updated row,
fail it, and return `(nil, nil)` — "nothing queued" while five runs still are.
SQLite serialises writers, so each statement sees the previous claim and the bug is
invisible there. A Go retry loop is the wrong fix: it cannot distinguish "lost the
race" from "queue empty" without duplicating the entire membership/scoping predicate.

## Contract

- [x] On Postgres, six concurrent claims over six queued runs return six distinct
      runs, none nil (`TestClaimRunIsAtomicAcrossRunners`, `-count=20` on a
      throwaway `postgres:16`).
- [x] SQLite's statement is unchanged (no `FOR UPDATE` syntax, which it rejects);
      pinned by `TestClaimRunSQLUsesSkipLockedOnlyOnPostgres`.
- [x] A repository built on a transaction (no dialect) still claims using the
      portable statement — the assertion is optional.
- [x] The full suite passes on SQLite and on Postgres with **no** `-skip`.
- [x] `TestPushDeleteRemovesTheAttachmentBlob` passes on Postgres (delete payload
      is the bare JSON id, as `docs/API.md` specifies).
- [x] `TestReadyzReportsTheDatabaseState` passes on Postgres (cleanup skips the
      schema drop when the test closed the pool itself).

**Wire Constraint:** No response or request shape changes; `docs/API.md` and
`cmd/server/contract_test.go` are untouched.

## Non-goal

- No lease/reaper behaviour change and no new claim scheduling policy.
- No new agent-run test beyond the existing concurrency test; it already fails
  without the clause on Postgres.

## Validation

- **Iteration:** `go test ./internal/entities ./internal/sync ./cmd/server`.
- **Before handoff:** `go test -race ./internal/entities`, then the full suite on
  SQLite **and** with `TASKTRACKER_TEST_POSTGRES_URL` pointed at a throwaway
  Postgres, without `-skip`.
- **Stays pending:** production Postgres under real contention; the test is six
  goroutines on one host.

## Handoff

- **Done:** `claimRunSQL` appends `FOR UPDATE SKIP LOCKED` when the querier reports
  Postgres (`db.DB.IsPostgres`); `claimRunTemplate` holds the statement and
  `agentRunColumns` is still appended after `Sprintf`. `NewAgentRepository` call
  sites now pass the pool (`database`), not `database.DB`, because the embedded
  `*sql.DB` hides the dialect method. The two test-side Postgres defects are fixed
  (delete payload, schema cleanup). Verified: full `go test ./...` green on SQLite
  and on Postgres without `-skip`; `-race` on `internal/entities`.
- **Pending:** merge and push (not pushed).
- **Next step:** merge the branch; this spec is deleted in this same commit,
  because the durable rule below is already in the doc edit.
- **Gotchas:** passing `database.DB` silently restores the race — the dialect
  assertion fails and the portable statement runs. Any future claim call site must
  take the pool. Do not add a Go retry loop: it cannot tell "lost the race" from
  "queue empty" without duplicating the membership/scoping predicate. Keep the
  format verb (`%s`) out of `agentRunColumns`.

## Promote

- **To `docs/ARCHITECTURE.md`:** `### Agent execution` — the claim takes
  `FOR UPDATE SKIP LOCKED` on Postgres because contenders otherwise select the same
  oldest row and lose the claim rather than take the next one; SQLite needs no
  clause because writers serialise.
