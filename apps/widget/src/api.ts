/**
 * api.ts — public-surface API client for the Arena Tickets widget.
 *
 * All requests use only unauthenticated public endpoints:
 *   GET /v1/public/feeds/{feed_token}/events/{event_id}
 *   GET /v1/event-sessions/{id}/schema   (ETag-cached, immutable per checksum)
 *   GET /v1/event-sessions/{id}/seat-status[?since_version=N]
 *   POST /v1/public/feeds/{token}/checkout/start   (WID-D)
 *   GET  /v1/public/checkout/{token}               (WID-D)
 *   POST /v1/public/checkout/{token}/recover       (WID-D, wraps WID-0c)
 *
 * The schema cache is module-level so a single widget instance does not
 * re-download the geometry on every re-render.  It is keyed by session ID
 * and stores the ETag alongside the parsed response for conditional requests.
 */

import type {
  FeedEventDetailResponse,
  SchemaResponse,
  SchemaCacheEntry,
  SeatStatusResponse,
} from './types.js';
import type {
  CheckoutStartPayload,
  CheckoutStartResponse,
  CheckoutStatusResponse,
  CheckoutRecoverResponse,
} from './lib/checkout.js';

// ─── Structured API error ────────────────────────────────────────────────────

/**
 * Error thrown by API helpers when the backend returns a non-2xx response
 * with a structured error body.
 *
 * Still an `Error` (message format is unchanged), but carries the machine-
 * readable fields so UI code can branch on them, e.g. read
 * `err.details.conflicts` after a 409 from `postCheckoutStart`.
 */
export class ApiError extends Error {
  /** HTTP status code of the failed response. */
  readonly status: number;
  /** Machine-readable error code from the response body (or `http_<status>`). */
  readonly code: string;
  /** Structured error details from the response body (e.g. `conflicts`). */
  readonly details: Record<string, unknown>;

  constructor(
    message: string,
    opts: { status: number; code?: string; details?: Record<string, unknown> },
  ) {
    super(message);
    this.name = 'ApiError';
    this.status = opts.status;
    this.code = opts.code ?? `http_${opts.status}`;
    this.details = opts.details ?? {};
  }
}

// ─── Schema ETag cache ───────────────────────────────────────────────────────

/** Module-level cache: session_id → { etag, schema }. */
const schemaCache = new Map<string, SchemaCacheEntry>();

/** Clear the cache (used in tests and on unmount). */
export function clearSchemaCache(): void {
  schemaCache.clear();
}

// ─── Feed event detail ───────────────────────────────────────────────────────

/**
 * Fetch the full event detail (including sessions) from the public feed.
 *
 * @throws Error when the response is non-2xx.
 */
export async function fetchFeedEvent(
  feedToken: string,
  eventId: string,
): Promise<FeedEventDetailResponse> {
  const url = `/v1/public/feeds/${encodeURIComponent(feedToken)}/events/${encodeURIComponent(eventId)}`;
  const res = await fetch(url);
  if (!res.ok) {
    throw new Error(`fetchFeedEvent HTTP ${res.status}: ${res.statusText}`);
  }
  return res.json() as Promise<FeedEventDetailResponse>;
}

// ─── Session schema ──────────────────────────────────────────────────────────

/**
 * Fetch the session geometry schema, honoring ETag-based caching.
 *
 * When the server returns 304 Not Modified, the cached SchemaResponse is
 * returned immediately without JSON parsing.  The schema is treated as
 * immutable once cached because the ETag equals the geometry_checksum.
 *
 * @throws Error when the response is non-2xx (and not 304).
 */
export async function fetchSessionSchema(sessionId: string): Promise<SchemaResponse> {
  const url = `/v1/event-sessions/${encodeURIComponent(sessionId)}/schema`;

  const cached = schemaCache.get(sessionId);
  const headers: Record<string, string> = {};
  if (cached) {
    headers['If-None-Match'] = cached.etag;
  }

  const res = await fetch(url, { headers });

  if (res.status === 304 && cached) {
    return cached.schema;
  }
  if (!res.ok) {
    throw new Error(`fetchSessionSchema HTTP ${res.status}: ${res.statusText}`);
  }

  const schema = (await res.json()) as SchemaResponse;
  const etag = res.headers.get('ETag') ?? '';
  if (etag) {
    schemaCache.set(sessionId, { etag, schema });
  }
  return schema;
}

// ─── Seat status (snapshot + delta) ─────────────────────────────────────────

/**
 * Fetch the full seat-status snapshot for a session.
 *
 * @throws Error when the response is non-2xx.
 */
export async function fetchSeatStatus(sessionId: string): Promise<SeatStatusResponse> {
  const url = `/v1/event-sessions/${encodeURIComponent(sessionId)}/seat-status`;
  const res = await fetch(url);
  if (!res.ok) {
    throw new Error(`fetchSeatStatus HTTP ${res.status}: ${res.statusText}`);
  }
  return res.json() as Promise<SeatStatusResponse>;
}

/**
 * Fetch a seat-status delta since a given version cursor.
 * Returns only rows whose status changed after `sinceVersion`.
 *
 * @throws Error when the response is non-2xx.
 */
export async function fetchSeatStatusDelta(
  sessionId: string,
  sinceVersion: number,
): Promise<SeatStatusResponse> {
  const url =
    `/v1/event-sessions/${encodeURIComponent(sessionId)}/seat-status` +
    `?since_version=${sinceVersion}`;
  const res = await fetch(url);
  if (!res.ok) {
    throw new Error(`fetchSeatStatusDelta HTTP ${res.status}: ${res.statusText}`);
  }
  return res.json() as Promise<SeatStatusResponse>;
}

// ─── Checkout (WID-D) ────────────────────────────────────────────────────────

/**
 * POST /v1/public/feeds/{feedToken}/checkout/start
 *
 * Creates a new anonymous checkout session, reserves seats / GA capacity,
 * and returns a `redirect_url` for the payment provider plus a `checkout_token`
 * for subsequent status / recovery calls.
 *
 * @throws ApiError when the response is non-2xx — carries `status`, `code`,
 *         and `details` (e.g. `details.conflicts` on a 409 seat conflict).
 */
export async function postCheckoutStart(
  feedToken: string,
  payload: CheckoutStartPayload,
): Promise<CheckoutStartResponse> {
  const url = `/v1/public/feeds/${encodeURIComponent(feedToken)}/checkout/start`;
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  if (!res.ok) {
    let detail = '';
    let code: string | undefined;
    let details: Record<string, unknown> | undefined;
    try {
      const body = (await res.json()) as {
        error?: string;
        message?: string;
        code?: string;
        details?: Record<string, unknown>;
      };
      detail = body.error ?? body.message ?? '';
      code = body.code ?? (body.error || undefined);
      details = body.details;
    } catch { /* ignore non-JSON error bodies */ }
    throw new ApiError(
      `postCheckoutStart HTTP ${res.status}${detail ? `: ${detail}` : ''}`,
      { status: res.status, code, details },
    );
  }
  return res.json() as Promise<CheckoutStartResponse>;
}

/**
 * GET /v1/public/checkout/{checkoutToken}
 *
 * Poll the anonymous order status.  No JWT required — the `checkout_token` in
 * the URL is the bearer credential.
 *
 * Possible statuses:
 *   pending  — payment in progress (keep polling)
 *   paid     — order complete; `tickets` array is populated
 *   expired  — hold expired; call `postCheckoutRecover` if recoverable
 *   failed   — payment abandoned; terminal
 *
 * @throws Error when the response is non-2xx.
 */
export async function getCheckoutStatus(
  checkoutToken: string,
): Promise<CheckoutStatusResponse> {
  const url = `/v1/public/checkout/${encodeURIComponent(checkoutToken)}`;
  const res = await fetch(url);
  if (!res.ok) {
    throw new Error(`getCheckoutStatus HTTP ${res.status}: ${res.statusText}`);
  }
  return res.json() as Promise<CheckoutStatusResponse>;
}

/**
 * POST /v1/public/checkout/{checkoutToken}/recover
 *
 * Attempt to re-capture the same seats/GA when the hold has expired (WID-0c).
 * Returns a fresh `expires_at` timestamp when successful.
 *
 * Should only be called when `getCheckoutStatus` returns `expired`.
 *
 * @throws Error when the response is non-2xx (409 = seats no longer available).
 */
export async function postCheckoutRecover(
  checkoutToken: string,
): Promise<CheckoutRecoverResponse> {
  const url = `/v1/public/checkout/${encodeURIComponent(checkoutToken)}/recover`;
  const res = await fetch(url, { method: 'POST' });
  if (!res.ok) {
    throw new Error(`postCheckoutRecover HTTP ${res.status}: ${res.statusText}`);
  }
  return res.json() as Promise<CheckoutRecoverResponse>;
}
