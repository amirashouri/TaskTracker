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

Milestones 0–8 are complete. Milestone 9 tracks customer installation and
exposure work; see [`docs/PLAN.md`](docs/PLAN.md) for the active roadmap and
[`docs/PLAN-ARCHIVE.md`](docs/PLAN-ARCHIVE.md) for detailed plans and history.

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

`make` with no target lists the same operations as targets: `make up` starts the
`postgres` profile, `make up PROFILE=sqlite` starts the other one, `make down`
stops and removes whichever is running, and `make rebuild` adds `--build`. Each
one reads the same `.env` as the commands below.

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

   All other variables (`SERVER_BIND`, `SERVER_PORT`, `CORS_ORIGINS`,
   `LOG_LEVEL`, etc.) have sensible defaults in `.env.example` — override them
   only if your setup requires it.

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

4. **Create the first admin.** There is no self-signup: an admin provisions
   every user, and the first one has to be created from the command line. Run
   the subcommand inside the running server container, where it inherits the
   compose environment (`DATABASE_URL`, `JWT_SECRET`) and needs nothing else:

   ```bash
   docker compose --profile postgres exec server-pg \
     /app/server user-create \
     --email admin@example.com \
     --password 'a-strong-password' \
     --display-name 'Admin' \
     --admin
   ```

   Use `server-sqlite` in place of `server-pg` for the SQLite profile. It
   prints `Created user … (role=admin)`. See
   [`docs/USER-MANAGEMENT.md`](docs/USER-MANAGEMENT.md) for the flags and how
   the admin then creates everyone else through the API.

5. **Stop:**

   ```bash
   docker compose --profile postgres down       # or: sqlite
   ```

   Add `-v` to also delete the database volume — **this destroys all
   data**, so use it only for a fresh start.

### After changing the code

`up -d` recreates the *container* from the image that already exists. It does
**not** rebuild the image, so after editing the server a plain `up -d` brings
the old binary back and the change looks like it had no effect at all:

```bash
# Rebuild the image from the working tree, then recreate the container.
docker compose --profile postgres up -d --build
```

The database lives in a named volume (`pg-data` / `sqlite-data`) and is
untouched by a rebuild. To check which build is actually running, compare the
image's creation time against your last edit:

```bash
docker images --format '{{.Repository}}\t{{.CreatedAt}}' | grep tasktracker
```

This is the Docker counterpart to the bare-metal upgrade steps in
[`docs/SELF-HOSTING.md`](docs/SELF-HOSTING.md) — § 10, Upgrades.

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
  -e SERVER_BIND=0.0.0.0 \
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
family revocation, sign-out), `internal/ratelimit` (window, per-key isolation,
eviction), and `internal/middleware` (`Auth`, `AdminOnly`, `CORS`, security
headers, request IDs, logging levels, panic recovery).

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
`build`, and `test`. The `test` job runs the suite under the race detector
(`go test -race`) and reports coverage, with a `test-postgres` job alongside it
for dialect parity. Race detection is on because the sync engine, the rate
limiter and refresh-token rotation are concurrent, and a data race there is a
correctness bug a plain run cannot see. Coverage is a reported trend, not a
gate. It is intentionally minimal — a gate, not a release pipeline. Build and
test are green, so a red run is a real regression.

## Documentation

- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — architecture and code review.
- [`docs/API.md`](docs/API.md) — the HTTP contract: auth, users, and the sync endpoints, checked against the client's own DTOs.
- [`docs/PLAN.md`](docs/PLAN.md) — concise active roadmap and task index.
- [`docs/PLAN-ARCHIVE.md`](docs/PLAN-ARCHIVE.md) — completed-work history and
  detailed plans for pending tasks without specs.
- [`docs/INSTALL.md`](docs/INSTALL.md) — **customer-facing install guide**: what you need, `tt install`, `tt expose` for LAN/Tailscale/Cloudflare, Day 2 operations, and troubleshooting.
- [`docs/SELF-HOSTING.md`](docs/SELF-HOSTING.md) — the bare-metal/systemd deployment path, without Docker.
- [`docs/USER-MANAGEMENT.md`](docs/USER-MANAGEMENT.md) — roles, CLI bootstrap, token lifecycle, and how a user gets provisioned.
- [`AGENTS.md`](AGENTS.md) — working agreement for this repository, including the wire-compatibility policy.
- [`.env.example`](.env.example) — every configuration variable.

## License

No license has been chosen — all rights reserved by default.
