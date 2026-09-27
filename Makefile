# Upsera developer tasks. `make help` lists them.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BIN     ?= bin/upsera
COMPOSE := docker compose -f deploy/docker-compose.yml --profile local-db

# colima: point Docker and testcontainers at its socket when it exists and
# DOCKER_HOST is not already set. Only passed to docker/test commands, never
# to the server (where DOCKER_HOST means the socket proxy).
COLIMA_SOCK := $(HOME)/.config/colima/default/docker.sock
ifeq ($(DOCKER_HOST),)
ifneq ($(wildcard $(COLIMA_SOCK)),)
DOCKER_ENV := DOCKER_HOST=unix://$(COLIMA_SOCK) TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock
endif
endif

.DEFAULT_GOAL := help
.PHONY: help build web server dev test test-short test-web lint fmt vet gen-api check \
	env docker-build up down logs ps restart clean

help: ## list targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

# ---- build ----

web/node_modules: web/package-lock.json
	npm --prefix web ci
	@touch web/node_modules

web: web/node_modules ## build the dashboard into web/dist/app
	npm --prefix web run build

server: ## build the server binary (embeds whatever web/dist/app holds)
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN) ./cmd/upsera

build: web server ## build dashboard + server into bin/upsera

dev: web/node_modules ## Vite dev server on :5173 (proxies /api to a server on :3080)
	npm --prefix web run dev

# ---- quality ----

test: ## Go tests incl. Postgres integration tests (needs Docker), race detector
	$(DOCKER_ENV) go test -race -count=1 ./...

test-short: ## Go unit tests only
	go test -short ./...

test-web: web/node_modules ## Vitest
	npm --prefix web test

vet: ## go vet
	go vet ./...

fmt: ## gofmt the Go code
	gofmt -w cmd internal web/*.go

lint: web/node_modules ## gofmt check, go vet, eslint + tsc
	@out=$$(gofmt -l cmd internal web/*.go); if [ -n "$$out" ]; then echo "needs gofmt:"; echo "$$out"; exit 1; fi
	go vet ./...
	npm --prefix web run lint
	cd web && npx tsc -b

gen-api: web/node_modules ## regenerate web/src/api/schema.d.ts from the Go API
	npm --prefix web run gen:api

check: lint test test-web web ## everything CI runs

# ---- docker ----

env: ## create deploy/.env with random secrets (local-db) if missing
	@if [ -f deploy/.env ]; then echo "deploy/.env exists, leaving it alone"; else \
		pw=$$(openssl rand -hex 16); secret=$$(openssl rand -base64 32); \
		sed -e "s|^APP_SECRET=.*|APP_SECRET=$$secret|" \
		    -e "s|change-me|$$pw|g" deploy/.env.example > deploy/.env; \
		echo "wrote deploy/.env"; fi

docker-build: ## build the upsera image
	$(DOCKER_ENV) UPSERA_VERSION=$(VERSION) $(COMPOSE) build upsera

up: env ## start upsera + Postgres + socket proxy on http://localhost:3080
	$(DOCKER_ENV) UPSERA_VERSION=$(VERSION) $(COMPOSE) up -d --build
	@echo "Upsera: http://localhost:$${UPSERA_PORT:-3080}"

down: ## stop the stack (keeps the database volume)
	$(DOCKER_ENV) $(COMPOSE) down

restart: ## rebuild and restart only the upsera service
	$(DOCKER_ENV) UPSERA_VERSION=$(VERSION) $(COMPOSE) up -d --build upsera

logs: ## follow upsera logs
	$(DOCKER_ENV) $(COMPOSE) logs -f upsera

ps: ## show stack status
	$(DOCKER_ENV) $(COMPOSE) ps

clean: ## remove build output (not the database volume; use `docker compose ... down -v` for that)
	rm -rf bin web/dist/app web/tsconfig.tsbuildinfo
