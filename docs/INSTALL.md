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

## Step 0 — Install the `tt` CLI

Download the latest release for your platform. **Copy only the block for your OS:**

### Linux (amd64 / x86_64)

```bash
curl -fsSL https://github.com/amirashouri/TaskTrackerKMP/releases/latest/download/tt_linux_amd64 -o /usr/local/bin/tt
chmod +x /usr/local/bin/tt
```

### Linux (arm64 / aarch64)

```bash
curl -fsSL https://github.com/amirashouri/TaskTrackerKMP/releases/latest/download/tt_linux_arm64 -o /usr/local/bin/tt
chmod +x /usr/local/bin/tt
```

### macOS (Apple Silicon / arm64)

```bash
curl -fsSL https://github.com/amirashouri/TaskTrackerKMP/releases/latest/download/tt_darwin_arm64 -o /usr/local/bin/tt
chmod +x /usr/local/bin/tt
```

### macOS (Intel / amd64)

```bash
curl -fsSL https://github.com/amirashouri/TaskTrackerKMP/releases/latest/download/tt_darwin_amd64 -o /usr/local/bin/tt
chmod +x /usr/local/bin/tt
```

### Windows (PowerShell, amd64)

```powershell
iwr -useb https://github.com/amirashouri/TaskTrackerKMP/releases/latest/download/tt_windows_amd64.exe -outfile $env:USERPROFILE\tt.exe
# Add $env:USERPROFILE to your PATH if not already present
```

### Verify

```bash
tt --help
```

You should see the command list: `install`, `expose`, `status`, `doctor`, `logs`, `upgrade`, `uninstall`, `version`.

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

> **Note:** The iOS app requires a TestFlight invite or building from source (see the [KMP client repo](../TaskTrackerKMP) for build instructions). The Android APK and Desktop native installers are published to GitHub Releases on every tag.

## Step 1 — Run `tt install`

```bash
tt install
```

This is an interactive wizard. Here's a transcript from a clean Ubuntu 24.04 host:

```text
$ tt install
Welcome to TaskTracker setup.

Database [sqlite/postgres] [sqlite]:
Data directory [/var/lib/tasktracker]:
Exposure provider [lan/tailscale/cloudflare] [lan]:
Instance name [tasktracker]:
Host port for HTTP server [8080]:
Admin email: admin@company.com
Admin password: ********
Confirm admin password: ********
Admin display name (optional): Admin

Preflight checks…
  ✓ Docker installed
  ✓ Docker daemon running
  ✓ Data directory writable
  ✓ Port 8080 free
  ✓ Sufficient disk space (need 512 MiB, have 45.2 GiB)

Starting stack…
  ✓ Compose file written
  ✓ Containers started
  ✓ Server ready on http://127.0.0.1:8080

JWT_SECRET=3f2a1c4e9b8d7f6a5e4d3c2b1a0f9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2
Store this secret; it is not printed again.

Installed "tasktracker". Server: http://127.0.0.1:8080
Admin: admin@company.com
If this host is a laptop, keep it plugged in, on Ethernet, and with the lid open: a laptop that sleeps is a server that is down.

Next: open http://127.0.0.1:8080 on this host and sign in as admin@company.com.

Provider lan is configured; run `tt expose --provider=lan` to publish it.
```

### What just happened

- **Database**: Chose SQLite (zero external dependencies). Data lives in `/var/lib/tasktracker/tasktracker.db`.
- **Provider**: Chose `lan` (default). The server is only reachable on this host for now.
- **Instance name**: `tasktracker` — used as the Docker Compose project name.
- **Admin**: Created the first admin user. No self-signup exists.
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
```

**Caveats:**
- Cloudflare Free/Pro/Business plans have a 100 MiB request-body limit. `ATTACHMENT_MAX_BYTES` is validated at startup; if you need larger uploads, you must use a different provider or Enterprise plan.
- DNS propagation for the new CNAME can take minutes to hours.

### What the QR code and `tasktracker://connect` link do

After a successful `tt expose`, you'll see:

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

Output:

```text
Instance: tasktracker
Mode: sqlite
Provider: cloudflare
Server: https://tasks.company.com (verified)
Port: 8080
Compose: /var/lib/tasktracker/docker-compose.yml
Image: tasktracker-server:v1.2.3
Containers:
  server  Up 2 hours (healthy)
  cloudflared  Up 2 hours
```

### Health check

```bash
tt doctor
```

Output:

```text
$ tt doctor
Running checks…
  ✓ containers   server, cloudflared running
  ✓ ready        HTTPS https://tasks.company.com/readyz → 200
  ✓ address      published on 127.0.0.1:8080
  ✓ database     sqlite: 12.3 MiB
  ✓ clock        server time within 1s of host
  ⊘ provider     cloudflare: run `tt expose --provider=cloudflare --domain tasks.company.com` to verify the URL; doctor does not retain the exposed name
  ✓ backup       manual procedure: sqlite3 /var/lib/tasktracker/tasktracker.db ".backup '/var/backups/tasktracker-$(date +%F).db'"
```

- `provider` is always `skip` because the install state doesn't store the exposed URL. Re-run `tt expose` to re-verify.

### View logs

```bash
tt logs              # all containers
tt logs server       # just the server
tt logs -f           # follow (Ctrl-C to stop)
```

### Upgrade

```bash
tt upgrade
```

Pulls the new image, recreates the container, and runs migrations. The JWT secret and data volume are preserved.

### Backup

**SQLite (single file):**

```bash
sqlite3 /var/lib/tasktracker/tasktracker.db \
  ".backup '/var/backups/tasktracker-$(date +%F).db'"
```

Or stop and copy:

```bash
sudo systemctl stop docker  # if using systemd Docker
cp /var/lib/tasktracker/tasktracker.db /var/backups/
# also copy -wal and -shm files if they exist
```

**Postgres:**

```bash
pg_dump -Fc -U tasktracker tasktracker > /var/backups/tasktracker-$(date +%F).dump
```

### Restore

**SQLite:** Replace the file while Docker is stopped, then start. The server re-applies migrations on startup.

**Postgres:**

```bash
sudo systemctl stop docker
pg_restore --clean --if-exists -U tasktracker -d tasktracker /var/backups/tasktracker-2026-10-13.dump
sudo systemctl start docker
```

> **Test a restore at least once.** A backup nobody has restored is a hypothesis.

### Uninstall

```bash
tt uninstall
```

You must type the instance name to confirm. This stops and removes containers and the compose file. **The data volume is NOT deleted** — the SQLite file or Postgres volume remains. Delete it manually if you want a clean slate.

```text
$ tt uninstall
This will stop and remove the "tasktracker" instance. Type the instance name to confirm: tasktracker
Removed.
```

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