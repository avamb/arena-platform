/**
 * api.ts — public-surface API client for the Arena Tickets widget.
 *
 * All requests use only unauthenticated public endpoints:
 *   GET /v1/public/feeds/{feed_token}/events/{event_id}
 *   GET /v1/event-sessions/{id}/schema   (ETag-cached, immutable per checksum)
 *   GET /v1/event-sessions/{id}/seat-status[?since_version=N]
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
