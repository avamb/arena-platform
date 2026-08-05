/**
 * types.ts — shared TypeScript types for the Arena Tickets widget.
 *
 * These types mirror the backend API contract:
 *   GET /v1/public/feeds/{token}/events/{id}  → FeedEventDetailResponse
 *   GET /v1/event-sessions/{id}/schema         → SchemaResponse
 *   GET /v1/event-sessions/{id}/seat-status    → SeatStatusResponse
 */

// ─── Geometry ────────────────────────────────────────────────────────────────

export interface Canvas {
  width: number;
  height: number;
}

/** A single 2D vertex of a GA hit-test polygon (canvas space). */
export interface GeometryPoint {
  x: number;
  y: number;
}

export interface GeometryCategory {
  index: number;
  name: string;
  color: string;
  price_hint?: string;
  currency_hint?: string;
  /**
   * AB-40: category kind. Empty / "seated" (canonically empty) means the
   * category is bound by fill colour to coordinate-bearing seats. The value
   * "general_admission" marks a bulk-capacity GA category that carries no
   * per-seat coordinates.
   */
  kind?: string;
  /**
   * AB-40: declared bulk capacity for a GA category. Always 0 / omitted for
   * seated categories. Used both for the "N places" hint in the widget UI
   * and as the per-area upper bound on quantity pickers.
   */
  capacity?: number;
  /**
   * AB-40 A1 / D1: optional hit-test polygon in canvas space for GA
   * categories imported from an SVG `#GA <name>` element. Absent on the
   * GA-only hand-entered path — those GA categories render as an
   * always-visible tier card under the map instead of a clickable polygon.
   */
  polygon?: GeometryPoint[];
}

export interface Seat {
  key: string;
  number: string;
  x: number;
  y: number;
  radius: number;
  category_index: number;
  barcode_hint?: string | null;
}

export interface Row {
  key: string;
  name: string;
  seats: Seat[];
}

export interface Section {
  key: string;
  name: string;
  rows: Row[];
}

export interface StandingZone {
  key: string;
  name: string;
  capacity: number;
}

export interface Geometry {
  schema_version: number;
  canvas: Canvas;
  categories: GeometryCategory[];
  sections: Section[];
  /**
   * Retired by AB-40/AB-51: GA capacity now lives on categories with
   * kind='general_admission'. Kept optional for geometries stored before
   * the retirement; new geometries omit the key entirely.
   */
  standing_zones?: StandingZone[];
  tables: unknown[];
  decor_svg: string;
}

// ─── Schema endpoint ─────────────────────────────────────────────────────────

/** Category price entry from /schema — category + resolved tier/price. */
export interface CategoryPrice {
  index: number;
  name: string;
  color: string;
  price_hint?: string;
  currency_hint?: string;
  tier_id?: string;
  tier_name?: string;
  pricing_mode?: string;
  price_amount?: number;
  currency?: string;
}

/** Response from GET /v1/event-sessions/{id}/schema. */
export interface SchemaResponse {
  session_id: string;
  event_id: string;
  admission_mode: string;
  seating_plan_version_id: string;
  seat_status_version: number;
  geometry_checksum: string;
  capacity_seated: number;
  capacity_standing: number;
  geometry: Geometry;
  category_prices: CategoryPrice[];
}

/** Cached schema with ETag for conditional requests. */
export interface SchemaCacheEntry {
  etag: string;
  schema: SchemaResponse;
}

// ─── Seat status endpoint ─────────────────────────────────────────────────────

/** Valid seat status values from the backend (AB rename: blocked → unavailable). */
export type SeatStatusValue = 'available' | 'held' | 'sold' | 'unavailable';

/** Response from GET /v1/event-sessions/{id}/seat-status[?since_version=N]. */
export interface SeatStatusResponse {
  session_id: string;
  status_version: number;
  seats: Record<string, SeatStatusValue>;
  delta: boolean;
}

// ─── Feed (public event list / detail) ───────────────────────────────────────

export interface BuyerField {
  key: string;
  required: boolean;
  enabled: boolean;
}

export interface Tier {
  id: string;
  name: string;
  pricing_mode: string;
  price_amount: number;
  currency: string;
  pwyw_min?: number | null;
  pwyw_max?: number | null;
  capacity?: number | null;
  sale_window_start?: string | null;
  sale_window_end?: string | null;
  sort_order: number;
}

/**
 * One row of a session media gallery (AB-47b/AB-47c). Exactly one of
 * `poster_url` (kind='poster') or `video_url` (kind='video') is set.
 * `position` is the server-assigned 0..N-1 order.
 */
export interface FeedSessionMediaItem {
  kind: 'poster' | 'video';
  poster_url?: string | null;
  video_url?: string | null;
  position: number;
}

/** Session as returned by the public feed event detail endpoint. */
export interface FeedSession {
  id: string;
  start_at: string;
  end_at: string;
  capacity_total: number;
  status: string;
  /** Populated only for seated/hybrid sessions. */
  admission_mode?: string;
  /** URL to fetch the session schema (only for seated/hybrid sessions). */
  schema_url?: string;
  /** URL to fetch the seat status (only for seated/hybrid sessions). */
  seat_status_url?: string;
  buyer_fields: BuyerField[];
  tiers: Tier[];
  /**
   * Resolved poster cover (AB-47c): session's own poster_media_id when set,
   * otherwise the event-level fallback (event.poster_media_id). Both
   * fields resolve to the same media object.
   */
  poster_media_id?: string | null;
  poster_url?: string | null;
  /**
   * Ordered per-session media gallery (AB-47b/AB-47c). Empty array when
   * the session has no gallery items. The gallery is additive to the
   * cover above; the cover is NOT included in this list.
   */
  media_gallery: FeedSessionMediaItem[];
}

export interface FeedEvent {
  id: string;
  /** Short operator-facing event number (Wave 4). */
  display_number: number;
  org_id: string;
  name: string;
  description?: string | null;
  status: string;
  /**
   * Earliest session start (Wave 4, AB-37). Null when the event has no
   * sessions — such events carry no date at all.
   */
  first_session_at: string | null;
  /** Latest session end. Null when the event has no sessions. */
  last_session_at: string | null;
  /** Distinct venue names of the event's active sessions (AB-36). */
  venue_names: string[];
  visibility: string;
  image_url?: string | null;
  /**
   * Event-level poster cover (AB-47/AB-47c). Sessions fall back to this
   * media object when they do not carry their own poster_media_id.
   */
  poster_media_id?: string | null;
  poster_url?: string | null;
  created_at: string;
  updated_at: string;
  sessions: FeedSession[];
}

/** Response from GET /v1/public/feeds/{token}/events/{event_id}. */
export interface FeedEventDetailResponse {
  event: FeedEvent;
}

/**
 * One event in the GET /v1/public/feeds/{token}/events list response.
 * The list serializer emits the event WITHOUT its sessions — session data
 * comes from the per-event detail endpoint.
 */
export type FeedEventSummary = Omit<FeedEvent, 'sessions'>;

/** Response from GET /v1/public/feeds/{token}/events (list). */
export interface FeedEventsListResponse {
  events: FeedEventSummary[];
}
