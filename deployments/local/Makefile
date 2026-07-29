# Local development stack for the Temporal platform spine.
#
#   make up       build/start API, worker, Postgres, Temporal (+UI), and NATS
#   make health   block until every service reports healthy
#   make ps       show container status
#   make logs     tail all service logs
#   make down     stop and remove containers (keeps data)
#   make down VOLUMES=1   also delete the Postgres volume

COMPOSE ?= docker compose
FILE := $(dir $(lastword $(MAKEFILE_LIST)))compose.yaml

.PHONY: up down config health ps logs

up:
	$(COMPOSE) -f $(FILE) up -d --build

down:
	$(COMPOSE) -f $(FILE) down $(if $(VOLUMES),--volumes,)

config:
	$(COMPOSE) -f $(FILE) config --quiet

ps:
	$(COMPOSE) -f $(FILE) ps

logs:
	$(COMPOSE) -f $(FILE) logs -f

# health polls docker's own health status for each service that declares a
# healthcheck, then confirms the Temporal frontend answers. It exits non-zero if
# any service is not healthy within the compose-defined retry windows.
health:
	@echo "waiting for postgres, temporal, nats, and api to become healthy..."
	@for svc in managed-agent-postgres managed-agent-temporal managed-agent-nats managed-agent-api; do \
		printf "  %-28s " "$$svc"; \
		for i in $$(seq 1 60); do \
			state=$$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' $$svc 2>/dev/null || echo missing); \
			if [ "$$state" = "healthy" ]; then echo "healthy"; break; fi; \
			if [ "$$i" = "60" ]; then echo "NOT healthy ($$state)"; exit 1; fi; \
			sleep 2; \
		done; \
	done
	@echo "all services healthy"
