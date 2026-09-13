/**
 * shared/auth.js — Shared authentication helpers for arena_new k6 load tests.
 *
 * Exports:
 *   login(baseUrl, email, password) → { token, userId }
 *   devToken(baseUrl, userId, role)  → { token }   (dev-only stub endpoint)
 *
 * Usage:
 *   import { devToken } from './shared/auth.js';
 *   const { token } = devToken(__ENV.BASE_URL, 'test-user-id', 'member');
 */

import http from 'k6/http';
import { check } from 'k6';

/**
 * Login with email/password and return a Bearer token.
 * Returns null on failure (caller should bail on the iteration).
 */
export function login(baseUrl, email, password) {
  const res = http.post(
    `${baseUrl}/v1/auth/login`,
    JSON.stringify({ email, password }),
    { headers: { 'Content-Type': 'application/json' } },
  );

  const ok = check(res, {
    'auth/login 200': (r) => r.status === 200,
  });
  if (!ok) return null;

  const body = res.json();
  return {
    token: body.access_token,
    userId: body.user_id,
  };
}

/**
 * Issue a dev JWT via POST /v1/dev/auth/token (mounted only when ENABLE_DEV_AUTH=true,
 * which docker-compose sets for the api service).
 *
 * The endpoint decodes with DisallowUnknownFields and expects
 * {actor_id, org_id?, roles[], ttl_seconds?}. This helper used to send
 * {user_id, role}, which the server rejects with 400, so every script built on
 * it (scanner.js, checkout.js) silently ran unauthenticated (fixed 2026-09-13).
 *
 * @param {string}  baseUrl  e.g. "http://localhost:8080"
 * @param {string}  userId   UUID embedded as the JWT subject (actor_id)
 * @param {string}  role     "admin" | "org_admin" | "member" (default "member")
 * @param {string}  orgId    optional organization UUID for org-scoped claims
 */
export function devToken(baseUrl, userId, role = 'member', orgId = undefined) {
  const body = { actor_id: userId, roles: [role], ttl_seconds: 3600 };
  if (orgId) body.org_id = orgId;
  const res = http.post(
    `${baseUrl}/v1/dev/auth/token`,
    JSON.stringify(body),
    { headers: { 'Content-Type': 'application/json' } },
  );

  const ok = check(res, {
    'dev/auth/token 200': (r) => r.status === 200,
  });
  if (!ok) return { token: null };

  const body = res.json();
  return { token: body.token || body.access_token };
}

/**
 * Return standard Bearer auth header object.
 */
export function bearerHeader(token) {
  return { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' };
}
