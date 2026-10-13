# TaskTrackerServer

Self-hosted backend for the [`TaskTrackerKMP`](../TaskTrackerKMP) client.

One Go binary, one database. Companies run their own copy on their own server,
so the deployment target is a single process plus Postgres or SQLite — not a
cluster of services. See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for why
that shape was chosen and what still needs building.

## Status

The server compiles, boots, and serves requests: configuration, JWT auth,
middleware and the sync engine are all wired in.

Milestone 1 of `docs/PLAN.md` is done: the data layer is consistent, sign-in
works, and push/pull round-trips a project and a work item. The next work is
Milestone 2 (versioned migrations) onward.

Verified: `gofmt`, `go build ./...`, `go vet ./...` and `go test ./...` all pass,
and an end-to-end run signs in, pushes a project and a work item, pulls them back
and cascades a delete.

## Requirements

- **Go 1.27** — the version in `go.mod`.
- **CGO** — `mattn/go-sqlite3` is a cgo driver. Build with `CGO_ENABLED=1` and a
  C compiler available, or set `DATABASE_URL` to Postgres and drop the SQLite
  driver from `internal/db`.
- **Postgres 14+** for a shared deployment, or SQLite for a single-node one.

## Configuration and secrets

Configuration is read from the environment. For convenience the server seeds the
environment from a file at `ENV_FILE` (default `.env`) at startup.

`.env` is git-ignored on purpose: **no secret ever belongs in this repository.**
Only `.env.example`, which contains no real values, is committed.

### Local setup

```bash
cp .env.example .env
openssl rand -hex 32          # copy the output into JWT_SECRET
mkdir -p data                 # SQLite does not create the parent directory
```

The server refuses to start if `JWT_SECRET` is missing or shorter than 32 bytes.

### Adding a secret

1. Add the variable to `.env.example` with an empty or example value, so the
   next operator knows it exists.
2. Read it in `internal/config/config.go` via `getEnv` / `getDurationEnv` /
   `getCSVEnv`, and reject a bad value in `Config.validate`.
3. Set the real value in your `.env` (never commit it).

### Production

Real environment variables always win over `.env`, so a host can inject secrets
without any file on disk:

- **systemd** — `EnvironmentFile=/etc/tasktracker/secrets.env` (mode `0600`,
  owned by the service user).
- **Docker / Compose** — `env_file:` or Docker secrets.
- **GitLab CI/CD** — project *Settings → CI/CD → Variables*, marked *Masked* and
  *Protected*.

If you keep a `.env` file on a server, restrict it to `chmod 600`. Rotating
`JWT_SECRET` invalidates every issued token.

## Running

```bash
go run ./cmd/server
```

Then `GET /healthz` should return `ok`.

## Verifying

```bash
gofmt -l . && go build ./... && go vet ./... && go test ./...
```

All four pass on `main`. `internal/config` has tests for the `.env` loader and
secret validation; the other packages have none yet — see `docs/PLAN.md` →
Milestone 6.

## Database and migrations

Schema DDL currently lives inline in `internal/db/db.go`; the `migrations/`
directory is empty. The migrations are re-run as `CREATE ... IF NOT EXISTS` on
every start and are not versioned, so they cannot evolve an existing database.
`docs/ARCHITECTURE.md` covers what that costs and what to do instead.

## CI/CD

`.gitlab-ci.yml` runs three stages on every push: `lint` (gofmt + `go vet`),
`build`, and `test`. It is intentionally minimal — a gate, not a release
pipeline. Build and test are red until the scaffold compiles.

## Documentation

- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — architecture and code review.
- [`docs/PLAN.md`](docs/PLAN.md) — the ordered, task-by-task implementation plan.
- [`AGENTS.md`](AGENTS.md) — working agreement for this repository.
- [`.env.example`](.env.example) — every configuration variable.

## License

No license has been chosen — all rights reserved by default.
