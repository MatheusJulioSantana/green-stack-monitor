.PHONY: run test bench lint build up down token

# ── Development ──────────────────────────────────────────────────────────────

run:
	JWT_SECRET="dev-secret-32-bytes-minimum-here" \
	PPROF_ENABLED=true \
	go run ./cmd/server

test:
	go test ./... -race -count=1

bench:
	go test ./... -bench=. -benchmem -run=^$

lint:
	golangci-lint run ./...

build:
	CGO_ENABLED=0 go build -o bin/server ./cmd/server

# ── Docker stack ──────────────────────────────────────────────────────────────

up:
	docker compose -f deployments/docker-compose.yml up --build -d
	@echo ""
	@echo "  App      → http://localhost:8080"
	@echo "  Metrics  → http://localhost:8080/metrics"
	@echo "  Badge    → http://localhost:8080/badge"
	@echo "  Prometheus → http://localhost:9090"
	@echo "  Grafana  → http://localhost:3000 (admin / admin)"
	@echo ""

down:
	docker compose -f deployments/docker-compose.yml down

# ── Utilities ─────────────────────────────────────────────────────────────────

# Generate a test JWT token (requires the server to be running for inspection).
# Usage: make token USER=alice ROLE=admin
token:
	go run ./cmd/server -issue-token \
		-user=$(USER) -role=$(ROLE) \
		-secret="dev-secret-32-bytes-minimum-here"

# Generate a strong JWT secret for production.
secret:
	openssl rand -hex 32
