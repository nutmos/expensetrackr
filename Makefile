# Build/test helpers. Run from the repo root (layout modelled on
# prometheus-operator: go.mod at the root, binaries in cmd/, packages in pkg/).

GO        ?= go
BIN      ?= bin/expense-server
ADDR     ?= 127.0.0.1:8080
GOLANGCI_LINT ?= golangci-lint
GOVULNCHECK_VERSION ?= v1.9.0

# Local Postgres (compose.yaml). The URLs point at the databases it creates;
# set EXPENSE_DATABASE_URL / EXPENSE_TEST_DATABASE_URL to use another server.
PG_PORT  ?= 5432
export EXPENSE_PG_PORT ?= $(PG_PORT)
export EXPENSE_DATABASE_URL ?= postgres://expense:expense@127.0.0.1:$(PG_PORT)/expense?sslmode=disable
export EXPENSE_TEST_DATABASE_URL ?= postgres://expense:expense@127.0.0.1:$(PG_PORT)/expense_test?sslmode=disable

export GOTOOLCHAIN ?= local
export CGO_ENABLED ?= 0

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN { FS = ":.*##" } /^[a-zA-Z_-]+:.*##/ { printf "  %-10s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build the server binary to $(BIN).
	$(GO) build -o $(BIN) ./cmd/server

.PHONY: run
run: build ## Build, then run the server on $(ADDR) against EXPENSE_DATABASE_URL (make db-up first).
	./$(BIN) -addr $(ADDR)

.PHONY: test
test: ## Run all tests.
	$(GO) test ./...

.PHONY: test-race
test-race: ## Run all tests with the race detector (needs cgo and a C compiler).
	CGO_ENABLED=1 $(GO) test -race ./...

.PHONY: vulncheck
vulncheck: ## Report known vulnerabilities reachable from the code (govulncheck).
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

.PHONY: vet
vet: ## Run go vet.
	$(GO) vet ./...

.PHONY: fmt
fmt: ## Format Go code (gofmt -s -w).
	gofmt -s -w cmd pkg

.PHONY: fmt-check
fmt-check: ## Fail if any Go file is not gofmt-formatted.
	@out="$$(gofmt -s -l cmd pkg)"; if [ -n "$$out" ]; then echo "needs gofmt:"; echo "$$out"; exit 1; fi

.PHONY: lint
lint: ## Run golangci-lint (config in .golangci.yml).
	$(GOLANGCI_LINT) run ./...

.PHONY: check
check: fmt-check vet lint test ## fmt-check + vet + lint + test.

.PHONY: db-up
db-up: ## Start the local Postgres (compose.yaml) and wait until it is ready.
	docker compose up -d --wait db

.PHONY: db-down
db-down: ## Stop the local Postgres; its data is kept.
	docker compose down

.PHONY: db-reset
db-reset: ## Stop the local Postgres and DELETE its data volume.
	docker compose down --volumes

.PHONY: db-psql
db-psql: ## Open psql on the local development database.
	docker compose exec db psql -U expense expense

.PHONY: clean
clean: ## Remove build output (bin/). Never touches the database.
	rm -rf bin
