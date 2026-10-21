# attachment-blob-root — Make the attachment blob root writable in every deployment

- **status:** in-progress
- **priority:** P1
- **plan:** `none` (standalone bug fix).

## Scope

Server only. No wire-format or response change.

**The defect:** every `POST /v1/projects/{p}/work-items/{w}/attachments/{id}/blob`
returns `500`. The default `ATTACHMENT_ROOT=./data/blobstore` is relative to the
process working directory, and neither supported deployment lets the service
user write there:

- image: `WORKDIR /app`, owned by `root:root`, process runs as `app` — the
  writable volume is `/data`;
- systemd unit: `WorkingDirectory=/opt/tasktracker` (root-owned) under
  `ProtectSystem=strict`; the only writable path is the `StateDirectory`
  (`/var/lib/tasktracker`).

The handler converts the resulting `mkdir: permission denied` into a 500 and
logs nothing about the cause, so the client retries forever while showing
"Waiting to upload" — which is an honest caption for a blob the server never
stored.

## Files

- `Dockerfile` — set `ATTACHMENT_ROOT=/data/blobstore` for the image.
- `docker-compose.yml` — named `blob-data:/data` volume on the Postgres server
  service; SQLite already mounts `sqlite-data:/data`.
- `internal/install/embed/docker-compose.tmpl` — the same `/data` volume for a
  `tt install` in Postgres mode (`tasktracker-blob-data`).
- `internal/install/embed/tasktracker.service` and `deploy/tasktracker.service` —
  `Environment=ATTACHMENT_ROOT=/var/lib/tasktracker/blobstore`; the
  `EnvironmentFile` still wins, so an operator can override it. The two files
  must stay byte-identical (`TestEmbeddedSystemdUnitMatchesDeploy`).
- `internal/attachments/blobstore.go` — `EnsureRoot`, a startup create+writability
  probe.
- `cmd/server/main.go` — call `EnsureRoot` and fail at boot with the resolved
  path instead of 500-ing per upload.
- `internal/attachments/handler.go` — log the underlying error in both 500
  branches (the cause was invisible).
- `internal/attachments/blobstore_test.go` — tests for `EnsureRoot`.
- `internal/install/plan_test.go` — the rendered compose persists `/data` in
  both database modes.
- `.env.example`, `docs/SELF-HOSTING.md`, `README.md` — document the variable,
  its deployment values, and what sits in `/data`.
- `specs/attachment-blob-root.md` — this file.

## Contract

- [x] `EnsureRoot` creates a missing nested directory and returns `nil`.
- [x] `EnsureRoot` returns an error naming the root when the path cannot be a
      writable directory (the test uses a file as the parent, which fails for
      every user including root).
- [x] The rendered Postgres compose mounts `/data` for the server service and
      declares the volume; SQLite keeps its existing mount.
- [x] An upload that previously 500-ed returns 200, the blob lands under
      `/data/blobstore`, and `attachments.blob_ref` is set for the row.
- [x] The production change reverted makes the `EnsureRoot` failure test red.

## Validation

- `scripts/check.sh` — green (all packages, `go vet`, gofmt).
- Live: rebuilt the running Postgres profile. Both pending uploads returned
  `200` on the first retry cycle; `/data/blobstore` holds two 150,942-byte
  files; `attachments.blob_ref` is the attachment id on both rows; the client's
  Room rows gained `blobRef` and `uploadedAt`, so the caption is now `Synced`.

## Handoff

- **Done:** root cause proved (`mkdir /app/data: permission denied` as the
  service user), the image/unit/compose paths now give the server a writable,
  volume-backed root, startup fails fast when it is not, and the handler logs
  the cause of a storage or link failure. Full check green; live upload
  verified.
- **Pending:** nothing in code. Two follow-ups outside this task:
  1. The fix reaches customers with the next image release; `tt install` will
     re-render the compose with `tasktracker-blob-data` on its next run.
  2. Client-side: an open editor does not re-read attachments after a
     background sweep uploads them, so its caption can lag until reopened.
     That belongs in `TaskTrackerKMP`, not here.
- **Next step:** review and merge the branch; publish a new server image.
- **Gotchas:**
  - Do not change the config default: a bare `./server` run in a writable cwd is
    still a valid deployment, and the startup probe now names the path when it
    is not.
  - `deploy/tasktracker.service` must stay byte-identical to the embedded copy.
  - The Postgres `/data` mount is only for blobs; the database stays in
    `pg-data`. `/data` in the running container was empty before the switch, so
    the named volume lost nothing.
  - The client's "Waiting to upload" caption was accurate — the bytes had never
    been stored. Do not "fix" that caption to claim a sync the server did not
    confirm.
