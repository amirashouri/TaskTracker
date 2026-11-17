# `tt update` — move a running server to a newer release

- **status:** in-progress
- **priority:** P1
- **context:** an operator who installed with `tt install` has no way to move
  the instance to a new release without uninstalling it. `tt install` is a
  repair by design — it keeps the recorded image tag/binary — so updating needs
  its own verb.

## Scope

`internal/install` (plan, runner, steps) and `cmd/tt` (new command, usage), plus
the operator docs that describe upgrades. No HTTP route, DTO, or client change,
so nothing crosses into the KMP repository.

## Files

- `internal/install/plan.go` — `ActionUpdate`, update steps and `nextUpdate`.
- `internal/install/runner.go` — execute the pull and fetch steps.
- `cmd/tt/update.go` — the `tt update` command.
- `cmd/tt/main.go` — dispatch and usage.
- `cmd/tt/update_test.go`, `internal/install/update_test.go`,
  `internal/install/runner_test.go` — the behaviour pins.
- `docs/INSTALL.md` — the Day 2 "Move to a new server version" section.
- `docs/SELF-HOSTING.md` — how a native operator updates.

## Contract

- [x] `tt update` on a Docker instance takes `--version <tag>`, pulls
  `tasktracker-server:<tag>` by reference, rewrites `install.json` and the
  compose file, brings the server up, and waits for `/readyz`. The recorded tag
  changes only after the new image is present. Pulling by reference is
  deliberate: the compose file is rewritten after the pull, so `compose pull`
  would fetch the tag the instance is already on.
- [x] `tt update` on a native instance takes `--server-binary-url <https url>`,
  downloads the binary over HTTPS into the path the unit supervises, restarts
  the unit, and waits for `/readyz`. The `.part`+rename download keeps the
  running process alive until the restart.
- [x] `tt never phones home`: "latest" is a tag or URL the operator names, never
  a URL derived from a version or a project endpoint.
- [x] `tt update` refuses: no installation, a Docker instance with no
  `--version`, `--server-binary-url` on Docker, a native instance with no URL,
  and a non-HTTPS URL.
- [x] `--dry-run` prints the plan and executes nothing.
- [x] Existing install/repair/logs/uninstall behaviour is unchanged.

## Validation

- `go test ./internal/install ./cmd/tt` — the development loop for both
  touched packages.
- `go test -race ./cmd/tt` — the command gains no goroutines, but the release
  gate's race sweep covers the CLI.
- A real Docker pull and a real systemd restart are environment behaviour and
  stay pending, not claimed.

## Handoff

- **Done:** `ActionUpdate` with Docker and native plans; `StepPullImage` and
  `StepFetchBinary` with runner support; `tt update` command, dispatch, usage;
  README-adjacent docs (`docs/INSTALL.md` §Day 2, `docs/SELF-HOSTING.md` §10).
  Focused validation: `go test -count=1 ./internal/install ./cmd/tt` green;
  `go build ./...` and `go vet ./cmd/tt ./internal/install` green.
- **Pending:** a real Docker pull and a real systemd restart, which are
  environment behaviour. A release-cut `scripts/check.sh race` also remains.
- **Next step:** cut a release, then exercise `tt update --version <tag>` on a
  Docker host and `tt update --server-binary-url <asset>` on a systemd host.
- **Gotchas:** `install` deliberately keeps the recorded image tag; do not make
  it pull. A Docker update rewrites the compose file from state, which only
  knows the loopback publish, so `tt expose` must be re-run on an exposed
  instance. `FetchBinary` only accepts HTTPS, and writing through `.part` is
  what keeps a running native process from executing a half-written file.

## Risks / notes

- Pulling before persisting the new tag keeps `status` honest when the pull
  fails; the compose file is still rewritten from the planned state only after
  a successful pull.
- A native binary can be replaced while the process runs because Linux keeps
  the open inode; the restart is what moves the process onto the new file.
