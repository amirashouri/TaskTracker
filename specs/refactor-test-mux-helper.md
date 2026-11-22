# refactor-test-mux-helper — one test composition root

- **status:** pending
- **priority:** P3
- **branch:** `refactor/test-mux-helper`
- **blocked by:** none
- **context:** Duplicate-code audit of 2026-11-22. `cmd/server/contract_test.go:166`
  and `cmd/server/main_test.go:189` each build the same 24-line handler graph
  (`admin`, `profile`, `me`, project, sync, agent, attachments) against a test
  database.

## Scope

- `cmd/server` test files only.

Production code must not change. The contract test's key-set assertions are
intentional duplication with the client DTOs and stay untouched.

## Files

- `cmd/server/main_test.go` or a new `cmd/server/testenv_test.go` — shared
  `newTestServer(t, db)` / `newTestMux(t, db)` helper.
- `cmd/server/contract_test.go`, `cmd/server/main_test.go` — call the helper.

## Contract

- [x] One helper (`mountTestMux` in `testenv_test.go`) builds the handler graph
      used by both tests; `newTestMux` keeps the existing call sites unchanged.
- [x] Each test still gets its own `*sql.DB` / temp store (`dbtest.NewDatabase`
      is still called per test), so tests remain independent and parallel-safe.
- [x] Rate-limit budget and JWT lifetimes stay caller-owned: contract keeps
      `New(100, time.Minute)` and 720h refresh, `newTestMux` keeps `New(5, …)`
      and 1h, and the attachment deadline stays `30s` in both.
- [x] `go test ./cmd/server` passes, including every contract key assertion;
      `go vet ./cmd/server` and `gofmt` are clean.

## Non-goal

- No production refactor, no route changes, no test deletion.
- No change to the key sets the contract test pins.

## Validation

- **Iteration:** `go test ./cmd/server`
- **Before handoff:** `go test ./cmd/server`
- **Stays pending:** nothing environment-specific.

## Handoff

- **Done:** `mountTestMux` in `cmd/server/testenv_test.go`;
  `newTestMux(t)` is a thin wrapper and `newContractEnv` calls the shared helper.
  Unused imports were removed from both test files. Verified:
  `go test ./cmd/server`, `go vet ./cmd/server`, `gofmt`.
- **Pending:** diff review and merge (branch not merged).
- **Next step:** review the diff against `main` and merge, then delete this spec
  in the final branch commit.
- **Gotchas:** do not move the limiter or JWT manager into the helper — the two
  tests deliberately use different budgets and lifetimes. The contract test's
  key-set assertions are intentional duplication with the client DTOs; leave
  them alone.

## Promote

- **To `docs/ARCHITECTURE.md`:** nothing — test plumbing.
