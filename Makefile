.DEFAULT_GOAL := up
COMPOSE ?= docker compose
RZD_WEB_PORT ?= 8080
export RZD_WEB_PORT

.PHONY: up down logs ps key check

up:
	$(COMPOSE) up --build --detach --wait --wait-timeout 180
	@echo "Open http://localhost:$(RZD_WEB_PORT)/#simulation"
	@$(MAKE) --no-print-directory key

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs --follow --tail 100

ps:
	$(COMPOSE) ps --all

key:
	$(COMPOSE) run --rm --no-deps --entrypoint python bootstrap /usr/local/bin/show-key.py

check:
	$(COMPOSE) config --quiet
