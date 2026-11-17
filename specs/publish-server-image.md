# publish-server-image — a tag publishes the Docker image `tt` pulls

- **status:** in-progress
- **priority:** P0
- **context:** `docs/RELEASE.md` found that a tag publishes only
  `tt-linux-amd64.tar.gz`; the release notes advertise a ghcr image and a Docker
  install pulls `tasktracker-server:<tag>`, which no job ever builds.

## Scope

`.github/workflows/release.yml`, `internal/install`, `cmd/tt`, and the install
and release docs. No HTTP/wire change, so nothing crosses into the KMP client.

## Files

- `internal/install/image.go` — the one constant for the published image path,
  and `ImageRef(tag)`.
- `internal/install/image_test.go` — pins the repository string, which is an
  external contract (where the workflow publishes and `tt` pulls).
- `internal/install/compose.go` and `internal/install/embed/docker-compose.tmpl`
  — the rendered compose names `ImageRef(state.ImageTag)`.
- `internal/install/plan.go` — a Docker update pulls `ImageRef(tag)`.
- `cmd/tt/main.go`, `cmd/tt/update.go` — status and update print the same ref.
- `.github/workflows/release.yml` — a tag-gated job that logs in to ghcr and
  pushes `ghcr.io/<owner>/tasktracker-server:<tag>` for linux/amd64, before the
  GitHub Release is created.
- `docs/RELEASE.md` — replace the Docker gap with the published path, the
  one-time public-package step, and the pull check.
- `docs/INSTALL.md` — the status transcripts print the ghcr ref.
- Tests: `internal/install/{plan,update}_test.go`, `cmd/tt/{install,main,update}_test.go`.

## Contract

- [ ] Pushing a `v*` tag publishes `ghcr.io/amirashouri/tasktracker-server:<tag>`
      for linux/amd64 from the tagged commit; a non-tag ref publishes no image.
      **Unverifiable locally — first tag push proves it.**
- [x] The rendered compose file and the update plan's pull step name the same
      ref, so a Docker install cannot plan one image and render another.
- [x] `tt status` and `tt update` print that ref, not a bare `tasktracker-server`.
- [x] One test pins the repository string; changing it is a deliberate edit.
- [x] `docs/RELEASE.md` no longer lists the Docker image as a gap; the native
      `server`-asset gap remains documented.
- [x] The GitHub Release is created only after the image push succeeds
      (`release` has `needs: image`).

## Validation

- `go test ./internal/install ./cmd/tt` — the development loop.
- `gofmt -l .` and `go vet ./internal/install ./cmd/tt`.
- The workflow itself is not runnable locally; the first real proof is a tag
  push. Record that as pending, do not claim it here.
- No concurrency was added, so `-race` is not needed for this diff; the tag cut
  runs `scripts/check.sh all` as usual.

## Handoff

- **Done:** `internal/install/image.go` + pinned test; compose template and plan
  use `ImageRef`; `tt status` / `tt update` print it; the tag-gated `image` job
  (ghcr login, buildx, linux/amd64 push) runs before the release job; docs and
  transcripts updated. `go test ./internal/install ./cmd/tt`, `go vet`, and
  `go build ./...` are green; the new pinned test fails when the constant is
  changed, and the compose tests fail when the template is reverted.
- **Pending:** the first tag push, and making the ghcr package public in its
  settings (once — `tt` pulls anonymously).
- **Next step:** merge, then cut the next patch tag and run the "Verify the
  artifact" commands in `docs/RELEASE.md`, including the anonymous
  `docker pull`.
- **Gotchas:** the image repository is deliberately not part of `install.json`
  — only the tag is stored, so an existing instance migrates by repairing
  (`tt install`), which re-renders the compose file. The ghcr path lives in the
  workflow and the Go constant; the test only guards the Go side, so a rename
  must edit both. Both jobs are guarded on `refs/tags/v*`, so a dispatch must
  name a tag ref (`gh workflow run release.yml --ref v0.1.10`) or it no-ops.

## Risks / notes

- Only linux/amd64 is built (the server binary has the same limit); arm64 hosts
  need emulation. Multi-arch is a separate task.
- The ghcr path is duplicated in the workflow by necessity; it is pinned by a
  test on the Go side and the spec names both places.
