# refactor-handler-preambles — one path-parameter project lookup and one blob-route preamble

- **status:** pending
- **priority:** P2
- **branch:** `refactor/handler-preambles`
- **blocked by:** none
- **context:** Duplicate-code audit of 2026-11-22. Two handler packages repeat
  their request preamble:
  - `internal/admin/projects.go` repeats `id := r.PathValue("id")` → empty check →
    `GetByID` → nil check in `Get`, `OpenItems`, `Update`, `Delete`
    (`:71`, `:103`, `:282`, `:390`), and repeats the change-feed payload encoding
    in `Create` (`:216–227`) and `Update` (`:326–338`).
  - `internal/attachments/handler.go` repeats the per-request deadline block and
    authenticate/path-parameter block in `Upload` (`:59`) and `Download` (`:125`).

## Scope

- `internal/admin/projects.go` — path lookup and feed-payload helpers.
- `internal/attachments/handler.go` — blob-route preamble helper.

No change to response shapes, status codes, or messages. `httpx` may be reused
but not modified unless a helper belongs there for both packages; prefer local
unexported helpers.

## Files

- `internal/admin/projects.go` — `projectFromPath`, `feedPayload`.
- `internal/admin/handler_test.go` / `internal/admin/projects_test.go` — status
  behaviour must stay covered (existing tests may be enough). `to confirm`.
- `internal/attachments/handler.go` — `beginBlob` (or two small helpers).
- `internal/attachments/handler_test.go` — 401/400/404/413 behaviour unchanged.

## Contract

- [x] The four project handlers obtain the project through one helper
      (`projectFromPath`) that keeps the 400-missing-id, 500-load-failure and
      404-not-found responses and messages unchanged.
- [x] Create and Update build the change-feed payload through one helper
      (`feedPayload`); the double-marshal (object → JSON string) and `Version`
      stripping happen once.
- [x] Upload and Download share `extendDeadlines` and `blobPath`, keeping the
      401/400 responses and log messages (per-action verb) unchanged.
- [x] `go test ./internal/admin ./internal/attachments ./cmd/server` passes;
      `go build ./...` and `gofmt` are clean.

## Non-goal

- No change to the project response key set or the requirement that `payload` is
  a JSON *string* in the feed.
- No change to streaming, Range handling, or the 25 MiB cap.

## Validation

- **Iteration:** `go test ./internal/admin ./internal/attachments`
- **Before handoff:** `go test ./internal/admin ./internal/attachments ./cmd/server`
- **Stays pending:** a real large-file upload through a proxy with its own timeouts.

## Handoff

- **Done:** `projectFromPath` + `feedPayload` in `internal/admin`; `extendDeadlines`
  + `blobPath` in `internal/attachments`. Verified: `go build ./...`,
  `go test ./internal/admin ./internal/attachments ./cmd/server`, `gofmt`.
- **Pending:** diff review and merge (branch not merged).
- **Next step:** review the diff against `main` and merge, then delete this spec
  in the final branch commit.
- **Gotchas:** Update and Delete now derive `change.EntityID` from
  `existing.ID` instead of re-reading the path value; they are the same string,
  but do not reintroduce a second source of truth for the id. `projectFromPath`
  writes the error response itself — callers must return on `!ok`.

## Promote

- **To `docs/ARCHITECTURE.md`:** nothing — plumbing, not a rule.
