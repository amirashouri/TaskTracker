# Licensing — add the project license, ship it with the image and the release

- **status:** done
- **priority:** P2
- **plan:** `none` for standalone work — the license is a repository-level
  policy decision, not a roadmap entry. The *what* and the rationale live in
  this file.

## Scope

Repository-level policy and documentation, plus the two distribution
surfaces the server has: the Docker image a customer runs, and the GitHub
Release body. No Go code, no API, no wire format.

The client repo carries the same change under its own spec, and the two
`LICENSE` files are byte-identical: the terms a reader accepts on one side
are the terms they get on the other.

## Files

- `LICENSE` (new) — MIT + Commons Clause, plus one extra term: no rebranding.
- `README.md` — the `## License` section, which said "No license has been
  chosen — all rights reserved by default".
- `Dockerfile` — the runtime stage copies `LICENSE` into the image, so the
  terms travel with the binary a customer actually runs.
- `.github/workflows/release.yml` — the release body gains a `### License`
  section. The same body had two dead links, both fixed here.
- `specs/license-and-readme.md` (new) — this file.

## Contract

- [ ] `LICENSE` exists at the repository root and is byte-identical to the
      client repository's `LICENSE`.
- [ ] The license grants use, hosting, deployment, and modification for
      personal, commercial, and production team use.
- [ ] The license withholds the right to *Sell* the software (Commons
      Clause) and the right to redistribute it under another name, brand, or
      product identity (the no-rebranding term).
- [ ] The license states that the author retains copyright and that the
      TaskTracker name and logos are the author's trademarks.
- [ ] The README's `## License` section summarises the same three points and
      links to `LICENSE`.
- [ ] The Docker image contains the `LICENSE` file at `/app/LICENSE`.
- [ ] The release body has a `### License` section naming the same terms and
      linking to the repository's `LICENSE`.
- [ ] The release body's `docs/INSTALL.md` link resolves (it pointed at
      `amirashouri/TaskTrackerServer`, which does not exist — 404), and its
      client link points at `amirashouri/TaskTrackerKMP` (it pointed at this
      repository, i.e. the server's own releases).

## Validation

- `scripts/check.sh` — the full suite: `gofmt`, `go build`, `go vet`,
  `go test`. The change touches no Go file, but the Docker and workflow edits
  are distribution changes, and the deciding command is the whole suite.
- `scripts/check.sh docs` is *not* the applicable scope here: `Dockerfile`
  and the release workflow are not documentation.
- The Dockerfile's `COPY` is checked against `.dockerignore`, because that
  file excludes `*.md` and would silently drop a license named `LICENSE.md`.
- The release workflow's YAML parses, and the rendered body is read.

## Handoff

- **Done:**
  - `LICENSE` added — MIT + Commons Clause, plus a no-rebranding term and a
    trademark paragraph. Identical to the client repository's file.
  - `README.md` — `## License` rewritten to state the three terms and link
    to `LICENSE`.
  - `Dockerfile` — the runtime stage copies `LICENSE` to `/app/LICENSE`.
  - `.github/workflows/release.yml` — the release body gains a `### License`
    section, and its two dead links now resolve.
- **Pending:** Nothing.
- **Next step:** None — merge the PR.
- **Gotchas:**
  - `.dockerignore` excludes `*.md` but not an extensionless `LICENSE`, so the
    file is in the build context. A license named `LICENSE.md` would *not*
    be, and the `COPY` would fail the build — which is the good failure, but
    the reason to know it.
  - GitHub's license *detection* will not recognise this file (the Commons
    Clause and the extra term make it a modified MIT), so the repository page
    may show "Other" rather than "MIT". That is expected, not a mistake.
  - The release body's `docs/INSTALL.md` link used
    `amirashouri/TaskTrackerServer`, and the client link pointed at this
    repository. The repository is `amirashouri/TaskTracker`; the client is
    `amirashouri/TaskTrackerKMP`. Both are fixed in the same edit.

## Risks / notes

- The Commons Clause is not OSI-approved, so this is a source-available
  license, not an open source one. The license file says so.
- The Docker image is the server's only distribution channel — the release
  body says "The server is distributed as a Docker image" — so the image is
  where the license has to be for a customer to ever see it.
