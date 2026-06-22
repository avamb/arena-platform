#!/usr/bin/env bash
# generate-clients.sh — Regenerate TypeScript client types from the Arena OpenAPI spec.
#
# This is a convenience wrapper around `make gen-ts-client` and
# `node scripts/gen-ts-client.mjs`.
#
# Usage (run from the repo root):
#   ./generate-clients.sh
#
# Alternatively:
#   make gen-ts-client
#   node scripts/gen-ts-client.mjs
#
# Output:
#   apps/backend/openapi/clients/ts/index.d.ts
#
# Prerequisites:
#   - Node.js >= 18
#   - npm install (installs openapi-typescript in node_modules)
#
# Re-run this script whenever apps/backend/openapi/openapi.yaml changes so
# the TypeScript client types stay in sync with the Go server contract.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
node "${SCRIPT_DIR}/scripts/gen-ts-client.mjs"
