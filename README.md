# TaskTrackerServer

Self-hosted backend for the [`TaskTrackerKMP`](../TaskTrackerKMP) client.

One Go binary, one database. Companies run their own copy on their own server,
so the deployment target is a single process plus Postgres or SQLite — not a
cluster of services. See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for why
that shape was chosen and what still needs building.

## Status

The server compiles, boots, and serves requests: configuration, JWT auth with
refresh-token rotation and sign-in throttling, admin-managed user provisioning,
middleware (security headers, request IDs, structured logging), and the sync
engine are all wired in.

Milestones 0–4 of `docs/PLAN.md` are done. Milestone 5 is done except for T5.6
(trusted-proxy client address). In milestone 6, T6.3 (middleware tests) and T6.5
(CI hardening) remain; milestone 7 (freezing the client contract) has not
started. See `docs/PLAN.md` for the full roadmap.

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

- **systemd** — `EnvironmentFile=/etc/tasktracker/secrets.env` (mode `0640`,
  owned by `root:tasktracker`). See
  [`docs/SELF-HOSTING.md`](docs/SELF-HOSTING.md) for the full unit.
- **Docker / Compose** — see [Docker](#docker) below. Pass `JWT_SECRET`
  as an environment variable or via `.env`; Docker secrets also work.
- **GitLab CI/CD** — project *Settings → CI/CD → Variables*, marked *Masked* and
  *Protected*.

If you keep a `.env` file on a server, restrict it to `chmod 600`. Rotating
`JWT_SECRET` invalidates every issued token.

## Running

```bash
go run ./cmd/server
```

Then `GET /healthz` should return `ok`, and `GET /readyz` should too once the
database answers. `/healthz` is liveness — it never touches the database, so a
database outage does not get the process restarted. `/readyz` is readiness: it
pings the database and returns `503` when that fails, which is what a load
balancer or container orchestrator should watch.

## Docker

The fastest way to run the server locally. Two profiles cover both
deployment shapes:

| Profile | What starts | When to use |
| --- | --- | --- |
| `postgres` | server + Postgres 16 | shared, multi-user, or you already run Postgres |
| `sqlite` | server only | single-node, zero external dependencies |

### Quick start

1. **Create a `.env` file** (Docker Compose reads it automatically):

   ```bash
   cp .env.example .env
   ```

   Edit `.env` and set the required values:

   ```dotenv
   # Required — at least 32 bytes. The server refuses to start without it.
   JWT_SECRET=<paste output of: openssl rand -hex 32>

   # Only needed for the postgres profile. Change the default.
   POSTGRES_PASSWORD=change-me
   ```

   All other variables (`SERVER_PORT`, `CORS_ORIGINS`, `LOG_LEVEL`, etc.)
   have sensible defaults in `.env.example` — override them only if your
   setup requires it.

2. **Pick a profile and start:**

   ```bash
   # Postgres — server + database, ready in seconds
   docker compose --profile postgres up -d

   # SQLite — single container, data in a Docker volume
   docker compose --profile sqlite up -d
   ```

3. **Verify:**

   ```bash
   curl http://localhost:8080/healthz    # → ok
   curl http://localhost:8080/readyz     # → ok once the database answers
   ```

   Both containers define a healthcheck against `/readyz`.

4. **Stop:**

   ```bash
   docker compose --profile postgres down       # or: sqlite
   ```

   Add `-v` to also delete the database volume — **this destroys all
   data**, so use it only for a fresh start.

### Environment variables

Docker Compose reads `.env` in the project root automatically. Every
variable you set there is passed through to the container, and the same
defaults from `.env.example` apply. You can also set variables inline:

```bash
JWT_SECRET=$(openssl rand -hex 32) docker compose --profile sqlite up -d
```

Real environment variables always win over `.env`, so a host can inject
secrets without any file on disk — see [Production](#production) below.

### What Docker handles for you

- **CGO and the C compiler** — `mattn/go-sqlite3` needs one; the Docker
  image includes it, so users don't install `gcc` on their machines.
- **Postgres** — the `postgres` profile starts a Postgres 16 container
  with a healthcheck; the server waits for it before booting.
- **SQLite data** — the `sqlite` profile mounts a Docker volume at
  `/data`, so the database survives container restarts.
- **Non-root user** — the server runs as `app`, not `root`.

### Building without Compose

```bash
docker build -t tasktracker-server .
docker run -p 8080:8080 -e JWT_SECRET=$(openssl rand -hex 32) \
  -e DATABASE_URL=sqlite:///data/tasktracker.db \
  -v tt-data:/data tasktracker-server
```

Not using Docker? [`docs/SELF-HOSTING.md`](docs/SELF-HOSTING.md) covers running
the binary directly on a Linux host under systemd — the unit file, secrets,
TLS termination, and backups.

## Verifying

```bash
gofmt -l . && go build ./... && go vet ./... && go test ./...
```

Tests run on SQLite by default. To run the same suite against Postgres:

```bash
docker run -d --name tt-pg -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=tasktracker_test -p 55432:5432 postgres:16

TASKTRACKER_TEST_POSTGRES_URL="postgres://postgres:postgres@localhost:55432/tasktracker_test?sslmode=disable" \
  go test ./...
```

Each test creates and drops its own schema, so the database must be disposable.
CI runs this as the `test-postgres` job.

Covered today: `internal/config` (secrets), `internal/db` (migrations, driver
branching, statement splitting), `internal/entities` (repository round-trips,
version conflicts, cascade deletes), `internal/sync` (push/pull, conflict and
rejected verdicts), `internal/auth` (sign-in, refresh rotation, reuse detection,
family revocation, sign-out). Middleware (`Auth`, `AdminOnly`, `CORS`) is still
exercised by hand only — see `docs/PLAN.md` → T6.3.

## Database and migrations

Migrations live in `internal/db/migrations/`, one file per driver sharing a
version number:

```
0001_initial.postgres.sql
0001_initial.sqlite.sql
```

They are embedded in the binary (`go:embed`) and applied on startup in version
order; each version runs in its own transaction and is recorded in
`schema_migrations`, so re-running applies nothing. To add one, create the next
number for **both** drivers — a version missing a dialect file fails
`go test ./internal/db`. Statements keep `IF NOT EXISTS` so a database created
before the runner existed is adopted on the next start.

## CI/CD

`.gitlab-ci.yml` runs three stages on every push: `lint` (gofmt + `go vet`),
`build`, and `test` — the last with a `test-postgres` job alongside the SQLite
run. It is intentionally minimal — a gate, not a release pipeline. Build and test
are green, so a red run is a real regression.

## Documentation

- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — architecture and code review.
- [`docs/PLAN.md`](docs/PLAN.md) — the ordered, task-by-task implementation plan.
- [`docs/SELF-HOSTING.md`](docs/SELF-HOSTING.md) — the bare-metal/systemd deployment path, without Docker.
- [`docs/USER-MANAGEMENT.md`](docs/USER-MANAGEMENT.md) — admin-managed user flow: CLI bootstrap, admin API, directory search.
- [`AGENTS.md`](AGENTS.md) — working agreement for this repository.
- [`.env.example`](.env.example) — every configuration variable.

## License

No license has been chosen — all rights reserved by default.
