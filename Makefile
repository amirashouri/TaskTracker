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

.DEFAULT_GOAL := help
.PHONY: help up rebuild down destroy logs ps tt release release-tt

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
		'  make tt                 build the tt CLI for this host' \
		'  make release VERSION=v1.2.3  build static tt binaries for every platform' \
		'  make release-tt VERSION=v1.2.3  build the curl-installable tt + server bundles'

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
