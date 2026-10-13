# HTTP API

The wire contract for `TaskTrackerServer`, as the `TaskTrackerKMP` client
actually calls it. Every shape below is checked against the client's own DTOs,
not just against the Go structs that produce it — the two drifted apart once
already, and three of the four mismatches found that way were invisible from the
server side alone.

For who may do what, how to bootstrap the first admin, and the token lifecycle,
see [`USER-MANAGEMENT.md`](USER-MANAGEMENT.md). For the design record, see
[`ARCHITECTURE.md`](ARCHITECTURE.md).

## Conventions

| | |
| --- | --- |
| Base path | `/v1` |
| Auth | `Authorization: Bearer <access-token>` on every route except `/healthz`, `/readyz` and the three `/v1/auth/*` routes |
| Content type | `application/json` |
| Request bodies | capped at 1 MiB; unknown fields are **rejected**, not ignored |
| Errors | `{"error": "..."}` with a 4xx or 5xx status |

### Two JSON conventions

The server has two, and which one applies depends on which half of the API you
are in:

| Area | Convention | Example |
| --- | --- | --- |
| Auth, admin, users | `snake_case` | `access_token`, `display_name` |
| Sync envelope and entity payloads | `camelCase` | `entityId`, `nextCursor`, `keyPrefix` |

This is not a preference either side gets to revise. The client adapts wire
names to Kotlin names with `@SerialName`, and its `ignoreUnknownKeys` is
deliberately **off** — so a field added to a response the client does not know
about is a decoding failure, not a value it can skip.

### Timestamps

Two formats, again split along the same line.

**Synced entity payloads** (`project`, `workItem`, `sprint`, `comment`) use
**ISO-8601 UTC at whole-second resolution**:

```
2026-09-26T20:13:32Z
```

Second resolution is the client's choice, documented in its `DateCoding`:
sub-second precision cannot be encoded and read back without loss, so a "which
row is newer" comparison against a lossy operand would be arbitrary. The server
truncates to whole seconds on the way in, so a value it stores is exactly a value
it hands back.

A bare epoch-millisecond number is also accepted on input, so a hand-rolled
`curl` does not have to know the format. A fractional second is truncated rather
than rejected. The server always **emits** the string form.

**The user object** in the auth and admin API uses `snake_case` epoch
milliseconds (`created_at`, `last_seen`), because that half of the API predates
the sync protocol and is a separate contract.

## Auth

### `POST /v1/auth/login`

```json
{ "email": "jane@example.com", "password": "her-password" }
```

Email is lower-cased and trimmed before lookup.

| Status | Body |
| --- | --- |
| `200` | [token pair](#token-pair) |
| `400` | `{"error": "email and password are required"}` |
| `401` | `{"error": "invalid credentials"}` |
| `429` | `{"error": "too many sign-in attempts"}`, with `Retry-After` in seconds |

Throttling is per address **and** per account, and is applied before the password
is verified so guessing is limited rather than merely slowed by bcrypt. A
successful sign-in clears the account's counter but not the address's.

### `POST /v1/auth/refresh`

```json
{ "refresh_token": "<refresh-token>" }
```

Rotates the token: the presented refresh token is revoked and a new one is issued
in the same family.

| Status | Body |
| --- | --- |
| `200` | [token pair](#token-pair) |
| `400` | `{"error": "refreshToken is required"}` |
| `401` | `{"error": "invalid refresh token"}` |

Presenting a token that was **already** rotated is treated as theft and revokes
the whole family. An access token presented here is rejected: the JWT carries a
`typ` claim that separates the two.

### `POST /v1/auth/signout`

Takes the same body as refresh. Revokes the token's whole family and returns
`204`.

Idempotent, and deliberately says nothing about whether the token was valid — a
sign-out that reported "no such token" would be an oracle. An access token stays
usable until it expires; that is what its short lifetime is for.

### Token pair

```json
{
  "access_token": "eyJhbGciOi...",
  "refresh_token": "eyJhbGciOi...",
  "user_id": "550e8400-e29b-41d4-a716-446655440000",
  "email": "jane@example.com",
  "display_name": "Jane Doe",
  "token_type": "Bearer",
  "expires_in": 3600
}
```

`expires_in` is the access token's lifetime in seconds.

## Users

### User object

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "email": "jane@example.com",
  "display_name": "Jane Doe",
  "position": "Designer",
  "avatar_id": "",
  "status": "active",
  "role": "member",
  "last_seen": 1700000000000,
  "created_at": 1699000000000
}
```

`password_hash` is never present in any response. `last_seen` is omitted when
null.

### `POST /v1/admin/users` — admin only

| Field | Required | Default |
| --- | --- | --- |
| `email` | yes | — |
| `password` | yes | — |
| `display_name` | no | `""` |
| `position` | no | `""` |
| `role` | no | `"member"` |

| Status | Body |
| --- | --- |
| `201` | user object |
| `400` | `{"error": "email and password are required"}` |
| `400` | `{"error": "role must be 'admin' or 'member'"}` |
| `409` | `{"error": "email already registered"}` |

### `GET /v1/admin/users` — admin only

`200` with an array of user objects, newest first.

### `PATCH /v1/admin/users/{id}` — admin only

```json
{ "display_name": "Jane Smith", "position": "Senior Designer", "role": "admin", "status": "inactive" }
```

Every field is optional; only the ones present are applied.

| Status | Body |
| --- | --- |
| `200` | updated user object |
| `400` | `{"error": "role must be 'admin' or 'member'"}` |
| `404` | `{"error": "user not found"}` |

### `DELETE /v1/admin/users/{id}` — admin only

Sets `status` to `"inactive"`; the row is not deleted, so `sync_log` and
`project_members` keep their referential integrity. A caller cannot deactivate
themselves.

| Status | Body |
| --- | --- |
| `204` | (empty) |
| `400` | `{"error": "cannot deactivate yourself"}` |
| `404` | `{"error": "user not found"}` |

### `GET /v1/users/search?q=` — any authenticated user

Case-insensitive substring match on `email` and `display_name`, limited to 50
results, newest first. `200` with an array of user objects. `400` when `q` is
missing or empty.

## Sync

The client is offline-first: it queues local changes in an outbox, pushes them
when it can, and pulls a cursor-paged change feed. Server-wins is the conflict
policy, and the client never computes a merge.

### Entity vocabulary

| `entity` | Payload |
| --- | --- |
| `project` | project object |
| `workItem` | work item object |
| `sprint` | sprint object |
| `comment` | comment object |

`operation` is `upsert` or `delete`. A delete's payload is the bare id, not a
document.

### `POST /v1/sync/push`

```json
{
  "changes": [
    {
      "id": "a-stable-client-generated-id",
      "entity": "workItem",
      "entityId": "44444444-4444-4444-4444-444444444444",
      "operation": "upsert",
      "payload": "{\"id\":\"44444444-...\",\"title\":\"Fix the thing\",\"createdAt\":\"2026-09-26T20:13:32Z\",...}",
      "version": "the-version-the-client-last-saw"
    }
  ]
}
```

`payload` is a **JSON string holding the document**, not the document itself.
The client's payload is an opaque byte array, and it encodes it with
`decodeToString` rather than base64 so the payload stays readable in a server
log. The server accepts the embedded-object form too, so the engine's own tests
and a hand-rolled `curl` both work.

`id` is generated by the client and stable across retries, which is what makes
this `POST` safe to retry. `version` is optional: omit it to overwrite
unconditionally, or send the version last read to get a conflict instead of
clobbering.

**Response** — one verdict per change, in the same order:

```json
{
  "results": [
    { "id": "a-stable-client-generated-id", "status": "accepted", "version": "the-version-now-stored" }
  ]
}
```

| `status` | Meaning | What the client does |
| --- | --- | --- |
| `accepted` | Applied. `version` is what the server now holds. | Drops the outbox row |
| `conflict` | The row moved since the client read it. `version` is what the server now holds. | Re-pulls the entity and replays its intent |
| `rejected` | Not permitted, or unparsable. `message` says why. | Dead-letters the row |
| `retry_later` | A transient failure. `message` says why. | Leaves the row queued |

`rejected` is the one to be careful with: it is terminal on the client, so a
server-side decoding problem turns into every change the app ever sent being
discarded rather than retried.

| Status | Body |
| --- | --- |
| `200` | `{"results": [...]}` |
| `400` | `{"error": "no changes to push"}` |
| `401` | `{"error": "unauthenticated"}` |
| `500` | `{"error": "sync failed"}` |

A change is only accepted if the caller is a member of the project it belongs to.
A project's own delete is the exception on the pull side — see below.

### `GET /v1/sync/changes/{scope}`

Query parameters:

| Name | Default | Notes |
| --- | --- | --- |
| `cursor` | — | Opaque, from the previous page's `nextCursor`. Omit for the first page |
| `limit` | `200` | Capped at `500` |

**Scopes:**

| Scope | Returns |
| --- | --- |
| `projects` | Project changes, plus every project delete, to every authenticated user |
| `workItems` | Work item changes — and the project changes that affect them, because renaming a project has to reach a client that never asked for projects |

The singular spellings `project` and `workItem` are still served as aliases.

**Response:**

```json
{
  "changes": [
    {
      "entity": "project",
      "entityId": "22222222-2222-2222-2222-222222222222",
      "operation": "upsert",
      "payload": "{\"id\":\"22222222-...\",\"name\":\"Renamed\",\"createdAt\":\"2026-09-26T20:13:32Z\",...}",
      "version": "the-version-now-stored"
    }
  ],
  "nextCursor": "418",
  "hasMore": false
}
```

| Field | Notes |
| --- | --- |
| `payload` | A JSON string, echoed verbatim from the change that created the log row |
| `nextCursor` | Omitted when the page is empty |
| `hasMore` | **Always present.** A missing value is a decoding error on the client, which will not guess `false` and silently end a pull with pages unread |
| `resyncRequired` | Omitted when false. See below |

The server does not re-encode the payload. It stores what it was given and
re-serves those bytes, so a change the client authored comes back exactly as it
was sent.

**`resyncRequired`** is set when the cursor points before the oldest log row
still retained — the change feed is pruned on a retention window, and a client
that has been away longer than that has missed changes it cannot be told about.
The client must drop its cursor and start the scope from the beginning. A
`resyncRequired` page is not a failure; the page itself is still valid.

| Status | Body |
| --- | --- |
| `200` | page |
| `400` | `{"error": "invalid limit"}` |
| `401` | `{"error": "unauthenticated"}` |
| `500` | `{"error": "sync failed"}` |

### A known gap

`push` accepts all four entities, and the engine applies all four. But only
`projects` and `workItems` have a pull scope, so **sprint and comment changes
are push-only**: the server stores them, and no pull returns them to any other
client. A comment that stays on the device that wrote it is worthless to every
other member, so this is a gap rather than a design.

## Compatibility

The client is an installed mobile app that syncs offline and cannot be updated on
the server's schedule. Two rules follow, and the second is stricter than it
sounds.

**A response may not gain a field.** The client leaves `ignoreUnknownKeys` off
deliberately — its own comment says so: a field renamed on the server has to fail
loudly at the boundary rather than silently decode as null on every device. The
consequence is symmetrical and easy to miss: a key the client's DTO does not
declare is a decoding failure, not a value it skips. So adding a response field
breaks every installed build just as surely as removing one.

**A response may not lose or rename a field** either, unless the client declares
it with a default. Kotlin treats a property with a default as optional and one
without as required, so `hasMore` must always be sent while `resyncRequired` and
`nextCursor` may be omitted.

The safe order for a new response field is therefore **client first**: ship a
client that declares it with a default, which works against both the old and the
new server, and only then start sending it.

A breaking change that cannot follow that order is a new `/v1` version and a
migration path. The full policy is in [`AGENTS.md`](../AGENTS.md).

`cmd/server/contract_test.go` holds the request and response pairs that pin this
document to the code. It compares **key names**, not just decoded values, because
Go matches JSON keys case-insensitively and a decode-based check cannot see a
rename like `entityId` → `entityID`.
