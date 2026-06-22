# arena_new — Backend Foundation Milestone
# One-line dev commands. All targets assume the working directory is the repo root.
#
# Prerequisites:
#   go 1.24+          https://go.dev/dl/
#   golangci-lint     https://golangci-lint.run/usage/install/
#
# Usage:
#   make lint           — static analysis (golangci-lint)
#   make test           — unit tests
#   make test-race      — unit tests with -race detector
#   make build          — compile all binaries to ./bin/
#   make run-api        — start the HTTP API server
#   make run-worker     — start the background worker
#   make migrate-up     — apply all pending DB migrations
#   make migrate-down   — roll back the last DB migration

.PHONY: lint test test-race build run-api run-worker migrate-up migrate-down

# ── Optional extra flags forwarded to go test / go build ──────────────────────
GOFLAGS ?=

# ── Directories ───────────────────────────────────────────────────────────────
BIN_DIR := bin

# ── Lint ──────────────────────────────────────────────────────────────────────
lint:
	golangci-lint run ./...

# ── Test ──────────────────────────────────────────────────────────────────────
test:
	go test $(GOFLAGS) ./...

test-race:
	go test -race $(GOFLAGS) ./...

# ── Build ─────────────────────────────────────────────────────────────────────
build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/arena-api          ./apps/backend/cmd/arena-api
	go build -o $(BIN_DIR)/arena-worker       ./apps/backend/cmd/arena-worker
	go build -o $(BIN_DIR)/arena-migrate      ./apps/backend/cmd/arena-migrate
	go build -o $(BIN_DIR)/arena-healthcheck  ./apps/backend/cmd/arena-healthcheck

# ── Run (dev convenience) ─────────────────────────────────────────────────────
run-api:
	go run ./apps/backend/cmd/arena-api

run-worker:
	go run ./apps/backend/cmd/arena-worker

# ── Migrations ────────────────────────────────────────────────────────────────
# Requires DATABASE_URL to be set in the environment or .env file.
migrate-up:
	go run ./apps/backend/cmd/arena-migrate up

migrate-down:
	go run ./apps/backend/cmd/arena-migrate down
