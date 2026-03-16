.PHONY: all build test dev up down migrate status kill ack resume orders risk proto audit trace config ledger test-certification test-report monitor monitor-down dashboard alertbot scraper backtest chaos

# ─── Protobuf ───

proto:
	protoc --go_out=. --go_opt=module=autonomy-platform \
	       --go-grpc_out=. --go-grpc_opt=module=autonomy-platform \
	       -I. proto/common.proto proto/risk.proto proto/execution.proto \
	       proto/watchdog.proto proto/mockexchange.proto

# ─── Build ───

all: build

build:
	go build ./cmd/...

build-linux:
	GOOS=linux GOARCH=amd64 go build ./cmd/...

# ─── Local Development ───

dev: up migrate
	@echo "Stack is running. Services:"
	@echo "  Postgres:  localhost:5432"
	@echo "  NATS:      localhost:4222 (monitoring: localhost:8222)"
	@echo ""
	@echo "Run services individually:"
	@echo "  go run ./cmd/data-ingestion"
	@echo "  go run ./cmd/risk-engine"
	@echo "  go run ./cmd/strategy-engine"
	@echo "  go run ./cmd/execution-engine"
	@echo "  go run ./cmd/watchdog"

up:
	docker compose up -d postgres nats
	@echo "Waiting for postgres..."
	@until docker compose exec -T postgres pg_isready -U trader -d autonomy 2>/dev/null; do sleep 1; done
	@echo "Infrastructure ready."

down:
	docker compose down

# ─── Database ───

migrate:
	@echo "Running migrations..."
	docker compose exec -T postgres psql -U trader -d autonomy -f /docker-entrypoint-initdb.d/001_initial.up.sql 2>/dev/null || true
	docker compose exec -T postgres psql -U trader -d autonomy -f /docker-entrypoint-initdb.d/002_order_intent_ledger.up.sql 2>/dev/null || true
	docker compose exec -T postgres psql -U trader -d autonomy -f /docker-entrypoint-initdb.d/003_recon_snapshots.up.sql 2>/dev/null || true
	docker compose exec -T postgres psql -U trader -d autonomy -f /docker-entrypoint-initdb.d/004_backtest.up.sql 2>/dev/null || true
	docker compose exec -T postgres psql -U trader -d autonomy -f /docker-entrypoint-initdb.d/005_market_data_logging.up.sql 2>/dev/null || true
	@echo "Migrations complete."

migrate-down:
	docker compose exec -T postgres psql -U trader -d autonomy -f /docker-entrypoint-initdb.d/001_initial.down.sql

reset-db: migrate-down migrate
	@echo "Database reset."

# ─── Testing ───

test:
	go test ./... -v -count=1

test-integration: up migrate
	go test ./tests/integration/... -v -count=1 -tags=integration

test-race:
	go test ./... -race -count=1

test-certification: up migrate
	@echo "Running Phase 10 certification tests..."
	go test ./tests/integration/... -v -count=1 -tags=integration -run "TestCert_" -timeout 10m

test-report: up migrate
	@echo "Generating paper trading certification report..."
	go test ./tests/integration/... -v -count=1 -tags=integration -run "TestCert_PaperTradingReport" -timeout 5m

# ─── Operator CLI shortcuts ───

status:
	go run ./cmd/trade-ctl status

kill:
	go run ./cmd/trade-ctl kill --level cancel_only --scope global --reason "manual operator kill"

ack:
	@echo "Usage: make ack CAUSE='your root cause here'"
	go run ./cmd/trade-ctl ack --scope global --cause "$(CAUSE)"

resume:
	go run ./cmd/trade-ctl resume --scope global

orders:
	go run ./cmd/trade-ctl orders

risk:
	go run ./cmd/trade-ctl risk

limits:
	go run ./cmd/trade-ctl limits

policy:
	go run ./cmd/trade-ctl policy

audit:
	go run ./cmd/trade-ctl audit

trace:
	@echo "Usage: make trace TRACE_ID=<uuid>"
	go run ./cmd/trade-ctl trace $(TRACE_ID)

config:
	go run ./cmd/trade-ctl config

ledger:
	go run ./cmd/trade-ctl ledger

# ─── Simulation ───

simulate: up migrate
	@echo "Starting paper trading simulation..."
	@echo "Starting data ingestion..."
	go run ./cmd/data-ingestion &
	@sleep 2
	@echo "Starting risk engine..."
	GRPC_PORT=50020 go run ./cmd/risk-engine &
	@sleep 1
	@echo "Starting execution engine..."
	GRPC_PORT=50040 RISK_ENGINE_ADDR=localhost:50020 WATCHDOG_ADDR=localhost:50055 go run ./cmd/execution-engine &
	@sleep 2
	@echo "Starting watchdog..."
	GRPC_PORT=50055 EXECUTION_ENGINE_ADDR=localhost:50040 go run ./cmd/watchdog &
	@sleep 1
	@echo "Starting strategy engine..."
	RISK_ENGINE_ADDR=localhost:50020 EXECUTION_ENGINE_ADDR=localhost:50040 go run ./cmd/strategy-engine &
	@echo ""
	@echo "All services running. Press Ctrl+C to stop."
	@echo "Monitor with: make status"
	@wait

dashboard:
	DASHBOARD_API_KEY=localdev go run ./cmd/dashboard

alertbot:
	set -a && . ./.env && set +a && go run ./cmd/alertbot

scraper:
	go run ./cmd/data-scraper

backtest:
	@echo "Usage: make backtest STRATEGY=simple-momentum FROM=2025-01-01 TO=2025-06-01"
	go run ./cmd/trade-ctl backtest --strategy=$(STRATEGY) --from=$(FROM) --to=$(TO)

chaos: up migrate
	@echo "Running chaos tests..."
	go test ./tests/chaos/... -v -count=1 -tags=chaos -timeout 10m

# ─── Monitoring ───

monitor: up
	docker compose up -d prometheus grafana
	@echo ""
	@echo "Monitoring stack running:"
	@echo "  Prometheus:  http://localhost:9090"
	@echo "  Grafana:     http://localhost:3000 (admin/localdev)"

monitor-down:
	docker compose stop prometheus grafana

# ─── Linting ───

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .

vet:
	go vet ./...
