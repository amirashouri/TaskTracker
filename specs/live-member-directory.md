# Live member directory — let an empty search list real accounts

- **status:** done
- **priority:** P1
- **plan:** no `docs/PLAN.md` entry — this is the server half of a cross-repo
  defect fix. The client's "new project → members" picker was showing a seeded
  fixture instead of the accounts the server already had. This file is the
  record on the server side.

## Scope

One handler and its tests, plus the API document. No schema change, no new
route, no change to the `userResponse` key set. The client already knows this
route; it was the empty-query refusal that kept it from being a directory.

## Files

- `internal/admin/handler.go` — `Search` treats a blank `q` as the live
  directory (`users.List`) instead of `400`; a non-empty `q` keeps
  `users.Search`.
- `internal/admin/handler_test.go` — `TestSearchWithNoQueryListsTheLiveDirectory`
  pins the `200`, the row, and the exclusion of a soft-deleted account.
- `docs/API.md` — the route's contract, including the deliberate difference
  between the blank listing and a typed query's treatment of deleted accounts.

## Contract

- [x] `GET /v1/users/search` with no `q` returns `200` and the non-deleted
      accounts, newest first. (`TestSearchWithNoQueryListsTheLiveDirectory`)
- [x] A soft-deleted account is absent from that blank listing. The same test
      creates a live and a deleted user and asserts only the live id comes back.
- [x] A whitespace-only `q` is trimmed to the same blank listing, because the
      handler trims before branching.
- [x] A non-empty `q` keeps the existing case-insensitive substring search,
      including its deliberate inclusion of deleted authors.
- [x] The response body still uses the existing `userResponse` shape; no field
      is added, removed or renamed. The client's `MemberResponse` decodes the
      same payload for both forms.
- [x] Reverting the handler to the `400` makes the new test fail at the status
      assertion.

## Validation

- Run: `go test` for the packages this task touches — `internal/admin` and
  `cmd/server`. `scripts/check.sh` is the release gate and sweeps every package.
- Counts: no host matrix and no baseline file on the server.
- The new test is behavioural, not a pixel test, so no `test-reviewer` pass is
  required.

## Handoff

- **Done:** the blank-query directory behaviour, the handler test, and the API
  documentation. `scripts/check.sh` is green.
- **Pending:** nothing on the server. The client half lives in
  `TaskTrackerKMP/specs/live-member-directory.md`.
- **Next step:** review and merge this branch. The client fallback already
  tolerates an older server that still answers `400` for blank `q`, but the
  picker only shows the live directory once this is deployed.
- **Gotchas:** the blank form uses `UserRepository.List`, which excludes
  `status = 'deleted'`; the non-empty form uses `Search`, which deliberately
  does not, so an old assignment can still name its author. Do not "fix" that
  asymmetry without reading the paragraph in `docs/API.md`: the picker's blank
  list is the addable set, the typed search is the historical directory. Do
  not add a new route or a new query parameter for this — the client is the
  fixed point and already calls `/v1/users/search`.

## Risks / notes

- The blank listing is not limited to 50 the way `Search` is. That is what lets
  the client fetch once and filter locally, and it is acceptable for this
  project's directory size; a deployment with thousands of accounts should add
  a dedicated capped directory method rather than reusing `List` blindly.
- The route was already available to any authenticated user and already
  searchable; blank `q` makes the same directory one request instead of a
  guessing game. It is a collaborator picker, not an admin account view, so it
  must not be confused with `GET /v1/admin/users`.
