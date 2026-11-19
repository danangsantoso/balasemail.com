# BalasEmail — developer commands. Run `make help` for the list.
SHELL := /bin/bash
BIN   := bin
PKGS  := ./...
COVER_MIN ?= 55

# Load .env (if present) for run-* targets.
-include .env
export
TEST_DATABASE_URL ?= postgres://balasemail:balasemail@localhost:5432/balasemail_test?sslmode=disable

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n",$$1,$$2}'

.PHONY: build
build: ## Build api, worker and smtpd into ./bin
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN)/ ./cmd/...

.PHONY: run-api run-worker run-smtpd
run-api: ## Run the REST API locally
	go run ./cmd/api
run-worker: ## Run the background worker locally
	go run ./cmd/worker
run-smtpd: ## Run the SMTP gateway locally
	go run ./cmd/smtpd

.PHONY: fmt
fmt: ## Format code
	gofmt -w .

.PHONY: lint
lint: ## Static analysis (go vet + golangci-lint)
	go vet $(PKGS)
	golangci-lint run

.PHONY: test
test: ## Unit tests (no database required)
	go test -race -count=1 $(PKGS)

.PHONY: test-integration
test-integration: ## Integration tests against PostgreSQL (TEST_DATABASE_URL)
	DATABASE_URL="$(TEST_DATABASE_URL)" go test -tags integration -count=1 -p 1 ./internal/app/

.PHONY: cover
cover: ## Unit + integration coverage, fails below COVER_MIN%
	DATABASE_URL="$(TEST_DATABASE_URL)" go test -tags integration -count=1 -p 1 \
		-coverpkg=./internal/... -coverprofile=coverage.out \
		$$(go list -tags integration -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./internal/...)
	@total=$$(go tool cover -func=coverage.out | awk '/^total:/ {sub("%","",$$3); print $$3}'); \
	echo "coverage: $$total% (minimum $(COVER_MIN)%)"; \
	awk -v t=$$total -v m=$(COVER_MIN) 'BEGIN{exit !(t>=m)}' || (echo "coverage below threshold" && exit 1)

.PHONY: up down logs
up: ## Start the full stack (postgres, api, worker, smtpd, mailpit, prometheus)
	docker compose -f deploy/docker-compose.yml up -d --build
down: ## Stop the stack
	docker compose -f deploy/docker-compose.yml down
logs: ## Tail stack logs
	docker compose -f deploy/docker-compose.yml logs -f api worker smtpd

.PHONY: db
db: ## Start only PostgreSQL (for `make run-*` and integration tests)
	docker compose -f deploy/docker-compose.yml up -d postgres
