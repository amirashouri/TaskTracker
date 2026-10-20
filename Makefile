# TaskTrackerServer — operator shortcuts for the two compose deployment shapes.
#
# Every target names an explicit --profile: docker-compose.yml puts both services behind
# profiles, so none of them is enabled without one.
COMPOSE ?= docker compose
PROFILE ?= postgres

.DEFAULT_GOAL := help
.PHONY: help up rebuild down destroy logs ps

help:
	@printf '%s\n' \
		'TaskTrackerServer — Docker shortcuts (PROFILE=postgres by default)' \
		'' \
		'  make up                 start the server, detached' \
		'  make up PROFILE=sqlite  start the single-container SQLite stack instead' \
		'  make rebuild            start and rebuild the image from the working tree' \
		'  make down               stop and remove everything this project started' \
		'  make destroy            make down, then delete the database volume (destroys data)' \
		'  make logs               follow the server logs' \
		'  make ps                 show the containers this project has'

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
