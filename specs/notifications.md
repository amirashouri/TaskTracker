# notifications — server-generated assignment and comment notifications

- **status:** in-progress
- **priority:** P1
- **branch:** `feat/notifications`
- **blocked by:** none
- **context:** Requested 2026-11-21: notify a member when they are assigned a work item or
  when somebody comments on a work item assigned to them, with an inbox, read/delete state
  and in-app popups. This is a **cross-repo wire change** — read `../AGENTS.md` §1/§2 before
  starting; it needs a Phase 1 `ACK`, and **the server lands first**. Companion client spec:
  `../TaskTrackerKMP/specs/notifications.md`. The client already carries a dormant
  `Notification` model, Room table and repository stub; this spec is what makes that model
  real and is the fixed point for its key set.

## Scope

- `internal/entities` — the notification value, its wire JSON, and the repository that owns
  the table's SQL.
- `internal/db/migrations` — new version `0007` creates `notifications` on both dialects.
- `internal/sync` — generate the two notification kinds inside the same transaction as the
  write that caused them; accept `notification` pushes for read/dismiss; serve a
  recipient-scoped `notifications` pull scope.
- `cmd/server` — route registration and the executable contract test.
- `docs/API.md` — entity vocabulary, the new scope, the push rules and the object shapes.

No existing entity's payload gains, loses or renames a key, and no existing scope changes.
A new entity and a new scope are additive: an installed client never requests `notifications`
and never decodes its payload. It must not reach auth, the admin project routes, the sync
queue, or the project/work-item/comment/attachment repositories beyond the two writes this
feature reads.

## Files

- `internal/entities/models.go` — `Notification`, `NotificationKind` constants,
  `NotificationFromJSON`, `ToJSON`.
- `internal/entities/notification_repository.go` — new repository: insert-if-absent,
  get by id, set read, dismiss, list by recipient (SQL only here).
- `internal/entities/notification_repository_test.go` — repository behaviour, including the
  unique source index and the read/dismiss ownership checks.
- `internal/db/migrations/0007_notifications.sqlite.sql` — `IF NOT EXISTS` DDL.
- `internal/db/migrations/0007_notifications.postgres.sql` — the same version, Postgres DDL.
- `internal/sync/notifications.go` — notification drafts, generation for an assignment and a
  comment, and the recipient-scoped pull helper. A new file rather than growth in
  `engine.go`.
- `internal/sync/engine.go` — entity allow-list, `ScopeNotifications`, the recipient-scoped
  branch of `Pull`/`oldestCursor`, the `notification` push branch, and the two generation
  call sites.
- `internal/sync/engine_test.go` — generation, recipient isolation, read-only push, and the
  rejection paths.
- `internal/sync/handler.go` — `PullNotification`.
- `cmd/server/main.go` — mount `GET /v1/sync/changes/notifications` (and the singular alias,
  matching the other scopes).
- `cmd/server/contract_test.go` — pin the notification object's key set in a pulled payload,
  prove the pull is recipient-scoped, and re-pin the existing scope key sets.
- `docs/API.md` — the additions below.

## Contract

- [ ] `notifications` exists on **both** SQLite and Postgres from migration `0007`, with
      `UNIQUE (recipient_id, source_kind, source_id)`, an index on `(recipient_id, created_at)`,
      and a soft-dismiss column. `go test ./internal/db` passes on both dialects.
- [ ] Creating a work item with an assignee, or changing an item's assignee to a different
      non-null member, writes one `task_assigned` notification for the new assignee.
      Re-saving with the same assignee writes none. A re-push of the same change cannot
      create a second row (the source index).
- [ ] Creating a comment on an item that has an assignee writes one `comment_added`
      notification for that assignee. Editing or deleting a comment writes none. A comment
      the assignee authored themselves notifies nobody.
- [ ] Nobody is notified about their own action: the recipient never equals the pushing user.
- [ ] `GET /v1/sync/changes/notifications?cursor=&limit=` returns only rows whose
      `recipient_id` is the caller. It is **not** project-membership scoped; a member of the
      project who is not the recipient receives nothing. Its envelope keys are the existing
      `changes`, `nextCursor`, `hasMore`, `resyncRequired`, and `oldestCursor` is
      recipient-scoped too, so one account's pruning cannot force a resync for another.
- [ ] `POST /v1/sync/push` accepts `entity: "notification"`:
      - `upsert` applies **only** `isRead` to an existing row owned by the caller; the kind,
        payload, recipient and timestamps are never taken from the client. An upsert for an
        id the caller does not own, or for an id that does not exist, is `rejected`.
      - `delete` marks the caller's own row dismissed. A delete for a row already dismissed
        or pruned is `accepted`, so an offline delete is idempotent.
      - No other field of a notification is client-writable, and a push can never create a
        notification.
- [ ] Notification generation happens in the same transaction as the causing write. A
      failure to write the notification is logged and does **not** change the push verdict:
      a derived notification must never be the reason a user's edit fails.
- [ ] The pulled notification object carries exactly the client's declared keys:

      `id`, `userID`, `kind`, `payload`, `isRead`, `createdAt`, `updatedAt`

      `payload` is a JSON **string** (the client declares `payload: String`, as it does for a
      sync change). There is deliberately **no `version` key**: the client's DTO does not
      declare one, and a gained key is a decoding failure. Read state is last-write-wins.
- [ ] `kind` is `task_assigned` or `comment_added`, and the payload string carries what a
      card needs without a join:
      - `task_assigned`: `workItemId`, `workItemKey`, `taskTitle`, `projectId`,
        `projectKeyPrefix`, `projectName`, `actorName`.
      - `comment_added`: the same plus `commentId`, `commentPreview` (trimmed, at most 160
        characters), `actorName` (the comment's author).
- [ ] `cmd/server/contract_test.go` pins the notification key set by exact name on the pulled
      payload, and pins that no existing scope gained a key.
- [ ] Every behaviour above has a test that fails when its production change is reverted;
      the recipient-scope test is mutation-checked by pointing `ScopeNotifications` at the
      caller's projects.

### Migration

```sql
CREATE TABLE IF NOT EXISTS notifications (
    id TEXT PRIMARY KEY,
    recipient_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    payload TEXT NOT NULL,
    source_kind TEXT NOT NULL,
    source_id TEXT NOT NULL,
    is_read BOOLEAN NOT NULL DEFAULT FALSE,
    dismissed_at BIGINT,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    version TEXT NOT NULL,
    UNIQUE (recipient_id, source_kind, source_id)
);
CREATE INDEX IF NOT EXISTS idx_notifications_recipient
    ON notifications(recipient_id, created_at);
```

`source_kind` is `workItem` or `comment`; `source_id` is the work item id plus the new
version for an assignment, and the comment id for a comment.

## Non-goal

- No mentions, due-today or status-changed generation. Those kinds already exist in the
  client enum for future local rules; this task generates only the two requested events.
- No email, OS push (APNs/FCM), web push or SSE/websocket delivery. The popup reaches a
  running client through the existing pull cycle; OS-level delivery is a separate task.
- No per-project or per-kind mute/preferences, and no notification settings screen.
- No unread-count endpoint: the count is derived locally from the recipient's rows.
- No watchers/followers and no notifying the comment author or a thread's other
  participants — the recipient is the current assignee, exactly as requested.
- No server-side retention sweep yet. The client purges at 90 days; if the table needs a
  server sweep, that is a later task with its own spec.

## Validation

- **Iteration:** `go test ./internal/entities ./internal/sync` for each touched package.
- **Before handoff:** `go test ./cmd/server` (the contract test), and
  `TASKTRACKER_TEST_POSTGRES_URL=<throwaway> go test ./internal/entities ./internal/sync ./internal/db`
  — a schema change is proven only on both drivers.
- **Concurrency:** `go test -race ./internal/sync`; the engine is shared state and the new
  branch runs inside its transaction.
- **Mutation:** revert the generation call for each kind and confirm its test fails naming
  the missing notification; point the scope at the caller's projects and confirm the
  recipient-isolation test fails.
- **Stays pending:** a real deployment's notification volume and the OS-level delivery path.
- `scripts/check.sh all` only when a release is cut; not as this task's gate.

## Handoff

- **Done:** Planning only. Scope, wire shape, generation rules, recipient scoping and the
  read/dismiss semantics are agreed in this spec.
- **Pending:** Phase 1 `ACK`; implementation of migration `0007`, the repository, the engine
  branches, the route, the contract test and `docs/API.md`.
- **Next step:** after `ACK`, add migration `0007` on both dialects and the repository with
  its tests, then the two generation call sites in `engine.go`.
- **Gotchas:**
  - `applyWorkItem` must compare the **stored** assignee before the update, not the payload
    twice; the prior row is the only place the change is visible.
  - A notification names no project. `resolveProject` returns `""` and the membership check
    must be skipped, or every read/dismiss push is rejected as "change does not name a
    project".
  - The pull's normal predicate is project membership. `ScopeNotifications` needs its own
    predicate on `notifications.recipient_id`; reusing the project query would either leak
    every member's inbox or return nothing.
  - `oldestCursor` must filter by recipient too, or any account whose old rows were pruned
    makes every other account resync.
  - `sync_log.entity_id` is text; a notification id is a UUID, so no widening is needed.
  - Do not add `version` to the wire object even though the table has one; the client DTO
    would reject the key.
  - A pull replayed from an empty cursor delivers a create and then its dismiss; the client
    sink must be idempotent in both directions.

## Promote

- **To `docs/ARCHITECTURE.md`:** under "Decisions retained from completed task specs":
  notifications are server-generated, recipient-scoped rather than project-scoped, and the
  only client-writable notification fields are `isRead` and dismissal; generation is
  best-effort inside the causing transaction so a derived event can never fail a user's
  write.

## Handoff

### Done

- `0007_notifications` migration for SQLite and Postgres, unique on
  `(recipient_id, source_kind, source_id)` so a replayed source event is a no-op.
- `Notification` entity, wire JSON (`id`, `userID`, `kind`, `payload`, `isRead`,
  `createdAt`, `updatedAt`), payload shapes, and the notification repository.
- Assignment and comment generation inside the causing transaction; best-effort, no
  self-notification, no duplicate on an unchanged assignee.
- Recipient-scoped `notifications` pull scope; read/dismiss push accepted only for the
  recipient; a fabricated create is rejected.
- `cmd/server/contract_test.go` pins the object keys, the payload string, recipient
  isolation and the full-document read update. `docs/API.md` documents all of it.

### Pending / next

- Branch `feat/notifications`; pending review and merge.
- Postgres execution was not verified: `TASKTRACKER_TEST_POSTGRES_URL` is unset, so the
  repository test ran against SQLite only. Run it once with that URL before merging.
- No device/browser visual pass of the inbox and popup yet (client side).

### Gotchas

- Generated rows are logged as a **JSON string** holding the document, matching the
  client's `payload: String` change type. Logging the object makes every installed
  client fail to decode the feed.
- `version`, `sourceKind`, `sourceID` and `dismissedAt` are `json:"-"`. A new wire key is
  a contract break; `cmd/server`'s key-set test is the executable check.
- The only client writes are `{"isRead": true|false}` and a delete. There is no create.
- Assignment `source_id` is `workItem.ID:workItem.Version`, so a re-save with the same
  assignee does not notify again.
