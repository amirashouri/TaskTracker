# attachment-server-storage — Store WorkItem attachments on the server

- **status:** in-progress
- **priority:** P1
- **plan:** proposed (no `docs/PLAN.md` entry yet). Companion client spec:
  `../TaskTrackerKMP/specs/attachment-client-sync.md`. This is a **dual-repo wire
  change**: the new `attachment` sync entity and the `/v1/.../attachments/:id/blob`
  endpoints are shared contract, so they require the parent `AGENTS.md` §1 ACK before
  either side is implemented.

## Scope

Server-side only. The goal: a project member on any device can see (and download) the
attachments another member attached to a work item.

What changes:
- The sync engine gains a 5th entity, `attachment` — **metadata only, JSON, no bytes**.
- Binary blobs get a dedicated authenticated file API, scoped by project membership.
- A new `attachments` table (both dialects) plus blob storage on disk.

What does **not** change:
- The sync engine's contract ("opaque payloads, never invents domain rules"). Attachment
  *metadata* is just another entity with a JSON payload.
- The pull-scoping model (`sync_log.project_id` + membership). It is reused unchanged.
- `httpx.DecodeJSON` and the 1 MiB body cap stay as-is — blobs never touch that path.

## Files

- `internal/db/migrations/0005_attachments.sqlite.sql` + `0005_attachments.postgres.sql`
  — new `attachments` table (see Contract). Register in the migrator.
- `internal/entities/models.go` — add `OutboxEntityAttachment OutboxEntity = "attachment"`;
  add `Attachment` struct + `ToJSON()` + `AttachmentFromJSON()` (mirror `Comment`).
- `internal/entities/attachment_repository.go` (new) — `Create`, `GetByID`, `ByWorkItem`,
  `ByProject`, `Delete`, `SetBlobRef`. Mirror `comment_repository.go`: no DB-level FK
  cascade (the repository deletes child rows itself, because a cascade cannot write an
  outbox row).
- `internal/sync/engine.go` — three edits, all mechanical, mirroring `comment`:
  - `applyChange` switch: add `entities.OutboxEntityAttachment` → `applyAttachment`.
  - `resolveProject`: add the case. On upsert decode the payload's `workItemID` and return
    its project; on delete read the attachment's `work_item_id` first (like a comment reads
    its work item). This is what makes `canWrite` (membership) and `sync_log.project_id`
    work for attachments with **zero** new authz code.
  - `currentVersion`: add the `attachment` case.
  - `applyAttachment`: version compare-and-swap like the others. On **delete**, also remove
    the blob file from `AttachmentRoot` (best-effort; a failed file delete must not fail
    the metadata delete).
- `internal/sync/handler.go` — add `PullAttachment(w, r)` + route, mirroring `PullWorkItem`.
  The push path already handles any entity generically, so no push handler edit is needed.
- `internal/config/config.go` — add `AttachmentRoot string` (env `ATTACHMENT_ROOT`,
  default `./data/blobstore`), `AttachmentMaxBytes int64` (env `ATTACHMENT_MAX_BYTES`,
  default `25 << 20` to match the client `AttachFile.MAX_ATTACHMENT_BYTES`), and validate
  (root must be non-empty, max bytes > 0).
- `internal/attachments/handler.go` (new package) — the file API:
  - `POST /v1/projects/:projectId/work-items/:workItemId/attachments/:attachmentId/blob`
    uses `http.MaxBytesReader(r.Body, cfg.AttachmentMaxBytes)` (NOT `DecodeJSON`); checks
    membership via the existing `isMember`; writes `${AttachmentRoot}/${projectId}/${attachmentId}.blob`;
    on success calls `AttachmentRepository.SetBlobRef`.
  - `GET  .../blob` — streams the file if the caller is a member; supports `Range` (optional).
  - `413` over cap → friendly body; `403` for non-member; `404` for unknown attachment.
  - `DELETE` of the bytes is driven by the `attachment` delete change flowing through sync
    (`applyAttachment`), not a separate endpoint, to keep one deletion path.

## Contract

- [ ] A `POST` of an `attachment` change by a project member is `accepted`; a non-member
  gets `rejected` ("not a member of this project") — proven by an engine test calling
  `applyChange` with a non-member `userID`.
- [ ] `resolveProject` for an `attachment` delete returns the correct project even though
  the payload is a bare id (reads `work_item_id` first).
- [ ] A member's `Pull(attachment, ...)` returns only attachments whose `project_id` they
  belong to; a non-member of that project receives none (reuses the existing `sync_log`
  membership `WHERE` clause — assert via a pull test).
- [ ] Blob upload over `AttachmentMaxBytes` returns `413`; under it returns `200` and sets
  `blob_ref`; a non-member upload returns `403`.
- [ ] Blob download by a non-member returns `403`; by a member returns the bytes.
- [ ] Deleting an `attachment` (via sync) removes both the row and the on-disk blob; a
  failed blob delete does not fail the row delete.
- [ ] Migration `0005` applies on **both** sqlite and postgres and creates
  `attachments(id, work_item_id, project_id, file_name, mime_type, size_bytes, blob_ref,
  created_by, created_at, version)` with indexes on `work_item_id` and `project_id`.
- [ ] The `attachment` wire value is exactly `"attachment"` (matches the client enum), and
  the size cap equals the client's `25 MiB`.

## Validation

- `scripts/check.sh all` and `scripts/check.sh race` (the sync engine and the blob file
  writes are both concurrent paths). `go build ./...` + `go vet ./...` already clean; the
  existing `config`/`entities`/`sync`/`cmd/server` suites pass.
- New tests next to the code they cover (T12): `attachment_repository_test.go`,
  `engine_test.go` additions for `applyAttachment` (membership + conflict + delete-removes-blob),
  `internal/attachments/handler_test.go` (authz + 413 + download).
- Never green a check by deleting code or weakening an assertion.

## Handoff

Filled at the end of each session so work can resume in another agent or tool.

- **Done:** T1–T4 implemented. Migration `0005_attachments` (both dialects). `Attachment`
  entity + `attachment_repository.go`. Engine: `OutboxEntityAttachment`, `applyAttachment`,
  `resolveProject` (attachment case), `currentVersion` (attachment case), `PullAttachment`
  route. Config `AttachmentRoot`/`AttachmentMaxBytes` (+ validation). Blob file API
  (`internal/attachments`): `POST`/`GET .../attachments/:id/blob`, membership via
  `engine.IsMember`, size cap via `http.MaxBytesReader` → 413, download via `ServeContent`
  (Range-aware). `go build ./...` + `go vet ./...` clean; `config`, `entities`, `sync`,
  `cmd/server` tests green.
- **Pending:** T12 — attachment-specific tests (engine membership/conflict/delete-removes-blob;
  handler authz + 413 + download). Client side (spec `attachment-client-sync.md`, T5–T11).
- **Next step:** client data model (T5): `OutboxEntity.Attachment` + `AttachmentRecord`
  (+projectID, +blobRef) + Room migration.
- **Gotchas:** blob path formula is shared between `engine.removeAttachmentBlob` and
  `attachments.BlobStore.path` — flat `${root}/${id}.blob`; keep them in step. Membership for
  blobs reuses `engine.IsMember` (same query as the change feed). `SetBlobRef` deliberately does
  NOT bump version, so an upload does not rebroadcast the attachment. `NewEngine` now takes
  `(db, attachmentRoot)`; all call sites updated. The client half is the other side of the §1 ACK.

## Risks / notes

- The hardest thing to get wrong-and-ship is putting bytes in the sync payload. Do not.
- Blob storage root must be created on startup and live outside the DB file path.
- Server-side orphaned-blob cleanup on delete is best-effort; never let it block the
  metadata delete.
- Existing client-only attachments have no server row yet; the client spec covers the
  backfill (enqueue + upload on first sync). Server needs no special handling beyond
  accepting normal `attachment` upserts.
