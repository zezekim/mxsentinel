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

.PHONY: build-cpanel-plugin
build-cpanel-plugin: ## Cross-build the cPanel/WHM plugin for the server (GOOS/GOARCH overridable)
	GOOS=$(or $(GOOS),linux) GOARCH=$(or $(GOARCH),amd64) go build -o bin/mxsentinel-plugin ./cmd/cpanel-plugin
	@echo "built bin/mxsentinel-plugin — copy it + plugins/cpanel/ to the WHM box, then run plugins/cpanel/install.sh"

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

.PHONY: up-app
up-app: ## Build + run the FULL platform in containers (services + dashboard)
	$(COMPOSE) --profile app up --build -d

.PHONY: install
install: ## Interactive production installer (run on the VPS) — prompts, writes .env, deploys
	bash deploy/install.sh

.PHONY: up-prod
up-prod: ## Production deploy behind Caddy TLS (needs deploy/.env; see docs/deploy-vps.md)
	$(COMPOSE) -f deploy/docker-compose.prod.yml --profile app --env-file deploy/.env up -d --build

.PHONY: down
down: ## Stop the local dev stack (add --profile app to stop app services too)
	$(COMPOSE) --profile app down

.PHONY: restart
restart: ## Restart the local dev stack (services + app). For prod: bash deploy/install.sh --restart
	$(COMPOSE) --profile app restart

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

.PHONY: run-correld
run-correld: ## Run the correlation engine (rejection spikes -> DNS-change root cause)
	go run ./cmd/correld

.PHONY: run-repd
run-repd: ## Run the reputation monitor (DNSBL checks -> reputation.blacklist_hit)
	go run ./cmd/repd

.PHONY: run-aid
run-aid: ## Run the AI diagnostics daemon (incidents -> LLM root-cause + remediation)
	go run ./cmd/aid

.PHONY: run-incidentd
run-incidentd: ## Run the incident recorder (reputation/DNS events -> incidents)
	go run ./cmd/incidentd

.PHONY: run-cpaneld
run-cpaneld: ## Run the cPanel/WHMCS sync + metrics push daemon
	go run ./cmd/cpaneld

.PHONY: run-dmarcpulld
run-dmarcpulld: ## Pull DMARC reports from dmarc.squidix.org into Postgres + ClickHouse
	go run ./cmd/dmarcpulld

.PHONY: run-apid
run-apid: ## Run the REST API server on :8080
	go run ./cmd/apid

.PHONY: apikey
apikey: ## Create a read+write API token for the demo tenant (printed once)
	go run ./cmd/mxctl apikey create --tenant demo --scopes read,write

.PHONY: user
user: ## Create a demo owner login (EMAIL=you@example.com PASSWORD=secret)
	go run ./cmd/mxctl user create --tenant demo --email $(or $(EMAIL),admin@demo.test) --password $(or $(PASSWORD),changeme) --role owner

.PHONY: web-dev
web-dev: ## Run the Next.js dashboard dev server (log in at /login, or set NEXT_PUBLIC_API_TOKEN)
	cd web && npm install && npm run dev

.PHONY: bootstrap
bootstrap: up ## Start stack, then migrate + ensure streams (waits for health)
	@echo "waiting for services to become healthy..."
	@sleep 8
	$(MAKE) migrate
	$(MAKE) bus-ensure
