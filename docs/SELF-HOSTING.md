# Self-Hosting (bare metal)

Running `TaskTrackerServer` directly on a Linux host under systemd, without
Docker. For the container path see [Docker](../README.md#docker) in the README;
for what the server is and how to configure it, see the README as well.

The whole deployment is one binary, one database, one secret. There is no
message bus, no second service, and no external scheduler.

## 1. Prerequisites

| Need | Why |
| --- | --- |
| Linux with systemd | the unit in `deploy/tasktracker.service` |
| A C compiler and `CGO_ENABLED=1` **to build** | `mattn/go-sqlite3` is a cgo driver |
| Postgres 14+ **or** nothing | SQLite needs no server |
| `sqlite3` CLI, for backups | only if you use SQLite |

The C compiler is a build-time need only. A binary built with SQLite support
carries it; the host does not need `gcc`.

## 2. Build and install the binary

```bash
git clone <your-fork> tasktracker && cd tasktracker
CGO_ENABLED=1 go build -o server ./cmd/server

sudo install -d -o root -g root /opt/tasktracker
sudo install -m 0755 -o root -g root server /opt/tasktracker/server
```

Cross-compiling from macOS or Windows to Linux will not work for the SQLite
build: cgo needs a Linux C toolchain. Build on the target host, in a Linux
container, or use the Docker image.

If you run Postgres only, you can build without SQLite and skip cgo — but the
driver is compiled in unconditionally today, so `CGO_ENABLED=1` is the simple
answer.

## 3. Service user and directories

```bash
sudo useradd --system --home /var/lib/tasktracker --shell /usr/sbin/nologin tasktracker
sudo install -d -o tasktracker -g tasktracker -m 0750 /var/lib/tasktracker
sudo install -d -o root -g root -m 0755 /etc/tasktracker
```

`StateDirectory=tasktracker` in the unit makes systemd manage
`/var/lib/tasktracker`, so creating it by hand is belt-and-braces. The service
user never logs in and owns nothing but its own state directory.

## 4. Configuration and secrets

All configuration is environment variables; the server also seeds them from a
file named by `ENV_FILE` (default `.env`), but under systemd use
`EnvironmentFile` instead so the file lives outside the working directory. Real
environment variables always win over the file, so the unit's file and an
exported value cannot fight. CLI subcommands are separate processes and do not
inherit the unit's environment — see §7.

```bash
sudo tee /etc/tasktracker/secrets.env >/dev/null <<EOF
JWT_SECRET=$(openssl rand -hex 32)
DATABASE_URL=sqlite:///var/lib/tasktracker/tasktracker.db
SERVER_BIND=127.0.0.1
SERVER_PORT=8080
LOG_LEVEL=info
CORS_ORIGINS=
SIGNIN_MAX_ATTEMPTS=10
SIGNIN_WINDOW=15m
SYNC_LOG_RETENTION=720h
EOF

# Root-owned, readable only by root and the service group.
sudo chown root:tasktracker /etc/tasktracker/secrets.env
sudo chmod 0640 /etc/tasktracker/secrets.env
```

**`JWT_SECRET` must be at least 32 bytes.** The server refuses to start
otherwise. Rotating it invalidates every issued token, so treat it like a
database password: generate it once, store it in your secret manager, and do
not put it in the repository. `.env` is git-ignored; only `.env.example`, which
holds no real values, is committed.

Every variable is documented in [`.env.example`](../.env.example). The ones
worth knowing:

| Variable | Default | Notes |
| --- | --- | --- |
| `DATABASE_URL` | `sqlite:///data/tasktracker.db` | see the two forms below |
| `SERVER_BIND` | `127.0.0.1` | IP literal; loopback keeps the server off the network |
| `SERVER_PORT` | `8080` | the port the server listens on |
| `JWT_SECRET` | — | required, ≥32 bytes |
| `JWT_ACCESS_TTL` | `1h` | access tokens are stateless; this bounds a stolen one |
| `JWT_REFRESH_TTL` | `720h` | how long a session can idle and still be resumed |
| `CORS_ORIGINS` | localhost pair | comma-separated browser origins; mobile clients send no `Origin` |
| `LOG_LEVEL` | `info` | one of `debug`, `info`, `warn`, `error` |
| `SIGNIN_MAX_ATTEMPTS` | `10` | per address and per account, per window; `0` disables |
| `SIGNIN_WINDOW` | `15m` | the window for the above |
| `SYNC_LOG_RETENTION` | `720h` | change-feed rows are pruned after this; `0` keeps them forever |

`DATABASE_URL` has exactly two forms:

```
postgres://user:password@host:5432/dbname
sqlite:///absolute/path/to/tasktracker.db
```

Note the three slashes in the SQLite form: everything after `sqlite://` is the
path, so `sqlite:///var/lib/...` means `/var/lib/...`. The parent directory
must exist — SQLite will not create it.

## 5. Database

### SQLite (single node)

Nothing to install. Create the parent directory, point `DATABASE_URL` at a file
inside it, and make sure the service user can write there. The server enables
WAL and a busy timeout and serialises writes on a single connection, which is
the right shape for one process.

SQLite is a good fit for a small team on one host. It is not a fit for two
server processes sharing a file over a network filesystem.

### Postgres (shared or larger install)

```bash
sudo -u postgres psql <<'SQL'
CREATE ROLE tasktracker LOGIN PASSWORD 'change-me';
CREATE DATABASE tasktracker OWNER tasktracker;
SQL
```

```dotenv
DATABASE_URL=postgres://tasktracker:change-me@127.0.0.1:5432/tasktracker
```

Keep the connection on the loopback interface, or require TLS in `pg_hba.conf`
if the database is on another host. The server applies its migrations on
startup, so the role needs `CREATE` on the schema; it does not need superuser.

## 6. Install the service

```bash
sudo cp deploy/tasktracker.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now tasktracker
```

```bash
systemctl status tasktracker
journalctl -u tasktracker -f
curl -fsS http://127.0.0.1:8080/healthz   # → ok
curl -fsS http://127.0.0.1:8080/readyz    # → ok once the database answers
```

`/healthz` is liveness and never touches the database, so an outage does not
get the process restarted. `/readyz` is readiness and returns `503` when the
database is unreachable — watch that one, not `/healthz`, if you add monitoring.

## 7. Create the first admin

There is no self-signup. On a fresh install there are no users, so sign-in
cannot succeed until you make one:

```bash
sudo -u tasktracker env ENV_FILE=/etc/tasktracker/secrets.env \
  /opt/tasktracker/server user-create \
  --email admin@example.com \
  --password 'a-strong-password' \
  --display-name 'Admin' \
  --admin
```

`user-create` is a separate process, so it does **not** inherit the unit's
`EnvironmentFile` — pass `ENV_FILE` explicitly, or it will look for `.env` in
the working directory and refuse to start for want of `JWT_SECRET`. It applies
migrations itself and writes to the database directly, so it works whether or
not the service is running. The password is hashed before it is stored, and the
command refuses an email that already exists.

### In a container

If you deployed the Docker image rather than the systemd unit, the same
subcommand runs inside the server container. It inherits the compose
environment, so no `ENV_FILE` is needed:

```bash
docker compose --profile postgres exec server-pg \
  /app/server user-create \
  --email admin@example.com \
  --password 'a-strong-password' \
  --display-name 'Admin' \
  --admin
```

Use `server-sqlite` for the SQLite profile. The container deployment itself is
covered in the [README](../README.md#docker).

After that, the admin creates everyone else through the API — see
[`docs/USER-MANAGEMENT.md`](USER-MANAGEMENT.md).

## 8. TLS termination

The server speaks plain HTTP and has no TLS options. **Do not expose port 8080
to the network.** Terminate TLS at a reverse proxy on the same host.

nginx:

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name tasks.example.com;

    ssl_certificate     /etc/letsencrypt/live/tasks.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/tasks.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host              $host;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}

server {
    listen 80;
    server_name tasks.example.com;
    return 301 https://$host$request_uri;
}
```

Caddy, which obtains and renews the certificate itself:

```
tasks.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

`SERVER_BIND` defaults to `127.0.0.1`, so the server listens only on this
host's loopback interface; `SERVER_PORT` sets the port. Leave both alone and
let the proxy be the only path in. Setting `SERVER_BIND=0.0.0.0` exposes the
server directly — do that only if you restrict the port with a firewall to the
addresses that should reach it.

### A caveat behind a proxy

The sign-in rate limiter keys on the peer address it sees, and the server does
**not** read `X-Forwarded-For`. Behind a reverse proxy every request therefore
appears to come from the proxy, so the per-address limit becomes a single
global limit while the per-account limit keeps working. On a small
single-organisation install that is usually acceptable — raise
`SIGNIN_MAX_ATTEMPTS` if legitimate users trip it. Trusting the header without
knowing which proxies are yours would let anyone forge their address, which is
worse; the trusted-proxy fix is tracked as T9.1 in
[`PLAN.md`](PLAN.md), with its original rationale in
[`PLAN-ARCHIVE.md`](PLAN-ARCHIVE.md).

## 9. Backups and restore

Back up the database. Nothing else holds state: the binary is reproducible and
the configuration is in your secret store.

### SQLite

Do not copy the file while the service is running — WAL means the copy can miss
recent commits. Use SQLite's online backup, which is safe with the service up:

```bash
sqlite3 /var/lib/tasktracker/tasktracker.db \
  ".backup '/var/backups/tasktracker-$(date +%F).db'"
```

Or stop the service and copy the file (plus any `-wal` and `-shm` beside it):

```bash
sudo systemctl stop tasktracker
sudo cp /var/lib/tasktracker/tasktracker.db /var/backups/
sudo systemctl start tasktracker
```

Restore by replacing the file while the service is stopped, then starting it.
The server re-applies migrations on startup, so restoring an older file is
safe: it is upgraded in place.

### Postgres

```bash
pg_dump -Fc -U tasktracker tasktracker > /var/backups/tasktracker-$(date +%F).dump
```

Restore:

```bash
sudo systemctl stop tasktracker
pg_restore --clean --if-exists -U tasktracker -d tasktracker /var/backups/tasktracker-2026-10-13.dump
sudo systemctl start tasktracker
```

Schedule whichever of these fits your host — a systemd timer, cron, or your
existing backup agent — and test a restore at least once. A backup nobody has
restored is a hypothesis.

## 10. Upgrades

```bash
CGO_ENABLED=1 go build -o server ./cmd/server
sudo systemctl stop tasktracker
sudo install -m 0755 -o root -g root server /opt/tasktracker/server
sudo systemctl start tasktracker
```

Migrations run on startup and each version is recorded in `schema_migrations`,
so a restart applies only what is new. A failed migration rolls back inside its
own transaction and the server refuses to start, which is the safe failure: the
old binary still works against the old schema.

Take a backup before an upgrade that includes a migration. Rolling back the
binary is easy; rolling back a schema change is not.

**Upgrading from a build before `SERVER_BIND`.** The default changed from every
interface to loopback. A bare-metal install that relied on direct LAN access
must now set `SERVER_BIND=0.0.0.0`; an install behind a reverse proxy should
keep the default. Docker Compose installs set the container-side bind
explicitly and publish the port on loopback.

## 11. Operations

- **Logs** are structured, one line per request, on stdout — read them with
  `journalctl -u tasktracker`. Each line carries the `X-Request-ID` the client
  sent or the server generated, and that id is echoed in the response header,
  so a reported failure can be traced to its line.
- **Change feed growth** is bounded by `SYNC_LOG_RETENTION`, swept hourly. A
  client whose cursor is older than what survived is told to resync rather than
  silently missing changes.
- **Restarting** is safe and fast: connections are drained on `SIGTERM`, and
  the database is the only state.
- **Scaling** is not supported. One process, one database. Two processes
  against one SQLite file will corrupt it; two against one Postgres database
  would work for reads but the rate limiter is per-instance by design.

## 12. Troubleshooting

| Symptom | Likely cause |
| --- | --- |
| `JWT_SECRET must be set and at least 32 bytes` | the `EnvironmentFile` is unreadable or the secret is short |
| `unsupported database URL` | `DATABASE_URL` is neither `postgres://` nor `sqlite://` |
| `unable to open database file` | the SQLite parent directory does not exist or is not writable by `tasktracker` |
| `LOG_LEVEL ... is not one of` | a typo in the level name; the server refuses to start on purpose |
| `/readyz` returns `503` | the database is down or unreachable from the service |
| 401 on every request after a restart | `JWT_SECRET` changed, so all tokens were invalidated |
| Sign-in returns `429` | the rate limiter tripped; wait out `SIGNIN_WINDOW` or raise `SIGNIN_MAX_ATTEMPTS` |
