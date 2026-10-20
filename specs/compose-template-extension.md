# Compose template extension — keep editors from parsing a Go template as YAML

- **status:** done
- **priority:** P3
- **plan:** none — standalone chore; no roadmap entry and no behaviour change.

## Scope

Rename the embedded Compose template so YAML-aware editors stop reporting its
Go directives as syntax errors. The rendered file keeps its name and its bytes;
only the source template's extension changes.

## Files

- `internal/install/embed/docker-compose.tmpl` — renamed from
  `docker-compose.yml`; content unchanged.
- `internal/install/compose.go` — `composeTemplatePath` points at the new name,
  with a comment explaining why.
- `specs/compose-template-extension.md` — this record.

## Contract

- [x] The embedded template no longer carries a `.yml` name, so an IDE does not
      parse `{{ if … }}` as YAML.
- [x] `RenderCompose` output is byte-identical for `sqlite` and `postgres`
      installs aside from freshly generated secrets.
- [x] The rendered file a customer receives is still `docker-compose.yml`
      (`State.ComposePath` is unchanged).
- [x] `go test ./...` and `scripts/check.sh` stay green; no other reference to
      the old embedded path remains.

## Validation

- `scripts/check.sh` — green.
- Rendered both modes before and after the rename, normalized the generated
  `JWT_SECRET`/`POSTGRES_PASSWORD` values, and diffed: no differences.
- `rg "embed/docker-compose.yml"` — no matches.
- The rendered `sqlite` and `postgres` files still pass `docker compose config`.

## Handoff

- **Done:** the template is `embed/docker-compose.tmpl` and the embed path is
  updated. Rendered output is unchanged.
- **Pending:** nothing; the branch is local and unpushed.
- **Next step:** push/merge if desired, then the client-side T9.7 work.
- **Gotchas:** do not rename the rendered file — `State.ComposePath` and every
  customer repair depend on `docker-compose.yml`. The template name is free to
  change because `//go:embed embed` includes whatever the directory holds.

## Risks / notes

- The IDE warning was a false positive on a Go `text/template`, not a Compose
  error. The rendered file was already valid YAML.
- If a future task splits the template per driver, keep every template outside
  a `.yml` name for the same reason.
