# User Management

This server has **no self-signup**. Every user is created by an admin. The
first admin is bootstrapped from the command line; after that, all user
management happens through the admin API.

This is a deliberate design for a self-hosted, single-organisation tool: the
operator decides who has access, and there is no open registration surface to
abuse.

## Roles

| Role | Can manage users? | Can use the system? |
| --- | --- | --- |
| `admin` | Yes — create, list, update, deactivate | Yes |
| `member` | No | Yes |

The role is stored in the `users.role` column (migration `0003`, both drivers)
and carried in the JWT `role` claim. The `AdminOnly` middleware gates
`/v1/admin/*` routes; `/v1/users/search` and the sync endpoints are open to any
authenticated user.

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

You can also create a `member` by omitting `--admin`. The command works
whether or not the server is running, because it writes to the database
directly.

In Docker:

```bash
docker exec -it <container-name> /app/server user-create \
  --email admin@example.com --password '...' --admin
```

## Admin API

All admin routes require a valid access token whose `role` claim is `admin`.
They are mounted under `/v1/admin/users`.

### Create a user

```
POST /v1/admin/users
Authorization: Bearer <access-token>
Content-Type: application/json

{
  "email": "jane@example.com",
  "password": "her-password",
  "display_name": "Jane Doe",
  "position": "Designer",
  "role": "member"
}
```

| Field | Required | Default |
| --- | --- | --- |
| `email` | yes | — |
| `password` | yes | — |
| `display_name` | no | `""` |
| `position` | no | `""` |
| `role` | no | `"member"` |

**Responses:**

| Status | Body |
| --- | --- |
| `201` | [user object](#user-object) |
| `400` | `{"error": "email and password are required"}` |
| `409` | `{"error": "email already registered"}` |

### List all users

```
GET /v1/admin/users
Authorization: Bearer <access-token>
```

Returns `200` with an array of [user objects](#user-object), ordered by
`created_at` descending.

### Update a user

```
PATCH /v1/admin/users/{id}
Authorization: Bearer <access-token>
Content-Type: application/json

{
  "display_name": "Jane Smith",
  "position": "Senior Designer",
  "role": "admin",
  "status": "inactive"
}
```

All fields are optional — only the fields present in the request body are
updated. `role` accepts `"admin"` or `"member"`.

**Responses:**

| Status | Body |
| --- | --- |
| `200` | updated [user object](#user-object) |
| `400` | `{"error": "role must be 'admin' or 'member'"}` |
| `404` | `{"error": "user not found"}` |

### Deactivate a user

```
DELETE /v1/admin/users/{id}
Authorization: Bearer <access-token>
```

Sets `status` to `"inactive"` but does not delete the row, preserving
referential integrity in `sync_log` and `project_members`.

A user cannot deactivate themselves — the endpoint returns `400` if the
caller's ID matches `{id}`.

**Responses:**

| Status | Body |
| --- | --- |
| `204` | (empty) |
| `400` | `{"error": "cannot deactivate yourself"}` |
| `404` | `{"error": "user not found"}` |

## Directory search

Available to any authenticated user (admin or member). The KMP client uses this
to find teammates when assigning work items.

```
GET /v1/users/search?q=jane
Authorization: Bearer <access-token>
```

Searches `email` and `display_name` case-insensitively, limited to 50 results.
Returns `200` with an array of [user objects](#user-object).

If `q` is empty or missing, returns `400`.

## User object

All user-facing responses use this shape:

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "email": "jane@example.com",
  "display_name": "Jane Doe",
  "position": "Designer",
  "avatar_id": "",
  "status": "active",
  "role": "member",
  "last_seen": 1700000000000,
  "created_at": 1699000000000
}
```

| Field | Type | Notes |
| --- | --- | --- |
| `id` | string (UUID) | server-generated |
| `email` | string | lower-cased |
| `display_name` | string | may be empty |
| `position` | string | may be empty |
| `avatar_id` | string | may be empty |
| `status` | string | `"active"` (default) or `"inactive"` |
| `role` | string | `"admin"` or `"member"` |
| `last_seen` | int64 (epoch millis) | nullable; omitted when null |
| `created_at` | int64 (epoch millis) | server-set on creation |

`password_hash` is never included in any response.

## Token lifecycle

| Action | What happens to tokens |
| --- | --- |
| Sign-in (`POST /v1/auth/login`) | A new token family is started; access + refresh issued |
| Refresh (`POST /v1/auth/refresh`) | The presented refresh token is revoked and replaced in the same family |
| Sign-out (`POST /v1/auth/signout`) | The entire token family is revoked |
| Admin deactivates a user | The user's refresh tokens are not auto-revoked; their access token expires per TTL |

Sign-out is idempotent: it returns `204` whether or not the token was valid,
and the client calls it best-effort before clearing local state.

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
| Route wiring | `cmd/server/main.go` (`runServer`) |
| Migration 0003 (role column) | `internal/db/migrations/0003_user_roles.{postgres,sqlite}.sql` |
