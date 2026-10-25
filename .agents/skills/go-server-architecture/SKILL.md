---
name: go-server-architecture
description: Use when adding or changing a package, an HTTP endpoint, wiring in cmd/server, or middleware in TaskTrackerServer. Covers this repository's single-binary monolith boundaries, the composition root, and handler/middleware conventions.
---

# Server architecture

`TaskTrackerServer` is one Go module, self-hosted as one binary against one
database. The architecture is a monolith with `internal/` package boundaries,
not a set of services. Read `docs/ARCHITECTURE.md` for design decisions, then
confirm current behavior in the code.

## Boundaries

| Package | Owns | Rule |
| --- | --- | --- |
| `cmd/server` | composition root | the only place concrete types are constructed |
| `internal/config` | environment configuration | no other package reads `os.Getenv` |
| `internal/db` | driver selection, migrations | the only place that opens a `*sql.DB` |
| `internal/entities` | models, repositories | the only place that writes SQL |
| `internal/auth` | JWT, sign-in/refresh | no SQL; goes through a repository |
| `internal/middleware` | Chain, Auth, Logging, CORS, Recover | transport concerns only |
| `internal/sync` | sync engine, push/pull handlers | routes opaque payloads |

A handler never reaches into the database. It calls a repository or the sync
engine. When you need a new capability, add a method to the owning package rather
than importing `database/sql` somewhere new.

## The composition root

`cmd/server/main.go` builds the object graph in one direction and stops:

```
config.Load → db.New → db.Migrate → repositories → sync engine → mux → middleware → ListenAndServe
```

- Construct a dependency once, here, and pass it down. Do not build a repository
  inside a handler or a global.
- Repository constructors take `*sql.DB`; `db.New` returns `*db.DB`, so pass
  `database.DB`, not `database`.
- Startup failures are fatal and explicit (`log.Fatalf` with the cause). Never
  continue with a half-built graph.

## Handlers and middleware

Routes are registered on a `http.ServeMux` with method patterns:

```go
mux.HandleFunc("POST /v1/auth/login", authHandler.SignIn)
mux.Handle("POST /v1/sync/push", middleware.Auth(jwtManager)(http.HandlerFunc(syncHandler.Push)))
```

Protected routes are wrapped per-route with `middleware.Auth`; cross-cutting
middleware (`Logging`, `CORS`, `Recover`) is applied once around the whole mux
with `middleware.Chain`.

Adding an endpoint:

1. Implement the handler in the owning `internal/` package. Read the request,
   validate input, call a repository or the engine, write JSON.
2. Register it on the mux in `main.go`, wrapping it with `middleware.Auth` if it
   needs a session.
3. Keep the URL under `/v1/`.
4. Add a test in the same package.

## Versioning and compatibility

The client is an installed mobile app that syncs offline; it cannot be upgraded
on your schedule. Do not change the shape of a response the client already reads
without a new `/v`-prefixed route.

## Before you finish

```bash
go test ./internal/<pkg>        # the development loop: the packages you touched
scripts/check.sh               # the release gate: gofmt, build, vet, tests
```

Focused package tests are what a task is verified by; `scripts/check.sh` runs
when a release is cut. Use `go test -race ./internal/<pkg>` for concurrent code
and `scripts/check.sh docs` for documentation-only changes. Do not delete code
or stub checks to make the tree green.
