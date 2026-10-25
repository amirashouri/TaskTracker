#!/usr/bin/env bash
#
# The one place this project's release validation command lives. `AGENTS.md`
# describes the scopes; this file says what they run.
#
# Development runs focused package tests (`go test ./internal/<pkg>`); this script
# is the gate a release is cut against, not a per-task check.
#
#   scripts/check.sh          # the release gate (four checks, gofmt enforced)
#   scripts/check.sh race     # go test -race ./...  (what CI runs; needed for concurrency)
#   scripts/check.sh docs     # a docs-only change: nothing to build
#
# `gofmt -l .` on its own exits 0 even when it lists files, so the formatting check
# here fails on a non-empty list rather than printing it and moving on.
set -euo pipefail

cd "$(dirname "$0")/.."

scope="${1:-all}"
case "$scope" in
  all)
    unformatted="$(gofmt -l .)"
    if [ -n "$unformatted" ]; then
      printf 'gofmt: these files need formatting:\n%s\n' "$unformatted" >&2
      exit 1
    fi
    go build ./... && go vet ./... && go test ./...
    ;;
  race) exec go test -race ./... ;;
  docs) echo "docs-only change: nothing to build." ;;
  *)    echo "unknown scope: '$scope' (use all|race|docs)" >&2; exit 2 ;;
esac
