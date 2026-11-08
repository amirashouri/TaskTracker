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

### One JSON convention

Every shape on this server is `camelCase`, including the auth, admin and user
objects. There is no second convention to remember.

This is not a preference either side gets to revise. The client adapts wire
names to Kotlin names with `@SerialName`, and its `ignoreUnknownKeys` is
deliberately **off** — so a field added to a response the client does not know
about is a decoding failure, not a value it can skip.

### Timestamps

All timestamps — synced entities (`project`, `workItem`, `sprint`, `comment`),
agent runs, and the user/auth/admin objects alike — are **ISO-8601 UTC at
whole-second resolution**:

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
{ "refreshToken": "<refresh-token>" }
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
  "accessToken": "eyJhbGciOi...",
  "refreshToken": "eyJhbGciOi...",
  "userId": "550e8400-e29b-41d4-a716-446655440000",
  "email": "jane@example.com",
  "displayName": "Jane Doe"
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
  "displayName": "Jane Doe",
  "position": "Designer",
  "avatarId": "",
  "status": "active",
  "role": "member",
  "lastSeen": "2023-11-14T22:13:20Z",
  "createdAt": "2023-11-03T08:26:40Z"
}
```

`passwordHash` is never present in any response. `lastSeen` is omitted when
null. Presence (`presence`, `presenceNote`) is deliberately **not** part of
this object; it belongs to the self-profile shape below, so adding it here would
break installed clients that decode this object strictly.

### `POST /v1/admin/users` — admin only

| Field | Required | Default |
| --- | --- | --- |
| `email` | yes | — |
| `password` | yes | — |
| `displayName` | no | `""` |
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
{ "displayName": "Jane Smith", "position": "Senior Designer", "role": "admin", "status": "inactive" }
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
`email` and `displayName`, limited to 50 results, newest first. An absent or
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
  "displayName": "Jane Doe",
  "position": "Product owner",
  "avatarId": "7",
  "presence": "away",
  "presenceNote": "At the dentist"
}
```

`presence` is always present and defaults to `"active"`. The empty-string fields
are omitted (`omitempty`); the client declares them with defaults. `email` and
`role` are read-only: changing an email needs a verification flow that does not
exist, and the role is admin-managed.

### `PATCH /v1/me`

```json
{ "displayName": "Jane Doe", "presence": "away", "presenceNote": "At the dentist" }
```

Every field is optional; only the keys present are applied. Email, role,
`status`, `password`, `lastSeen` and `createdAt` are unknown fields here and
are rejected (`DisallowUnknownFields`). An empty object is a `400`, so a client
that believes it saved something is told it did not.

| Field | Rule |
| --- | --- |
| `displayName` | trimmed; 1–120 characters; blank rejected |
| `position` | trimmed; 0–120 characters; empty clears |
| `avatarId` | `""` or exactly one ASCII digit `0`–`9` |
| `presence` | `active`, `away`, `be_right_back`, or `busy`; empty rejected |
| `presenceNote` | trimmed; 0–280 characters; empty clears |

`presence` and `presenceNote` are **not** `status`. `status` answers "may this
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
  "statuses": [
    { "id": "s-todo", "name": "To do", "category": "todo", "order": 0, "color": "gray" }
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

`statuses` is the project's admin-defined workflow catalog, the same shape and
the same rules. `category` is one of the fixed `WorkItemStatus` values
(`backlog`, `todo`, `in_progress`, `in_review`, `done`, `closed`) and is the
coarse axis the overdue, archive and terminal rules read, so renaming a status
or adding one never changes whether an item is overdue or finished. A work item
references one entry by `statusKey` and keeps its `category` in the existing
`status` field. Both keys are **omitted when empty**, so a project that has not
adopted a catalog adds no key to the response and an installed client that
predates the field still decodes every payload. A new project starts with no
catalog; it is written through the `PATCH` below, exactly as `tags` is.

`repoUrl` is the project's optional repository address. Create and `PATCH`
accept it (`http`, `https` or `ssh`; a password in the URL is refused), the
server stores it for the agent brief, and project responses emit it as
`repoUrl` **only when non-empty**. The KMP DTO declares the key with a default,
so a project without a repository and a response from an older server both
decode as "unset". An omitted `repoUrl` in a `PATCH` preserves the stored value
rather than clearing it; send an empty string to clear it. Only this admin
route may write it — a membership sync push cannot change it.

### `POST /v1/admin/projects` — admin only

Creates a project. The body is the create form's fields plus the id the client
minted — clients name their own project ids on the sync path too, so a response
lost in flight leaves a row the caller can name rather than a second project.
`agentEnabled` is optional and defaults to off; the create form offers it so a
project can be born ready for a paired agent:

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

### `DELETE /v1/admin/projects/{id}` — admin only

Removes the project and everything filed under it — work items, sprints and
comments go with it through the schema's cascade. The caller does not have to be
a member of it, which is why this is an admin route rather than a sync push.

The delete is applied through the sync engine, so the row and its change-feed
entry commit together: the project's members pull the deletion and retire it
locally, exactly as they would a delete one of their own clients pushed. The
login row's payload is the bare id, the shape a client's own delete carries.

| Status | Body |
| --- | --- |
| `204` | none |
| `401` | no bearer token |
| `404` | `{"error": "project not found"}` |

The response is deliberately empty: there is no project left to describe.

### `GET /v1/admin/projects/{id}/open-items` — admin only

How many of the project's work items are neither `done` nor `closed`. The admin
projects screen reads it before offering a removal, because a project with open
work is archived instead of deleted — the rule the member list already applies.

```json
{"openItemCount": 2}
```

A zero means the project has nothing to lose, so the offer is a delete. The
count is a route of its own rather than a field on the project object: the
project payload is the sync entity contract, and a response that gained a key
would fail the decode of every installed client.

`404` if the project is absent.

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

**Sprint windows.** Two unfinished sprints in one project may not share an instant.
Both endpoints are inclusive and match the client's scheduling rule. The server
checks this on push: a create or update that overlaps an unfinished sibling returns
`status: "rejected"` with `message` naming the blocker (e.g.
`"'Sprint 1' overlaps this window. Finish it before starting another."` or
`"'Sprint 1' overlaps this window. Finish it before saving this sprint."`). A
finished sprint never blocks, and editing a sprint without moving its window does
not block itself. The refusal is permanent, so re-sending the same payload will
not succeed; a device that was offline and pushed a colliding sprint keeps its
local row as a dead-lettered change. No response fields are added or removed by
this rule.

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

## Agent runs

A work item in progress can be handed to an LLM agent. The agent does **not** run
inside this server: it is a worker on the machine that holds the code
(`tt agent run`), so the server never calls a model and never stores a provider
key. It owns the queue, the machine credential, and the progress log.

This half of the API is `camelCase` with ISO-8601 whole-second timestamps, the
same convention the whole document now uses.

**Two credentials, never interchangeable.** The runner routes accept an opaque
`ttag_…` token issued by `POST /v1/agent/runners`; every other route in this
document accepts a user access token. A runner token on a user route is a `401`,
and a user token on a runner route is a `401`.

Agent state is **not** in the change feed. There is no `agentRun` entity, and no
existing response gained a field. A run is server-owned, time-sensitive state a
device cannot produce offline, so it is polled rather than synced.

A machine can be scoped to one project with a pairing code. The code is
short-lived, single-use and stored only as a hash; redeeming it mints the
long-lived runner token and assigns the machine to the code's project. A machine
registered through `POST /v1/agent/runners` has no project and keeps the older
machine-wide scope, so already-installed CLIs keep working.

The code is issued by a project member. The resulting machine is still scoped to
that member's runs (`requested_by`) as well as to the project, so pairing cannot
widen what a machine is allowed to execute.

### App routes — user token, scoped by project membership

| Route | Body / query | Result |
| --- | --- | --- |
| `POST /v1/agent/runs` | `{"workItemId": "…", "idempotencyKey": "…"}` | `201` new run, `200` replay of the same key. `404` unknown item, `403` non-member, `409` not `in_progress`, `409` a live run exists, `409` the project's agent switch is off |
| `GET /v1/agent/runs?workItemId=&limit=` | `limit` default 20, max 50 | `{"runs": [...]}`, newest first |
| `GET /v1/agent/runs/{id}?afterSeq=` | `afterSeq` default 0 | `{"run": …, "events": [...], "hasMore": bool}` |
| `POST /v1/agent/runs/{id}/cancel` | — | `{"run": …}` with `cancelRequested` |
| `POST /v1/agent/runners/pairing-codes` | `{"projectId": "…"}` | `201 {"code": "ttpc_…", "projectId": …, "expiresAt": …}`; member only, expires in 10 minutes |
| `POST /v1/agent/runners` | `{"name", "provider", "modelLabel"}` | `201 {"runner": …, "token": "ttag_…"}` — token shown once; legacy machine-wide scope |
| `GET /v1/agent/runners` | — | `{"runners": [...]}`, revoked and paused included |
| `DELETE /v1/agent/runners/{id}` | — | `204`, idempotent; `404` for an unknown or another user's runner |

`hasMore` is always present. The client declares it without a default, so a
missing value would silently end a progress page.

`idempotencyKey` is optional and opaque (at most 200 characters). The same
work item, caller and key returns the run the first request created with `200`,
so a retry after a lost response cannot create duplicate work. A replay returns
the stored run even when it is terminal; dispatching again after a failure needs
a new key. A different key while a run is live still answers `409`. The run id is
minted before the brief and stays stable across the replay.

### Fleet controls — admin token

The Settings screen's Agent block shows the caller's paired machines and, for an
administrator, stops or starts one. These two routes are `POST` under the admin
chain, because changing what the whole account's fleet will do is an
administrative act rather than a member's own.

| Route | Body | Result |
| --- | --- | --- |
| `POST /v1/agent/runners/{id}/stop` | — | `200 {"runner": …}` with `pausedAt` set; live runs held by that machine are flagged for cancel |
| `POST /v1/agent/runners/{id}/start` | — | `200 {"runner": …}` with `pausedAt` cleared |

A stop is **not** a revocation. The token stays valid, the row stays in the
caller's list, and a start restores the same machine without re-pairing. What a
stop actually does is refuse new claims: the claim query ignores a paused runner,
so a machine that is still running `tt agent run` simply receives no work. The
server cannot end a process on a customer's machine, and cancel remains
cooperative — the worker sees `cancelRequested` on its next heartbeat.

A revoked machine cannot be started: both routes answer `404` for a revoked or
unknown runner, so a stop cannot be used to probe for ids.

### Runner routes — opaque runner token

| Route | Body | Result |
| --- | --- | --- |
| `POST /v1/agent/pair` | `{"code", "name", "provider", "modelLabel"}` | `201 {"runner": …, "token": "ttag_…"}`; `401` for an unknown, spent or expired code. **No bearer token** — the code is the credential |
| `POST /v1/agent/runs/claim` | `{"waitSeconds": 25}` (default 25, max 30) | `200 {"run": …}` or `204` when idle |
| `POST /v1/agent/runs/{id}/events` | `{"events":[{"kind","message","payload"}]}`, max 200 | `200 {"accepted": n, "nextSeq": seq}` |
| `POST /v1/agent/runs/{id}/heartbeat` | — | `200 {"cancelRequested": bool, "leaseExpiresAt": …}` |
| `POST /v1/agent/runs/{id}/finish` | `{"outcome":"succeeded"\|"failed"\|"cancelled", "summary", "error"}` | `200 {"run": …}`, idempotent by status |
| `POST /v1/agent/runners/self/revoke` | — | `204`; retires the machine that presented the token, and only that one. This is what `tt agent disconnect` calls, because the CLI holds a runner token rather than a user JWT |

All four run routes answer `409` when the run is not held by that runner, so a
stale worker cannot report on work that was reassigned. If a worker stops
heartbeating, the server fails the run with `"runner lost"` and frees the item's
live slot.

A paired machine only claims runs in its assigned project. The filter sits
inside the queue's candidate selection, so an older run in another project
cannot hide this machine's own work. A legacy runner with no project keeps
claiming across its owner's projects.

### Agent brief

`taskDocument` on a claimed run is an opaque JSON string with a versioned
schema. It replaces the old flat snapshot; a new `schemaVersion` is additive and
the worker receives the stored document verbatim.

```json
{
  "schemaVersion": 1,
  "runId": "550e8400-e29b-41d4-a716-446655440000",
  "project": {
    "id": "22222222-2222-2222-2222-222222222222",
    "keyPrefix": "TT",
    "name": "Demo",
    "repoUrl": "https://github.com/acme/demo.git"
  },
  "task": {
    "id": "44444444-4444-4444-4444-444444444444",
    "key": "TT-12",
    "title": "Fix the failing sync test",
    "details": "…",
    "status": "in_progress",
    "priority": "high",
    "tags": ["Bug"]
  },
  "nodes": [
    { "id": "task:44444444-4444-4444-4444-444444444444", "type": "task", "key": "TT-12", "title": "Fix the failing sync test", "status": "in_progress" },
    { "id": "project:22222222-2222-2222-2222-222222222222", "type": "project", "key": "TT", "title": "Demo" }
  ],
  "edges": [
    { "from": "task:44444444-4444-4444-4444-444444444444", "to": "project:22222222-2222-2222-2222-222222222222", "type": "BELONGS_TO" }
  ],
  "limits": { "maxNodes": 64, "maxTags": 20, "maxDependencies": 10, "maxComments": 20, "maxCommentRunes": 4000, "maxTaskDetailsRunes": 20000 }
}
```

Node IDs are stable and namespaced (`task:<id>`, `project:<id>`, `tag:<id>`,
`comment:<id>`). Edges are directional: `task → prerequisite` is `DEPENDS_ON`,
`task → project` is `BELONGS_TO`, `task → tag` is `TAGGED_WITH` and
`task → comment` is `HAS_COMMENT`.

The graph is bounded and permission-filtered. It contains the run's project, its
task, direct prerequisites in the same project, the task's tags, and its most
recent comments — no other project's history, no attachments, and no repository
credential. The task's own `details` and each comment body are truncated at the
stated rune limits. `targetBranch` is omitted until project metadata carries a
branch; `repoUrl` names the repository but is not authorization for it. Private
access belongs to the agent's own credential store.

### Objects

Run, app shape:

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "workItemId": "44444444-4444-4444-4444-444444444444",
  "projectId": "22222222-2222-2222-2222-222222222222",
  "status": "queued",
  "requestedBy": "550e8400-e29b-41d4-a716-446655440000",
  "cancelRequested": false,
  "createdAt": "2026-11-03T12:00:00Z",
  "summary": "",
  "error": ""
}
```

`runnerId`, `startedAt` and `finishedAt` are omitted until they exist, and the
client declares them with defaults. `status` is `queued`, `claimed`, `running`,
`succeeded`, `failed` or `cancelled`. `cancelRequested` is a request, not a
result: the worker stops at its next heartbeat.

Event:

```json
{ "seq": 41, "kind": "status", "message": "Editing TT-12", "payload": "{}", "createdAt": "2026-11-03T12:00:09Z" }
```

`payload` is an opaque JSON **string**, so a new event kind never changes this key
set. The claim response additionally carries `taskDocument`, also an opaque JSON
string: the task snapshot taken at dispatch, which the server stores and
re-serves verbatim.

Runner, app shape:

```json
{
  "id": "66666666-6666-6666-6666-666666666666",
  "name": "dev-machine",
  "provider": "openai-compatible",
  "modelLabel": "llama3.1",
  "lastSeenAt": "2026-11-03T12:00:00Z",
  "pausedAt": "2026-11-03T12:05:00Z",
  "createdAt": "2026-11-03T11:00:00Z"
}
```

`lastSeenAt`, `revokedAt` and `pausedAt` are omitted until they exist, and the
client declares them with defaults. `pausedAt` is the reversible stop;
`revokedAt` is retirement. The client derives "running" from `lastSeenAt`
against its own read time (two minutes), because the server touches that column
on every claim long poll and does not answer freshness itself.

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
document to the code, including the agent run and event key sets. It compares **key names**, not just decoded values, because
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
