# refactor-command-exec-helpers — one captured-command helper per binary

- **status:** pending
- **priority:** P3
- **branch:** `refactor/command-exec-helpers`
- **blocked by:** none
- **context:** Duplicate-code audit of 2026-11-22.
  - `cmd/tt/expose.go:487 runCloudflareCommand` and
    `cmd/tt/tailscale.go:106 runTailscaleCommand` are identical except the binary
    name and error prefix.
  - `internal/install/checks.go:62 Compose`, `:84 Service` and
    `internal/install/preflight.go:35 DockerRoot` repeat
    `exec.CommandContext` + two `strings.Builder`s + fold-stderr-into-error.
  - `cmd/tt/install.go:183` (dry-run) and `:429` (real) each build the same
    nine-field `install.Command`.

## Scope

- `cmd/tt` — one command runner for cloudflared/tailscale; one
  `commandFromOptions`.
- `internal/install` — one capture helper used by the check and preflight
  adapters.

The two packages stay separate: do not export a helper from `cmd/tt` or import
`cmd/tt` from `internal/install`.

## Files

- `cmd/tt/expose.go`, `cmd/tt/tailscale.go`, new or existing shared file in
  `cmd/tt` — `runExternal(ctx, program, args...)`.
- `cmd/tt/expose_cloudflare_test.go`, `expose_tailscale_test.go` — error text
  still names the program.
- `cmd/tt/install.go` — `commandFromOptions`.
- `internal/install/checks.go`, `internal/install/preflight.go` — shared
  `runCapture`.
- `internal/install/checks_test.go`, `preflight_test.go` — output/error parity.

## Contract

- [x] Expose's command path and the Tailscale adapter use one runner
      (`runExternal` in `cmd/tt/run.go`); a failure still includes the program
      name, the joined args, and stderr (falling back to stdout) when present.
- [x] The install check/preflight adapters share `runCapture` with the same
      trimming and `%w: %s` error shape; `strict` preserves Compose/DockerRoot
      behaviour and `lenient` preserves `systemctl is-active` (non-zero exit with
      state on stdout is a result).
- [x] `composeArgs` behaviour and the `docker info` command strings are unchanged.
- [x] `tt install --dry-run` and the real path construct the same base command
      (`commandFromOptions`); the real path only adds the admin fields.
- [x] `go test ./cmd/tt ./internal/install` passes; `go build ./...` and `gofmt`
      are clean.

## Non-goal

- No new CLI flags or subcommands.
- No change to `exec.CommandContext` timeouts, working directories, or
  environment.

## Validation

- **Iteration:** `go test ./cmd/tt ./internal/install`
- **Before handoff:** `go test ./cmd/tt ./internal/install`
- **Stays pending:** real `cloudflared`/`tailscale`/`docker` invocations on a
  host that has them.

## Handoff

- **Done:** `cmd/tt/run.go` holds `runExternal` plus the two adapters;
  `internal/install` has `runCapture` serving Compose, Service and DockerRoot;
  `commandFromOptions` builds the install command for preview and reality.
  Verified: `go build ./...`, `go test ./cmd/tt ./internal/install`, `gofmt`.
- **Pending:** diff review and merge (branch not merged).
- **Next step:** review the diff against `main` and merge, then delete this spec
  in the final branch commit.
- **Gotchas:** `Service` is the lenient case — a non-zero `systemctl is-active`
  exit is a result when stdout names the state, so do not fold it into `strict`.
  The two packages intentionally keep separate helpers; do not export one across
  the `cmd/tt` boundary.

## Promote

- **To `docs/ARCHITECTURE.md`:** nothing — plumbing, not a rule.
