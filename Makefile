# arena_new — Backend Foundation Milestone
# One-line dev commands. All targets assume the working directory is the repo root.
#
# Prerequisites:
#   go 1.24+          https://go.dev/dl/
#   golangci-lint     https://golangci-lint.run/usage/install/
#   oapi-codegen v2   go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.4.1
#   sqlc v2           https://docs.sqlc.dev/en/stable/overview/install.html
#
# Usage:
#   make lint             — static analysis (golangci-lint)
#   make test             — unit tests
#   make test-race        — unit tests with -race detector
#   make build            — compile all binaries to ./bin/
#   make gen-openapi      — regenerate Go types from apps/backend/openapi/openapi.yaml
#   make gen-ts-client    — regenerate TypeScript client types to openapi/clients/ts/
#   make sqlc-generate    — regenerate typed SQL wrappers from apps/backend/sqlc.yaml
#   make run-api          — start the HTTP API server
#   make run-worker       — start the background worker
#   make migrate-up       — apply all pending DB migrations
#   make migrate-down     — roll back the last DB migration

.PHONY: lint test test-race build gen-openapi gen-ts-client sqlc-generate run-api run-worker migrate-up migrate-down

# ── Optional extra flags forwarded to go test / go build ──────────────────────
GOFLAGS ?=

# ── Directories ───────────────────────────────────────────────────────────────
BIN_DIR := bin

# ── Code generation ───────────────────────────────────────────────────────────
# Regenerates Go types from the OpenAPI spec. Re-run whenever openapi.yaml
# changes — any field rename or removal used by handler code cascades to a
# compile error in `go build ./...` (proven by feature #34 coupling test).
gen-openapi:
	@mkdir -p apps/backend/internal/adapters/http/openapi
	go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.4.1 \
		--config=apps/backend/openapi/oapi-codegen.yaml \
		apps/backend/openapi/openapi.yaml

# Regenerates TypeScript client types from the OpenAPI spec.
# Output: apps/backend/openapi/clients/ts/index.d.ts
# Requires Node.js ≥ 18 (node_modules must be installed: npm install).
# Verify with: npx tsc --noEmit apps/backend/openapi/clients/ts/index.d.ts
gen-ts-client:
	node scripts/gen-ts-client.mjs

# Regenerates type-safe Go query wrappers from SQL files.
#
# Source .sql files : apps/backend/internal/adapters/postgres/queries/
# Generated output  : apps/backend/internal/adapters/postgres/gen/
# Config            : apps/backend/sqlc.yaml
#
# Re-run whenever a .sql query file is added or changed — the generated Go
# code will then need to be committed alongside the SQL source.
#
# Prerequisites: sqlc v2 must be on your PATH.
#   Install: go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
#   Or download a pre-built binary from https://docs.sqlc.dev/en/stable/overview/install.html
sqlc-generate:
	cd apps/backend && sqlc generate

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
