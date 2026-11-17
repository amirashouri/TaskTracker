# Releasing TaskTracker Server

One tag push is the whole release. `.github/workflows/release.yml` takes over
from there: it pushes the server image to ghcr, then builds `tt` for every
platform, generates the changelog from the commits since the previous tag, and
attaches the six `tt` binaries plus the Linux/amd64 `tt` + `server` bundle to a
GitHub Release. The image is what a Docker install pulls; the binaries are what
the install blocks in `README.md` and `docs/INSTALL.md` fetch through
`releases/latest/download/…`, so no asset name may change.

## What the tag decides

- **The version.** The tag is passed to `make release-tt VERSION=…` and baked in
  with `-ldflags -X main.version`; `tt version` prints it, and a Docker install
  pins `ghcr.io/amirashouri/tasktracker-server:<tag>`.
- **The changelog.** The release body is `git log <previous tag>..<tag>` with
  one bullet per commit subject, so write the subjects for a customer.
- **`releases/latest`.** Every `v*` tag becomes a normal (non-draft,
  non-prerelease) release, so testing the pipeline with a throwaway tag moves
  the download URL every install one-liner points at. Test the build with
  `make release VERSION=v0.0.0-test` instead of pushing a tag.

Versions are SemVer with a `v` prefix: patch for fixes, minor for additive and
backwards-compatible changes, major for a wire-contract break. A wire change
still follows the client-first order in the workspace `AGENTS.md` §2 — the tag
itself does not create compatibility.

## Before you tag

Tag the commit you validated, not whatever is checked out.

```bash
git switch main
git pull --ff-only
git status --short          # must print nothing

scripts/check.sh all
scripts/check.sh race       # the full race sweep, same as CI
```

This is the release gate from `AGENTS.md`: the focused per-package tests are the
development loop, but a tag is the one time the full sweep must pass. The tag
job builds and publishes; it runs no tests, so a red `main` would ship.

## Cut the release

```bash
git tag -a v0.1.11 -m "TaskTracker Server v0.1.11"
git push origin v0.1.11
```

That push is the trigger. Push the tag by name; `git push --tags` also publishes
every local tag you may have forgotten.

Annotated tags (`-a`) are preferred because they record who cut the release and
when. The workflow only requires the `v` prefix.

> **First release only:** the ghcr package starts private. `tt` pulls it
> anonymously, so open the package's settings on GitHub and set visibility to
> **Public**. Until then every Docker install fails with `denied`.

## Watch it

```bash
gh run list --workflow release.yml
gh run watch <run-id>
gh release view v0.1.11
```

Or open **Actions → release** in the repository. A successful run is two green
jobs: `image` pushes the tag to ghcr, then `release` attaches seven assets —
`tt-linux-amd64.tar.gz` and the six bare `tt` binaries (`linux`, `darwin`,
`windows` × `amd64`, `arm64`, with `.exe` on Windows). The image job runs first,
so a release page only exists once its image does. `release-tt` depends on
`release`, so every listed file exists before the upload starts; adding a
platform to `PLATFORMS` in the `Makefile` means adding it to the workflow's
`files:` list too.

`.gitlab-ci.yml` has a `release` job on any tag, not just `v*`. It builds the
artifacts for that pipeline's `dist/`, keeps them for a week, and publishes
nothing; the GitHub Release is what customers download.

## Verify the artifact

```bash
# Log out first: this must prove an anonymous pull, which is what `tt` does.
docker logout ghcr.io >/dev/null 2>&1 || true
docker pull ghcr.io/amirashouri/tasktracker-server:v0.1.11

# The tag must be an index carrying both platforms, not a single amd64 image.
docker buildx imagetools inspect ghcr.io/amirashouri/tasktracker-server:v0.1.11
```

On Apple Silicon the pull selects linux/arm64; `imagetools` lists amd64 and
arm64 under the moving tag.

Then the tarball, which proves the tag, the binary version, and the bundle
layout agree.

```bash
curl -fsSL https://github.com/amirashouri/TaskTracker/releases/download/v0.1.11/tt-linux-amd64.tar.gz \
  -o /tmp/tt.tar.gz
tar -tzf /tmp/tt.tar.gz            # tt-linux-amd64, server

mkdir -p /tmp/tt && tar -xzf /tmp/tt.tar.gz -C /tmp/tt
# The bundle is linux/amd64; from macOS run the static binary in a container.
docker run --rm --platform linux/amd64 -v /tmp/tt:/tt alpine:3.21 \
  /tt/tt-linux-amd64 version       # v0.1.11
```

And the moving URLs the docs use — every asset must redirect to the new tag, not
to a previous one:

```bash
for asset in tt-linux-amd64.tar.gz tt-linux-amd64 tt-linux-arm64 \
             tt-darwin-amd64 tt-darwin-arm64 \
             tt-windows-amd64.exe tt-windows-arm64.exe; do
  printf '%s -> ' "$asset"
  curl -fsSIL "https://github.com/amirashouri/TaskTracker/releases/latest/download/$asset" \
    | grep -i '^location:' | head -1
done
```

Then run the bare binary for the host you are on, which proves it is the right
architecture and carries the tag:

```bash
curl -fsSL -o /tmp/tt-darwin-arm64 \
  https://github.com/amirashouri/TaskTracker/releases/latest/download/tt-darwin-arm64
chmod +x /tmp/tt-darwin-arm64
/tmp/tt-darwin-arm64 version       # the tag you just pushed
```

Use the `darwin` asset on a Mac and the `linux` one on a Linux host. The Windows
assets cannot be executed from the verify shell; `file` proves they are PE
binaries and the redirect above proves the `.exe` suffix survived.

## When something goes wrong

| Situation | What to do |
| --- | --- |
| The job failed before the release existed | `gh run rerun <run-id>`. It rebuilds the same tagged commit, so the artifact still matches the tag. |
| The wrong commit or version was tagged | Do not delete and re-push a tag that has a published release; its download URL was public. Fix `main` and cut the next patch (`v0.1.12`), and say what happened in its notes. |
| The release exists with a wrong or missing asset | Do not move the tag; `latest` already points at it. Replace the file on the existing release with `gh release upload <tag> <file> --clobber`, then fix the workflow on `main` before the next tag. |
| A manual dispatch is tempting | Both jobs are guarded on a `refs/tags/v*` ref, so dispatching a branch does nothing. Dispatch the tag itself when you need to: `gh workflow run release.yml --ref v0.1.11`. Releases are normally cut by tag push. |

## Known gaps

- **No bare `server` asset.** The release publishes the image and the tarball,
  but `tt update --server-binary-url` wants a bare `server` file, not a tarball.
  A native instance updates by extracting the binary from the release tarball
  and hosting it at an HTTPS URL you control. The native install path is
  unaffected: the tarball itself carries `server` beside `tt`.
- **Native arm64.** The Docker image is published for linux/amd64 and
  linux/arm64, which covers Docker Desktop on Apple Silicon and Windows and
  arm64 Linux hosts. The bundle's `server` is linux/amd64 only and no arm64
  bundle is published, so `--runtime native` on arm64 needs a build from source.
- **Unsigned binaries.** `tt` is not code-signed or notarized. The documented
  `curl` download carries no quarantine flag, so it runs; a browser download on
  macOS can trip Gatekeeper ("unidentified developer"), and Windows SmartScreen
  may warn on the first run.

## After the release

- The client is a separate repo and its own tag; the release notes link to it at
  the same version. Do not tag the client from here.
- If the release carries a migration, say so in the notes. `tt update` applies
  it on the next start, and the safe advice is to back up first.
- A Docker instance upgrades with `tt update --version v0.1.11`, which pulls
  `ghcr.io/amirashouri/tasktracker-server:v0.1.11`.
