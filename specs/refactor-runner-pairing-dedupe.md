# refactor-runner-pairing-dedupe — one runner-registration decode and one field validator

- **status:** pending
- **priority:** P2
- **branch:** `refactor/runner-pairing-dedupe`
- **blocked by:** none
- **context:** Duplicate-code audit of 2026-11-22.
  - `internal/runner/client.go:196 Register` and `:231 Pair` repeat the anonymous
    response struct, the decode error, the empty-id/token validation, and the
    `Registration` construction. Only the endpoint, credential and request body
    differ.
  - `internal/agent/handler.go:385` (`Pair`) and `internal/agent/app_handler.go:480`
    (`RegisterRunner`) repeat the trim + `maxRunnerNameLength` validation loop and
    its 400 message.

## Scope

- `internal/runner/client.go` — one `postRegistration` helper.
- `internal/agent/handler.go`, `internal/agent/app_handler.go` — one
  `validateRunnerFields` helper in the package.

No wire change: the request and response key sets of `/v1/agent/runners`,
`/v1/agent/pair` stay identical.

## Files

- `internal/runner/client.go` — `postRegistration(ctx, endpoint, token, payload)`.
- `internal/runner/client_test.go` — Register/Pair decode and error tests.
- `internal/agent/handler.go`, `internal/agent/app_handler.go` —
  `validateRunnerFields(w, name, provider, modelLabel) bool`.
- `internal/agent/handler_test.go`, `app_handler_test.go` — 400 boundary tests.

## Contract

- [x] `Register` and `Pair` share one decode/validate/construct path
      (`postRegistration`); the only per-call arguments are endpoint, bearer
      token, JSON body and the flow name used in errors.
- [x] A response missing the runner id or token still returns the same errors:
      `%s returned no runner id or token` with the flow name kept distinct
      ("registration" / "pairing"), so CLI output is unchanged.
- [x] Both agent routes still reject a name/provider/modelLabel over 120 runes
      with status 400 and the same message (`validateRunnerFields`).
- [x] `go test ./internal/runner ./internal/agent ./cmd/server` passes;
      `go build ./...` and `gofmt` are clean.

## Non-goal

- No change to runner tokens, hashing, lease handling, or the pairing-code flow.
- No merge of `runnerRunResponse` and `appRunResponse` — their key sets differ on
  purpose and are contract-tested.

## Validation

- **Iteration:** `go test ./internal/runner ./internal/agent`
- **Before handoff:** `go test ./internal/runner ./internal/agent ./cmd/server`
- **Stays pending:** a real `tt agent connect` against a live server.

## Handoff

- **Done:** `postRegistration` in `internal/runner/client.go`; both `Register`
  and `Pair` are wiring. `validateRunnerFields` in `internal/agent` serves both
  `Handler.Pair` and `AppHandler.RegisterRunner`; `handler.go` no longer imports
  `unicode/utf8`. Verified: `go build ./...`,
  `go test ./internal/runner ./internal/agent ./cmd/server`, `gofmt`.
- **Pending:** diff review and merge (branch not merged).
- **Next step:** review the diff against `main` and merge, then delete this spec
  in the final branch commit.
- **Gotchas:** keep the flow name in the error (`registration` vs `pairing`) —
  the CLI surfaces these directly and an existing message test relies on the
  server sentence, not this one, but changing it is user-visible. The response
  key set is shared with `cmd/server/contract_test.go`; do not touch it.

## Promote

- **To `docs/ARCHITECTURE.md`:** nothing — plumbing, not a rule.
