#!/usr/bin/env node
/**
 * gen-ts-client.mjs — Generates TypeScript client types from the Arena OpenAPI spec.
 *
 * Steps:
 *  1. Runs openapi-typescript to generate base types (paths, components, operations).
 *  2. Appends named type aliases for the most-used schema types (ergonomic imports).
 *  3. Appends error code string-literal union types (for exhaustive switch handling).
 *
 * Run:  make gen-ts-client
 *   or: node scripts/gen-ts-client.mjs
 */
import { execSync } from "node:child_process";
import { appendFileSync, mkdirSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const root = join(scriptDir, "..");
const specPath = join(root, "apps", "backend", "openapi", "openapi.yaml");
const outDir  = join(root, "apps", "backend", "openapi", "clients", "ts");
const outPath = join(outDir, "index.d.ts");

mkdirSync(outDir, { recursive: true });

// ── Step 1: generate base types ───────────────────────────────────────────────
console.log("Generating TypeScript client types from OpenAPI spec...");
execSync(
  `npx openapi-typescript "${specPath}" --output "${outPath}"`,
  { stdio: "inherit", cwd: root }
);

// ── Step 2 & 3: append named aliases and error code enums ────────────────────
const NAMED_EXPORTS = `
// ──────────────────────────────────────────────────────────────────────────────
// Named type aliases — shorthand for components["schemas"]["..."]
// Import directly instead of drilling into the components namespace:
//   import type { EchoRequest, EchoResponse, ErrorEnvelope } from "./index.d.ts";
// ──────────────────────────────────────────────────────────────────────────────

/** Structured error response envelope returned by all 4xx/5xx responses. */
export type ErrorEnvelope = components["schemas"]["ErrorEnvelope"];

export type HealthzResponse = components["schemas"]["HealthzResponse"];
export type ReadyzResponse = components["schemas"]["ReadyzResponse"];
export type InfoResponse = components["schemas"]["InfoResponse"];

/** Request body for POST /v1/echo */
export type EchoRequest = components["schemas"]["EchoRequest"];
/** Successful response body for POST /v1/echo (200 OK) */
export type EchoResponse = components["schemas"]["EchoResponse"];

export type DevTokenRequest    = components["schemas"]["DevTokenRequest"];
export type DevTokenResponse   = components["schemas"]["DevTokenResponse"];
export type DevAuthTokenRequest  = components["schemas"]["DevAuthTokenRequest"];
export type DevAuthTokenResponse = components["schemas"]["DevAuthTokenResponse"];

// ──────────────────────────────────────────────────────────────────────────────
// Error code string-literal union types.
//
// All Arena API error codes follow the dotted-namespace pattern:
//   "<namespace>.<sub_code>"  (e.g. "auth.token_expired")
// These union types enable exhaustive switch/case and IDE autocomplete on
// the ErrorEnvelope.error.code field returned by 4xx/5xx responses.
// ──────────────────────────────────────────────────────────────────────────────

/** HTTP-layer error codes (routing, content negotiation, body size limits). */
export type HttpErrorCode =
    | "http.not_found"
    | "http.method_not_allowed"
    | "http.payload_too_large"
    | "http.unsupported_media_type"
    | "http.bad_request";

/** Authentication and authorisation error codes. */
export type AuthErrorCode =
    | "auth.token_missing"
    | "auth.token_expired"
    | "auth.token_invalid"
    | "auth.token_malformed";

/** Echo endpoint business-logic error codes. */
export type EchoErrorCode =
    | "echo.invalid_body"
    | "echo.message_required";

/** External dependency error codes (database, cache, downstream services). */
export type DependencyErrorCode =
    | "dependency.database_unavailable";

/** Internal server error codes (unexpected / unhandled failures). */
export type InternalErrorCode =
    | "internal.unexpected";

/**
 * Union of all known Arena API error codes.
 * Use this type when writing client-side error handlers that need to cover
 * every possible code the server can return.
 *
 * @example
 *   function handleError(code: ApiErrorCode) {
 *     switch (code) {
 *       case "auth.token_expired":  return refreshToken();
 *       case "http.payload_too_large": return showFileTooLargeError();
 *       // ...
 *     }
 *   }
 */
export type ApiErrorCode =
    | HttpErrorCode
    | AuthErrorCode
    | EchoErrorCode
    | DependencyErrorCode
    | InternalErrorCode;
`;

appendFileSync(outPath, NAMED_EXPORTS, "utf8");

console.log("Named exports and error code enums appended.");
console.log("Output: " + outPath);
