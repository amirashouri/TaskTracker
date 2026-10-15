# User Management

This server has **no self-signup**. Every user is created by an admin. The first
admin is bootstrapped from the command line; after that, all user management
happens through the admin API.

This is a deliberate design for a self-hosted, single-organisation tool: the
operator decides who has access, and there is no open registration surface to
abuse.

**This document is the operational guide** — who may do what, how to get the
first account, and what happens to a session over time. The request and response
shapes live in [`API.md`](API.md), which is the single place the wire contract is
written down.

## Roles

| Role | Can manage users? | Can use the system? |
| --- | --- | --- |
| `admin` | Yes — create, list, update, deactivate | Yes |
| `member` | No | Yes |

The role is stored in the `users.role` column (migration `0003`, both drivers)
and carried in the JWT `role` claim. The `AdminOnly` middleware gates
`/v1/admin/*` routes; `/v1/users/search` and the sync endpoints are open to any
authenticated user.

A user's role is re-read from the token on every request, so promoting or demoting
someone takes effect on their next token — not retroactively on the one they are
holding.

## Bootstrapping the first admin

On a fresh install there are no users, so sign-in cannot succeed. The CLI
subcommand creates the first one without an HTTP endpoint:

```bash
go run ./cmd/server user-create \
  --email admin@example.com \
  --password 'a-strong-password' \
  --display-name 'Admin' \
  --admin
```

Flags:

| Flag | Required | Default | Description |
| --- | --- | --- | --- |
| `--email` | yes | — | login email; stored lower-cased |
| `--password` | yes | — | plain-text; hashed with bcrypt before storage |
| `--display-name` | no | `""` | shown in the directory and responses |
| `--position` | no | `""` | job title, shown in the directory |
| `--admin` | no | `false` | sets role to `admin` |

The command:

1. Loads configuration (`.env` + environment, same as the server).
2. Connects to `DATABASE_URL` and runs migrations.
3. Checks that the email is not already registered.
4. Hashes the password with bcrypt (`DefaultCost`).
5. Inserts the user and prints `Created user <email> (id=..., role=...)`.

You can also create a `member` by omitting `--admin`. The command works whether
or not the server is running, because it writes to the database directly.

Because it loads the same configuration as the server, it needs the same
`JWT_SECRET`. Under systemd the unit's `EnvironmentFile` is not inherited by a
one-off command, so pass it explicitly — see
[`SELF-HOSTING.md`](SELF-HOSTING.md) for the exact invocation.

In Docker the same subcommand runs inside the server container. With Compose,
`exec` into the service — `server-pg` for the Postgres profile, `server-sqlite`
for the SQLite one — so it inherits the compose environment (`DATABASE_URL`,
`JWT_SECRET`) and needs nothing else:

```bash
docker compose --profile postgres exec server-pg \
  /app/server user-create \
  --email admin@example.com \
  --password 'a-strong-password' \
  --display-name 'Admin' \
  --admin
```

With a plain `docker run` (no Compose), exec against the container by name:

```bash
docker exec -it <container-name> /app/server user-create \
  --email admin@example.com --password '...' --admin
```

## Deactivating a user

`DELETE /v1/admin/users/{id}` sets `status` to `"inactive"` rather than deleting
the row, which preserves referential integrity in `sync_log` and
`project_members`. A caller cannot deactivate themselves.

Deactivation does **not** revoke the user's existing tokens. Their access token
stays usable until it expires, and their refresh token keeps working until it
does — so a deactivated user can still rotate. Treat deactivation as removing
future access, and see the token lifecycle below for what that means in practice.

## Token lifecycle

| Action | What happens to tokens |
| --- | --- |
| Sign-in (`POST /v1/auth/login`) | A new token family is started; access + refresh issued |
| Refresh (`POST /v1/auth/refresh`) | The presented refresh token is revoked and replaced in the same family |
| Sign-out (`POST /v1/auth/signout`) | The entire token family is revoked |
| Admin deactivates a user | The user's refresh tokens are not auto-revoked; their access token expires per TTL |

Refresh tokens rotate, and a token that was already rotated is treated as theft:
presenting one revokes its whole family. That is what makes a stolen refresh
token worth at most one use, and it is also why the client must not race two
refreshes with the same token — a legitimate second use looks exactly like a
replay.

Sign-out is idempotent: it returns `204` whether or not the token was valid, and
the client calls it best-effort before clearing local state.

## End-to-end flow

```
1. Bootstrap    →  go run ./cmd/server user-create --email admin@... --password ... --admin
2. Sign in      →  POST /v1/auth/login  →  access_token + refresh_token
3. Create users →  POST /v1/admin/users  (admin token required)
4. New users sign in independently with their own credentials
5. Any user searches the directory →  GET /v1/users/search?q=...
6. Admin updates or deactivates as needed →  PATCH / DELETE /v1/admin/users/{id}
```

## Implementation reference

| Component | File |
| --- | --- |
| Role constants & User model | `internal/entities/models.go` |
| User repository (CRUD, UpdateRole, SetStatus, Search) | `internal/entities/user_repository.go` |
| JWT claims with role | `internal/auth/auth.go` |
| Auth + AdminOnly middleware | `internal/middleware/middleware.go` |
| Admin handler (Create, List, Update, Deactivate, Search) | `internal/admin/handler.go` |
| CLI bootstrap command | `cmd/server/main.go` (`createUserCmd`) |
| Route wiring | `cmd/server/main.go` (`mount`) |
| Migration 0003 (role column) | `internal/db/migrations/0003_user_roles.{postgres,sqlite}.sql` |
| Contract tests | `cmd/server/contract_test.go` |
