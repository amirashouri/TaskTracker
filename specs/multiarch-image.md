# multiarch-image — publish the server image for amd64 and arm64

- **status:** in-progress
- **priority:** P1
- **context:** `v0.1.10` published `ghcr.io/amirashouri/tasktracker-server`
  for linux/amd64 only. Docker Desktop on Apple Silicon, Windows on arm64, and
  arm64 Linux servers have nothing to pull, so `tt install` fails at `pull`.

## Scope

`.github/workflows/release.yml` and the release/install docs. The Dockerfile
stays as it is unless a platform proves it cannot build. No Go code change: the
image reference is already `ImageRef`, and Docker selects the host platform from
a manifest list without `tt` passing anything.

## Files

- `.github/workflows/release.yml` — `setup-qemu` (arm64), then buildx with
  `platforms: linux/amd64,linux/arm64` and GHA layer cache.
- `docs/RELEASE.md` — verify the manifest list; known gaps reduced to the
  amd64-only native tarball.
- `docs/INSTALL.md` — the architecture note now says the image covers both
  architectures; the native `server` is still amd64.
- `docs/ARCHITECTURE.md` — the install/exposure decision records the multi-arch
  image and the amd64 tarball as separate limits.

## Contract

- [ ] A `v*` tag publishes an OCI index for
      `ghcr.io/amirashouri/tasktracker-server:<tag>` with a linux/amd64 and a
      linux/arm64 manifest. **Proven for the local build; the next tag proves
      the published one.**
- [x] The Dockerfile builds unchanged for both platforms under a container
      buildx driver; the arm64 image starts on an arm64 host.
- [ ] A plain `docker pull` on this arm64 machine selects linux/arm64 (no
      `--platform`), which is what Docker Desktop and `tt` do. **Follows from
      the published index; not locally provable without a reachable registry.**
- [x] `docs/RELEASE.md` verifies the index and no longer lists amd64-only as an
      image gap; the native tarball limit stays documented.
- [x] No Go behavior changes, so the existing tests and `ImageRef` stay as they
      are.

## Validation

- `docker buildx build --platform linux/amd64,linux/arm64 --output type=oci`
  produced an index with `linux/amd64` and `linux/arm64` manifests (plus the
  buildx attestation manifests).
- `docker buildx build --platform linux/arm64 --load`, then a container with
  SQLite and `SERVER_BIND=0.0.0.0`: migrations ran and `/readyz` answered `200`
  natively on this arm64 host. The amd64 image starts under emulation and fails
  on the missing secret, as expected.
- `docker buildx imagetools inspect` after the first tag — the published index
  is the real proof, recorded as pending until then.
- Docs-only and workflow changes: `scripts/check.sh docs`; the tag cut runs the
  full gate.

## Handoff

- **Done:** QEMU (arm64) + buildx `platforms: linux/amd64,linux/arm64` and GHA
  layer cache in the image job; both platforms built locally into one OCI
  index; arm64 image ran and served `/readyz 200` with SQLite; amd64 image
  executed; RELEASE/INSTALL/ARCHITECTURE updated. No Go changes.
- **Pending:** the first tag after merge (`v0.1.11`) to prove the published
  index and the default host-platform pull.
- **Next step:** merge, cut `v0.1.11`, run `docker buildx imagetools inspect`
  and a plain `docker pull`, and confirm `linux/arm64` is selected here.
- **Gotchas:** QEMU is only needed on the amd64 runner; this Mac is arm64 and
  builds that side natively. A local `registry:2` cannot be reached from the
  buildkit container on `localhost:5001`, so default-selection stays a
  post-release check. The tarball's `server` and published `tt` remain
  linux/amd64. Buildx attestations show as `unknown/unknown` manifests in the
  index — normal, not a broken platform.

## Risks / notes

- Emulated arm64 compile is the slow part; GHA cache keeps repeat releases
  reasonable. If it proves too slow, cross-compiling CGO with a musl toolchain
  is the fallback, not a blocker.
- The tarball's `server` is still linux/amd64 (built by `make release-tt` on the
  runner); native arm64 installs remain out of scope.
