# tt-install-interrupt — make Ctrl+C leave `tt install`

- **status:** done
- **priority:** P0
- **plan:** none — standalone bug fix reported against the shipped CLI

## Scope

`cmd/tt` (wizard, prompts, signal-to-exit mapping) and the production runner in
`internal/install`. No wire format, no state schema, no Docker behaviour change:
the server container is started detached and is untouched by the CLI exiting.

## Files

- `cmd/tt/prompt.go` — the terminal prompter reads through a context-aware
  helper so a cancelled run returns instead of blocking on `ReadString`.
- `cmd/tt/install.go` — wires the run context into the prompter; every wizard
  failure reports through `fail`.
- `cmd/tt/main.go` — `fail` maps an interrupt to exit status 130 with no error
  line; `execute` uses it too, so `tt logs` also leaves quietly.
- `cmd/tt/prompt_test.go` — each prompt returns `errInterrupted` on cancellation.
- `cmd/tt/install_test.go` — an interrupted wizard exits 130, prints nothing,
  and runs no steps.
- `internal/install/runner_test.go` — the production command runner stops its
  child when the context is cancelled.

## Contract

- [x] Ctrl+C at any wizard prompt ends `tt install` with exit status 130 and no
      "tt install: ..." line.
- [x] Ctrl+C during a long step (`docker compose up`, `tt logs`) ends the CLI
      with exit status 130 and no error line.
- [x] The three new tests fail when the cancellation handling is removed and
      pass with it (verified by temporarily reverting each production change).
- [x] `scripts/check.sh` and `go test -race ./cmd/tt/... ./internal/install/...`
      are green.

## Validation

- Run: `go test ./cmd/tt/... ./internal/install/...` while iterating.
  `scripts/check.sh` (all packages) is the release gate.
- `go test -race ./cmd/tt/... ./internal/install/...` — the prompter adds a
  goroutine per read, so the race run belongs to this task, not to the release.
- Manual: built binary under a pty; Ctrl+C at `Database [sqlite/postgres]`,
  during a hanging `docker compose up` (fake docker), and during `tt logs` —
  each exits 130.

## Handoff

- **Done:** `signal.NotifyContext` in `cmd/tt/main.go` consumes SIGINT, so the
  process no longer dies on its own. The wizard's blocking read was the only
  place that never observed the cancellation, so Ctrl+C at a prompt did nothing.
  Prompts now read through `readLine`, which selects on the run context, and
  `fail` turns an interrupt into exit 130 without printing a failure. The
  uncommitted `exec.CommandContext(context.Background(), ...)` workaround in
  `internal/install/runner.go` was removed: it decoupled children from the run
  context without being the cause of the hang, and left `docker compose`
  processes alive after the CLI was signalled.
- **Pending:** nothing.
- **Next step:** none — merge the branch.
- **Gotchas:** `signal.NotifyContext` disables the default SIGINT behaviour for
  the whole process; any future blocking read (new prompt, new input source)
  must take the context or Ctrl+C silently stops working there. Exit 130 is
  deliberate; do not map interrupts to 1.

## Risks / notes

- The abandoned read goroutine is left to finish on its own; a terminal read
  cannot be cancelled portably. The process exits immediately afterwards.
- `fail` treats any error observed while the context is cancelled as an
  interrupt. That is correct for a CLI whose only cancellation source is a
  signal; re-check if a deadline is ever added.
