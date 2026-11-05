# Installing TaskTracker for a Team

This guide walks a customer through installing and running TaskTracker on their
own server. It assumes no prior knowledge of Go, Docker, or the project's
internals — just a Linux host and a terminal.

> **Looking for the bare-metal/systemd path?** See [`docs/SELF-HOSTING.md`](SELF-HOSTING.md).
> This guide uses Docker Compose and the `tt` CLI.

## What you need

| Item | Notes |
|------|-------|
| A Linux host | Any distribution with a package manager (Debian/Ubuntu, Fedora/RHEL, Arch). Docker runs on anything with a kernel ≥ 3.10. |
| Root or sudo access | Required to install Docker and start the daemon. |
| A domain (optional) | Only if you want HTTPS from the internet. `lan` mode needs none. |

**Providers and accounts:**

| Provider | What it gives you | Account needed |
|----------|-------------------|----------------|
| `lan` | Plain HTTP on your LAN. No TLS, no internet exposure. | None |
| `tailscale` | HTTPS via Tailscale Funnel (public) or Serve (tailnet-only). | Free Tailscale account |
| `cloudflare` | HTTPS via Cloudflare Tunnel on your own domain. | Cloudflare account with a domain |

> **Mobile note:** All clients — desktop, Android, and iOS — work over `lan`. The first connection from a phone asks one permission (Android: local network; iOS: Local Network prompt), and plain HTTP stays inside your network. If your team also needs access from outside the LAN, use `tailscale` or `cloudflare`, which serve HTTPS.

## Step 0 — Get the `tt` CLI

`tt` is a static Go binary that bootstraps the server, the database, and the
first admin account, then prints the address the phone scans. It does not need
Docker on the machine that runs it — only on the host that runs the server, and
it installs that too. It never phones home: the URL you pass is the URL you
get.

### Fastest: one curl command, no cloning

On a tagged release, CI publishes a Linux/amd64 tarball containing `tt` and the
`server` binary it supervises. The URL below targets this asset on the latest
GitHub release:

```bash
# Linux amd64 — the only platform with a server binary today:
curl -fsSL https://github.com/amirashouri/TaskTracker/releases/latest/download/tt-linux-amd64.tar.gz \
  | tar -xz -C /usr/local/bin
sudo mv /usr/local/bin/tt-linux-amd64 /usr/local/bin/tt
chmod +x /usr/local/bin/tt

# Install. The tarball already has the server binary, so no Docker, no clone:
tt install --runtime native --server-binary ./server
```

The release job also builds standalone `tt` binaries for other platforms, but
currently publishes only the Linux/amd64 bundle as a GitHub release asset.

### Build from source (developers and unreleased versions)

If there is no tarball for your platform or you want the working tree, build
`tt` with Go 1.27:

```bash
make tt
./dist/tt install
```

Per-platform builds use `go build` directly; `make tt` does not take
`GOOS`/`GOARCH`:

```bash
mkdir -p dist && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/tt-linux-arm64 ./cmd/tt
```

To build every platform at once, including the Linux/amd64 curl-installable
bundle:

```bash
make release VERSION=v1.2.3        # static tt binaries
make release-tt VERSION=v1.2.3    # ... plus the Linux/amd64 tt + server bundle
```

### Verify

```bash
tt --help
```

You should see the command list: `install`, `expose`, `status`, `doctor`,
`logs`, `uninstall`, `version`, `agent`.

## Get the TaskTracker App

The TaskTracker client runs on your phone or desktop and connects to your server via the pairing link/QR code from `tt expose`.

| Platform | Download |
|----------|----------|
| **Android** | [GitHub Releases → `tasktracker-android-*.apk`](https://github.com/amirashouri/TaskTrackerKMP/releases/latest) |
| **iOS** | [TestFlight](https://testflight.apple.com/join/...) (or build from source via Xcode) |
| **macOS** | [GitHub Releases → `TaskTracker-*.dmg`](https://github.com/amirashouri/TaskTrackerKMP/releases/latest) |
| **Windows** | [GitHub Releases → `TaskTracker-*.msi`](https://github.com/amirashouri/TaskTrackerKMP/releases/latest) |
| **Linux** | [GitHub Releases → `TaskTracker-*.deb`](https://github.com/amirashouri/TaskTrackerKMP/releases/latest) |

### Installing the Desktop App

The desktop app is distributed as native installers for each platform.

#### macOS (.dmg)

```bash
# Download the latest release
curl -fsSL https://github.com/amirashouri/TaskTrackerKMP/releases/latest/download/TaskTracker-*.dmg -o TaskTracker.dmg

# Open and install
open TaskTracker.dmg
# In the Finder window that opens, drag TaskTracker to Applications folder
```

Or simply download the `.dmg` from the browser and double-click to install.

#### Windows (.msi)

```powershell
# Download the latest release
iwr -useb https://github.com/amirashouri/TaskTrackerKMP/releases/latest/download/TaskTracker-*.msi -outfile TaskTracker.msi

# Install
msiexec /i TaskTracker.msi
```

Or download the `.msi` from the browser and double-click to install.

#### Linux (.deb) — Ubuntu/Debian

```bash
# Download the latest release
curl -fsSL https://github.com/amirashouri/TaskTrackerKMP/releases/latest/download/TaskTracker-*.deb -o TaskTracker.deb

# Install
sudo dpkg -i TaskTracker.deb
# If there are missing dependencies:
sudo apt-get install -f
```

> **Note:** The iOS app requires a TestFlight invite or building from source (see the [KMP client repo](https://github.com/amirashouri/TaskTrackerKMP) for build instructions). The Android APK and Desktop native installers are published to GitHub Releases on every tag.

## Step 1 — Run `tt install`

```bash
tt install
```

It checks Docker **first**. The server and its database both run in containers,
so nothing can be installed on a host that cannot run them — and in SQLite mode
as much as in Postgres. If Docker is missing, `tt install` offers to install it
(Debian/Ubuntu, Fedora/RHEL, Arch) and to start its daemon before asking you
anything; with no terminal to answer (`--yes`, or a piped stdin), it stops and
names the manual install instead.

Only two of the questions are prompts. Provider, instance name and port are
flags with defaults (`lan`, `tasktracker`, `8080`); pass them to change them. The
remaining checks — Docker present and running, data directory writable, port
free, disk space — are silent and only speak up when they fail, each naming the
fix.

Here's a transcript from a clean Ubuntu 24.04 host that already runs Docker:

```text
$ ./dist/tt install
Choose sqlite or postgres [sqlite]:
Data directory [/var/lib/tasktracker]:
Admin email: admin@company.com
Admin password:
Confirm admin password:
JWT_SECRET=3f2a1c4e9b8d7f6a5e4d3c2b1a0f9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2
Store this secret; it is not printed again.
 Container tasktracker-server-1  Started

Installed "tasktracker". Server: http://127.0.0.1:8080
Admin: admin@company.com
LAN address: 192.168.1.42
If this host is a laptop, keep it plugged in, on Ethernet, and with the lid open: a laptop that sleeps is a server that is down.
Next: open http://127.0.0.1:8080 on this host and sign in as admin@company.com.
Pairing: no verified server URL yet.
Run "tt expose --provider=lan" to publish one; it prints the pairing link and QR code.
```

### What just happened

- **Database**: SQLite by default. Its file lives in the `tasktracker-data`
  container volume, not in the data directory. Choosing `postgres` also starts a
  `postgres:16-alpine` service with its own volume.
- **Data directory**: `/var/lib/tasktracker` — where `tt` writes
  `docker-compose.yml`. The install state goes to `/etc/tasktracker/install.json`
  (`~/.config/tasktracker/install.json` on macOS and Windows).
- **Provider**: `lan`, the `--provider` default. The server is published on
  `127.0.0.1` only; `tt expose` is what makes it reachable from other devices.
- **Instance name**: `tasktracker`, the `--name` default — the Docker Compose
  project name.
- **Admin**: Created inside the running server container. No self-signup exists.
- **JWT_SECRET**: Generated once, printed once. **Save it.** Rotating it invalidates every token.

### Non-interactive install (automation)

```bash
tt install --yes \
  --admin-email admin@company.com \
  --admin-password 'StrongPass123' \
  --admin-display-name 'Admin' \
  --provider lan \
  --mode sqlite
```

> **Warning:** `--yes` skips all prompts. If Docker is missing, it **fails closed** — it will not install Docker automatically. Install Docker first, then re-run.

### No Docker (native, Linux with systemd)

A SQLite install can run without Docker at all. The wizard offers it after you
choose SQLite; the answer is the only thing that turns the runtime on:

```bash
# The tarball already has the server binary beside tt:
tt install --runtime native --server-binary ./server

# Or fetch one over HTTPS into the data directory. tt never phones home, so
# the URL is an explicit argument the operator chooses:
tt install --runtime native \
  --server-binary-url https://github.com/amirashouri/TaskTracker/releases/download/v0.1.6/server \
  --data-dir /var/lib/tasktracker
```

Native is Linux + systemd only, SQLite only, provider `lan` only. The server
runs as the `tasktracker` system account, the data directory is chowned to it,
and `tt` writes a systemd unit at `/etc/systemd/system/tasktracker.service`.
`--server-binary-url` only accepts `https://`, refuses `http://`, and refuses
with `--runtime docker`. It lands the binary in the data directory next to the
instance, so a repair reuses the file that is already there.

### Dry-run (audit what would happen)

```bash
tt install --dry-run
```

Prints the rendered `docker-compose.yml` and executes nothing.

## Step 2 — Expose the server

The server starts on `127.0.0.1:8080`. To reach it from other devices:

### LAN (default provider)

```bash
tt expose --provider=lan
```

Output:

```text
$ tt expose --provider=lan
Exposed on the LAN: http://192.168.1.42:8080
Plain HTTP on your local network: trust this network, and do not forward this port from the internet.
`tt install` rewrites the compose file to loopback; re-run `tt expose` after a repair.
Pairing link: tasktracker://connect?url=https%3A%2F%2F192.168.1.42%3A8080
QR code: (ASCII QR printed to terminal)
```

This rewrites the compose file to publish `0.0.0.0:8080:8080` and verifies the LAN address works. **Do not forward this port to the internet.**

> **Mobile note:** Desktop and mobile clients all connect over `lan`. On the first connection a phone asks one permission (Android: local network; iOS: the Local Network prompt) — grant it and sign in. Plain HTTP is fine inside a network you trust; for access from outside the LAN use a TLS provider (below).

### Tailscale (public HTTPS or tailnet-only)

```bash
# Prerequisites: Tailscale installed, `tailscale up` done, MagicDNS + HTTPS certs enabled in admin console
tt expose --provider=tailscale
```

For tailnet-only (no public internet):

```bash
tt expose --provider=tailscale --tailnet-only
```

Output:

```text
$ tt expose --provider=tailscale
Exposed with Tailscale Funnel: https://myserver.tailnet.ts.net
The server stays bound to loopback; Tailscale reaches it from 127.0.0.1 over HTTPS.
To stop exposing: tailscale funnel reset
Pairing link: tasktracker://connect?url=https%3A%2F%2Fmyserver.tailnet.ts.net
QR code: (ASCII QR printed to terminal)
```

**Caveats:**
- Funnel is beta, has an unpublished bandwidth cap, and the Personal plan is non-commercial.
- Funnel may be disabled by tailnet ACLs — the command will tell you.
- The install state doesn't persist the exposed name; `tt doctor` skips URL checks.

### Cloudflare Tunnel (your own domain)

```bash
# Prerequisites: domain on Cloudflare, `cloudflared tunnel login` done
tt expose --provider=cloudflare --domain tasks.company.com
```

Output:

```text
$ tt expose --provider=cloudflare --domain tasks.company.com
Exposed with Cloudflare Tunnel: https://tasks.company.com
The server stays bound to loopback; Cloudflare terminates TLS and proxies to it.
To stop exposing: cloudflared tunnel delete <tunnel-id>
Pairing link: tasktracker://connect?url=https%3A%2F%2Ftasks.company.com
QR code: (ASCII QR printed to terminal)
```

**Caveats:**
- Cloudflare Free/Pro/Business plans have a 100 MiB request-body limit. `ATTACHMENT_MAX_BYTES` is validated at startup; if you need larger uploads, you must use a different provider or Enterprise plan.
- DNS propagation for the new CNAME can take minutes to hours.

### What the QR code and `tasktracker://connect` link do

After a successful `tt expose`, the command prints a pairing link and a
scannable terminal QR code:

```text
Pairing link: tasktracker://connect?url=https%3A%2F%2Ftasks.company.com
QR code: (ASCII QR printed to terminal)
```

- **The link/QR carries only the server URL.** It does **not** carry a password.
- Install the TaskTracker app first (see [Get the TaskTracker App](#get-the-tasktracker-app) above).
- Open the link on a phone with the TaskTracker app installed, or scan the QR.
- The app will open, connect to the URL, and prompt for sign-in.

## Day 2 — Operations

### Check status

```bash
tt status
```

Prints the install state and runs read-only checks. The state section is always
there; the checks section is what `tt status` and `tt doctor` share, with
`doctor` adding the two that only matter when something is wrong.

Output:

```text
instance: tasktracker
runtime: docker
mode: sqlite
provider: cloudflare
image: tasktracker-server:v1.2.3
server: http://127.0.0.1:8080
compose: /var/lib/tasktracker/docker-compose.yml
[ok] containers server running
[ok] readyz the server answered 200
[ok] address published at 127.0.0.1:8080 (this host only)
[ok] database 12.3 MiB on disk
[skip] backup no backup recorded; tt does not create backups
```

### Health check

```bash
tt doctor
```

Same checks as `tt status`, plus the two that only matter when something is
wrong:

```text
$ tt doctor
instance: tasktracker
runtime: docker
mode: sqlite
provider: cloudflare
image: tasktracker-server:v1.2.3
server: http://127.0.0.1:8080
compose: /var/lib/tasktracker/docker-compose.yml
[ok] containers server running
[ok] readyz the server answered 200
[ok] address published at 127.0.0.1:8080 (this host only)
[ok] database 12.3 MiB on disk
[skip] backup no backup recorded; tt does not create backups
[ok] clock local and server clocks agree
[skip] provider cloudflare: provider health is not checked yet
```

- `provider` is always `skip`: the install state does not store the exposed URL,
  and `tt doctor` does not retain it. Re-run `tt expose` to re-verify.
- `backup` is always `skip`, not a warning: `tt` does not create backups, so
  there is no age it could honestly report. Back up the data volume yourself
  (see §3 below).

### View logs

```bash
tt logs              # all containers
tt logs server       # just the server
tt logs -f           # follow (Ctrl-C to stop)
```

### Move to a new server version

`tt` has no `upgrade` command. To move to a new server version, change the image
tag in the compose file or the binary path in the unit file and run
`tt install` again — it repairs the existing instance in place, keeping the
database and the admin account:

```bash
tt install --yes --admin-email admin@company.com --admin-password 'StrongPass123'
```

### Backup

**SQLite (single file inside a Docker volume):**

```bash
docker compose -f /var/lib/tasktracker/docker-compose.yml exec server \
  sqlite3 /data/tasktracker.db \
  ".backup '/data/tasktracker-$(date +%F).db'"
docker compose -f /var/lib/tasktracker/docker-compose.yml cp server:/data/tasktracker-$(date +%F).db /var/backups/
```

Or stop the stack and copy the volume:

```bash
docker compose -f /var/lib/tasktracker/docker-compose.yml down
docker run --rm -v tasktracker-tasktracker-data:/data -v /var/backups:/backups \
  alpine cp /data/tasktracker.db /backups/tasktracker-$(date +%F).db
# also copy -wal and -shm files if they exist
```

**Postgres:**

```bash
docker compose -f /var/lib/tasktracker/docker-compose.yml exec postgres \
  pg_dump -Fc -U tasktracker tasktracker > /var/backups/tasktracker-$(date +%F).dump
```

### Restore

**SQLite:** Replace the file while the stack is stopped, then start. The server
re-applies migrations on startup.

```bash
docker compose -f /var/lib/tasktracker/docker-compose.yml down
docker run --rm -v tasktracker-tasktracker-data:/data -v /var/backups:/backups \
  alpine cp /backups/tasktracker-2026-10-13.db /data/tasktracker.db
docker compose -f /var/lib/tasktracker/docker-compose.yml up -d
```

**Postgres:**

```bash
docker compose -f /var/lib/tasktracker/docker-compose.yml stop
docker compose -f /var/lib/tasktracker/docker-compose.yml exec postgres \
  pg_restore --clean --if-exists -U tasktracker -d tasktracker /var/backups/tasktracker-2026-10-13.dump
docker compose -f /var/lib/tasktracker/docker-compose.yml start
```

> **Test a restore at least once.** A backup nobody has restored is a hypothesis.

### Uninstall

```bash
tt uninstall
```

You must type the instance name to confirm. This stops and removes containers and the compose file. **The data volume is NOT deleted** — the SQLite file or Postgres volume remains. Delete it manually if you want a clean slate.

```text
$ tt uninstall
Type the instance name "tasktracker" to uninstall: tasktracker
Removed "tasktracker". Data volumes were left in place.
```

## Delegating a task to an agent

A work item in **In Progress** can be handed to an LLM agent that runs on the
machine holding the code. The server never calls a model and never stores a
provider key: it queues the run, the machine claims it, and the app shows the
progress while the item's editor is open.

You need the `tt` CLI on the developer machine, a checkout of the project, and
either a local model server or an API key for a hosted one.

### 1. Pair the machine

```bash
tt agent connect \
  --server http://192.168.1.20:8080 \
  --email you@example.com \
  --name workstation \
  --provider openai-compatible \
  --model llama3.1 \
  --base-url http://localhost:11434/v1
```

It signs in once, registers this machine, and writes the runner credential to
`~/.config/tasktracker/agent.json` (mode `0600`), or to `$TT_AGENT_CONFIG` when
that is set. The password is used once and is not stored. Run
`tt agent connect --force` to replace an existing pairing.

| Provider | Reaches | Needs |
| --- | --- | --- |
| `openai-compatible` | Ollama (default `http://localhost:11434/v1`), LM Studio, llama.cpp, vLLM, OpenAI | `--model`; `--base-url` for anything but local Ollama; `--api-key` for a hosted endpoint |
| `anthropic` | Anthropic's Messages API | `--model` and `--api-key` (or `TT_AGENT_API_KEY`) |
| `echo` | No model; reports the task back | Nothing — useful to prove the wiring |

A provider key stays on the developer machine. Do not put one in the server's
`.env`: the server has no field for it and would not use it.

### 2. Check the pairing

```bash
tt agent status
```

Prints the server, runner, provider and model, then probes the server's
`/healthz` endpoint. It shows that a token is stored but never prints the token
itself.

### 3. Run the worker

```bash
tt agent run --workspace ~/code/your-project
```

`--workspace` is the directory the agent may read and edit, and the directory
its commands run in; paths outside it are refused. Use `--once` to take at most
one run, `--max-steps` (default 25) to bound a run that never finishes, and
`--provider` / `--model` / `--base-url` / `--api-key` to override the paired
configuration. Without `--api-key`, `TT_AGENT_API_KEY` is used.

While the worker is connected, a task in progress in the app offers **Send to
agent**. The editor then shows the run's status, its latest step, the event log,
and **Cancel run**. Cancel is cooperative: the runner observes it at its next
heartbeat (20 s by default), so the status stays live for a moment after the tap.

Review the diff before committing. The agent writes real files and runs real
commands in the workspace; treat it as a colleague's uncommitted branch, not as
a trusted build step.

### 4. Stop or unpair

```bash
tt agent disconnect
```

Revokes this machine on the server, then deletes the local config. If the server
is unreachable the local file is still removed and the command says the machine
could not be retired; use **Revoke** in the app once it is back.

### If it does not work

| Symptom | Cause | Fix |
| --- | --- |
| `tt agent run: not connected` | No pairing on this machine | `tt agent connect` |
| `unknown provider` | A provider name this build does not run | Use `openai-compatible`, `anthropic` or `echo` |
| `provider … needs a model` | No `--model` at connect or run time | Pass `--model`, or `tt agent connect --force --model …` |
| `model endpoint answered 404` | `--base-url` is not the API root | Ollama is `http://localhost:11434/v1`; OpenAI is `https://api.openai.com/v1` |
| `an agent run is already in progress` | One live run per item | Wait for it to finish, or cancel it in the app |
| `unauthenticated` after re-pairing | The old token was revoked | `tt agent connect --force` |

## Troubleshooting

| Message | Cause | Fix |
|---------|-------|-----|
| `docker: command not found` | Docker not installed | Run `tt install` — it will offer to install Docker for you (Debian/Ubuntu/Fedora/Arch). Or install manually: `sudo apt install docker.io` etc. |
| `Docker is installed but its daemon is not reachable` | Daemon not running | `sudo systemctl start docker` (Linux) or start Docker Desktop (macOS/Windows) |
| `port 8080 is already in use` | Another process on 8080 | `sudo lsof -i :8080` → kill it, or use `--port` with `tt install` |
| `data directory … is not writable` | Permissions | `sudo chown -R $USER /var/lib/tasktracker` or choose `--data-dir` you own |
| `not enough free space` | Disk full | Free space or use `--data-dir` on a larger partition |
| `JWT_SECRET must be set and at least 32 bytes` | Missing/short secret | Generate one: `openssl rand -hex 32` |
| `server time within 1s of host` failed | Clock drift | Run `sudo systemctl restart systemd-timesyncd` or configure NTP |
| `android: cleartext HTTP traffic not permitted` | A client build before mobile LAN parity, or a root pointing at a public host | Update the app; the client only allows plain HTTP to local addresses (`validateServerAddress` refuses public `http`) — use `tailscale` or `cloudflare` for public access |
| `ATTACHMENT_MAX_BYTES exceeds Cloudflare's 100 MiB limit` | Attachment cap too high for Cloudflare | Lower `ATTACHMENT_MAX_BYTES` in `.env` or use `tailscale`/`lan` |
| `tailscale: funnel: access denied` | Funnel disabled in ACLs | Enable Funnel for this node in Tailscale admin console |
| `cloudflared: tunnel not found` | Tunnel deleted or wrong token | Re-run `tt expose --provider=cloudflare --domain ...` |

## The constraint (read this once)

**This project owns no infrastructure.** No wildcard domain, no hosted relay, no provisioning endpoint, no shared failure domain. The `tt` CLI never phones home. Your exposure is a provider *you* hold an account with (Tailscale, Cloudflare), or nothing (`lan`). This is a product constraint, not a preference — the project earns nothing, so it cannot carry a recurring cost or a shared failure domain.

If you want a static IP or a VPS with Caddy, that's your infrastructure to run and pay for. The project documents the shape (see `docs/SELF-HOSTING.md` § T9.11) but does not provide it.