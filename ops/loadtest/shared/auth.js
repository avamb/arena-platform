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
 * Issue a dev JWT via POST /v1/dev/auth/token (works only when DEBUG_ROUTES=true).
 * Useful in CI load tests where no real user DB is seeded.
 *
 * @param {string}  baseUrl  e.g. "http://localhost:8080"
 * @param {string}  userId   any UUID to embed in the JWT sub claim
 * @param {string}  role     "admin" | "org_admin" | "member" (default "member")
 */
export function devToken(baseUrl, userId, role = 'member') {
  const res = http.post(
    `${baseUrl}/v1/dev/auth/token`,
    JSON.stringify({ user_id: userId, role }),
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
