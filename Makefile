# Repository-level development and deployment entry points.

GO ?= go
DOCKER ?= docker
COMPOSE ?= docker compose
GOLANGCI_LINT ?= golangci-lint

BIN_DIR ?= bin
BINARY ?= $(BIN_DIR)/managed-agent
IMAGE ?= managed-agent-go:local
VERSION ?= dev
REVISION ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
GOPROXY ?=
LINT_BASE ?= origin/main

DOCKER_BUILD_ARGS := --build-arg VERSION=$(VERSION) --build-arg REVISION=$(REVISION)
ifneq ($(strip $(GOPROXY)),)
DOCKER_BUILD_ARGS += --build-arg GOPROXY=$(GOPROXY)
endif

.DEFAULT_GOAL := help

.PHONY: help build lint test test-race vet verify docs-check image image-smoke \
	local-config local-up local-down local-health local-ps local-logs

help:
	@echo "Development"
	@echo "  make build          build $(BINARY)"
	@echo "  make lint           lint changes relative to $(LINT_BASE)"
	@echo "  make test           run unit tests"
	@echo "  make test-race      run tests with the race detector"
	@echo "  make vet            run go vet"
	@echo "  make verify         run the core Go checks"
	@echo "  make docs-check     install and verify documentation dependencies"
	@echo
	@echo "Container"
	@echo "  make image          build $(IMAGE)"
	@echo "  make image-smoke    build the image and verify its entrypoint"
	@echo
	@echo "Local stack"
	@echo "  make local-up       build and start the local stack"
	@echo "  make local-health   wait for local services to become healthy"
	@echo "  make local-ps       show local service status"
	@echo "  make local-logs     follow local service logs"
	@echo "  make local-down     stop the stack (VOLUMES=1 also removes data)"

build:
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -o $(BINARY) ./cmd/managed-agent

lint:
	$(GOLANGCI_LINT) run --new-from-rev=$(LINT_BASE) ./...

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

verify: lint test test-race vet

docs-check:
	npm --prefix website ci
	npm --prefix website run typecheck
	npm --prefix website run build

image:
	$(DOCKER) build $(DOCKER_BUILD_ARGS) --tag $(IMAGE) .

image-smoke: image
	$(DOCKER) run --rm $(IMAGE) serve -h >/dev/null

local-config:
	$(MAKE) -C deployments/local config COMPOSE='$(COMPOSE)'

local-up:
	$(MAKE) -C deployments/local up COMPOSE='$(COMPOSE)'

local-down:
	$(MAKE) -C deployments/local down COMPOSE='$(COMPOSE)' VOLUMES='$(VOLUMES)'

local-health:
	$(MAKE) -C deployments/local health COMPOSE='$(COMPOSE)'

local-ps:
	$(MAKE) -C deployments/local ps COMPOSE='$(COMPOSE)'

local-logs:
	$(MAKE) -C deployments/local logs COMPOSE='$(COMPOSE)'
