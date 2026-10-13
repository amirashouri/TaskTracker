---
name: go-config-and-secrets
description: Use when adding or reading a configuration value, a .env variable, an environment variable, or a secret such as JWT_SECRET in TaskTrackerServer. Covers the config loader, validation, and the rule that secrets never enter the repository.
related:
  - go-server-architecture
  - go-migrations-and-repositories
---

# Configuration and secrets

Configuration is environment-only. `internal/config` is the single reader; no
other package calls `os.Getenv`.

## How loading works

`config.Load()`:

1. Reads `ENV_FILE` (default `.env`) and seeds the process environment from it
   with `loadEnvFile`. The file is optional, and a key already present in the
   environment is **not** overwritten — real environment variables always win.
2. Reads each value through `getEnv`, `getDurationEnv` or `getCSVEnv`.
3. Calls `validate`, which stops startup on a missing database URL or a
   `JWT_SECRET` shorter than 32 bytes.

`Load` returns `(*Config, error)`. `main.go` treats a non-nil error as fatal.

## Adding a configuration value

1. Add it to `.env.example` with an empty or example value, commented if the
   meaning is not obvious. This is the file operators read.
2. Add the field to `Config` and read it in `Load` with the right helper:
   `getEnv` (string), `getDurationEnv` (a `time.Duration`, e.g. `1h`),
   `getCSVEnv` (comma-separated list).
3. If an invalid value must stop startup, add the rule to `Config.validate` and
   return an error naming the variable.
4. Add or extend a test in `internal/config/config_test.go`.

## The secret rule

**No secret is ever committed.** `.env` is git-ignored; only `.env.example`,
which contains no real values, is tracked.

- Local: `cp .env.example .env`, then generate a secret with
  `openssl rand -hex 32`.
- Production: real environment variables override the file, so a host injects
  secrets without a file on disk — systemd `EnvironmentFile` (mode `0600`),
  Docker/Compose `env_file` or secrets, or masked GitLab CI/CD variables.
- A `.env` kept on a server must be `chmod 600`.
- Never log a secret, echo it in a command that lands in CI output, or paste it
  into an issue. Rotating `JWT_SECRET` invalidates every issued token.

If you find a secret already committed, do not just delete it in a new commit —
the value stays in history. Rotate it, then remove it.

## Testing anything that reads the environment

Tests must not depend on the developer's real `.env`. Follow
`internal/config/config_test.go`:

- Point `ENV_FILE` at a file in `t.TempDir()`.
- Use `t.Setenv`, or the file's `isolate` helper, which clears a variable and
  restores its previous state in `t.Cleanup`.
- Because `loadEnvFile` only sets variables that are absent, a test that wants a
  file value must clear the variable first.

## Common mistakes

- Calling `os.Getenv` outside `internal/config`.
- Adding a variable to the code but not to `.env.example`, so operators never
  learn it exists.
- Giving a secret a safe-looking default. That is how `JWT_SECRET` ended up with
  a placeholder default in the scaffold; there is no default now, and that is
  deliberate.
