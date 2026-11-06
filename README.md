# TaskTrackerServer

[![LinkedIn](https://img.shields.io/badge/LinkedIn-Amirreza%20Ashouri-0A66C2?logo=linkedin&logoColor=white)](https://www.linkedin.com/in/amirreza-ashouri-229019156)
[![Gmail](https://img.shields.io/badge/Gmail-amirashouri72%40gmail.com-EA4335?logo=gmail&logoColor=white)](mailto:amirashouri72@gmail.com)
![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)
![Database](https://img.shields.io/badge/database-Postgres%20%7C%20SQLite-4169E1?logo=postgresql&logoColor=white)

> **Own your task tracker.**
> The self-hosted backend for the [TaskTracker](https://github.com/amirashouri/TaskTrackerKMP) client. One Go binary, one database, on a server you control — no cluster, no SaaS, no telemetry. It handles auth, users, and the offline sync engine that keeps every client in step. Companies run their own copy; you can too, in one command.

---

## ⚡ Quick start — one command

Fetch the `tt` CLI, install the server, and reach it from your phone:

```bash
# 1. Install the tt CLI (Linux amd64)
curl -fsSL https://github.com/amirashouri/TaskTracker/releases/latest/download/tt-linux-amd64.tar.gz \
  | tar -xz -C /usr/local/bin tt-linux-amd64
sudo mv /usr/local/bin/tt-linux-amd64 /usr/local/bin/tt && chmod +x /usr/local/bin/tt

# 2. Install — asks only for an admin email and password
tt install

# 3. Publish it so other devices can connect
tt expose --provider=lan
```

`tt install` sets up Docker (and installs it for you if missing), picks SQLite or Postgres, renders the compose file, waits for the server to answer `/readyz`, and creates the first admin account. `tt expose` prints a pairing link and QR code — scan it in the [app](https://github.com/amirashouri/TaskTrackerKMP) and sign in.

Prefer a native install with no Docker? The tarball already ships the server binary:

```bash
tt install --runtime native --server-binary ./server
```

Automate the whole thing with flags:

```bash
tt install --yes \
  --admin-email admin@company.com \
  --admin-password 'StrongPass123' \
  --provider lan
```

> Full operator guide — exposure providers, Day 2 operations, backups, and troubleshooting — lives in [`docs/INSTALL.md`](docs/INSTALL.md). Prefer systemd over Docker? See [`docs/SELF-HOSTING.md`](docs/SELF-HOSTING.md).

---

## 🐳 Run with Docker Compose

The fastest path if you already have the repo checked out. Two profiles cover both deployment shapes:

| Profile | What starts | Use when |
| :--- | :--- | :--- |
| `postgres` | server + Postgres 16 | shared, multi-user, or you already run Postgres |
| `sqlite` | server only | single-node, zero external dependencies |

```bash
# 1. Create your config and set the required values
cp .env.example .env
openssl rand -hex 32          # paste into JWT_SECRET in .env
                              # set POSTGRES_PASSWORD too for the postgres profile

# 2. Start a profile
docker compose --profile postgres up -d     # or: --profile sqlite up -d

# 3. Verify
curl http://localhost:8080/healthz          # → ok
curl http://localhost:8080/readyz           # → ok once the database answers
```

There is no self-signup, so create the first admin inside the running container:

```bash
docker compose --profile postgres exec server-pg \
  /app/server user-create \
  --email admin@example.com \
  --password 'a-strong-password' \
  --display-name 'Admin' \
  --admin
```

Use `server-sqlite` instead of `server-pg` for the SQLite profile. After editing code, rebuild before restarting: `docker compose --profile postgres up -d --build`.

`make` wraps the common operations: `make up`, `make up PROFILE=sqlite`, `make down`, `make rebuild`. See [`.env.example`](.env.example) for every variable.

---

## 🔧 Run from source

```bash
# 1. Requirements: Go 1.27, a C compiler for CGO (SQLite), and Postgres 14+ or SQLite

# 2. Configure
cp .env.example .env
openssl rand -hex 32          # paste into JWT_SECRET (min 32 bytes)
mkdir -p data                 # SQLite won't create the parent directory

# 3. Run
go run ./cmd/server
```

Then `GET /healthz` returns `ok`, and `GET /readyz` returns `ok` once the database answers. Liveness vs. readiness is deliberate — `/healthz` never touches the database, `/readyz` does.

### Required configuration

| Variable | Purpose |
| :--- | :--- |
| `JWT_SECRET` | Signing key, **at least 32 bytes**. The server refuses to start without it. |
| `DATABASE_URL` | `sqlite:///data/tasktracker.db` or `postgres://user:pass@host:5432/tasktracker` |
| `SERVER_BIND` / `SERVER_PORT` | Default `127.0.0.1:8080` — loopback by default so a forgotten install isn't exposed |

Real environment variables always win over `.env`, so a host can inject secrets with no file on disk (systemd, Docker secrets, CI/CD variables).

---

## ✅ Verify

```bash
gofmt -l . && go build ./... && go vet ./... && go test ./...
```

Tests run on SQLite by default. Point them at Postgres with a throwaway server:

```bash
docker run -d --name tt-pg -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=tasktracker_test -p 55432:5432 postgres:16

TASKTRACKER_TEST_POSTGRES_URL="postgres://postgres:postgres@localhost:55432/tasktracker_test?sslmode=disable" \
  go test ./...
```

For day-to-day work, the fast loop is the packages you touched: `go test ./internal/<pkg>`. `scripts/check.sh all` (or `race`) is the release gate, not a per-change check.

---

## 🗄 Database & migrations

Schema is versioned SQL in `internal/db/migrations/`, one file per driver sharing a version number (`0001_initial.postgres.sql`, `0001_initial.sqlite.sql`). Files are embedded in the binary and applied on startup, each in its own transaction. New changes add the next version for **both** drivers — `go test ./internal/db` fails if one dialect is missing.

---

## 📚 Documentation

- [`docs/INSTALL.md`](docs/INSTALL.md) — **customer install guide**: `tt install`, `tt expose` (LAN/Tailscale/Cloudflare), Day 2 ops, troubleshooting.
- [`docs/SELF-HOSTING.md`](docs/SELF-HOSTING.md) — bare-metal/systemd deployment without Docker.
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — architecture and durable decisions.
- [`docs/API.md`](docs/API.md) — the HTTP contract, checked against the client's DTOs.
- [`docs/USER-MANAGEMENT.md`](docs/USER-MANAGEMENT.md) — roles, CLI bootstrap, and token lifecycle.
- [`specs/`](specs/README.md) — the task index; one spec per unit of work in progress.
- [`AGENTS.md`](AGENTS.md) — working agreement and wire-compatibility policy.

---

## 📄 License

[MIT + Commons Clause](LICENSE), plus one extra term: no rebranding.

Run, host, and deploy the backend for your own team — personal, commercial, or in production — and change the code. You may not sell it or repackage it under your own name and brand; the code stays the author's, and the "TaskTracker" name and logos are the author's trademarks.

The full text is in [`LICENSE`](LICENSE). The same license covers the [client](https://github.com/amirashouri/TaskTrackerKMP).

---

*Made by [Amirreza Ashouri](https://www.linkedin.com/in/amirreza-ashouri-229019156) — stars and issues welcome.*