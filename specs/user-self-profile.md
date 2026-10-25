# user-self-profile — Serve and update the signed-in user's own profile

- **status:** done
- **priority:** P2
- **plan:** none (standalone; no `docs/PLAN.md` entry). The client is the fixed point for the
  wire shape; this file is the server half.
- **ACK:** granted 2026-10-21; the five client-side decisions are accepted as recommended, so
  `presence`/`presence_note` are new columns and `users.status` stays lifecycle.

## Scope

`internal/db/migrations` (one new version, both dialects), `internal/entities` (the two
presence columns and a profile read/update that is separate from the admin `Update`),
a new `internal/profile` handler package, `cmd/server/main.go` (routes), `docs/API.md`, and
`cmd/server/contract_test.go` (the client's transcription).

The admin and directory user responses do **not** change. The new keys exist only in the new
`/v1/me` shape, so installed clients that decode `userResponse` strictly keep working.

## Files

- `internal/db/migrations/0006_user_presence.sqlite.sql` *(new)* — `ALTER TABLE users ADD
  COLUMN presence …` and `presence_note …`; no `IF NOT EXISTS` on SQLite (the runner records
  the version), matching `0003_user_roles`.
- `internal/db/migrations/0006_user_presence.postgres.sql` *(new)* — the same with
  `ADD COLUMN IF NOT EXISTS`.
- `internal/entities/models.go` — `Presence` and `PresenceNote` on `User`; document that
  `Status` remains the account lifecycle and these are presence.
- `internal/entities/user_repository.go` — a `profileColumns` projection (no password hash,
  no timestamps), `GetProfile(ctx, id)`, and `UpdateProfile(ctx, id, patch)` where a `nil`
  field is unchanged. Do **not** add the new columns to `Update`: the admin path and any
  last-seen write would otherwise clobber a concurrent presence change.
- `internal/profile/handler.go` *(new)* — `GET /v1/me` and `PATCH /v1/me`; the caller id comes
  from `auth.UserFrom(r.Context())`, never from the body or a path value.
- `internal/profile/handler_test.go` *(new)* — ownership, validation, and the empty-patch case.
- `cmd/server/main.go` — build the handler and mount both routes behind
  `middleware.Auth(jwtManager)`, alongside the existing protected routes.
- `docs/API.md` — a new `## Me` section with the request/response shapes and the validation
  table; state explicitly that `email`/`role` are not writable and that `presence` is not
  the account `status`.
- `cmd/server/contract_test.go` — transcribe the client's `UserProfileResponse` and PATCH body
  from the KMP spec, decode a real `GET`/`PATCH` response into them, and assert the admin
  `userResponse` key set did not move.

## Wire contract

`GET /v1/me` — any authenticated role; the caller's own row only.

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "email": "jane@example.com",
  "display_name": "Jane Doe",
  "position": "Product owner",
  "avatar_id": "7",
  "presence": "away",
  "presence_note": "At the dentist"
}
```

`PATCH /v1/me` — same auth. Every field optional; only present fields are applied; the
response is the `GET` shape. `email`, `role`, `status`, `password`, `last_seen` and
`created_at` are unknown fields and are rejected by `DisallowUnknownFields`.

```json
{ "display_name": "Jane Doe", "presence": "away", "presence_note": "At the dentist" }
```

Validation (all failures `400`, never `500`):

| field | rule |
| --- | --- |
| `display_name` | trimmed; 1–120 runes; blank rejected |
| `position` | trimmed; 0–120 runes; empty clears |
| `avatar_id` | `""` or exactly one ASCII digit `0`–`9` |
| `presence` | one of `active`, `away`, `be_right_back`, `busy`; empty rejected |
| `presence_note` | 0–280 runes; empty clears |
| body | no keys at all → `400 "no fields to update"` |

`presence`/`presence_note` default to `active`/`""` for existing rows, so an upgraded account
keeps the settings screen's current default.

## Contract

- [x] `GET /v1/me` returns the caller's row and never another user's, even when the token's
      `uid` exists alongside other accounts. `handler_test` signs in two users and asserts
      each sees only itself.
- [x] `GET /v1/me` omits the password hash, `last_seen` and `created_at`; the response decodes
      into the client's strict DTO. The contract test is the pin.
- [x] `PATCH` with one key changes only that key and returns the updated row;
      `presence_note` can be cleared with `""` but `presence` cannot.
- [x] `PATCH` with no keys, an unknown key, a bad presence, a two-digit avatar, or a 121-rune
      name is `400`; the row is unchanged afterwards.
- [x] `users.status` is untouched by both routes; an account with `inactive`/`deleted` is not
      revived by a profile write. The test reads the row back.
- [x] The migration applies on SQLite **and** Postgres, and `go test ./internal/db` accepts
      the paired version. A missing dialect file fails.
- [x] An existing row read before the migration has `presence = 'active'` and
      `presence_note = ''` afterwards.
- [x] The admin `GET /v1/admin/users` and `GET /v1/users/search` responses are unchanged.
      `contract_test.go` decodes them into the existing transcription and asserts the key set.
- [x] Both routes are `401` without a token and `200` for a member (no admin requirement).
- [x] Reading the id from a query parameter or the body instead of `auth.UserFrom` makes the
      two-user test fail: the test sends no id, so a handler that expects one answers `401`
      and the two accounts are no longer proven separate. Verified by mutation, then reverted.
- [x] The two `UPDATE` statements stay disjoint: `UserRepository.Update` (admin) names no
      presence column and `UpdateProfile` names no `status`, so neither route can erase the
      other's field. Reading both SET lists is the pin; the profile side also has
      `TestPatchLeavesAnInactiveAccountInactive`.

## Validation

- Run: `go test` on the touched packages while iterating; `scripts/check.sh` (full sweep)
  is the release gate. The new package is not concurrent, so `race` is not required.
- Postgres: run the suite with `TASKTRACKER_TEST_POSTGRES_URL` pointed at a throwaway server.
  A schema change is verified only once both dialects pass.
- Contract test runs under `cmd/server` and must fail if a key is renamed on either side.

## Handoff

- **Done:** the `0006_user_presence` migration (both dialects), `Presence`/`PresenceNote` on
  `User`, `GetProfile`/`UpdateProfile`, the `internal/profile` handler, both routes,
  `docs/API.md`, the handler tests, and the client-transcribed contract test.
  `scripts/check.sh` is green. A full Postgres run (`TASKTRACKER_TEST_POSTGRES_URL`) is green
  for `internal/db`, `internal/entities`, and `internal/profile`; `cmd/server` reports two
  **pre-existing** cleanup failures in `TestHealthz…`/`TestReadyz…` because those tests close
  the database and `dbtest` then drops the schema on a closed handle. That harness defect is
  not from this change and is not in scope here.
- **Pending:** nothing. The contract is checked, the suite is green and the branch is
  merge-ready; the merge itself is the only remaining repository action.
- **Next step:** merge this branch first, then the client branch. `/v1/me` is additive, so the
  client needs no migration and can ship before or after a server deploy.
- **Gotchas:** SQLite has no `ADD COLUMN IF NOT EXISTS`; the new columns are not the account
  `status`; `DisallowUnknownFields` makes any client drift a `400`, which is the intended
  early signal; `Update` keeps its old SET list so presence survives admin and last-seen
  writes; `GetProfile`/`UpdateProfile` are the only callers of the new columns.

## Risks / notes

- The client's `position` is an enum and the admin form is free text on the same column.
  Reading an unknown label as `null` is deliberate; the server must not normalize or reject
  legacy text written by an admin.
- A future presence display in the member directory would need `presence` on `userResponse`,
  which is a separate, client-first change; do not add it here.
- If `GET /v1/me` is later cached client-side across accounts, the response's `id` is the
  guard against showing one user's profile to another.
