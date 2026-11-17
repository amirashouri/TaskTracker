# cli-release-assets — publish `tt` for every OS and say which one to download

- **status:** in-progress
- **priority:** P1
- **context:** the multi-arch image lets a Mac or Windows host *run* the server,
  but the release publishes one asset, the linux/amd64 `tt` bundle. Those users
  cannot get the CLI without Go, and the README only documents the Linux amd64
  command.

## Scope

`.github/workflows/release.yml`, `README.md`, `docs/INSTALL.md`,
`docs/RELEASE.md`, `docs/ARCHITECTURE.md`. `make release` already builds all six
`tt` binaries — the release job only has to attach them. No Go change: the CLI
already supports Docker on darwin/windows (`internal/install/docker.go`,
`preflight.go`) and keeps its state under `$HOME` off Linux (`state.go`).

## Files

- `.github/workflows/release.yml` — attach the six `dist/tt-*` binaries next to
  `dist/tt-linux-amd64.tar.gz`; the release body shows the per-platform command.
- `README.md` — quick start picks the asset per OS/arch instead of assuming
  Linux amd64.
- `docs/INSTALL.md` — Step 0 replaces "only the Linux/amd64 bundle is
  published" with the asset table and the native-runtime limit.
- `docs/RELEASE.md` — asset list, verify commands and known gaps.
- `docs/ARCHITECTURE.md` — the distribution decision names the platforms the
  assets cover.

## Contract

- [ ] A `v*` tag attaches `tt-linux-amd64.tar.gz` plus `tt-linux-amd64`,
      `tt-linux-arm64`, `tt-darwin-amd64`, `tt-darwin-arm64`,
      `tt-windows-amd64.exe`, `tt-windows-arm64.exe`. **The workflow lists all
      seven; the first tag after merge proves the upload.**
- [x] Each attached bare binary is the matching `GOOS`/`GOARCH` build and prints
      the tag from `tt version` — `make release` output checked with `file`, and
      `tt-darwin-arm64 version` printed `v0.0.0-test` on this arm64 Mac.
- [x] `README.md` and `docs/INSTALL.md` give one copy-paste command per platform
      through `releases/latest/download/<asset>`, and every named asset exists in
      the workflow's `files:` list.
- [x] The docs keep the one real limit: `--runtime native` is Linux + systemd +
      amd64 (the bundle's `server`), while Docker mode covers all six platforms
      through the multi-arch image.
- [x] No asset is renamed: `tt-linux-amd64.tar.gz` stays, because it is an
      install URL already published in the docs.

## Validation

- `make release VERSION=v0.0.0-test` builds all six binaries; run
  `dist/tt-darwin-arm64 version` on this arm64 Mac and `file` each one to
  confirm the target OS/arch.
- Compare `ls dist/tt-*` against the workflow's `files:` list — every entry must
  exist before `softprops/action-gh-release` runs.
- Workflow YAML parse; `scripts/check.sh docs` for a docs/workflow diff.
- The published assets are only provable by a tag; record that as pending.

## Handoff

- **Done:** the release job now attaches `tt-linux-amd64.tar.gz` plus the six
  bare `tt` binaries and its body carries the asset table. `README.md` and
  `docs/INSTALL.md` give one download+install block per platform through
  `releases/latest/download/<asset>`; `docs/RELEASE.md` verifies all seven
  redirects and the seven-asset job; `docs/ARCHITECTURE.md` names the assets as
  a documented contract. Verified locally: `make release VERSION=v0.0.0-test`
  built all six with the expected formats (`Mach-O` amd64/arm64, static `ELF`
  amd64/aarch64, `PE32+` amd64/aarch64), `tt-darwin-arm64 version` printed
  `v0.0.0-test`, the workflow's `files:` list matches the Makefile outputs, YAML
  parses, `scripts/check.sh docs` green.
- **Pending:** the published assets are only provable by a tag — the next tag
  must serve all seven `latest` URLs and the darwin binary must print it.
- **Next step:** merge, cut the next tag, and run the RELEASE.md verification
  block.
- **Gotchas:** asset names are install URLs, so a rename breaks a documented
  command; the Windows asset must keep `.exe`. `release-tt` depends on
  `release`, so the six binaries exist before `softprops/action-gh-release`
  runs. The bare binaries are unsigned, which is recorded as a known gap.

## Risks / notes

- `softprops/action-gh-release` fails if a named file is missing; `release-tt`
  depends on `release`, so the binaries exist by then, but the list and the
  Makefile `PLATFORMS` must stay in sync.
- `tt` on macOS/Windows runs Docker only; Docker Desktop is a manual install
  there and `tt install` prints the official URL instead of installing it.
- A bare Windows asset must keep the `.exe` suffix or Windows will not run it.
