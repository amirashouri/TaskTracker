# refactor-notification-payload-base — one base payload for generated notifications

- **status:** pending
- **priority:** P3
- **branch:** `refactor/notification-payload-base`
- **blocked by:** none
- **context:** Duplicate-code audit of 2026-11-22.
  `internal/sync/notifications.go:116 assignmentNotification` and `:152
  commentNotification` both read the project identity, warn identically, and
  build the same `NotificationPayload` base (work item id/key/title, project
  id/prefix/name, actor name). Only the kind, source and comment-specific fields
  differ.

## Scope

- `internal/sync/notifications.go` — one base-payload helper.

No change to when a notification is generated, its dedupe key, or its stored JSON.

## Files

- `internal/sync/notifications.go` — `notificationBase(ctx, q, item, actorID)`.
- `internal/sync/notifications_test.go` — payload contents for assignment and
  comment notifications.

## Contract

- [x] Both generators obtain the shared payload fields from one helper
      (`notificationBase`); each adds only its own fields (`CommentID`,
      `CommentPreview`) and its own `Kind`/`SourceKind`/`SourceID`.
- [x] A project-identity read failure still logs the same warning and yields no
      notification (`notificationBase` returns nil before marshaling).
- [x] The stored payload JSON keeps every key the client decodes, with the same
      omission rules (`omitempty` on comment fields); only the shared fields are
      assembled in the helper.
- [x] `go test ./internal/sync` passes; `go build ./...` and `gofmt` are clean.

## Non-goal

- No new notification kind; no change to `writeGeneratedNotification` or the
  change-feed row.
- No merge of the two generators — their triggers and source ids differ.

## Validation

- **Iteration:** `go test ./internal/sync`
- **Before handoff:** `go test ./internal/sync ./cmd/server`
- **Stays pending:** nothing environment-specific.

## Handoff

- **Done:** `notificationBase` added; both generators now add only their own
  fields. Verified: `go build ./...`, `go test ./internal/sync`, `gofmt`.
- **Pending:** diff review and merge (branch not merged).
- **Next step:** review the diff against `main` and merge, then delete this spec
  in the final branch commit.
- **Gotchas:** the comment fields must stay `omitempty` so an assignment payload
  does not gain `commentId`/`commentPreview`; `notificationBase` deliberately
  logs and returns nil rather than returning an error, because a notification is
  derived and must never fail the edit that caused it.

## Promote

- **To `docs/ARCHITECTURE.md`:** nothing — plumbing, not a rule.
