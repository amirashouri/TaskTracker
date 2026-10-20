# TaskTrackerServer — operator shortcuts for the compose shapes and the tt CLI.
#
# Every target names an explicit --profile: docker-compose.yml puts both services behind
# profiles, so none of them is enabled without one.
COMPOSE ?= docker compose
PROFILE ?= postgres
VERSION ?= dev
DIST ?= dist
PLATFORMS ?= darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

.DEFAULT_GOAL := help
.PHONY: help up rebuild down destroy logs ps tt release

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
		'  make release VERSION=v1.2.3  build static tt binaries for every platform'

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
