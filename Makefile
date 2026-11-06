# TaskTrackerServer — operator shortcuts for the compose shapes and the tt CLI.
#
# Every target names an explicit --profile: docker-compose.yml puts both services behind
# profiles, so none of them is enabled without one.
COMPOSE ?= docker compose
PROFILE ?= postgres
VERSION ?= dev
DIST ?= dist
PLATFORMS ?= darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64
# release-tt bundles a static tt and the server binary it supervises. The
# server binary uses CGO for the SQLite driver, so it only runs on the host it
# was built for: the bundle is per-platform, and the tarball is what the curl
# one-liner fetches. Linux/amd64 is the only platform with a server binary
# today; the other platforms ship tt alone and install with --runtime docker.
SERVER_PLATFORMS ?= linux/amd64
# The compose service holding the server for the selected profile, so the admin
# shortcuts reach whatever `make up` started.
SERVER_SERVICE ?= $(if $(filter sqlite,$(PROFILE)),server-sqlite,$(if $(filter expose,$(PROFILE)),server-pg-expose,server-pg))

.DEFAULT_GOAL := help
.PHONY: help up rebuild down destroy logs ps admin admin-reset-password tt release release-tt clean-desktop-cache

help:
	@printf '%s\n' \
		'TaskTrackerServer — Docker and CLI shortcuts (PROFILE=postgres by default)' \
		'' \
		'  make up                 start the server, detached' \
		'  make up PROFILE=sqlite  start the single-container SQLite stack instead' \
		'  make rebuild            start and rebuild the image from the working tree' \
		'  make down               stop and remove everything this project started' \
		'  make destroy            make down, then delete the database volume (destroys data)' \
		'  make logs               follow the server logs' \
		'  make ps                 show the containers this project has' \
		'  make admin              list users and roles in the running stack' \
		'  make admin-reset-password EMAIL=... PASSWORD=...  change an existing password' \
		'  make tt                 build the tt CLI for this host' \
		'  make release VERSION=v1.2.3  build static tt binaries for every platform' \
		'  make release-tt VERSION=v1.2.3  build the curl-installable tt + server bundles' \
		'  make clean-desktop-cache  uninstall the desktop app: remove TaskTracker.app, its macOS state (Application Support/Caches/Preferences), and the KMP build caches' \
		''

up:
	$(COMPOSE) --profile $(PROFILE) up -d

rebuild:
	$(COMPOSE) --profile $(PROFILE) up -d --build

# Every profile, so this closes the stack that is actually running rather than the one
# this shell last named: a `down` scoped to one profile leaves the other profiles'
# containers up.
down:
	$(COMPOSE) --profile postgres --profile sqlite --profile expose down --remove-orphans

destroy:
	$(COMPOSE) --profile postgres --profile sqlite --profile expose down -v --remove-orphans

logs:
	$(COMPOSE) --profile $(PROFILE) logs -f --tail=200

ps:
	$(COMPOSE) --profile postgres --profile sqlite --profile expose ps

# `tt admin show` reads the state file `tt install` writes, and a compose dev
# stack has none. These run the same server subcommands inside the running
# container, so a dev instance can be inspected and repaired with make alone.
admin:
	$(COMPOSE) --profile $(PROFILE) exec -T $(SERVER_SERVICE) /app/server admin-list

# The password is an argument because the container has no TTY; this is a dev
# convenience, not a way to keep a password out of the shell's history.
admin-reset-password:
	@test -n "$(EMAIL)" || { echo "make admin-reset-password: set EMAIL=<account email>"; exit 2; }
	@test -n "$(PASSWORD)" || { echo "make admin-reset-password: set PASSWORD=<new password>"; exit 2; }
	$(COMPOSE) --profile $(PROFILE) exec -T $(SERVER_SERVICE) /app/server user-set-password \
		--email "$(EMAIL)" --password "$(PASSWORD)"

tt:
	@mkdir -p $(DIST)
	go build -trimpath -o $(DIST)/tt ./cmd/tt

# Static because a customer installing a runtime first is where the funnel
# dies. The tag in the binary is the server image tag it will pull.
release:
	@mkdir -p $(DIST)
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		out=$(DIST)/tt-$$os-$$arch; \
		if [ "$$os" = "windows" ]; then out=$$out.exe; fi; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
			-ldflags "-s -w -X main.version=$(VERSION)" \
			-o $$out ./cmd/tt || exit 1; \
	done

# release-tt is the curl-installable bundle: a static tt and the server binary
# it supervises, zipped per platform. The server binary is built with CGO for
# the SQLite driver, so it runs only on the hosts it was built for; the bundle
# is therefore per-platform, and the tarball is what the curl one-liner
# fetches. A customer who wants a server they can fetch over HTTPS and
# supervise with systemd gets one tarball per platform.
release-tt: release
	@mkdir -p $(DIST)/bundle
	@for platform in $(SERVER_PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		tt=tt-$$os-$$arch; \
		if [ "$$os" = "windows" ]; then tt=$$tt.exe; fi; \
		cp $(DIST)/$$tt $(DIST)/bundle/$$tt || exit 1; \
		CGO_ENABLED=1 GOOS=$$os GOARCH=$$arch go build -trimpath \
			-ldflags "-s -w -X main.version=$(VERSION)" \
			-o $(DIST)/bundle/server ./cmd/server || exit 1; \
		tar -C $(DIST)/bundle -czf $(DIST)/tt-$$os-$$arch.tar.gz \
			$$tt server || exit 1; \
		rm -f $(DIST)/bundle/$$tt $(DIST)/bundle/server; \
		echo "bundled $$tt + server -> $(DIST)/tt-$$os-$$arch.tar.gz"; \
	done

# The desktop app ships as a Compose Multiplatform JVM app living next door in
# TaskTrackerKMP. That directory can pick up stale `build/` and Gradle caches,
# which are not part of the source tree and do not belong in a clean checkout or
# a fresh IDE workspace. This target removes those, and — because it exists to
# produce a genuinely fresh install — the installed app and everything the app
# wrote while running.
# Run from TaskTrackerServer; resolves the client root relative to this Makefile.
KMP_ROOT ?= $(if $(wildcard ../TaskTrackerKMP),../TaskTrackerKMP,$(error set KMP_ROOT=<path-to-TaskTrackerKMP>))

# The three places a macOS Desktop install touches outside the source tree:
#
#   /Applications/TaskTracker.app   the bundle `jpackage` produced from
#                                   `nativeDistributions.packageName`, which is what
#                                   the user launched. Removing build outputs alone
#                                   leaves this — and therefore the old binary —
#                                   in place.
#   ~/Library/Application Support/  the app's data: TaskTracker.store and its
#     TaskTracker                   -shm/-wal/.lck companions, the attachments blob
#                                   root, preferences.properties.
#   ~/Library/{Caches,Preferences,  state AppKit and the JVM write for a launched
#     HTTPStorages,Saved Application app, keyed by the app's name or bundle id.
#     State,WebKit}
#
# The source tree at $(KMP_ROOT) is the app you built; these paths are the app you
# ran. A stale one is why a fresh build can behave exactly like the old one — a
# store written by a build that predates a schema change, or a cached session that
# keeps replaying a request the current client builds differently.
#
# Idempotent: a second run reports each path as already absent instead of failing.
DESKTOP_APP_BUNDLES ?= /Applications/TaskTracker.app $(HOME)/Applications/TaskTracker.app
DESKTOP_APPLICATION_SUPPORT_DIR ?= $(HOME)/Library/Application Support/TaskTracker
# `preferencesFile()` resolved one level above the app folder before that was
# fixed, so an older build wrote its preferences — cached profile, appearance,
# window geometry — into the shared Application Support root. Removed while it may
# still be there, since the current build reads the file inside TaskTracker/ and
# would otherwise leave the old one behind forever.
DESKTOP_LEGACY_PREFERENCES ?= $(HOME)/Library/Application Support/preferences.properties

clean-desktop-cache:
	@echo "==> stopping a running TaskTracker"
	@-pkill -f 'TaskTracker\.app/Contents/MacOS' 2>/dev/null || true
	@echo "==> removing the installed app"
	@for bundle in $(DESKTOP_APP_BUNDLES); do \
		if [ -e "$$bundle" ]; then rm -rf "$$bundle" && echo "removed $$bundle"; \
		else echo "already absent: $$bundle"; fi; \
	done
	@echo "==> removing the app's persisted state"
	@for path in "$(DESKTOP_APPLICATION_SUPPORT_DIR)" "$(DESKTOP_LEGACY_PREFERENCES)"; do \
		if [ -e "$$path" ]; then rm -rf "$$path" && echo "removed $$path"; \
		else echo "already absent: $$path"; fi; \
	done
	@for dir in "$(HOME)/Library/Caches" "$(HOME)/Library/Preferences" "$(HOME)/Library/HTTPStorages" "$(HOME)/Library/Saved Application State" "$(HOME)/Library/WebKit"; do \
		[ -d "$$dir" ] || continue; \
		for hit in "$$dir"/*[Tt]ask[Tt]racker* "$$dir"/*"Task Tracker"*; do \
			[ -e "$$hit" ] || continue; \
			case "$$hit" in *com.tasktracker.tests.*) continue ;; esac; \
			rm -rf "$$hit" && echo "removed $$hit"; \
		done; \
	done
	@echo "==> removing build outputs and Gradle caches in $(KMP_ROOT)"
	@cd $(KMP_ROOT) && rm -rf build .gradle androidApp/build build-logic/build build-logic/convention/build composition-root/build iosApp/build packages/*/build shared-ui/build
	@cd $(KMP_ROOT) && find . -name '.DS_Store' -delete
	@echo "desktop app and cache removed"
