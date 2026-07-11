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

export interface GeometryCategory {
  index: number;
  name: string;
  color: string;
  price_hint?: string;
  currency_hint?: string;
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
  standing_zones: StandingZone[];
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

/** Valid seat status values from the backend. */
export type SeatStatusValue = 'available' | 'held' | 'sold' | 'blocked';

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
}

export interface FeedEvent {
  id: string;
  org_id: string;
  venue_id?: string | null;
  name: string;
  description?: string | null;
  status: string;
  start_at: string;
  end_at: string;
  visibility: string;
  image_url?: string | null;
  created_at: string;
  updated_at: string;
  sessions: FeedSession[];
}

/** Response from GET /v1/public/feeds/{token}/events/{event_id}. */
export interface FeedEventDetailResponse {
  event: FeedEvent;
}
