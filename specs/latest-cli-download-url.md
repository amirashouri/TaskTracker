# Stable CLI release download URL

- **status:** in-progress
- **priority:** P1
- **context:** Keep the documented CLI install command current across tagged releases.

## Scope

The GitHub release workflow and the CLI install commands in `README.md` and
`docs/INSTALL.md`. No runtime or HTTP contract changes.

## Files

- `.github/workflows/release.yml` — build and attach the Linux amd64 CLI bundle
  to each GitHub release.
- `README.md` — use GitHub's latest-release asset URL.
- `docs/INSTALL.md` — keep the full install guide consistent.

## Contract

- [x] Each tag-triggered GitHub release builds and uploads
      `tt-linux-amd64.tar.gz`.
- [x] The install commands use
      `releases/latest/download/tt-linux-amd64.tar.gz`, not a versioned URL.
- [x] Existing build and install behavior is otherwise unchanged.

## Validation

- Inspect the workflow and docs diff for the expected asset name and URL.
- Verify no stale versioned CLI download URLs remain in those docs.

## Handoff

- **Done:** Added Linux amd64 bundle generation and release upload to the tag
  workflow; updated both install guides to use the stable latest URL; corrected
  the docs to distinguish published release assets from other built binaries.
- **Pending:** Verify a real tagged release publishes the asset. The existing
  `v0.1.8` release currently has no assets, so its latest-download URL will not
  work until an asset is uploaded or a later release is created.
- **Next step:** On the next release, confirm
  `tt-linux-amd64.tar.gz` appears under GitHub release assets.
- **Gotchas:** The existing `v0.1.8` release will not gain an asset from this
  workflow change. The latest URL will work once a release created by the
  updated workflow publishes the asset.

## Risks / notes

- GitHub's `releases/latest/download/...` resolves against the latest
  non-prerelease release; that release must contain an asset with the exact
  requested filename.
