# Project custom tags — catalog on the project, ids on the work item

- **status:** done
- **priority:** P2
- **plan:** no `docs/PLAN.md` entry — standalone cross-repo feature. The client half is
  [`../../TaskTrackerKMP/specs/project-custom-tags.md`](../../TaskTrackerKMP/specs/project-custom-tags.md).

## Scope

- `internal/entities` — `TagID`, `ProjectTag`, `Project.Tags`, `WorkItem.TagIDs`, and the
  read/write paths for both.
- `internal/db/migrations` — `0007_project_tags.{sqlite,postgres}.sql`.
- `cmd/server` — contract tests for the widened project and work-item payloads.
- `docs/API.md` — the project object's `tags` field and the create/PATCH rules.

No new route, sync entity, or sync scope: the catalog is part of the project document
that the admin API and the change feed already carry.

## Contract

- [x] A project row stores its tag catalog and returns it on every route that answers a
      project object: list, get, create, patch and the sync feed.
- [x] A work item stores `tag_ids` and returns `tagIDs` as a non-null array, because the
      sync feed re-serves the payload as JSON and the client's list field cannot decode
      `null`.
- [x] A missing or unreadable tag blob decodes to an empty, non-nil catalog rather than
      failing the project.
- [x] The admin PATCH replaces the catalog wholesale; the create route ignores it and
      starts empty.
- [x] The admin response key set still equals the sync payload's key set — the contract
      test seeds the feed with a client-shaped payload that now includes `tags`, and it is
      green.
- [x] Reverting the mapping (dropping `tags`/`tagIDs`) turns the entity and contract tests
      red.

## Validation

- Run: `go test` for the packages this task touches — `internal/db`, `internal/entities`,
  `cmd/server` and `cmd/tt`. `scripts/check.sh` is the release gate; it sweeps every
  package.
- Counts: the server has no host matrix and no baseline file; the full suite is green.
- The tests are behavioural/contract tests, not pixel tests, so no `test-reviewer` pass is
  required.

## Handoff

- **Done:** schema migration, entity mapping, non-nil JSON arrays, admin/sync payloads and
  the contract-test seed are implemented. `scripts/check.sh` is green.
- **Pending:** none in this repo.
- **Next step:** merge the server PR after the client lands (the client can land first; it
  decodes a project without `tags` with a default).
- **Gotchas:**
  - `color` and `icon` are opaque strings on the server by design. Do not validate them
    against a server-side enum; a newer client must be able to add a value without a
    migration.
  - `TagIDs` and `BlockedByIDs` must stay non-nil on scan, or the feed emits `null` and an
    installed client fails to decode the payload.
  - The catalog is admin-owned UI, but the sync push still writes the project document as
    sent. A member push echoes the catalog it holds; that is accepted, not a privilege
    escalation, and the admin edit is the authoritative writer.

## Risks / notes

- Deleting a tag does not rewrite work items. Stale ids are tolerated by every reader, so
  a cleanup job would be the only thing that ever needed to resolve them.
