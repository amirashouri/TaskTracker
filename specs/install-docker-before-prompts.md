# install-docker-before-prompts — check Docker before the install wizard asks anything

- **status:** in-progress
- **priority:** P2
- **plan:** `none` — standalone follow-up to the T9.1 `tt` install wizard; no
  `docs/PLAN.md` entry.

## Scope

`cmd/tt`, `internal/install`, and the install sections of `docs/INSTALL.md`. No
request/response shape or wire-format change.

## Files

- `cmd/tt/install.go` — `freshInstall` validates the non-interactive admin flags
  and ensures Docker before the mode and data-directory prompts; `installWith`
  drops the prompter for a non-interactive run; both `EnsureDocker` calls pass
  the CLI's stdout as `PreflightOptions.Out`.
- `internal/install/preflight.go` — `PreflightOptions.Out` (nil means
  `os.Stderr`) and the Docker bootstrap reports through it; the fail-closed
  messages say "non-interactive" rather than "--yes was specified".
- `cmd/tt/install_test.go` — `TestInstallEnsuresDockerBeforeAskingTheWizard`,
  `TestInstallNonInteractiveDoesNotPromptForDocker`; the prompt-interruption
  test stubs `ensureDocker` so it no longer inspects the host it runs on.
- `internal/install/docker_test.go` —
  `TestEnsureDockerWritesToTheConfiguredWriter`.
- `docs/INSTALL.md` — Step 1 transcript and "What just happened" now match what
  the wizard prints.

## Contract

- [x] `tt install` reaches `EnsureDocker` before the first prompt.
- [x] Docker stays required in both database modes and is still ensured before
      `Preflight` and before any install step runs.
- [x] The ordering test fails against the old order with
      `events = [Database [sqlite/postgres] Data directory docker]`.
- [x] `repairInstall` is unchanged: it was already Docker-first.
- [x] A `--yes` invocation missing its admin flags still fails before any Docker
      consent prompt.
- [x] A non-interactive run never consults the prompter: with Docker missing it
      fails with "not allowed" and runs no install command.
- [x] The Docker bootstrap writes its plan and progress to the caller's writer,
      not `os.Stderr`.
- [x] `docs/INSTALL.md` Step 1 lists only the prompts that exist, checks Docker
      first, and drops the preflight checklist the CLI never printed.

## Validation

- `gofmt -l cmd/tt internal/install`, `go vet ./cmd/tt ./internal/install` — clean.
- `go test -count=1 ./cmd/tt` — green.
- `go test -count=1 ./internal/install` — green.
- Each new test was run against the removed fix and failed for the expected
  reason (the `--yes` case ran a Docker install unattended; the writer case
  produced no output on the configured writer).
- `scripts/check.sh` not run; it is the release gate.

## Handoff

- **Done:** ordering, the non-interactive prompter invariant, the bootstrap's
  output routing, and the Step 1 transcript are all in place, with focused tests
  passing and each test confirmed to fail without its fix.
- **Pending:** commit and merge were not requested; the spec stays in-progress
  until the task branch is committed and reviewed.
- **Next step:** commit on a `feat/…` branch, then promote the durable lines
  ("Docker is ensured before the prompts because the server runs in a container
  in both modes", "a nil prompter is the only fail-closed signal the bootstrap
  has") into `docs/ARCHITECTURE.md` and delete this spec.
- **Gotchas:** `EnsureDocker` reads only `PreflightOptions.Platform` and `Out`.
  `docs/INSTALL.md` Step 0 still claims `tt --help` lists `upgrade`, which the
  usage text does not.

## Risks / notes

- Moving the check earlier means a Docker-install consent prompt now precedes
  the wizard's questions. That is intended: failing first is the point.
- `TestWizardInterruptedAtAPromptExitsWith130` must keep a stubbed
  `ensureDocker`; with the real one it depends on whether the test host has a
  reachable Docker daemon.
