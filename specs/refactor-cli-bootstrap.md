# refactor-cli-bootstrap — one database bootstrap for the server subcommands

- **status:** pending
- **priority:** P2
- **branch:** `refactor/cli-bootstrap`
- **blocked by:** none
- **context:** Duplicate-code audit of 2026-11-22. `createUserCmd`,
  `setUserPasswordCmd`, `listAdminsCmd` and `migrateCmd` in
  `cmd/server/main.go:426–584` each repeat `config.Load` → `db.New` →
  `defer Close` → `db.Migrate`, and the first two repeat the same
  "email and password are required" validation.

## Scope

- `cmd/server/main.go` — only the four one-shot subcommands and their helpers.
- `cmd/server/main_test.go` — adjust if a helper is tested directly.

Must not touch `runServer`, `mount`, the route table, or any handler package.

## Files

- `cmd/server/main.go` — add `openDatabase()` (config load, connect, migrate,
  cleanup) and `requireEmailPassword(fs)`.
- `cmd/server/main_test.go` — coverage for the validation helper if behaviour
  changes. `to confirm`.

## Contract

- [x] The four subcommands call one bootstrap helper
      (`openDatabase`); no subcommand repeats
      `config.Load`/`db.New`/`db.Migrate` inline.
- [x] Each subcommand still exits non-zero with a plain-text message on a config,
      connection, or migration failure (`cliFatal("%v", err)` keeps the same
      text; bootstrap errors are wrapped with the same prefixes).
- [x] `user-create` and `user-set-password` still reject a missing email or
      password with the same message and usage output (`requireEmailPassword`).
      No automated test: the helper calls `os.Exit`; verified by inspection.
- [x] `go test ./cmd/server` passes, including the contract suite; `go build ./...`
      and `gofmt -l cmd/server` are clean.

## Non-goal

- No subcommand is added, removed, or renamed; `main`'s dispatch is unchanged.
- No change to any HTTP route or response.

## Validation

- **Iteration:** `go test ./cmd/server`
- **Before handoff:** `go build ./...`, `go vet ./cmd/server`
- **Stays pending:** a real `./server user-create` against a live Postgres.

## Handoff

- **Done:** `openDatabase()` and `requireEmailPassword()` extracted; the four
  subcommands are wiring only. Verified: `go build ./...`,
  `go test ./cmd/server`, `gofmt -l cmd/server`.
- **Pending:** diff review and merge (branch not merged).
- **Next step:** review the diff against `main` and merge, then delete this spec
  in the final branch commit.
- **Gotchas:** `migrateCmd` does not need the user repository, so it discards the
  first return value and calls the cleanup directly. `requireEmailPassword` calls
  `os.Exit`, which is why it has no unit test; do not add one that forks the
  process just for this.

## Promote

- **To `docs/ARCHITECTURE.md`:** nothing — plumbing, not a rule.
