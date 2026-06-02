# MX Sentinel — developer Makefile.
COMPOSE := docker compose -f deploy/docker-compose.yml
CONFIG  ?= deploy/config/mxsentinel.example.yaml
export MXS_CONFIG := $(CONFIG)

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: tidy
tidy: ## go mod tidy
	go mod tidy

.PHONY: build
build: ## Build all binaries into ./bin
	go build -o bin/ ./...

.PHONY: vet
vet: ## go vet
	go vet ./...

.PHONY: test
test: ## Run unit tests (no external services needed)
	go test ./...

.PHONY: lint
lint: ## Run golangci-lint (if installed)
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed; skipping"

.PHONY: fmt
fmt: ## Format code
	gofmt -w .

.PHONY: up
up: ## Start the local dev stack
	$(COMPOSE) up -d

.PHONY: down
down: ## Stop the local dev stack
	$(COMPOSE) down

.PHONY: logs
logs: ## Tail dev stack logs
	$(COMPOSE) logs -f

.PHONY: migrate
migrate: ## Apply all migrations (postgres + clickhouse)
	go run ./cmd/mxctl migrate up

.PHONY: migrate-status
migrate-status: ## Show migration status
	go run ./cmd/mxctl migrate status

.PHONY: seed
seed: ## Seed a demo tenant + domain
	go run ./cmd/mxctl seed

.PHONY: bus-ensure
bus-ensure: ## Create/update JetStream streams
	go run ./cmd/mxctl bus ensure

.PHONY: selftest
selftest: ## Publish + read back one of each event family
	go run ./cmd/mxctl bus selftest

.PHONY: run-dnsd
run-dnsd: ## Run the DNS validator (polls monitored domains every 60s)
	go run ./cmd/dnsd --interval 60s

.PHONY: replay
replay: ## Replay the sample maillog into the bus (static demo tenant, no DB needed)
	go run ./cmd/telemetryd --replay test/fixtures/maillog.sample --skip-db \
	  --tenant 00000000-0000-0000-0000-000000000001 --node-ip 198.51.100.5

.PHONY: ingest-dmarc
ingest-dmarc: ## Ingest the sample DMARC report once (needs a tenant owning example.com)
	go run ./cmd/dmarcd --file test/fixtures/dmarc/valid.xml

.PHONY: run-dmarcd
run-dmarcd: ## Watch ./dmarc-drop for DMARC report files and ingest them
	go run ./cmd/dmarcd --dir ./dmarc-drop

.PHONY: run-apid
run-apid: ## Run the REST API server on :8080
	go run ./cmd/apid

.PHONY: apikey
apikey: ## Create an API token for the demo tenant (printed once)
	go run ./cmd/mxctl apikey create --tenant demo

.PHONY: web-dev
web-dev: ## Run the Next.js dashboard dev server (needs NEXT_PUBLIC_API_TOKEN)
	cd web && npm install && npm run dev

.PHONY: bootstrap
bootstrap: up ## Start stack, then migrate + ensure streams (waits for health)
	@echo "waiting for services to become healthy..."
	@sleep 8
	$(MAKE) migrate
	$(MAKE) bus-ensure
