# Sprint window overlap — enforce the client's scheduling rule on the push path

- **status:** done
- **priority:** P1
- **plan:** no `docs/PLAN.md` entry — standalone cross-repo fix. The client half is
  [`../../TaskTrackerKMP/specs/sprint-window-overlap.md`](../../TaskTrackerKMP/specs/sprint-window-overlap.md).

## Scope

- `internal/entities` — the window check and its typed refusal in the sprint
  repository.
- `internal/sync` — the verdict mapping for that refusal.
- `docs/API.md` — the push contract states the rule.
- No schema, DTO, or payload change.

## Files

- `internal/entities/sprint_repository.go` — `refusesOverlap` and
  `SprintWindowOverlapError`; called from `Create` and `Update`.
- `internal/sync/engine.go` — map the refusal to a permanent `rejected` verdict.
- `internal/entities/repository_test.go` — repository-level refusal, the
  finished-sibling exception, and the self-exclusion on update.
- `internal/sync/engine_test.go` — an overlapping second push is `rejected` and
  names the blocker.
- `docs/API.md` — one paragraph under the push response table.

## Contract

- [x] A sprint create whose window shares an instant with an **unfinished**
      sprint in the same project is refused, and the error names the blocker.
- [x] A finished sprint never blocks, and a finished sprint being written never
      is refused: history is not a timetable.
- [x] An update is checked against the merged window and excludes the row being
      updated, so editing a sprint without moving its window is not a conflict.
- [x] The push verdict for a refused sprint is `rejected` (terminal on the
      client), not `retry_later`, and `message` is the client store's sentence:
      `'<blocker>' overlaps this window. Finish it before starting another.` on a
      create, `…before saving this sprint.` on an update.
- [x] Adjacent windows are allowed: ending at `t` and starting at `t+1` does not
      overlap, while both endpoints are inclusive, matching the client's
      `sprintBlockingOverlap`.
- [x] No JSON key, column, or migration changes; the installed client decodes the
      same payloads it did before.

## Validation

- `go test ./internal/entities ./internal/sync` — the development loop for the
  two touched packages.
- `go test -run TestSprintWindow ./internal/entities` and
  `go test -run TestPushRefuses ./internal/sync` for the focused runs.
- SQLite is the default test driver; this change adds no SQL, so the Postgres run
  proves the same statements rather than a new schema. Record it as pending for
  the release gate if a Postgres URL is not available locally.
- Mutation check: comment out the `refusesOverlap` call in `Create` and confirm
  the new tests fail, then restore.

## Handoff

- **Done:** `Create` and `Update` refuse inclusive overlaps with unfinished
  siblings and return a typed error naming the blocker; finished sprints and the
  row being updated are excluded. The push engine maps the refusal to terminal
  `rejected` with the client store's create/update sentence. Repository and push
  tests cover overlap, adjacency, finished-sprint exceptions, self-exclusion,
  and the verdict message. `docs/API.md` documents the rule. Both touched package
  suites and focused tests pass; the mutation check confirms the create test
  fails when its guard is removed.
- **Pending:** the optional Postgres release-gate run; no Postgres test URL was
  configured locally. This change adds no SQL or schema changes.
- **Next step:** merge the reviewed task branch; no implementation work remains.
- **Gotchas:**
  - The rule lives in the repository, not `applySprint`, for the same reason the
    client's lives in its store: it is the only layer that sees the project's
    other sprints, and it runs inside the push transaction.
  - `rejected` is terminal on the client. This refusal must only fire for a
    window the project genuinely cannot hold — never for a finished sprint, and
    never for the row being updated against itself.
  - Two truly concurrent pushes for one project can still interleave under
    Postgres READ COMMITTED. The defect this fixes is the sequential offline
    create that lands after the first device's sprint is committed; that case is
    covered by the transaction-scoped read.

## Risks / notes

- The client's `LocalDataSyncSink` applies a pulled sprint without a check — by
  design. If this server ever accepted an overlap, every device would hold two
  open sprints and no layer would reconcile them.
- A `rejected` sprint push is dead-lettered on the creating device, which keeps
  its local row. Surfacing dead-letter reasons in the sprint UI is a separate
  sync-UI task; this spec only guarantees the reason is in the verdict.
