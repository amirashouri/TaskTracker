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
  "display_name": "Jane Doe"
}
```

The access token's lifetime is its JWT's own `exp` claim, and the scheme is always
`Bearer` — so neither is repeated in the envelope. This example is the client's
`AuthTokenResponse`, key for key; it carried `token_type` and `expires_in` until
those were found to break every sign-in, because the client rejects an undeclared
key rather than skipping it.

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
null. Presence (`presence`, `presence_note`) is deliberately **not** part of
this object; it belongs to the self-profile shape below, so adding it here would
break installed clients that decode this object strictly.

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

### `GET /v1/admin/users?status=` — admin only

`200` with an array of user objects, newest first, in one of two buckets:

| `status` | Returns |
| --- | --- |
| omitted, or `active` | every user that has not been deleted, including `inactive` ones |
| `deleted` | the soft-deleted users only |

`?"status=active"` is the whole non-deleted set. An `inactive` member can still
sign in and be managed; only `deleted` is gone. Any other value is a `400`, so a
typo is not read as an empty list.

| Status | Body |
| --- | --- |
| `200` | array of user objects |
| `400` | `{"error": "status must be 'active' or 'deleted'"}` |

### `PATCH /v1/admin/users/{id}` — admin only

```json
{ "display_name": "Jane Smith", "position": "Senior Designer", "role": "admin", "status": "inactive" }
```

Every field is optional; only the ones present are applied.

`status` may be set to `"active"` or `"inactive"`. It may **not** be set to
`"deleted"`: removing a member's access has to go through `DELETE`, which also
revokes their sessions. A status write that marked an account deleted would leave
it signed in. Reactivating a deleted member is an ordinary status write of
`"active"`.

| Status | Body |
| --- | --- |
| `200` | updated user object |
| `400` | `{"error": "role must be 'admin' or 'member'"}` |
| `400` | `{"error": "status must be 'active' or 'inactive'; DELETE the member to remove access"}` |
| `404` | `{"error": "user not found"}` |

### `DELETE /v1/admin/users/{id}` — admin only

Soft-deletes the member. The account is marked `"deleted"`, which refuses its
next sign-in and refresh, and every live refresh token it holds is revoked so its
open sessions stop rotating. The row is not removed, so `sync_log` and
`project_members` keep their referential integrity and a reactivate can find it.

Access tokens are stateless and are not revoked; one already issued stays usable
until its `exp` (at most an hour).

| Status | Body |
| --- | --- |
| `204` | (empty) |
| `400` | `{"error": "cannot delete yourself"}` |
| `404` | `{"error": "user not found"}` |
| `409` | `{"error": "cannot delete the last admin"}` |

The last-admin refusal counts admin rows rather than trusting the caller's own
role claim, because a claim can outlive the role it names.

### `GET /v1/users/search?q=` — any authenticated user

The collaborator directory. With a query, a case-insensitive substring match on
`email` and `display_name`, limited to 50 results, newest first. An absent or
blank `q` lists every non-deleted account, newest first — the member picker
opens with no text and needs somebody to offer before the user types. `200`
with an array of user objects.

A **non-empty** query deliberately does not exclude deleted members: a deleted
account is still the author of the work it left behind. The blank listing is the
live directory, because that is the set a new project can add as a collaborator.
The members list, which is about managing accounts, uses `?status=` and is the
admin-only view of both.

## Me — the signed-in user's own profile

Any authenticated user, member or admin. The caller is always the token's `uid`;
neither route accepts a user id, so one account cannot read or write another's
profile through them.

### `GET /v1/me`

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

`presence` is always present and defaults to `"active"`. The empty-string fields
are omitted (`omitempty`); the client declares them with defaults. `email` and
`role` are read-only: changing an email needs a verification flow that does not
exist, and the role is admin-managed.

### `PATCH /v1/me`

```json
{ "display_name": "Jane Doe", "presence": "away", "presence_note": "At the dentist" }
```

Every field is optional; only the keys present are applied. Email, role,
`status`, `password`, `last_seen` and `created_at` are unknown fields here and
are rejected (`DisallowUnknownFields`). An empty object is a `400`, so a client
that believes it saved something is told it did not.

| Field | Rule |
| --- | --- |
| `display_name` | trimmed; 1–120 characters; blank rejected |
| `position` | trimmed; 0–120 characters; empty clears |
| `avatar_id` | `""` or exactly one ASCII digit `0`–`9` |
| `presence` | `active`, `away`, `be_right_back`, or `busy`; empty rejected |
| `presence_note` | trimmed; 0–280 characters; empty clears |

`presence` and `presence_note` are **not** `status`. `status` answers "may this
account sign in?" (`active`/`inactive`/`deleted`) and is admin-owned; `presence`
answers "is this person available?" and belongs to the user. A presence write
cannot reactivate or inactivate an account, and an admin status write does not
change presence.

| Status | Body |
| --- | --- |
| `200` | the updated self-profile object |
| `400` | `{"error": "display name is required"}` |
| `400` | `{"error": "presence must be active, away, be_right_back or busy"}` |
| `400` | `{"error": "no fields to update"}` |
| `401` | `{"error": "authentication required"}` |

## Projects (admin)

Projects otherwise reach a client only through the sync feed, which is scoped to
membership. These routes are what let an admin see, create and edit every project
on the server, whether or not they are a member of it. All four are admin only.

The project object is the same shape the sync feed carries — the client's project
model decodes either:

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "keyPrefix": "TT",
  "name": "Demo",
  "details": "",
  "tags": [
    { "id": "660e8400-e29b-41d4-a716-446655440000", "name": "Bug", "color": "red", "icon": "bug" }
  ],
  "memberIds": ["550e8400-e29b-41d4-a716-446655440000"],
  "isArchived": false,
  "nextItemNumber": 1,
  "createdAt": "2026-09-26T20:13:32Z",
  "updatedAt": "2026-09-26T20:13:32Z",
  "version": "the-version-now-stored"
}
```

`tags` is the project's admin-defined tag catalog. `color` and `icon` are the
client's design-system keys; the server stores them verbatim and a client that
does not know one simply does not draw it. A work item carries only `tagIDs`,
and a tag removed from the catalog leaves stale ids on items, which every reader
drops when it cannot resolve them. The catalog travels with the project, so it
has no route or sync entity of its own.

### `POST /v1/admin/projects` — admin only

Creates a project. The body is the create form's fields plus the id the client
minted — clients name their own project ids on the sync path too, so a response
lost in flight leaves a row the caller can name rather than a second project:

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "keyPrefix": "TT",
  "name": "Demo",
  "details": "",
  "memberIds": ["550e8400-e29b-41d4-a716-446655440000"]
}
```

`id` may be omitted, and the server mints one. `isArchived`, `nextItemNumber`,
`createdAt`, `updatedAt` and `version` are the server's and are not read from the
body: a project being created is active, has issued no keys, and needs the
`version` its first edit saves against. A new project starts with an empty `tags`
catalog; tags are added through the `PATCH` below.

The create is applied through the sync engine as a project push authorized by the
admin route rather than by membership — an admin creating a project for other
people is not on its member list — so the row, its member set and its change-feed
entry commit together, and the members the body names pull the project exactly as
they would one of their own clients pushed.

| Status | Body |
| --- | --- |
| `201` | the stored project object |
| `400` | `{"error": "invalid request body"}` |
| `400` | `{"error": "name and keyPrefix are required"}` |
| `409` | `{"error": "project already exists"}` |

The `409` is deliberate rather than an upsert: a create that overwrote the row it
collided with would write its own `nextItemNumber` over a project that has issued
keys since, handing one number out to two work items.

### `GET /v1/admin/projects?status=` — admin only

`200` with an array of project objects in one of two buckets, the same two the
members list offers except that archiving is the project's own `isArchived` flag
rather than a deleted state:

| `status` | Returns |
| --- | --- |
| omitted, or `active` | projects with `isArchived` false |
| `archived` | projects with `isArchived` true |

| Status | Body |
| --- | --- |
| `200` | array of project objects |
| `400` | `{"error": "status must be 'active' or 'archived'"}` |

### `GET /v1/admin/projects/{id}` — admin only

One project, whether or not the caller is a member of it: the seed for the edit
screen, which then saves through the `PATCH` below. It answers with the same
object the create, the list and the `PATCH` return, so the client decodes all four
into one DTO.

| Status | Body |
| --- | --- |
| `200` | the project object |
| `404` | `{"error": "project not found"}` |

### `PATCH /v1/admin/projects/{id}` — admin only

The body is the whole project, as the client holds it; the edit screen is seeded
with the project and saves it back. `id` and `createdAt` are taken from the row,
not from the body. `version` is the version the caller read: it is the
compare-and-swap, and omitting it is last-write-wins, exactly as in a push.

The edit is applied through the sync engine, so the row and its change-feed entry
commit together, and the project's members pull the edit as they would one of
their own. The response is the stored project, carrying the new `version` the
client saves against next time.

`tags` is replaced wholesale, like every other field in the body: the edit
screen sends the catalog it holds. This is the only route that writes it, and
removing a tag does not rewrite the items that referenced it.

| Status | Body |
| --- | --- |
| `200` | the stored project object |
| `400` | `{"error": "invalid request body"}` |
| `404` | `{"error": "project not found"}` |
| `409` | `{"error": "the project changed since it was read"}` |

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

### The contract test can drift too

That file is a hand-transcription of the client's DTOs — which is what lets it
disagree with the server, and also what lets it disagree with the *client*. It
did. `clientTokenPair` carried `token_type` and `expires_in`, which the client has
never declared in any commit, so the test **required** the server to send two keys
the client rejects: both suites green, and no sign-in or refresh working.

So a passing contract test is not proof of agreement. It proves the server matches
the transcription, and the transcription is the part that has to be re-read
against `TaskTrackerKMP`'s DTOs when the two are suspected of diverging.
