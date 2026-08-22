/**
 * Events admin module (feature #281 / E-3, #282 / E-4).
 *
 * Sessions sub-table CRUD (feature #282) lives in SessionsTab below and
 * wires the full lifecycle of the session API:
 *   POST   /v1/organizations/{org_id}/events/{event_id}/sessions
 *   PATCH  /v1/organizations/{org_id}/events/{event_id}/sessions/{id}
 *   DELETE /v1/organizations/{org_id}/events/{event_id}/sessions/{id}
 * Client-side guards mirror the backend: end_at > start_at and
 * capacity_total > 0 are enforced before submit; sibling-overlap is
 * detected on the loaded list and surfaced as a non-blocking warning
 * (the backend also reports has_overlapping_sessions on the envelope).
 *
 * Replaces the SAUI-12 /events placeholder shell with a real list +
 * detail screen backed by the events API in
 * apps/backend/internal/platform/httpserver/events.go:
 *
 *   GET    /v1/events?visibility=...                       cross-org list (event.read)
 *   GET    /v1/events/{id}                                 single event   (event.read)
 *   POST   /v1/organizations/{org_id}/events/{id}/status   status txn     (event.publish)
 *   GET    /v1/organizations/{org_id}/events/{event_id}/sessions
 *                                                          drawer sessions
 *   GET    /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers
 *                                                          drawer tiers
 *   GET    /v1/events/{event_id}/publications              drawer pubs (publication.read)
 *   GET    /v1/organizations                               org filter dropdown
 *
 * The route is intentionally read-only-plus-status-transitions: full
 * CRUD (create / edit / delete) is delegated to a later wave. This
 * scope ships the operator surface the spec called out -- list with
 * filters, detail drawer with five tabs, and lifecycle transitions.
 *
 * Status transitions (event lifecycle):
 *
 *   draft     → published, cancelled
 *   published → cancelled, archived
 *   cancelled → archived
 *
 * 422 `event.invalid_transition` from the backend is surfaced inline
 * with the action button so the operator immediately sees why a move
 * was rejected.
 *
 * Channels column:
 *   The events table has no first-class "channels" field. We render a
 *   small badge based on the lazily-fetched publications inside the
 *   detail drawer's Publications tab; the LIST view shows a dash for
 *   the column with a hint to open the drawer (a per-row publications
 *   fan-out would multiply N+1 queries against the API). When a future
 *   list-side publications summary is added to the EventItem shape we
 *   wire it in here.
 *
 * Activity tab:
 *   There is no per-event audit endpoint yet. The tab renders an
 *   honest empty-state instead of a fake feed.
 *
 * Mock data: NONE. Everything in this module hits the live backend.
 * No globalThis / devStore / mockDb.
 */
import { createRoute } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Fragment,
  useEffect,
  useMemo,
  useState,
  type CSSProperties,
  type FormEvent,
  type ReactNode,
} from "react";
import { Route as RootRoute } from "./__root";
import { ApiError, authedFetch, uploadMedia } from "@/lib/api/client";
import { RequirePermission } from "@/components/RequirePermission";
import { useAuth } from "@/lib/auth/useAuth";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";
import {
  ResponsiveTable,
  type ResponsiveTableColumn,
  mobileFormBarStyle,
  singleColumnFormStyle,
} from "@/components/layout";
import { SessionSeatingBindPanel } from "@/routes/sessionSeatingBind";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/events",
  component: EventsRoute,
});

// ---------------------------------------------------------------------------
// Backend response shapes
// ---------------------------------------------------------------------------

export const EVENT_STATUSES = [
  "draft",
  "published",
  "cancelled",
  "archived",
] as const;
export type EventStatus = (typeof EVENT_STATUSES)[number];

export const EVENT_VISIBILITIES = ["public", "private", "unlisted"] as const;
export type EventVisibility = (typeof EVENT_VISIBILITIES)[number];

export type EventVisibilityFilter = EventVisibility | "all";

export interface EventItem {
  readonly id: string;
  readonly display_number: number;
  readonly org_id: string;
  readonly name: string;
  readonly description: string | null;
  readonly status: EventStatus;
  /**
   * Earliest start_at over the event's active sessions (AB-37). Null when
   * the event has no sessions — such events render NO date anywhere.
   */
  readonly first_session_at: string | null;
  /** Latest end_at over the event's active sessions. Null when no sessions. */
  readonly last_session_at: string | null;
  /** Distinct venue names of the event's active sessions (may be empty). */
  readonly venue_names: readonly string[];
  readonly visibility: EventVisibility;
  readonly image_url: string | null;
  readonly created_at: string;
  readonly updated_at: string;
}

interface EventListEnvelope {
  readonly events: readonly EventItem[];
}

interface EventEnvelope {
  readonly event: EventItem;
}

interface OrganizationSummary {
  readonly id: string;
  readonly name: string;
  readonly slug?: string;
  readonly display_number?: number;
}

interface OrganizationListEnvelope {
  readonly organizations: readonly OrganizationSummary[];
}

interface VenueSummary {
  readonly id: string;
  readonly name: string;
  readonly display_number?: number;
}

interface VenueListEnvelope {
  readonly venues: readonly VenueSummary[];
}

// ---------------------------------------------------------------------------
// Event form helpers (create / edit)
// ---------------------------------------------------------------------------

export interface EventFormValues {
  name: string;
  description: string;
  org_id: string;
  visibility: EventVisibility | "";
}

export function emptyEventForm(): EventFormValues {
  return {
    name: "",
    description: "",
    org_id: "",
    visibility: "",
  };
}

export function eventToForm(e: EventItem): EventFormValues {
  return {
    name: e.name,
    description: e.description ?? "",
    org_id: e.org_id,
    visibility: e.visibility,
  };
}

export interface EventFormErrors {
  name?: string;
  org_id?: string;
}

/**
 * Since Wave 4 (AB-36/AB-37) events carry no dates and no venue — both
 * belong to sessions — so the only client-side guards left are the name
 * and (on create) the owning organization.
 */
export function validateEventForm(
  v: EventFormValues,
  requireOrg: boolean,
): EventFormErrors {
  const errors: EventFormErrors = {};
  if (v.name.trim() === "") errors.name = "Name is required.";
  if (requireOrg && v.org_id.trim() === "")
    errors.org_id = "Organization is required.";
  return errors;
}

export interface SessionItem {
  readonly id: string;
  readonly event_id: string;
  /** Venue this session takes place at (AB-36). Always set. */
  readonly venue_id: string;
  readonly start_at: string;
  readonly end_at: string;
  /** DERIVED: bound plan → capacity_override → venue capacity_default. */
  readonly capacity_total: number;
  readonly capacity_override: number | null;
  readonly status: "draft" | "scheduled" | "cancelled" | "completed" | string;
  readonly admission_mode: "general_admission" | "assigned_seats" | "hybrid";
  readonly seating_plan_version_id: string | null;
  /** ISO 4217 currency all of this session's tiers are denominated in (AB-38). */
  readonly currency: string;
  readonly currency_source: "derived" | "override";
  readonly created_at: string;
  readonly updated_at: string;
  readonly has_overlapping_sessions?: boolean;
  /** Optional session-level poster artwork (AB-47). Overrides event-level poster. */
  readonly poster_media_id?: string | null;
}

interface SessionListEnvelope {
  readonly sessions: readonly SessionItem[];
  readonly has_overlapping_sessions?: boolean;
}

export interface TicketTierItem {
  readonly id: string;
  readonly session_id: string;
  readonly name: string;
  readonly pricing_mode: "free" | "fixed" | "pwyw" | string;
  readonly price_amount: number;
  readonly currency: string;
  readonly pwyw_min?: number | null;
  readonly pwyw_max?: number | null;
  readonly capacity?: number | null;
  readonly sale_window_start?: string | null;
  readonly sale_window_end?: string | null;
  readonly sort_order: number;
  /** AB-48: inventory bound to the category (list endpoint only). */
  readonly seat_count?: number | null;
  readonly ga_unit_count?: number | null;
}

/** AB-48: one scheduled price window (ticket_tier_prices). */
export interface TierPriceWindow {
  readonly id?: string;
  readonly valid_from: string;
  readonly valid_to: string | null;
  readonly price_amount: number;
}

export interface TierPriceSchedule {
  readonly tier_id: string;
  readonly base_price_amount: number;
  readonly current_price: number;
  readonly next_price_change_at: string | null;
  readonly windows: readonly TierPriceWindow[];
}

interface TierPriceScheduleEnvelope {
  readonly price_schedule: TierPriceSchedule;
}

/** AB-48: editable window row (local datetime strings, decimal price). */
export interface PriceWindowFormRow {
  readonly valid_from: string;
  readonly valid_to: string;
  readonly price_amount: string;
}

/**
 * Validate + convert schedule rows to the wire shape. Returns an error
 * message or the windows. Mirrors the backend rules: RFC3339 bounds,
 * valid_to > valid_from, non-negative price, no overlaps.
 */
export function buildPriceWindows(
  rows: readonly PriceWindowFormRow[],
): { windows?: TierPriceWindow[]; error?: string } {
  const out: TierPriceWindow[] = [];
  for (let i = 0; i < rows.length; i++) {
    const r = rows[i]!;
    const from = parseLocalDatetime(r.valid_from);
    if (from === null) return { error: `Window ${i + 1}: start is required.` };
    let to: Date | null = null;
    if (r.valid_to.trim() !== "") {
      to = parseLocalDatetime(r.valid_to);
      if (to === null) return { error: `Window ${i + 1}: end is invalid.` };
      if (to.getTime() <= from.getTime()) {
        return { error: `Window ${i + 1}: end must be after start.` };
      }
    }
    const cents = decimalToCents(r.price_amount);
    if (cents === null || cents < 0) {
      return { error: `Window ${i + 1}: price must be a decimal (e.g. 12.50).` };
    }
    out.push({
      valid_from: toRFC3339(r.valid_from),
      valid_to: to === null ? null : toRFC3339(r.valid_to),
      price_amount: cents,
    });
  }
  const sorted = [...out].sort(
    (a, b) => new Date(a.valid_from).getTime() - new Date(b.valid_from).getTime(),
  );
  for (let i = 1; i < sorted.length; i++) {
    const prev = sorted[i - 1]!;
    const cur = sorted[i]!;
    if (prev.valid_to === null || new Date(cur.valid_from) < new Date(prev.valid_to)) {
      return { error: "Windows overlap — each moment may carry exactly one price." };
    }
  }
  return { windows: sorted };
}

interface TicketTierListEnvelope {
  readonly ticket_tiers?: readonly TicketTierItem[];
  readonly tiers?: readonly TicketTierItem[];
}

export interface EventPublication {
  readonly id: string;
  readonly event_id: string;
  readonly feed_token_id: string;
  readonly city_id: string | null;
  readonly published_at: string;
}

interface EventPublicationListEnvelope {
  readonly publications: readonly EventPublication[];
}

export interface CityItem {
  readonly id: string;
  readonly country_id: string;
  readonly country_iso2: string;
  readonly slug: string;
  readonly name: string;
}

interface CityListEnvelope {
  readonly cities: readonly CityItem[];
}

export interface PublicationFormValues {
  /**
   * AB-43: the primary input is the sales channel; the feed token is
   * resolved (or auto-issued) behind the scenes so the operator never has
   * to know the token UUID.
   */
  sales_channel_id: string;
  /**
   * When "advanced" is opened, the operator may pin a specific existing
   * feed token for the selected channel; otherwise the newest active
   * token is used (or a new one is issued when the channel has none).
   */
  feed_token_id: string;
  city_id: string;
  /** True when the operator opened the advanced disclosure to override defaults. */
  advanced_open: boolean;
}

export interface PublicationRequestBody {
  feed_token_id: string;
  city_id?: string | null;
}

// AB-43: sales channel + feed token shapes.
export interface SalesChannelSummary {
  readonly id: string;
  readonly display_number: number;
  readonly name: string;
}

interface SalesChannelListEnvelope {
  readonly channels: readonly SalesChannelSummary[];
}

export interface FeedTokenSummary {
  readonly id: string;
  readonly token: string;
  readonly sales_channel_id: string;
  readonly label: string;
  readonly is_active: boolean;
  readonly revoked_at: string | null;
  readonly created_at: string;
}

interface FeedTokenListEnvelope {
  readonly feed_tokens: readonly FeedTokenSummary[];
}

interface FeedTokenEnvelope {
  readonly feed_token: FeedTokenSummary;
}

// AB-43: venues are consulted to derive the city scope from the event's
// first session's venue, so the operator does not re-enter data the
// platform already knows.
export interface VenueForCityLookup {
  readonly id: string;
  readonly city_id: string | null;
}

interface VenueLookupListEnvelope {
  readonly venues: readonly VenueForCityLookup[];
}

// ---------------------------------------------------------------------------
// Pure helpers (exported for unit tests)
// ---------------------------------------------------------------------------

export function isEventStatus(value: string): value is EventStatus {
  return (EVENT_STATUSES as readonly string[]).includes(value);
}

export function isEventVisibility(value: string): value is EventVisibility {
  return (EVENT_VISIBILITIES as readonly string[]).includes(value);
}

/**
 * Allowed status transitions, mirroring the backend state machine
 * documented in the OpenAPI UpdateEventStatusRequest schema. Re-applying
 * the same status is a server-side no-op and intentionally not offered
 * in the UI.
 */
export function allowedTransitions(status: EventStatus): readonly EventStatus[] {
  switch (status) {
    case "draft":
      return ["published", "cancelled"];
    case "published":
      return ["cancelled", "archived"];
    case "cancelled":
      return ["archived"];
    case "archived":
      return [];
  }
}

/**
 * Filter events whose `first_session_at` falls inside an inclusive date
 * range. Both bounds are optional ("" = unbounded). Inputs are
 * `<input type="date">` strings (yyyy-MM-dd, local TZ-naive); we compare
 * by ISO date prefix so an off-by-one timezone shift in the client does
 * not silently drop events near midnight UTC. Events with no sessions
 * (first_session_at === null) are excluded as soon as either bound is
 * set — they have no date to compare against.
 */
export function filterEventsByDateRange<
  T extends { first_session_at: string | null },
>(
  events: readonly T[],
  startAfter: string,
  endBefore: string,
): readonly T[] {
  const after = startAfter.trim();
  const before = endBefore.trim();
  if (after === "" && before === "") {
    return events;
  }
  return events.filter((e) => {
    if (e.first_session_at === null) {
      return false;
    }
    const day = e.first_session_at.slice(0, 10);
    if (after !== "" && day < after) {
      return false;
    }
    if (before !== "" && day > before) {
      return false;
    }
    return true;
  });
}

export function filterEventsByOrg<T extends { org_id: string }>(
  events: readonly T[],
  orgID: string,
): readonly T[] {
  if (orgID.trim() === "") {
    return events;
  }
  return events.filter((e) => e.org_id === orgID);
}

export function filterEventsByStatus<T extends { status: string }>(
  events: readonly T[],
  status: EventStatus | "",
): readonly T[] {
  if (status === "") {
    return events;
  }
  return events.filter((e) => e.status === status);
}

export function paginate<T>(items: readonly T[], page: number, pageSize: number): {
  rows: readonly T[];
  page: number;
  totalPages: number;
} {
  const total = items.length;
  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const clamped = Math.min(Math.max(1, page), totalPages);
  const start = (clamped - 1) * pageSize;
  return {
    rows: items.slice(start, start + pageSize),
    page: clamped,
    totalPages,
  };
}

export function formatDateTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return iso;
  }
  const pad = (n: number) => String(n).padStart(2, "0");
  return (
    `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())} ` +
    `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())} UTC`
  );
}

export function formatDateOnly(iso: string): string {
  return iso.slice(0, 10);
}

export function posterInitial(name: string): string {
  const trimmed = name.trim();
  return trimmed.length > 0 ? trimmed[0]!.toUpperCase() : "?";
}

export const PAGE_SIZE = 25;

// ---------------------------------------------------------------------------
// Session form helpers (exported for unit tests)
// ---------------------------------------------------------------------------

export const SESSION_STATUSES = [
  "draft",
  "scheduled",
  "cancelled",
  "completed",
] as const;
export type SessionStatus = (typeof SESSION_STATUSES)[number];

export const SESSION_ADMISSION_MODES = [
  "general_admission",
  "assigned_seats",
  "hybrid",
] as const;
export type SessionAdmissionMode = (typeof SESSION_ADMISSION_MODES)[number];

export function isSessionAdmissionMode(
  value: string,
): value is SessionAdmissionMode {
  return (SESSION_ADMISSION_MODES as readonly string[]).includes(value);
}

export interface SessionFormValues {
  /** Required — every session takes place at a venue (AB-36). */
  readonly venue_id: string;
  readonly start_at: string;
  readonly end_at: string;
  /**
   * Optional operator capacity ("" = derive from the venue / bound plan).
   * Replaces the former required capacity_total input — capacity is now
   * resolved server-side (AB-36).
   */
  readonly capacity_override: string;
  readonly status: SessionStatus;
  /** Create-mode only; ignored by the PATCH body builder. */
  readonly admission_mode: SessionAdmissionMode;
  /** Required when admission_mode is assigned_seats / hybrid (create). */
  readonly seating_plan_version_id: string;
  /** Optional explicit ISO 4217 override ("" = derive / keep, AB-38). */
  readonly currency: string;
}

export interface SessionFormErrors {
  readonly venue_id?: string;
  readonly start_at?: string;
  readonly end_at?: string;
  readonly capacity_override?: string;
  readonly status?: string;
  readonly seating_plan_version_id?: string;
  readonly currency?: string;
}

/**
 * Parse an `<input type="datetime-local">` value (YYYY-MM-DDTHH:MM, no
 * timezone) into a Date interpreted in the operator's local time. Returns
 * null on a blank or unparseable input.
 */
export function parseLocalDatetime(value: string): Date | null {
  const trimmed = value.trim();
  if (trimmed === "") {
    return null;
  }
  // datetime-local strings are tz-naive. The rest of the module renders
  // and round-trips them through UTC (toLocalDatetimeValue → toRFC3339),
  // so we interpret the value as UTC by appending Z; passing the raw
  // string to `new Date()` would otherwise apply the operator's local
  // timezone shift and silently break overlap comparisons against the
  // UTC ISO timestamps returned by the API.
  const iso = /Z$|[+-]\d{2}:?\d{2}$/.test(trimmed) ? trimmed : `${trimmed}Z`;
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return null;
  }
  return d;
}

/**
 * Convert an RFC3339 timestamp into the YYYY-MM-DDTHH:MM string accepted
 * by `<input type="datetime-local">`. We render in UTC to keep the round
 * trip lossless when the operator is comparing against the table column,
 * which is also rendered in UTC.
 */
export function toLocalDatetimeValue(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return "";
  }
  const pad = (n: number) => String(n).padStart(2, "0");
  return (
    `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}` +
    `T${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}`
  );
}

/**
 * Convert a datetime-local value back to RFC3339 (UTC). The value is
 * assumed to represent a UTC wall-clock (matches toLocalDatetimeValue),
 * so we append :00Z without applying a timezone shift.
 */
export function toRFC3339(value: string): string {
  return `${value}:00Z`;
}

/** Empty form values suitable for the "Add session" form. */
export function emptySessionForm(): SessionFormValues {
  return {
    venue_id: "",
    start_at: "",
    end_at: "",
    capacity_override: "",
    status: "draft",
    admission_mode: "general_admission",
    seating_plan_version_id: "",
    currency: "",
  };
}

/**
 * Populate a form from an existing session for editing. The currency
 * field starts blank — it is an OVERRIDE input; the session's current
 * currency (+ provenance) is rendered read-only next to it.
 */
export function sessionToForm(s: {
  venue_id: string;
  start_at: string;
  end_at: string;
  capacity_override: number | null;
  status: string;
  admission_mode: string;
  seating_plan_version_id: string | null;
}): SessionFormValues {
  return {
    venue_id: s.venue_id,
    start_at: toLocalDatetimeValue(s.start_at),
    end_at: toLocalDatetimeValue(s.end_at),
    capacity_override:
      s.capacity_override !== null ? String(s.capacity_override) : "",
    status: isSessionStatus(s.status) ? s.status : "draft",
    admission_mode: isSessionAdmissionMode(s.admission_mode)
      ? s.admission_mode
      : "general_admission",
    seating_plan_version_id: s.seating_plan_version_id ?? "",
    currency: "",
  };
}

export function isSessionStatus(value: string): value is SessionStatus {
  return (SESSION_STATUSES as readonly string[]).includes(value);
}

/**
 * Client-side validation mirroring the server-side guards from
 * sessions.go (post-AB-36/AB-38): venue_id is required, start_at and
 * end_at are required RFC3339 timestamps, end_at must be strictly after
 * start_at, capacity_override is OPTIONAL but must be a positive integer
 * when supplied, a seated admission mode requires a seating plan version
 * (create), and an explicit currency must be 3 uppercase letters.
 * Returns an errors map keyed by field; empty map means valid.
 */
export function validateSessionForm(
  values: SessionFormValues,
): SessionFormErrors {
  const errors: { -readonly [K in keyof SessionFormErrors]?: string } = {};

  if (values.venue_id.trim() === "") {
    errors.venue_id = "Venue is required.";
  }

  const start = parseLocalDatetime(values.start_at);
  if (start === null) {
    errors.start_at = "Start is required.";
  }
  const end = parseLocalDatetime(values.end_at);
  if (end === null) {
    errors.end_at = "End is required.";
  }
  if (start !== null && end !== null && end.getTime() <= start.getTime()) {
    errors.end_at = "End must be after start.";
  }

  const capStr = values.capacity_override.trim();
  if (capStr !== "") {
    if (!/^\d+$/.test(capStr)) {
      errors.capacity_override = "Capacity override must be a whole number.";
    } else {
      const cap = Number(capStr);
      if (cap <= 0) {
        errors.capacity_override =
          "Capacity override must be greater than zero.";
      } else if (cap > 2_000_000_000) {
        // The backend stores capacities as int32; refuse anything that
        // would overflow before we hit the wire.
        errors.capacity_override = "Capacity override is too large.";
      }
    }
  }

  if (!isSessionStatus(values.status)) {
    errors.status = "Status is invalid.";
  }

  if (
    values.admission_mode !== "general_admission" &&
    values.seating_plan_version_id.trim() === ""
  ) {
    errors.seating_plan_version_id =
      "A seating plan version is required for seated sessions.";
  }

  const cur = values.currency.trim();
  if (cur !== "" && !/^[A-Z]{3}$/.test(cur)) {
    errors.currency =
      "Currency must be a 3-letter uppercase ISO 4217 code (e.g. EUR).";
  }

  return errors;
}

/**
 * Build the JSON request body for POST / PATCH sessions. capacity_total
 * is never sent (AB-36 — capacity is derived server-side); optional
 * fields are omitted when blank so PATCH leaves them unchanged.
 * admission_mode / seating_plan_version_id are create-only (the PATCH
 * surface routes seating changes through the bind endpoint).
 */
export function buildSessionRequestBody(
  v: SessionFormValues,
  mode: "create" | "edit",
): Record<string, unknown> {
  const body: Record<string, unknown> = {
    venue_id: v.venue_id,
    start_at: toRFC3339(v.start_at),
    end_at: toRFC3339(v.end_at),
    status: v.status,
  };
  if (v.capacity_override.trim() !== "") {
    body.capacity_override = Number(v.capacity_override.trim());
  }
  if (v.currency.trim() !== "") {
    body.currency = v.currency.trim().toUpperCase();
  }
  if (mode === "create") {
    body.admission_mode = v.admission_mode;
    if (v.admission_mode !== "general_admission") {
      body.seating_plan_version_id = v.seating_plan_version_id;
    }
  }
  return body;
}

// ---------------------------------------------------------------------------
// Ticket-tier form helpers (feature #283; exported for unit tests)
// ---------------------------------------------------------------------------

export const TIER_PRICING_MODES = ["fixed", "free", "pwyw"] as const;
export type TierPricingMode = (typeof TIER_PRICING_MODES)[number];

export function isTierPricingMode(value: string): value is TierPricingMode {
  return (TIER_PRICING_MODES as readonly string[]).includes(value);
}

export interface TierFormValues {
  readonly name: string;
  readonly pricing_mode: TierPricingMode;
  /** Decimal string (major units, e.g. "12.50"). Converted to cents on submit. */
  readonly price_amount: string;
  /** Decimal string; only meaningful when pricing_mode === "pwyw". */
  readonly pwyw_min: string;
  readonly pwyw_max: string;
  /** Integer string; "" means unlimited. */
  readonly capacity: string;
  /** datetime-local string (YYYY-MM-DDTHH:MM, treated as UTC). */
  readonly sale_window_start: string;
  readonly sale_window_end: string;
  /** Integer string. */
  readonly sort_order: string;
}

export interface TierFormErrors {
  readonly name?: string;
  readonly pricing_mode?: string;
  readonly price_amount?: string;
  readonly pwyw_min?: string;
  readonly pwyw_max?: string;
  readonly capacity?: string;
  readonly sale_window_start?: string;
  readonly sale_window_end?: string;
  readonly sort_order?: string;
}

export function emptyTierForm(): TierFormValues {
  return {
    name: "",
    pricing_mode: "fixed",
    price_amount: "",
    pwyw_min: "",
    pwyw_max: "",
    capacity: "",
    sale_window_start: "",
    sale_window_end: "",
    sort_order: "0",
  };
}

export function tierToForm(t: TicketTierItem): TierFormValues {
  return {
    name: t.name,
    pricing_mode: isTierPricingMode(t.pricing_mode) ? t.pricing_mode : "fixed",
    price_amount: centsToDecimal(t.price_amount),
    pwyw_min:
      t.pwyw_min !== null && t.pwyw_min !== undefined
        ? centsToDecimal(t.pwyw_min)
        : "",
    pwyw_max:
      t.pwyw_max !== null && t.pwyw_max !== undefined
        ? centsToDecimal(t.pwyw_max)
        : "",
    capacity:
      t.capacity !== null && t.capacity !== undefined ? String(t.capacity) : "",
    sale_window_start:
      t.sale_window_start !== null && t.sale_window_start !== undefined
        ? toLocalDatetimeValue(t.sale_window_start)
        : "",
    sale_window_end:
      t.sale_window_end !== null && t.sale_window_end !== undefined
        ? toLocalDatetimeValue(t.sale_window_end)
        : "",
    sort_order: String(t.sort_order),
  };
}

/**
 * Convert an integer cents amount to a fixed two-decimal string suitable
 * for the form input. Negative values aren't expected (the backend
 * rejects them) but we render them faithfully so a corrupt row doesn't
 * silently roundtrip to 0.
 */
export function centsToDecimal(cents: number): string {
  const sign = cents < 0 ? "-" : "";
  const abs = Math.abs(Math.trunc(cents));
  const whole = Math.trunc(abs / 100);
  const frac = abs - whole * 100;
  return `${sign}${whole}.${frac < 10 ? "0" : ""}${frac}`;
}

/**
 * Parse a decimal-string price (e.g. "12.50") into integer cents.
 * Returns null on a malformed input. Accepts at most 2 fractional
 * digits — anything finer would silently round and corrupt accounting.
 */
export function decimalToCents(raw: string): number | null {
  const trimmed = raw.trim();
  if (trimmed === "") {
    return null;
  }
  if (!/^\d+(\.\d{1,2})?$/.test(trimmed)) {
    return null;
  }
  const [whole, frac = ""] = trimmed.split(".");
  const padded = (frac + "00").slice(0, 2);
  const cents = Number(whole) * 100 + Number(padded);
  if (!Number.isSafeInteger(cents)) {
    return null;
  }
  return cents;
}

/**
 * Validate a TierFormValues against the contract documented in
 * ticket_tiers.go and internal/domain/catalog.ValidatePricingMode.
 *
 * Currency is NOT part of the tier form any more (AB-38): every tier of
 * a session is denominated in the session currency, which the server
 * stamps on create/patch — the editor renders it read-only.
 */
export function validateTierForm(values: TierFormValues): TierFormErrors {
  const errors: { -readonly [K in keyof TierFormErrors]?: string } = {};

  if (values.name.trim() === "") {
    errors.name = "Name is required.";
  } else if (values.name.length > 200) {
    errors.name = "Name must be at most 200 characters.";
  }

  if (!isTierPricingMode(values.pricing_mode)) {
    errors.pricing_mode = "Pricing mode must be fixed, free, or pwyw.";
  }

  // Mode-specific price/pwyw rules.
  if (values.pricing_mode === "fixed") {
    const cents = decimalToCents(values.price_amount);
    if (cents === null) {
      errors.price_amount = "Price must be a decimal (e.g. 12.50).";
    } else if (cents <= 0) {
      errors.price_amount =
        "Fixed price must be greater than zero — use free mode for $0 tiers.";
    }
  } else if (values.pricing_mode === "free") {
    if (values.price_amount.trim() !== "" && values.price_amount.trim() !== "0" && values.price_amount.trim() !== "0.00") {
      // We force 0 on submit, but warn here so the operator sees the override.
      // Not a blocking error.
    }
  } else if (values.pricing_mode === "pwyw") {
    // pwyw allows 0 baseline; pwyw_min/max are optional but ordered.
    if (values.pwyw_min.trim() !== "") {
      const minCents = decimalToCents(values.pwyw_min);
      if (minCents === null || minCents < 0) {
        errors.pwyw_min = "pwyw_min must be a non-negative decimal.";
      }
    }
    if (values.pwyw_max.trim() !== "") {
      const maxCents = decimalToCents(values.pwyw_max);
      if (maxCents === null || maxCents < 0) {
        errors.pwyw_max = "pwyw_max must be a non-negative decimal.";
      }
    }
    if (
      errors.pwyw_min === undefined &&
      errors.pwyw_max === undefined &&
      values.pwyw_min.trim() !== "" &&
      values.pwyw_max.trim() !== ""
    ) {
      const minC = decimalToCents(values.pwyw_min)!;
      const maxC = decimalToCents(values.pwyw_max)!;
      if (minC > maxC) {
        errors.pwyw_max = "pwyw_max must be greater than or equal to pwyw_min.";
      }
    }
  }

  if (values.capacity.trim() !== "") {
    if (!/^\d+$/.test(values.capacity.trim())) {
      errors.capacity = "Capacity must be a whole number.";
    } else {
      const cap = Number(values.capacity);
      if (cap <= 0) {
        errors.capacity = "Capacity must be greater than zero.";
      } else if (cap > 2_000_000_000) {
        errors.capacity = "Capacity is too large.";
      }
    }
  }

  const saleStart = values.sale_window_start.trim();
  const saleEnd = values.sale_window_end.trim();
  if (saleStart !== "" && parseLocalDatetime(saleStart) === null) {
    errors.sale_window_start = "Sale start must be a valid timestamp.";
  }
  if (saleEnd !== "" && parseLocalDatetime(saleEnd) === null) {
    errors.sale_window_end = "Sale end must be a valid timestamp.";
  }
  if (
    errors.sale_window_start === undefined &&
    errors.sale_window_end === undefined &&
    saleStart !== "" &&
    saleEnd !== ""
  ) {
    const s = parseLocalDatetime(saleStart)!;
    const e = parseLocalDatetime(saleEnd)!;
    if (e.getTime() <= s.getTime()) {
      errors.sale_window_end = "Sale end must be after sale start.";
    }
  }

  if (values.sort_order.trim() === "") {
    errors.sort_order = "Sort order is required.";
  } else if (!/^-?\d+$/.test(values.sort_order.trim())) {
    errors.sort_order = "Sort order must be an integer.";
  } else {
    const so = Number(values.sort_order);
    if (so < -2_000_000_000 || so > 2_000_000_000) {
      errors.sort_order = "Sort order is out of range.";
    }
  }

  return errors;
}

/**
 * Translate an ApiError from a tier endpoint into a human-readable
 * sentence. Mirrors the error catalogue documented in ticket_tiers.go
 * so the operator sees the same message regardless of whether the
 * violation was detected client-side or rejected by the server.
 */
export function mapTierError(err: ApiError): string {
  switch (err.code) {
    case "tier.missing_name":
    case "tier.invalid_name":
      return "Name is required.";
    case "tier.missing_pricing_mode":
    case "tier.invalid_pricing_mode":
      return "Pricing mode must be fixed, free, or pwyw.";
    case "tier.invalid_capacity":
      return "Capacity must be greater than zero.";
    case "tier.invalid_sale_window":
      return "Sale end must be after sale start.";
    case "tier.invalid_sale_window_start":
    case "tier.invalid_sale_window_end":
      return "Sale window timestamps must be valid.";
    case "tier.not_found":
      return "Ticket tier no longer exists. The list will be refreshed.";
    case "tier.insert_failed":
    case "tier.update_failed":
    case "tier.delete_failed":
      return err.message || "Server failed to persist the change.";
    case "pricing.fixed_price_required":
    case "catalog.pricing.fixed_price_required":
      return "Fixed-price tiers require a positive price.";
    case "pricing.free_price_must_be_zero":
    case "catalog.pricing.free_price_must_be_zero":
      return "Free tiers must have a zero price.";
    case "pricing.pwyw_min_greater_than_max":
    case "catalog.pricing.pwyw_min_greater_than_max":
      return "pwyw_min must be less than or equal to pwyw_max.";
    case "permissions.denied":
      return "Your account is missing the permission required for this action.";
    default:
      if (err.status === 401) {
        return "Session expired. Please sign in again.";
      }
      if (err.status === 403) {
        return "Forbidden — missing required tier permission.";
      }
      return `${err.message} (${err.code})`;
  }
}

/**
 * Find sibling sessions in `siblings` whose time range overlaps the
 * supplied [start, end) window. Two ranges overlap iff
 * start < otherEnd AND end > otherStart. The candidate session (when
 * editing) can be excluded by id. Returns the overlapping sessions in
 * input order; an empty array means no overlap.
 *
 * The backend exposes an authoritative `has_overlapping_sessions` flag
 * on each list/get response; this helper exists so the form can warn
 * the operator BEFORE the round-trip and so the warning can identify
 * which siblings will conflict.
 */
export function findOverlappingSessions<
  T extends { id: string; start_at: string; end_at: string },
>(
  siblings: readonly T[],
  start_at: string,
  end_at: string,
  excludeID: string | null,
): readonly T[] {
  const start = parseLocalDatetime(start_at);
  const end = parseLocalDatetime(end_at);
  if (start === null || end === null || end.getTime() <= start.getTime()) {
    return [];
  }
  const startMs = start.getTime();
  const endMs = end.getTime();
  return siblings.filter((s) => {
    if (excludeID !== null && s.id === excludeID) {
      return false;
    }
    const otherStart = new Date(s.start_at).getTime();
    const otherEnd = new Date(s.end_at).getTime();
    if (Number.isNaN(otherStart) || Number.isNaN(otherEnd)) {
      return false;
    }
    return startMs < otherEnd && endMs > otherStart;
  });
}

// ---------------------------------------------------------------------------
// Nav entry binding
// ---------------------------------------------------------------------------

const EVENTS_NAV_ENTRY = NAV_BY_PATH["/events"];
if (EVENTS_NAV_ENTRY === undefined) {
  throw new Error("events route: NAV_BY_PATH['/events'] missing");
}

// ---------------------------------------------------------------------------
// Page shell
// ---------------------------------------------------------------------------

function EventsRoute() {
  return (
    <RequirePermission entry={EVENTS_NAV_ENTRY}>
      <EventsModule />
    </RequirePermission>
  );
}

function EventsModule() {
  const { permissions } = useAuth();
  const canPublish = permissions.has("event.publish");
  const canCreateEvent = permissions.has("event.create");
  const canUpdateEvent = permissions.has("event.update");
  const canDeleteEvent = permissions.has("event.delete");
  const canReadPublications = permissions.has("publication.read");
  const canCreatePublication = permissions.has("publication.create");
  const canDeletePublication = permissions.has("publication.delete");
  const canCreateSession = permissions.has("session.create");
  const canUpdateSession = permissions.has("session.update");
  const canDeleteSession = permissions.has("session.delete");
  const canCreateTier = permissions.has("tier.create");
  const canUpdateTier = permissions.has("tier.update");
  const canDeleteTier = permissions.has("tier.delete");

  const [visibilityFilter, setVisibilityFilter] =
    useState<EventVisibilityFilter>("all");
  const [orgFilter, setOrgFilter] = useState<string>("");
  const [statusFilter, setStatusFilter] = useState<EventStatus | "">("");
  const [startAfter, setStartAfter] = useState<string>("");
  const [endBefore, setEndBefore] = useState<string>("");
  const [page, setPage] = useState<number>(1);
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  // AB-42 wizard resume target — set when the operator clicks
  // "Continue setup" on a draft event's drawer overview.
  const [resumeEvent, setResumeEvent] = useState<EventItem | null>(null);

  const listQuery = useQuery<EventListEnvelope, ApiError>({
    queryKey: ["events", "list", visibilityFilter],
    queryFn: () =>
      authedFetch<EventListEnvelope>({
        method: "GET",
        path: `/v1/events?visibility=${encodeURIComponent(visibilityFilter)}`,
      }),
    retry: (failureCount, err) => {
      if (err instanceof ApiError) {
        if (err.status === 401 || err.status === 403 || err.status === 0) {
          return false;
        }
        if (err.code === "permissions.denied") {
          return false;
        }
      }
      return failureCount < 2;
    },
    refetchOnWindowFocus: false,
  });

  const orgsQuery = useQuery<OrganizationListEnvelope, ApiError>({
    queryKey: ["events", "orgs"],
    queryFn: () =>
      authedFetch<OrganizationListEnvelope>({
        method: "GET",
        path: "/v1/organizations",
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  const allEvents = listQuery.data?.events ?? [];

  const filtered = useMemo(() => {
    const byOrg = filterEventsByOrg(allEvents, orgFilter);
    const byStatus = filterEventsByStatus(byOrg, statusFilter);
    const byDate = filterEventsByDateRange(byStatus, startAfter, endBefore);
    // Sort by first session, events with no sessions last.
    return [...byDate].sort((a, b) => {
      if (a.first_session_at === null && b.first_session_at === null) return 0;
      if (a.first_session_at === null) return 1;
      if (b.first_session_at === null) return -1;
      return a.first_session_at.localeCompare(b.first_session_at);
    });
  }, [allEvents, orgFilter, statusFilter, startAfter, endBefore]);

  const paged = useMemo(
    () => paginate(filtered, page, PAGE_SIZE),
    [filtered, page],
  );

  useEffect(() => {
    // Reset to page 1 whenever filters narrow the list to fewer pages.
    if (page !== paged.page) {
      setPage(paged.page);
    }
  }, [paged.page, page]);

  const orgsByID = useMemo(() => {
    const map = new Map<string, OrganizationSummary>();
    for (const o of orgsQuery.data?.organizations ?? []) {
      map.set(o.id, o);
    }
    return map;
  }, [orgsQuery.data]);

  const selectedEvent = useMemo(
    () => allEvents.find((e) => e.id === selectedID) ?? null,
    [allEvents, selectedID],
  );

  return (
    <section aria-labelledby="events-heading" style={pageStyle}>
      <header style={headerStyle}>
        <div>
          <h1 id="events-heading" style={headingStyle}>
            Events
          </h1>
          <p style={subheadingStyle}>
            Cross-organization events directory. List is shared across
            organizations; status transitions (draft, published, cancelled,
            archived) are owner-gated and require the{" "}
            <code style={monoStyle}>event.publish</code> permission.
          </p>
        </div>
        <div style={refreshWrapStyle}>
          {canCreateEvent ? (
            <button
              type="button"
              onClick={() => setCreateOpen(true)}
              style={primaryButtonStyle}
              data-testid="events-create-open"
            >
              Create Event
            </button>
          ) : null}
          <button
            type="button"
            onClick={() => listQuery.refetch()}
            style={refreshButtonStyle}
            disabled={listQuery.isFetching}
            data-testid="events-refresh"
          >
            {listQuery.isFetching ? "Refreshing…" : "Refresh"}
          </button>
        </div>
      </header>

      <FilterBar
        visibility={visibilityFilter}
        onVisibility={(v) => {
          setVisibilityFilter(v);
          setPage(1);
        }}
        org={orgFilter}
        onOrg={(v) => {
          setOrgFilter(v);
          setPage(1);
        }}
        orgs={orgsQuery.data?.organizations ?? []}
        orgsLoading={orgsQuery.isPending}
        status={statusFilter}
        onStatus={(v) => {
          setStatusFilter(v);
          setPage(1);
        }}
        startAfter={startAfter}
        onStartAfter={(v) => {
          setStartAfter(v);
          setPage(1);
        }}
        endBefore={endBefore}
        onEndBefore={(v) => {
          setEndBefore(v);
          setPage(1);
        }}
      />

      <EventsBody
        query={listQuery}
        rows={paged.rows}
        totalFiltered={filtered.length}
        page={paged.page}
        totalPages={paged.totalPages}
        onPageChange={setPage}
        orgsByID={orgsByID}
        onSelect={(id) => setSelectedID(id)}
      />

      {selectedEvent !== null ? (
        <EventDrawer
          event={selectedEvent}
          orgsByID={orgsByID}
          orgs={orgsQuery.data?.organizations ?? []}
          canPublish={canPublish}
          canUpdateEvent={canUpdateEvent}
          canDeleteEvent={canDeleteEvent}
          canReadPublications={canReadPublications}
          canCreatePublication={canCreatePublication}
          canDeletePublication={canDeletePublication}
          canCreateSession={canCreateSession}
          canUpdateSession={canUpdateSession}
          canDeleteSession={canDeleteSession}
          canCreateTier={canCreateTier}
          canUpdateTier={canUpdateTier}
          canDeleteTier={canDeleteTier}
          onClose={() => setSelectedID(null)}
          onDeleted={() => {
            setSelectedID(null);
            void listQuery.refetch();
          }}
          onUpdated={() => {
            void listQuery.refetch();
          }}
          onResume={(ev) => {
            setSelectedID(null);
            setResumeEvent(ev);
          }}
        />
      ) : null}

      {createOpen ? (
        <EventWizard
          orgs={orgsQuery.data?.organizations ?? []}
          onClose={() => {
            setCreateOpen(false);
            void listQuery.refetch();
          }}
          onCompleted={() => {
            setCreateOpen(false);
            void listQuery.refetch();
          }}
        />
      ) : null}
      {resumeEvent !== null ? (
        <EventWizard
          orgs={orgsQuery.data?.organizations ?? []}
          initialEvent={resumeEvent}
          onClose={() => {
            setResumeEvent(null);
            void listQuery.refetch();
          }}
          onCompleted={() => {
            setResumeEvent(null);
            void listQuery.refetch();
          }}
        />
      ) : null}
    </section>
  );
}

// ---------------------------------------------------------------------------
// Filter bar
// ---------------------------------------------------------------------------

interface FilterBarProps {
  visibility: EventVisibilityFilter;
  onVisibility: (v: EventVisibilityFilter) => void;
  org: string;
  onOrg: (v: string) => void;
  orgs: readonly OrganizationSummary[];
  orgsLoading: boolean;
  status: EventStatus | "";
  onStatus: (v: EventStatus | "") => void;
  startAfter: string;
  onStartAfter: (v: string) => void;
  endBefore: string;
  onEndBefore: (v: string) => void;
}

function FilterBar(props: FilterBarProps) {
  return (
    <div style={filterBarStyle} role="search" aria-label="Events filters">
      <label style={filterFieldStyle}>
        <span style={filterLabelStyle}>Organization</span>
        <select
          value={props.org}
          onChange={(e) => props.onOrg(e.target.value)}
          style={filterSelectStyle}
          data-testid="events-filter-org"
          disabled={props.orgsLoading}
        >
          <option value="">All organizations</option>
          {[...props.orgs]
            .sort((a, b) => a.name.localeCompare(b.name))
            .map((o) => (
              <option key={o.id} value={o.id} title={o.id}>
                {o.display_number !== undefined
                  ? `${o.name} · #${o.display_number}`
                  : o.name}
              </option>
            ))}
        </select>
      </label>
      <label style={filterFieldStyle}>
        <span style={filterLabelStyle}>Status</span>
        <select
          value={props.status}
          onChange={(e) => {
            const v = e.target.value;
            props.onStatus(v === "" ? "" : (v as EventStatus));
          }}
          style={filterSelectStyle}
          data-testid="events-filter-status"
        >
          <option value="">All statuses</option>
          {EVENT_STATUSES.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
      </label>
      <label style={filterFieldStyle}>
        <span style={filterLabelStyle}>Visibility</span>
        <select
          value={props.visibility}
          onChange={(e) =>
            props.onVisibility(e.target.value as EventVisibilityFilter)
          }
          style={filterSelectStyle}
          data-testid="events-filter-visibility"
        >
          <option value="all">All</option>
          {EVENT_VISIBILITIES.map((v) => (
            <option key={v} value={v}>
              {v}
            </option>
          ))}
        </select>
      </label>
      <label style={filterFieldStyle}>
        <span style={filterLabelStyle}>Starts on or after</span>
        <input
          type="date"
          value={props.startAfter}
          onChange={(e) => props.onStartAfter(e.target.value)}
          style={filterInputStyle}
          data-testid="events-filter-start"
        />
      </label>
      <label style={filterFieldStyle}>
        <span style={filterLabelStyle}>Starts on or before</span>
        <input
          type="date"
          value={props.endBefore}
          onChange={(e) => props.onEndBefore(e.target.value)}
          style={filterInputStyle}
          data-testid="events-filter-end"
        />
      </label>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Body: list table + pagination
// ---------------------------------------------------------------------------

interface BodyProps {
  query: ReturnType<typeof useQuery<EventListEnvelope, ApiError>>;
  rows: readonly EventItem[];
  totalFiltered: number;
  page: number;
  totalPages: number;
  onPageChange: (n: number) => void;
  orgsByID: ReadonlyMap<string, OrganizationSummary>;
  onSelect: (id: string) => void;
}

function EventsBody({
  query,
  rows,
  totalFiltered,
  page,
  totalPages,
  onPageChange,
  orgsByID,
  onSelect,
}: BodyProps) {
  if (query.isPending) {
    return (
      <div style={statusBoxStyle} role="status" aria-live="polite">
        Loading events from /v1/events…
      </div>
    );
  }
  if (query.isError) {
    return <EventsErrorState error={query.error} onRetry={() => query.refetch()} />;
  }
  if (rows.length === 0) {
    return (
      <div style={statusBoxStyle} role="status" data-testid="events-empty">
        {totalFiltered === 0
          ? "No events match the current filters."
          : "No events on this page."}
      </div>
    );
  }
  const columns: ResponsiveTableColumn<EventItem>[] = [
    {
      id: "poster",
      header: "Poster",
      hideOnMobile: true,
      renderCell: (ev) => <PosterThumb event={ev} />,
    },
    {
      id: "name",
      header: "Name",
      primary: true,
      renderCell: (ev) => (
        <span data-testid={`events-row-${ev.id}`}>
          <button
            type="button"
            style={linkButtonStyle}
            onClick={(e) => {
              e.stopPropagation();
              onSelect(ev.id);
            }}
            data-testid={`events-open-${ev.id}`}
          >
            {ev.name} · #{ev.display_number}
          </button>
          <div style={mutedHintStyle} title={ev.org_id}>
            {(() => {
              const org = orgsByID.get(ev.org_id);
              if (org === undefined) return shortenUUID(ev.org_id);
              return org.display_number !== undefined
                ? `${org.name} · #${org.display_number}`
                : org.name;
            })()}
          </div>
        </span>
      ),
    },
    {
      id: "venue",
      header: "Venue",
      renderCell: (ev) =>
        ev.venue_names.length > 0 ? (
          <span title={ev.venue_names.join(", ")}>
            {ev.venue_names.join(", ")}
          </span>
        ) : (
          <span style={mutedHintStyle}>—</span>
        ),
    },
    {
      id: "first_session",
      header: "First session",
      renderCell: (ev) =>
        ev.first_session_at !== null ? (
          formatDateTime(ev.first_session_at)
        ) : (
          <span style={mutedHintStyle}>No sessions</span>
        ),
    },
    {
      id: "status",
      header: "Status",
      renderCell: (ev) => <EventStatusBadge status={ev.status} />,
    },
    {
      id: "channels",
      header: "Channels",
      renderCell: () => (
        <span
          style={mutedHintStyle}
          title="Open the drawer's Publications tab to view channels."
        >
          —
        </span>
      ),
    },
  ];
  return (
    <>
      <div style={tableWrapStyle} role="region" aria-label="Events">
        <ResponsiveTable<EventItem>
          id="events-table"
          columns={columns}
          rows={rows}
          rowKey={(ev) => ev.id}
          onRowClick={(ev) => onSelect(ev.id)}
        />
      </div>
      <Pagination
        page={page}
        totalPages={totalPages}
        totalFiltered={totalFiltered}
        onChange={onPageChange}
      />
    </>
  );
}

function PosterThumb({ event }: { event: EventItem }) {
  if (event.image_url !== null && event.image_url !== "") {
    return (
      <img
        src={event.image_url}
        alt=""
        width={40}
        height={40}
        style={posterImgStyle}
      />
    );
  }
  return (
    <div style={posterFallbackStyle} aria-hidden="true">
      {posterInitial(event.name)}
    </div>
  );
}

function Pagination({
  page,
  totalPages,
  totalFiltered,
  onChange,
}: {
  page: number;
  totalPages: number;
  totalFiltered: number;
  onChange: (n: number) => void;
}) {
  if (totalFiltered <= PAGE_SIZE) {
    return null;
  }
  return (
    <div style={paginationStyle} data-testid="events-pagination">
      <button
        type="button"
        style={refreshButtonStyle}
        onClick={() => onChange(page - 1)}
        disabled={page <= 1}
        data-testid="events-prev"
      >
        Previous
      </button>
      <span style={mutedHintStyle}>
        Page {page} of {totalPages} · {totalFiltered} events
      </span>
      <button
        type="button"
        style={refreshButtonStyle}
        onClick={() => onChange(page + 1)}
        disabled={page >= totalPages}
        data-testid="events-next"
      >
        Next
      </button>
    </div>
  );
}

function EventsErrorState({
  error,
  onRetry,
}: {
  error: ApiError | null;
  onRetry: () => void;
}) {
  if (
    error instanceof ApiError &&
    (error.status === 403 || error.code === "permissions.denied")
  ) {
    return (
      <div style={errorBoxStyle} role="alert" data-testid="events-forbidden">
        <strong>Forbidden.</strong>
        <p style={errorParaStyle}>
          Your account is missing <code style={monoStyle}>event.read</code>.
          Ask a platform administrator to grant the permission.
        </p>
      </div>
    );
  }
  if (error instanceof ApiError && error.status === 401) {
    return (
      <div style={errorBoxStyle} role="status" data-testid="events-session-expired">
        <strong>Session expired.</strong>
        <p style={errorParaStyle}>Sign in again to reload events.</p>
      </div>
    );
  }
  return (
    <div style={errorBoxStyle} role="alert" data-testid="events-error">
      <strong>Failed to load events.</strong>
      <div style={errorCodeStyle}>{error?.code ?? "unknown.error"}</div>
      {error?.message ? <div style={errorParaStyle}>{error.message}</div> : null}
      <button type="button" style={errorRetryStyle} onClick={onRetry}>
        Retry
      </button>
    </div>
  );
}

function EventStatusBadge({ status }: { status: EventStatus }) {
  const palette: Record<EventStatus, CSSProperties> = {
    draft: { background: "#fef3c7", color: "#854d0e", borderColor: "#fde68a" },
    published: { background: "#dcfce7", color: "#166534", borderColor: "#86efac" },
    cancelled: { background: "#fee2e2", color: "#991b1b", borderColor: "#fca5a5" },
    archived: { background: "#f1f5f9", color: "#475569", borderColor: "#cbd5e1" },
  };
  return (
    <span
      style={{ ...statusBadgeStyle, ...palette[status] }}
      data-testid={`events-status-${status}`}
    >
      {status}
    </span>
  );
}

// ---------------------------------------------------------------------------
// Drawer: 5 tabs (Overview / Sessions / Tiers / Publications / Activity)
// ---------------------------------------------------------------------------

type DrawerTab = "overview" | "sessions" | "tiers" | "publications" | "activity";

const DRAWER_TABS: ReadonlyArray<{ id: DrawerTab; label: string }> = [
  { id: "overview", label: "Overview" },
  { id: "sessions", label: "Sessions" },
  { id: "tiers", label: "Ticket tiers" },
  { id: "publications", label: "Publications" },
  { id: "activity", label: "Activity" },
];

interface DrawerProps {
  event: EventItem;
  orgsByID: ReadonlyMap<string, OrganizationSummary>;
  orgs: readonly OrganizationSummary[];
  canPublish: boolean;
  canUpdateEvent: boolean;
  canDeleteEvent: boolean;
  canReadPublications: boolean;
  canCreatePublication: boolean;
  canDeletePublication: boolean;
  canCreateSession: boolean;
  canUpdateSession: boolean;
  canDeleteSession: boolean;
  canCreateTier: boolean;
  canUpdateTier: boolean;
  canDeleteTier: boolean;
  onClose: () => void;
  onDeleted: () => void;
  onUpdated: () => void;
  /**
   * AB-42: fired when the operator clicks "Continue setup" on a draft
   * event, so the page can open the wizard resumed at the first
   * incomplete step.
   */
  onResume: (event: EventItem) => void;
}

function EventDrawer({
  event,
  orgsByID,
  orgs,
  canPublish,
  canUpdateEvent,
  canDeleteEvent,
  canReadPublications,
  canCreatePublication,
  canDeletePublication,
  canCreateSession,
  canUpdateSession,
  canDeleteSession,
  canCreateTier,
  canUpdateTier,
  canDeleteTier,
  onClose,
  onDeleted,
  onUpdated,
  onResume,
}: DrawerProps) {
  const [tab, setTab] = useState<DrawerTab>("overview");
  // AB-44: the event drawer must NOT close on an outside click — the
  // operator is deep in a detail view and a stray backdrop click loses
  // context. Escape and the explicit × button remain the only dismissal
  // affordances. (Detail view has no dirty form of its own — the inline
  // Edit / Session / Tier editors track their own dirty state.)
  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="events-drawer-title"
      style={drawerBackdropStyle}
      data-testid="events-drawer"
      // Intentionally NO onClick={onClose}: see AB-44 comment above.
    >
      <aside style={drawerStyle} onClick={(e) => e.stopPropagation()}>
        <header style={drawerHeaderStyle}>
          <div>
            <h2 id="events-drawer-title" style={drawerTitleStyle}>
              {event.name}
            </h2>
            <div style={mutedHintStyle}>
              <code style={monoStyle}>{event.id}</code>
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            style={dialogCloseStyle}
            aria-label="Close"
            data-testid="events-drawer-close"
          >
            ×
          </button>
        </header>
        <nav style={drawerTabBarStyle} aria-label="Event detail tabs">
          {DRAWER_TABS.map((t) => (
            <button
              key={t.id}
              type="button"
              style={tab === t.id ? activeTabStyle : tabStyle}
              onClick={() => setTab(t.id)}
              data-testid={`events-tab-${t.id}`}
              aria-current={tab === t.id ? "page" : undefined}
            >
              {t.label}
            </button>
          ))}
        </nav>
        <div style={drawerContentStyle}>
          {tab === "overview" ? (
            <OverviewTab
              event={event}
              orgsByID={orgsByID}
              orgs={orgs}
              canPublish={canPublish}
              canUpdateEvent={canUpdateEvent}
              canDeleteEvent={canDeleteEvent}
              canCreateSession={canCreateSession}
              onDeleted={onDeleted}
              onUpdated={onUpdated}
              onResume={onResume}
            />
          ) : null}
          {tab === "sessions" ? (
            <SessionsTab
              event={event}
              canCreate={canCreateSession}
              canUpdate={canUpdateSession}
              canDelete={canDeleteSession}
            />
          ) : null}
          {tab === "tiers" ? (
            <TiersTab
              event={event}
              canCreate={canCreateTier}
              canUpdate={canUpdateTier}
              canDelete={canDeleteTier}
            />
          ) : null}
          {tab === "publications" ? (
            <PublicationsTab
              event={event}
              canRead={canReadPublications}
              canCreate={canCreatePublication}
              canDelete={canDeletePublication}
            />
          ) : null}
          {tab === "activity" ? <ActivityTab /> : null}
        </div>
      </aside>
    </div>
  );
}

function OverviewTab({
  event,
  orgsByID,
  orgs,
  canPublish,
  canUpdateEvent,
  canDeleteEvent,
  canCreateSession,
  onDeleted,
  onUpdated,
  onResume,
}: {
  event: EventItem;
  orgsByID: ReadonlyMap<string, OrganizationSummary>;
  orgs: readonly OrganizationSummary[];
  canPublish: boolean;
  canUpdateEvent: boolean;
  canDeleteEvent: boolean;
  canCreateSession: boolean;
  onResume: (event: EventItem) => void;
  onDeleted: () => void;
  onUpdated: () => void;
}) {
  const queryClient = useQueryClient();
  const [errMsg, setErrMsg] = useState<string | null>(null);
  const [okMsg, setOkMsg] = useState<string | null>(null);
  const [editOpen, setEditOpen] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [deleteErr, setDeleteErr] = useState<string | null>(null);
  const transitions = allowedTransitions(event.status);

  const deleteMutation = useMutation<void, ApiError, void>({
    mutationFn: () =>
      authedFetch<void>({
        method: "DELETE",
        path: `/v1/organizations/${event.org_id}/events/${event.id}`,
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["events"] });
      onDeleted();
    },
    onError: (err) => {
      setDeleteErr(`${err.message} (${err.code})`);
    },
  });

  const mutation = useMutation<EventEnvelope, ApiError, EventStatus>({
    mutationFn: (target) =>
      authedFetch<EventEnvelope>({
        method: "POST",
        path: `/v1/organizations/${event.org_id}/events/${event.id}/status`,
        body: { status: target },
      }),
    onSuccess: (data, target) => {
      setErrMsg(null);
      setOkMsg(`Status changed to ${target}.`);
      queryClient.invalidateQueries({ queryKey: ["events"] });
      // Re-fetch the single event too for any downstream readers.
      void queryClient.invalidateQueries({
        queryKey: ["events", "detail", data.event.id],
      });
    },
    onError: (err) => {
      setOkMsg(null);
      if (err.code === "event.invalid_transition") {
        setErrMsg(
          err.message ||
            "That status transition is not permitted from the current state.",
        );
      } else if (err.code === "permissions.denied" || err.status === 403) {
        setErrMsg(
          "Your account is missing event.publish. Ask a platform administrator.",
        );
      } else {
        setErrMsg(`${err.message} (${err.code})`);
      }
    },
  });

  return (
    <div style={tabBodyStyle}>
      {/* AB-42 resumability: a draft event surfaces a "Continue setup"
          action that reopens the wizard at the first incomplete step. */}
      {event.status === "draft" && canCreateSession ? (
        <div style={rowActionsStyle}>
          <button
            type="button"
            style={primaryButtonStyle}
            onClick={() => onResume(event)}
            data-testid="events-resume-setup"
          >
            Continue setup
          </button>
          <span style={mutedHintStyle}>
            Draft event — publish requires at least one session with a tier.
          </span>
        </div>
      ) : null}
      {/* Edit / Delete action buttons */}
      {(canUpdateEvent || canDeleteEvent) ? (
        <div style={rowActionsStyle}>
          {canUpdateEvent ? (
            <button
              type="button"
              style={refreshButtonStyle}
              onClick={() => {
                setEditOpen(true);
                setConfirmDelete(false);
              }}
              data-testid="events-edit-open"
              disabled={editOpen}
            >
              Edit
            </button>
          ) : null}
          {canDeleteEvent ? (
            <button
              type="button"
              style={dangerButtonStyle}
              onClick={() => {
                setConfirmDelete(true);
                setEditOpen(false);
                setDeleteErr(null);
              }}
              data-testid="events-delete-open"
              disabled={deleteMutation.isPending}
            >
              Delete
            </button>
          ) : null}
        </div>
      ) : null}

      {confirmDelete ? (
        <div style={confirmDeleteStyle} data-testid="events-delete-confirm">
          <span>
            Are you sure you want to delete this event? This cannot be undone.
          </span>
          <div style={rowActionsStyle}>
            <button
              type="button"
              style={dangerButtonStyle}
              onClick={() => deleteMutation.mutate()}
              disabled={deleteMutation.isPending}
              data-testid="events-delete-confirm-yes"
            >
              {deleteMutation.isPending ? "Deleting…" : "Yes, delete"}
            </button>
            <button
              type="button"
              style={refreshButtonStyle}
              onClick={() => {
                setConfirmDelete(false);
                setDeleteErr(null);
              }}
              disabled={deleteMutation.isPending}
              data-testid="events-delete-confirm-cancel"
            >
              Cancel
            </button>
          </div>
          {deleteErr !== null ? (
            <div style={formErrorStyle} role="alert" data-testid="events-delete-error">
              {deleteErr}
            </div>
          ) : null}
        </div>
      ) : null}

      {editOpen ? (
        <EditEventPanel
          event={event}
          orgs={orgs}
          onSuccess={() => {
            setEditOpen(false);
            void queryClient.invalidateQueries({ queryKey: ["events"] });
            onUpdated();
          }}
          onClose={() => setEditOpen(false)}
        />
      ) : null}

      <DetailRow label="Status">
        <EventStatusBadge status={event.status} />
      </DetailRow>
      <DetailRow label="Visibility">{event.visibility}</DetailRow>
      <DetailRow label="Organization">
        {(() => {
          const org = orgsByID.get(event.org_id);
          if (org === undefined) {
            return <code style={monoStyle}>{event.org_id}</code>;
          }
          const orgLabel =
            org.display_number !== undefined
              ? `${org.name} · #${org.display_number}`
              : org.name;
          return <span title={event.org_id}>{orgLabel}</span>;
        })()}
      </DetailRow>
      <DetailRow label="Venues">
        {event.venue_names.length > 0 ? (
          event.venue_names.join(", ")
        ) : (
          <span style={mutedHintStyle}>No sessions</span>
        )}
      </DetailRow>
      <DetailRow label="First session">
        {event.first_session_at !== null ? (
          formatDateTime(event.first_session_at)
        ) : (
          <span style={mutedHintStyle}>No sessions</span>
        )}
      </DetailRow>
      <DetailRow label="Last session">
        {event.last_session_at !== null ? (
          formatDateTime(event.last_session_at)
        ) : (
          <span style={mutedHintStyle}>No sessions</span>
        )}
      </DetailRow>
      <DetailRow label="Created">{formatDateOnly(event.created_at)}</DetailRow>
      <DetailRow label="Updated">{formatDateOnly(event.updated_at)}</DetailRow>
      {event.description !== null && event.description !== "" ? (
        <div style={descriptionBlockStyle}>
          <div style={detailLabelStyle}>Description</div>
          <p style={descriptionTextStyle}>{event.description}</p>
        </div>
      ) : null}

      <div style={transitionSectionStyle}>
        <div style={detailLabelStyle}>Status transitions</div>
        {transitions.length === 0 ? (
          <p style={mutedHintStyle}>
            No further transitions are allowed from <code style={monoStyle}>{event.status}</code>.
          </p>
        ) : !canPublish ? (
          <p style={mutedHintStyle}>
            Status transitions require the{" "}
            <code style={monoStyle}>event.publish</code> permission.
          </p>
        ) : (
          <div style={transitionButtonRowStyle}>
            {transitions.map((target) => (
              <button
                key={target}
                type="button"
                style={target === "cancelled" ? dangerButtonStyle : primaryButtonStyle}
                onClick={() => mutation.mutate(target)}
                disabled={mutation.isPending}
                data-testid={`events-transition-${target}`}
              >
                {mutation.isPending && mutation.variables === target
                  ? "Submitting…"
                  : `Set to ${target}`}
              </button>
            ))}
          </div>
        )}
        {errMsg !== null ? (
          <div style={formErrorStyle} role="alert" data-testid="events-transition-error">
            {errMsg}
          </div>
        ) : null}
        {okMsg !== null ? (
          <div style={successBoxStyle} role="status" data-testid="events-transition-ok">
            {okMsg}
          </div>
        ) : null}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// EventWizard — AB-42 multi-step "no event → sellable published event" flow.
//
// Replaces the flat single-modal Create Event that posted only the event
// and left it unsellable. Steps are:
//   1. event identity (org, name, visibility, description) — creates a
//      draft event immediately, so partial progress survives close.
//   2. first session (venue, dates, admission mode, seating plan). Reuses
//      SessionEditor; capacity is DERIVED and shown read-only there.
//   3. ticket tiers for the session (add-only list) + publish gate. Reuses
//      TierEditor. Publish fires POST .../status { status: "published" };
//      the backend refuses events with no session or any session missing
//      a tier and returns a specific code we surface inline.
//
// Resumability: when opened with an existing draft event the wizard skips
// to the first incomplete step (no sessions → step 2; some session
// without a tier → step 3; otherwise → the publish confirmation).
// Outside-click does NOT dismiss (AB-44 rule for critical dialogs) — only
// the explicit close/cancel/save-draft buttons do.
// ---------------------------------------------------------------------------

type WizardStep = 1 | 2 | 3;

interface EventWizardProps {
  orgs: readonly OrganizationSummary[];
  /** If provided, the wizard resumes on this draft event (no step 1). */
  initialEvent?: EventItem | null;
  onClose: () => void;
  onCompleted: () => void;
}

function EventWizard({
  orgs,
  initialEvent = null,
  onClose,
  onCompleted,
}: EventWizardProps) {
  const queryClient = useQueryClient();
  const [event, setEvent] = useState<EventItem | null>(initialEvent);
  const [session, setSession] = useState<SessionItem | null>(null);
  const [tiers, setTiers] = useState<readonly TicketTierItem[]>([]);
  const [step, setStep] = useState<WizardStep>(event === null ? 1 : 2);
  const [banner, setBanner] = useState<{
    kind: "ok" | "err";
    msg: string;
  } | null>(null);

  // When resuming on an existing draft event, discover its first session
  // (if any) so we can jump the operator to step 3 directly.
  const resumeSessionsQuery = useQuery<SessionListEnvelope, ApiError>({
    queryKey: ["events", "wizard-sessions", event?.id],
    // Only run on the resume path — a freshly-created event trivially has
    // no sessions yet, so skip the GET.
    enabled:
      initialEvent !== null &&
      event !== null &&
      session === null &&
      step === 2,
    queryFn: () =>
      authedFetch<SessionListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${event!.org_id}/events/${event!.id}/sessions`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  useEffect(() => {
    if (
      event !== null &&
      session === null &&
      resumeSessionsQuery.data !== undefined
    ) {
      const first = resumeSessionsQuery.data.sessions[0];
      if (first !== undefined) {
        setSession(first);
        setStep(3);
      }
    }
  }, [event, session, resumeSessionsQuery.data]);

  const resumeTiersQuery = useQuery<TicketTierListEnvelope, ApiError>({
    queryKey: ["events", "wizard-tiers", session?.id],
    enabled:
      event !== null && session !== null && tiers.length === 0 && step === 3,
    queryFn: () =>
      authedFetch<TicketTierListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${event!.org_id}/events/${event!.id}/sessions/${session!.id}/tiers`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  useEffect(() => {
    if (resumeTiersQuery.data !== undefined) {
      const list =
        resumeTiersQuery.data.ticket_tiers ??
        resumeTiersQuery.data.tiers ??
        [];
      if (list.length > 0) setTiers(list);
    }
  }, [resumeTiersQuery.data]);

  // Step-1 form state (event identity).
  const [values, setValues] = useState<EventFormValues>(emptyEventForm);
  const step1Errors = useMemo(() => validateEventForm(values, true), [values]);

  const createEventMutation = useMutation<
    EventEnvelope,
    ApiError,
    EventFormValues
  >({
    mutationFn: (v) => {
      const body: Record<string, unknown> = { name: v.name.trim() };
      if (v.description.trim() !== "") body.description = v.description.trim();
      if (v.visibility !== "") body.visibility = v.visibility;
      return authedFetch<EventEnvelope>({
        method: "POST",
        path: `/v1/organizations/${v.org_id}/events`,
        body,
      });
    },
    onSuccess: (data) => {
      setEvent(data.event);
      setBanner({
        kind: "ok",
        msg: `Draft event "${data.event.name}" created. Now add a session.`,
      });
      setStep(2);
      void queryClient.invalidateQueries({ queryKey: ["events"] });
    },
    onError: (err) => {
      setBanner({ kind: "err", msg: `${err.message} (${err.code})` });
    },
  });

  const publishMutation = useMutation<EventEnvelope, ApiError, void>({
    mutationFn: () =>
      authedFetch<EventEnvelope>({
        method: "POST",
        path: `/v1/organizations/${event!.org_id}/events/${event!.id}/status`,
        body: { status: "published" },
      }),
    onSuccess: (data) => {
      setBanner({ kind: "ok", msg: `Published "${data.event.name}".` });
      void queryClient.invalidateQueries({ queryKey: ["events"] });
      onCompleted();
    },
    onError: (err) => {
      setBanner({ kind: "err", msg: mapPublishError(err) });
    },
  });

  const stepStatus = (n: WizardStep): "done" | "current" | "todo" => {
    if (n < step) return "done";
    if (n === step) return "current";
    return "todo";
  };

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="event-wizard-title"
      style={createPanelBackdropStyle}
      data-testid="events-wizard"
      // AB-42/AB-44: do NOT close on outside click.
    >
      <div
        style={{ ...createPanelStyle, maxWidth: 780 }}
        onClick={(e) => e.stopPropagation()}
      >
        <header style={drawerHeaderStyle}>
          <div>
            <h2 id="event-wizard-title" style={drawerTitleStyle}>
              {event === null ? "Create event" : `Set up "${event.name}"`}
            </h2>
            <div style={mutedHintStyle} data-testid="events-wizard-progress">
              <span data-status={stepStatus(1)}>
                {stepStatus(1) === "done" ? "✓" : "1"} Event
              </span>
              {" · "}
              <span data-status={stepStatus(2)}>
                {stepStatus(2) === "done" ? "✓" : "2"} Session
              </span>
              {" · "}
              <span data-status={stepStatus(3)}>
                {stepStatus(3) === "done" ? "✓" : "3"} Tiers &amp; publish
              </span>
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            style={dialogCloseStyle}
            aria-label="Close wizard"
            data-testid="events-wizard-close"
          >
            ×
          </button>
        </header>

        <div style={createPanelBodyStyle}>
          {banner !== null ? (
            <div
              style={banner.kind === "ok" ? successBoxStyle : formErrorStyle}
              role={banner.kind === "ok" ? "status" : "alert"}
              data-testid={
                banner.kind === "ok"
                  ? "events-wizard-ok"
                  : "events-wizard-error"
              }
            >
              {banner.msg}
            </div>
          ) : null}

          {step === 1 ? (
            <form
              style={editorFormStyle}
              data-testid="events-wizard-step1-form"
              onSubmit={(e: FormEvent) => {
                e.preventDefault();
                if (Object.keys(step1Errors).length > 0) return;
                setBanner(null);
                createEventMutation.mutate(values);
              }}
            >
              <div style={editorGridStyle}>
                <label style={editorFieldStyle}>
                  <span style={editorLabelStyle}>Organization *</span>
                  <select
                    value={values.org_id}
                    onChange={(e) =>
                      setValues({ ...values, org_id: e.target.value })
                    }
                    style={editorInputStyle}
                    required
                    data-testid="events-wizard-org"
                  >
                    <option value="">— select org —</option>
                    {orgs.map((o) => (
                      <option key={o.id} value={o.id}>
                        {o.display_number !== undefined
                          ? `#${o.display_number} ${o.name}`
                          : o.name}
                      </option>
                    ))}
                  </select>
                  {step1Errors.org_id !== undefined ? (
                    <span style={fieldErrorStyle}>{step1Errors.org_id}</span>
                  ) : null}
                </label>
                <label style={editorFieldStyle}>
                  <span style={editorLabelStyle}>Name *</span>
                  <input
                    type="text"
                    value={values.name}
                    onChange={(e) =>
                      setValues({ ...values, name: e.target.value })
                    }
                    style={editorInputStyle}
                    required
                    data-testid="events-wizard-name"
                  />
                  {step1Errors.name !== undefined ? (
                    <span style={fieldErrorStyle}>{step1Errors.name}</span>
                  ) : null}
                </label>
                <label style={editorFieldStyle}>
                  <span style={editorLabelStyle}>Visibility</span>
                  <select
                    value={values.visibility}
                    onChange={(e) =>
                      setValues({
                        ...values,
                        visibility: isEventVisibility(e.target.value)
                          ? e.target.value
                          : "",
                      })
                    }
                    style={editorInputStyle}
                    data-testid="events-wizard-visibility"
                  >
                    <option value="">— default (public) —</option>
                    {EVENT_VISIBILITIES.map((v) => (
                      <option key={v} value={v}>
                        {v}
                      </option>
                    ))}
                  </select>
                </label>
              </div>
              <label style={editorFieldStyle}>
                <span style={editorLabelStyle}>Description (optional)</span>
                <textarea
                  value={values.description}
                  onChange={(e) =>
                    setValues({ ...values, description: e.target.value })
                  }
                  style={{
                    ...editorInputStyle,
                    minHeight: 72,
                    resize: "vertical",
                  }}
                  rows={3}
                  data-testid="events-wizard-description"
                />
              </label>
              <div style={mobileFormBarStyle}>
                <button
                  type="button"
                  style={refreshButtonStyle}
                  onClick={onClose}
                  disabled={createEventMutation.isPending}
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  style={primaryButtonStyle}
                  disabled={
                    createEventMutation.isPending ||
                    Object.keys(step1Errors).length > 0
                  }
                  data-testid="events-wizard-step1-submit"
                >
                  {createEventMutation.isPending
                    ? "Creating…"
                    : "Create draft & continue"}
                </button>
              </div>
            </form>
          ) : null}

          {step === 2 && event !== null ? (
            <div data-testid="events-wizard-step2">
              <p style={mutedHintStyle}>
                Add the first session. Every session pins a venue, dates and
                admission mode; capacity is derived from the venue or seating
                plan.
              </p>
              <SessionEditor
                event={event}
                mode={{ kind: "create" }}
                siblings={[]}
                onClose={onClose}
                onSaved={(_label, created) => {
                  if (created === undefined) return;
                  setSession(created);
                  setTiers([]);
                  setStep(3);
                  setBanner({
                    kind: "ok",
                    msg: "Session created. Now add at least one ticket tier.",
                  });
                  void queryClient.invalidateQueries({
                    queryKey: ["events", "detail", event.id, "sessions"],
                  });
                }}
                onError={(msg) => setBanner({ kind: "err", msg })}
              />
            </div>
          ) : null}

          {step === 3 && event !== null && session !== null ? (
            <WizardTiersStep
              event={event}
              session={session}
              tiers={tiers}
              onTierAdded={(t) => {
                setTiers((prev) => [...prev, t]);
                setBanner({ kind: "ok", msg: `Tier "${t.name}" added.` });
              }}
              onTierError={(msg) => setBanner({ kind: "err", msg })}
              onPublish={() => {
                setBanner(null);
                publishMutation.mutate();
              }}
              publishBusy={publishMutation.isPending}
              onSaveDraftAndClose={onClose}
            />
          ) : null}
        </div>
      </div>
    </div>
  );
}

/**
 * Human-friendly translation for the publish endpoint's error catalogue.
 * The two AB-42 codes explain the gate the operator just hit.
 */
export function mapPublishError(err: ApiError): string {
  switch (err.code) {
    case "event.publish_requires_session":
      return "Cannot publish: this event has no session yet.";
    case "event.publish_requires_priced_tier":
      return "Cannot publish: every session must have at least one ticket tier.";
    case "event.invalid_transition":
      return err.message || "Status transition is not allowed.";
    case "permissions.denied":
      return "Missing event.publish permission.";
    default:
      if (err.status === 401) return "Session expired. Sign in again.";
      if (err.status === 403) return "Forbidden — missing event.publish.";
      return `${err.message} (${err.code})`;
  }
}

interface WizardTiersStepProps {
  event: EventItem;
  session: SessionItem;
  tiers: readonly TicketTierItem[];
  onTierAdded: (t: TicketTierItem) => void;
  onTierError: (msg: string) => void;
  onPublish: () => void;
  publishBusy: boolean;
  onSaveDraftAndClose: () => void;
}

function WizardTiersStep({
  event,
  session,
  tiers,
  onTierAdded,
  onTierError,
  onPublish,
  publishBusy,
  onSaveDraftAndClose,
}: WizardTiersStepProps) {
  const [showForm, setShowForm] = useState(tiers.length === 0);
  const canPublish = tiers.length > 0;

  return (
    <div data-testid="events-wizard-step3">
      <p style={mutedHintStyle}>
        Add at least one ticket tier for the session on{" "}
        {formatDateTime(session.start_at)}. Currency is{" "}
        <strong>{session.currency}</strong> (
        {session.currency_source === "derived"
          ? "derived from venue"
          : "set manually on the session"}
        ) — every tier of this session shares it.
      </p>

      {tiers.length > 0 ? (
        <ul
          style={{ margin: "8px 0", paddingLeft: 20 }}
          data-testid="events-wizard-tier-list"
        >
          {tiers.map((t) => (
            <li key={t.id}>
              <strong>{t.name}</strong> — {t.pricing_mode}
              {t.pricing_mode === "fixed"
                ? ` · ${(t.price_amount / 100).toFixed(2)} ${t.currency}`
                : ""}
              {t.pricing_mode === "pwyw" &&
              t.pwyw_min !== null &&
              t.pwyw_min !== undefined
                ? ` · pwyw ${(t.pwyw_min / 100).toFixed(2)}${
                    t.pwyw_max !== null && t.pwyw_max !== undefined
                      ? `–${(t.pwyw_max / 100).toFixed(2)}`
                      : "+"
                  } ${t.currency}`
                : ""}
            </li>
          ))}
        </ul>
      ) : (
        <div style={statusBoxStyle} data-testid="events-wizard-tiers-empty">
          No tiers yet — the event cannot be published until you add one.
        </div>
      )}

      {showForm ? (
        <TierEditor
          event={event}
          session={session}
          mode={{ kind: "create", sessionID: session.id }}
          onClose={() => setShowForm(false)}
          onSaved={(_label, created) => {
            if (created !== undefined) onTierAdded(created);
            setShowForm(false);
          }}
          onError={onTierError}
        />
      ) : (
        <button
          type="button"
          style={refreshButtonStyle}
          onClick={() => setShowForm(true)}
          data-testid="events-wizard-add-tier"
        >
          + Add another tier
        </button>
      )}

      <div style={{ ...mobileFormBarStyle, marginTop: 16 }}>
        <button
          type="button"
          style={refreshButtonStyle}
          onClick={onSaveDraftAndClose}
          disabled={publishBusy}
          data-testid="events-wizard-save-draft"
        >
          Save draft &amp; close
        </button>
        <button
          type="button"
          style={primaryButtonStyle}
          onClick={onPublish}
          disabled={publishBusy || !canPublish}
          data-testid="events-wizard-publish"
          title={
            canPublish
              ? "Publish this event"
              : "Add at least one tier before you can publish."
          }
        >
          {publishBusy ? "Publishing…" : "Publish event"}
        </button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// EditEventPanel — inline panel for editing an existing event
// ---------------------------------------------------------------------------

interface EditEventPanelProps {
  event: EventItem;
  orgs: readonly OrganizationSummary[];
  onSuccess: () => void;
  onClose: () => void;
}

function EditEventPanel({ event, orgs, onSuccess, onClose }: EditEventPanelProps) {
  const [values, setValues] = useState<EventFormValues>(() =>
    eventToForm(event),
  );
  const errors = useMemo(() => validateEventForm(values, false), [values]);
  const [submitErr, setSubmitErr] = useState<string | null>(null);

  const mutation = useMutation<EventEnvelope, ApiError, EventFormValues>({
    mutationFn: (v) => {
      // AB-36/AB-37: no venue / date fields on events any more.
      const body: Record<string, unknown> = {};
      if (v.name.trim() !== "") body.name = v.name.trim();
      // description: null clears it, string sets it
      body.description = v.description.trim() !== "" ? v.description.trim() : null;
      if (v.visibility !== "") body.visibility = v.visibility;
      return authedFetch<EventEnvelope>({
        method: "PATCH",
        path: `/v1/organizations/${event.org_id}/events/${event.id}`,
        body,
      });
    },
    onSuccess: () => {
      onSuccess();
    },
    onError: (err) => {
      setSubmitErr(`${err.message} (${err.code})`);
    },
  });

  const hasErrors = Object.keys(errors).length > 0;

  // Org selector is read-only for edit — org cannot be changed.
  const currentOrg = orgs.find((o) => o.id === event.org_id);

  return (
    <form
      style={editorFormStyle}
      data-testid="events-edit-form"
      onSubmit={(e: FormEvent) => {
        e.preventDefault();
        if (hasErrors) return;
        setSubmitErr(null);
        mutation.mutate(values);
      }}
    >
      <div style={detailLabelStyle}>Edit Event</div>
      <div style={editorGridStyle}>
        <label style={editorFieldStyle}>
          <span style={editorLabelStyle}>Organization</span>
          <input
            type="text"
            value={
              currentOrg !== undefined
                ? currentOrg.display_number !== undefined
                  ? `#${currentOrg.display_number} ${currentOrg.name}`
                  : currentOrg.name
                : event.org_id
            }
            readOnly
            style={{ ...editorInputStyle, background: "#f1f5f9", color: "#64748b" }}
            data-testid="events-edit-org"
          />
        </label>

        <label style={editorFieldStyle}>
          <span style={editorLabelStyle}>Name *</span>
          <input
            type="text"
            value={values.name}
            onChange={(e) => setValues({ ...values, name: e.target.value })}
            style={editorInputStyle}
            required
            data-testid="events-edit-name"
          />
          {errors.name !== undefined ? (
            <span style={fieldErrorStyle}>{errors.name}</span>
          ) : null}
        </label>

        <label style={editorFieldStyle}>
          <span style={editorLabelStyle}>Visibility</span>
          <select
            value={values.visibility}
            onChange={(e) =>
              setValues({
                ...values,
                visibility: isEventVisibility(e.target.value)
                  ? e.target.value
                  : "",
              })
            }
            style={editorInputStyle}
            data-testid="events-edit-visibility"
          >
            <option value="">— keep current —</option>
            {EVENT_VISIBILITIES.map((v) => (
              <option key={v} value={v}>
                {v}
              </option>
            ))}
          </select>
        </label>

      </div>

      <label style={editorFieldStyle}>
        <span style={editorLabelStyle}>Description (optional)</span>
        <textarea
          value={values.description}
          onChange={(e) =>
            setValues({ ...values, description: e.target.value })
          }
          style={{ ...editorInputStyle, minHeight: 72, resize: "vertical" }}
          rows={3}
          data-testid="events-edit-description"
        />
      </label>

      {submitErr !== null ? (
        <div
          style={formErrorStyle}
          role="alert"
          data-testid="events-edit-error"
        >
          {submitErr}
        </div>
      ) : null}

      <div style={mobileFormBarStyle}>
        <button
          type="button"
          style={refreshButtonStyle}
          onClick={onClose}
          disabled={mutation.isPending}
          data-testid="events-edit-cancel"
        >
          Cancel
        </button>
        <button
          type="submit"
          style={primaryButtonStyle}
          disabled={mutation.isPending || hasErrors}
          data-testid="events-edit-submit"
        >
          {mutation.isPending ? "Saving…" : "Save Changes"}
        </button>
      </div>
    </form>
  );
}

interface SessionEnvelope {
  readonly session: SessionItem;
}

type SessionEditorMode =
  | { kind: "closed" }
  | { kind: "create" }
  | { kind: "edit"; session: SessionItem };

function SessionsTab({
  event,
  canCreate,
  canUpdate,
  canDelete,
}: {
  event: EventItem;
  canCreate: boolean;
  canUpdate: boolean;
  canDelete: boolean;
}) {
  const queryClient = useQueryClient();
  const queryKey = ["events", "detail", event.id, "sessions"] as const;
  const query = useQuery<SessionListEnvelope, ApiError>({
    queryKey,
    queryFn: () =>
      authedFetch<SessionListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${event.org_id}/events/${event.id}/sessions`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  // AB-44: load venues so the session table can show venue name instead of raw UUID.
  const venuesQuery = useQuery<VenueListEnvelope, ApiError>({
    queryKey: ["events", "venues", event.org_id],
    queryFn: () =>
      authedFetch<VenueListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${event.org_id}/venues`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });
  const venueByID = useMemo(() => {
    const m = new Map<string, VenueSummary>();
    for (const v of venuesQuery.data?.venues ?? []) m.set(v.id, v);
    return m;
  }, [venuesQuery.data]);

  const [editor, setEditor] = useState<SessionEditorMode>({ kind: "closed" });
  const [confirmDeleteID, setConfirmDeleteID] = useState<string | null>(null);
  const [actionErr, setActionErr] = useState<string | null>(null);
  const [actionOk, setActionOk] = useState<string | null>(null);

  const deleteMutation = useMutation<void, ApiError, string>({
    mutationFn: (id) =>
      authedFetch<void>({
        method: "DELETE",
        path: `/v1/organizations/${event.org_id}/events/${event.id}/sessions/${id}`,
      }),
    onSuccess: (_data, id) => {
      setActionErr(null);
      setActionOk(`Deleted session ${shortenUUID(id)}.`);
      setConfirmDeleteID(null);
      void queryClient.invalidateQueries({ queryKey });
    },
    onError: (err) => {
      setActionOk(null);
      setActionErr(mapSessionError(err));
    },
  });

  if (query.isPending) {
    return <div style={statusBoxStyle}>Loading sessions…</div>;
  }
  if (query.isError) {
    return (
      <div style={errorBoxStyle} role="alert">
        <strong>Failed to load sessions.</strong>
        <div style={errorCodeStyle}>{query.error?.code ?? "unknown.error"}</div>
      </div>
    );
  }
  const sessions = query.data?.sessions ?? [];
  const serverFlagsOverlap = Boolean(query.data?.has_overlapping_sessions);

  return (
    <div style={tabBodyStyle} data-testid="events-sessions-tab">
      <div style={sessionsHeaderStyle}>
        <div>
          <div style={detailLabelStyle}>Sessions</div>
          <div style={mutedHintStyle}>
            {sessions.length} session{sessions.length === 1 ? "" : "s"} for this
            event.
            {serverFlagsOverlap
              ? " Server reports overlapping sessions exist."
              : ""}
          </div>
        </div>
        {canCreate ? (
          <button
            type="button"
            style={primaryButtonStyle}
            onClick={() => {
              setActionErr(null);
              setActionOk(null);
              setEditor({ kind: "create" });
            }}
            data-testid="events-session-add"
            disabled={editor.kind !== "closed"}
          >
            Add session
          </button>
        ) : (
          <span style={mutedHintStyle}>
            Adding a session requires <code style={monoStyle}>session.create</code>.
          </span>
        )}
      </div>

      {actionErr !== null ? (
        <div style={formErrorStyle} role="alert" data-testid="events-session-action-error">
          {actionErr}
        </div>
      ) : null}
      {actionOk !== null ? (
        <div style={successBoxStyle} role="status" data-testid="events-session-action-ok">
          {actionOk}
        </div>
      ) : null}

      {canUpdate && sessions.length > 0 ? (
        <BulkPricingPanel event={event} sessions={sessions} />
      ) : null}

      {editor.kind === "create" ? (
        <SessionEditor
          event={event}
          mode={editor}
          siblings={sessions}
          onClose={() => setEditor({ kind: "closed" })}
          onSaved={(label) => {
            setActionErr(null);
            setActionOk(label);
            setEditor({ kind: "closed" });
            void queryClient.invalidateQueries({ queryKey });
          }}
          onError={(msg) => {
            setActionErr(msg);
            setActionOk(null);
          }}
        />
      ) : null}

      {sessions.length === 0 && editor.kind !== "create" ? (
        <div style={statusBoxStyle} data-testid="events-sessions-empty">
          No sessions have been scheduled for this event.
        </div>
      ) : null}

      {sessions.length > 0 ? (
        <div style={tableWrapStyle}>
          <table style={tableStyle} data-testid="events-sessions-table">
            <thead>
              <tr>
                <th scope="col" style={thStyle}>Starts</th>
                <th scope="col" style={thStyle}>Ends</th>
                <th scope="col" style={thStyle}>Venue</th>
                <th scope="col" style={thStyle}>Capacity</th>
                <th scope="col" style={thStyle}>Currency</th>
                <th scope="col" style={thStyle}>Status</th>
                <th scope="col" style={thStyle}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {sessions.map((s) => {
                const isEditing = editor.kind === "edit" && editor.session.id === s.id;
                return (
                  <Fragment key={s.id}>
                    <tr data-testid={`events-session-${s.id}`}>
                      <td style={tdStyle}>{formatDateTime(s.start_at)}</td>
                      <td style={tdStyle}>{formatDateTime(s.end_at)}</td>
                      <td style={tdStyle} title={s.venue_id}>
                        {(() => {
                          const v = venueByID.get(s.venue_id);
                          if (v === undefined) return shortenUUID(s.venue_id);
                          return v.display_number !== undefined
                            ? `${v.name} #${v.display_number}`
                            : v.name;
                        })()}
                      </td>
                      <td style={tdStyle}>{s.capacity_total.toLocaleString()}</td>
                      <td style={tdStyle}>{s.currency}</td>
                      <td style={tdStyle}>{s.status}</td>
                      <td style={tdStyle}>
                        <div style={rowActionsStyle}>
                          {canUpdate ? (
                            <button
                              type="button"
                              style={refreshButtonStyle}
                              onClick={() => {
                                setActionErr(null);
                                setActionOk(null);
                                setEditor({ kind: "edit", session: s });
                              }}
                              data-testid={`events-session-edit-${s.id}`}
                              disabled={isEditing}
                            >
                              Edit
                            </button>
                          ) : null}
                          {canDelete ? (
                            <button
                              type="button"
                              style={dangerButtonStyle}
                              onClick={() => {
                                setActionErr(null);
                                setActionOk(null);
                                setConfirmDeleteID(s.id);
                              }}
                              data-testid={`events-session-delete-${s.id}`}
                              disabled={deleteMutation.isPending}
                            >
                              Delete
                            </button>
                          ) : null}
                          <a
                            href={`/v1/organizations/${event.org_id}/events/${event.id}/sessions/${s.id}/macs-export?download=1`}
                            download
                            className="text-sm text-blue-600 hover:underline"
                            data-testid={`events-session-macs-export-${s.id}`}
                          >
                            MACS Export
                          </a>
                          {!canUpdate && !canDelete ? (
                            <span style={mutedHintStyle}>read-only</span>
                          ) : null}
                        </div>
                      </td>
                    </tr>
                    {confirmDeleteID === s.id ? (
                      <tr>
                        <td colSpan={7} style={tdStyle}>
                          <div
                            style={confirmDeleteStyle}
                            data-testid={`events-session-confirm-${s.id}`}
                          >
                            <span>
                              Delete session {formatDateTime(s.start_at)}? This
                              cannot be undone.
                            </span>
                            <div style={rowActionsStyle}>
                              <button
                                type="button"
                                style={dangerButtonStyle}
                                onClick={() => deleteMutation.mutate(s.id)}
                                disabled={deleteMutation.isPending}
                                data-testid={`events-session-confirm-yes-${s.id}`}
                              >
                                {deleteMutation.isPending ? "Deleting…" : "Yes, delete"}
                              </button>
                              <button
                                type="button"
                                style={refreshButtonStyle}
                                onClick={() => setConfirmDeleteID(null)}
                                disabled={deleteMutation.isPending}
                              >
                                Cancel
                              </button>
                            </div>
                          </div>
                        </td>
                      </tr>
                    ) : null}
                    {isEditing ? (
                      <tr>
                        <td colSpan={7} style={tdStyle}>
                          <SessionEditor
                            event={event}
                            mode={editor}
                            siblings={sessions}
                            onClose={() => setEditor({ kind: "closed" })}
                            onSaved={(label) => {
                              setActionErr(null);
                              setActionOk(label);
                              setEditor({ kind: "closed" });
                              void queryClient.invalidateQueries({ queryKey });
                            }}
                            onError={(msg) => {
                              setActionErr(msg);
                              setActionOk(null);
                            }}
                          />
                        </td>
                      </tr>
                    ) : null}
                    {canUpdate ? (
                      <tr>
                        <td colSpan={7} style={{ ...tdStyle, background: "#f9fafb" }}>
                          <SessionPosterUpload
                            event={event}
                            session={s}
                            onUploaded={() => {
                              void queryClient.invalidateQueries({ queryKey });
                            }}
                          />
                        </td>
                      </tr>
                    ) : null}
                    {canUpdate ? (
                      <tr>
                        <td colSpan={7} style={{ ...tdStyle, background: "#f9fafb" }}>
                          <SessionMediaGallery session={s} />
                        </td>
                      </tr>
                    ) : null}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
      ) : null}
    </div>
  );
}

interface SessionEditorProps {
  event: EventItem;
  mode: Exclude<SessionEditorMode, { kind: "closed" }>;
  siblings: readonly SessionItem[];
  onClose: () => void;
  /**
   * Called on successful create/edit. The optional `session` argument is the
   * SessionItem returned by the server; the AB-42 wizard consumes it to
   * advance to the tiers step. Existing callers may ignore this parameter.
   */
  onSaved: (successLabel: string, session?: SessionItem) => void;
  onError: (msg: string) => void;
}

interface SessionEditorSeatingPlanListEnvelope {
  readonly seating_plans: readonly {
    readonly id: string;
    readonly name: string;
  }[];
}

interface SessionEditorPlanVersionEnvelope {
  readonly seating_plan_version: {
    readonly id: string;
    readonly version_number: number;
    readonly capacity_seated: number;
    readonly capacity_standing: number;
  };
}

function SessionEditor({
  event,
  mode,
  siblings,
  onClose,
  onSaved,
  onError,
}: SessionEditorProps) {
  const initialValues =
    mode.kind === "edit" ? sessionToForm(mode.session) : emptySessionForm();
  const [values, setValues] = useState<SessionFormValues>(initialValues);
  const errors = useMemo(() => validateSessionForm(values), [values]);
  const editingID = mode.kind === "edit" ? mode.session.id : null;
  const overlaps = useMemo(
    () =>
      Object.keys(errors).length === 0
        ? findOverlappingSessions(
            siblings,
            values.start_at,
            values.end_at,
            editingID,
          )
        : [],
    [siblings, values.start_at, values.end_at, errors, editingID],
  );

  // Venue selector source (AB-36 — every session requires a venue).
  const venuesQuery = useQuery<VenueListEnvelope, ApiError>({
    queryKey: ["events", "venues", event.org_id],
    queryFn: () =>
      authedFetch<VenueListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${event.org_id}/venues`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  // Seating-plan-version picker (create mode, seated admission only).
  // Mirrors sessionSeatingBind.tsx: pick a plan from the venue's plan
  // list, probe a version number, and submit the resolved version UUID.
  const seated =
    mode.kind === "create" && values.admission_mode !== "general_admission";
  const [planId, setPlanId] = useState<string>("");
  const [versionN, setVersionN] = useState<string>("1");

  const plansQuery = useQuery<SessionEditorSeatingPlanListEnvelope, ApiError>({
    queryKey: ["events", "session-plans", values.venue_id] as const,
    enabled: seated && values.venue_id !== "",
    queryFn: () =>
      authedFetch<SessionEditorSeatingPlanListEnvelope>({
        method: "GET",
        path: `/v1/venues/${values.venue_id}/seating-plans`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  const versionQuery = useQuery<SessionEditorPlanVersionEnvelope, ApiError>({
    queryKey: ["events", "session-plan-version", planId, versionN] as const,
    enabled:
      seated && planId !== "" && /^\d+$/.test(versionN) && Number(versionN) > 0,
    queryFn: () =>
      authedFetch<SessionEditorPlanVersionEnvelope>({
        method: "GET",
        path: `/v1/seating-plans/${planId}/versions/${versionN}`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  const resolvedVersion = versionQuery.data?.seating_plan_version;
  const resolvedVersionID = seated ? (resolvedVersion?.id ?? "") : "";

  // Sync the resolved plan version UUID into the form values so
  // validateSessionForm / buildSessionRequestBody see it (create only).
  useEffect(() => {
    if (mode.kind !== "create") return;
    setValues((prev) =>
      prev.seating_plan_version_id === resolvedVersionID
        ? prev
        : { ...prev, seating_plan_version_id: resolvedVersionID },
    );
  }, [mode.kind, resolvedVersionID]);

  // AB-40 C2 (carried into AB-48): several sessions in one pass. Each
  // extra start creates one more session with the same venue / plan /
  // mode / duration. Each POST is its own atomic create; a failure midway
  // is reported with the count that did land, never hidden.
  const [extraStarts, setExtraStarts] = useState<string[]>([]);
  const [extraCreated, setExtraCreated] = useState<number>(0);

  const mutation = useMutation<SessionEnvelope, ApiError, SessionFormValues>({
    mutationFn: async (v) => {
      const body = buildSessionRequestBody(
        v,
        mode.kind === "create" ? "create" : "edit",
      );
      if (mode.kind === "create") {
        const first = await authedFetch<SessionEnvelope>({
          method: "POST",
          path: `/v1/organizations/${event.org_id}/events/${event.id}/sessions`,
          body,
        });
        const durationMs =
          (parseLocalDatetime(v.end_at)?.getTime() ?? 0) -
          (parseLocalDatetime(v.start_at)?.getTime() ?? 0);
        let created = 0;
        for (const extra of extraStarts) {
          const start = parseLocalDatetime(extra);
          if (start === null) continue;
          const end = new Date(start.getTime() + durationMs);
          await authedFetch<SessionEnvelope>({
            method: "POST",
            path: `/v1/organizations/${event.org_id}/events/${event.id}/sessions`,
            body: {
              ...body,
              start_at: start.toISOString(),
              end_at: end.toISOString(),
            },
          });
          created++;
          setExtraCreated(created);
        }
        return first;
      }
      return authedFetch<SessionEnvelope>({
        method: "PATCH",
        path: `/v1/organizations/${event.org_id}/events/${event.id}/sessions/${mode.session.id}`,
        body,
      });
    },
    onSuccess: (data) => {
      const extras = extraStarts.filter((e) => e.trim() !== "").length;
      onSaved(
        mode.kind === "create"
          ? extras > 0
            ? `Created session ${formatDateTime(data.session.start_at)} and ${extras} more date${extras === 1 ? "" : "s"}.`
            : `Created session ${formatDateTime(data.session.start_at)}.`
          : `Updated session ${shortenUUID(data.session.id)}.`,
        data.session,
      );
    },
    onError: (err) => {
      onError(mapSessionError(err));
    },
  });

  const submit = () => {
    if (Object.keys(errors).length > 0) {
      return;
    }
    mutation.mutate(values);
  };

  return (
    <form
      style={editorFormStyle}
      data-testid={
        mode.kind === "create"
          ? "events-session-form-create"
          : `events-session-form-edit-${mode.session.id}`
      }
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <div style={detailLabelStyle}>
        {mode.kind === "create" ? "Add session" : "Edit session"}
      </div>
      <div style={editorGridStyle}>
        <label style={editorFieldStyle}>
          <span style={editorLabelStyle}>Venue *</span>
          <select
            value={values.venue_id}
            onChange={(e) => {
              setValues({ ...values, venue_id: e.target.value });
              setPlanId("");
              setVersionN("1");
            }}
            style={editorInputStyle}
            required
            disabled={venuesQuery.isPending}
            data-testid="events-session-input-venue"
          >
            <option value="">— select venue —</option>
            {(venuesQuery.data?.venues ?? []).map((v) => (
              <option key={v.id} value={v.id}>
                {v.display_number !== undefined
                  ? `#${v.display_number} ${v.name}`
                  : v.name}
              </option>
            ))}
          </select>
          {errors.venue_id !== undefined ? (
            <span style={fieldErrorStyle}>{errors.venue_id}</span>
          ) : null}
        </label>
        <label style={editorFieldStyle}>
          <span style={editorLabelStyle}>Start (UTC)</span>
          <input
            type="datetime-local"
            value={values.start_at}
            onChange={(e) => setValues({ ...values, start_at: e.target.value })}
            style={editorInputStyle}
            required
            data-testid="events-session-input-start"
          />
          {errors.start_at !== undefined ? (
            <span style={fieldErrorStyle}>{errors.start_at}</span>
          ) : null}
        </label>
        <label style={editorFieldStyle}>
          <span style={editorLabelStyle}>End (UTC)</span>
          <input
            type="datetime-local"
            value={values.end_at}
            onChange={(e) => setValues({ ...values, end_at: e.target.value })}
            style={editorInputStyle}
            required
            data-testid="events-session-input-end"
          />
          {errors.end_at !== undefined ? (
            <span style={fieldErrorStyle}>{errors.end_at}</span>
          ) : null}
        </label>
        {mode.kind === "create" ? (
          <label style={editorFieldStyle}>
            <span style={editorLabelStyle}>Admission mode</span>
            <select
              value={values.admission_mode}
              onChange={(e) =>
                setValues({
                  ...values,
                  admission_mode: isSessionAdmissionMode(e.target.value)
                    ? e.target.value
                    : "general_admission",
                  seating_plan_version_id: "",
                })
              }
              style={editorInputStyle}
              data-testid="events-session-input-admission-mode"
            >
              <option value="general_admission">General admission</option>
              <option value="assigned_seats">Assigned seats</option>
              <option value="hybrid">Hybrid</option>
            </select>
          </label>
        ) : null}
        {(mode.kind === "create" &&
          values.admission_mode === "general_admission") ||
        (mode.kind === "edit" &&
          mode.session.seating_plan_version_id === null) ? (
          <label style={editorFieldStyle}>
            <span style={editorLabelStyle}>Capacity override (optional)</span>
            <input
              type="number"
              min={1}
              step={1}
              value={values.capacity_override}
              onChange={(e) =>
                setValues({ ...values, capacity_override: e.target.value })
              }
              placeholder="venue default"
              style={editorInputStyle}
              data-testid="events-session-input-capacity-override"
            />
            <span style={mutedHintStyle}>
              Defaults to the venue capacity when empty.
            </span>
            {errors.capacity_override !== undefined ? (
              <span style={fieldErrorStyle}>{errors.capacity_override}</span>
            ) : null}
          </label>
        ) : null}
        <label style={editorFieldStyle}>
          <span style={editorLabelStyle}>Status</span>
          <select
            value={values.status}
            onChange={(e) =>
              setValues({
                ...values,
                status: isSessionStatus(e.target.value)
                  ? e.target.value
                  : "draft",
              })
            }
            style={editorInputStyle}
            data-testid="events-session-input-status"
          >
            {SESSION_STATUSES.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
          {errors.status !== undefined ? (
            <span style={fieldErrorStyle}>{errors.status}</span>
          ) : null}
        </label>
        <label style={editorFieldStyle}>
          <span style={editorLabelStyle}>Override currency (optional)</span>
          {mode.kind === "edit" ? (
            <span
              style={mutedHintStyle}
              data-testid="events-session-currency-current"
            >
              {mode.session.currency} —{" "}
              {mode.session.currency_source === "derived"
                ? "derived from venue"
                : "set manually"}
            </span>
          ) : null}
          <input
            type="text"
            value={values.currency}
            onChange={(e) =>
              setValues({
                ...values,
                currency: e.target.value.toUpperCase(),
              })
            }
            placeholder={
              mode.kind === "create" ? "derived from venue" : "keep current"
            }
            maxLength={3}
            pattern="[A-Z]{3}"
            style={editorInputStyle}
            data-testid="events-session-input-currency"
          />
          {errors.currency !== undefined ? (
            <span style={fieldErrorStyle}>{errors.currency}</span>
          ) : null}
        </label>
        {seated ? (
          <>
            <label style={editorFieldStyle}>
              <span style={editorLabelStyle}>Seating plan *</span>
              <select
                value={planId}
                onChange={(e) => {
                  setPlanId(e.target.value);
                  setVersionN("1");
                }}
                style={editorInputStyle}
                disabled={values.venue_id === "" || plansQuery.isPending}
                data-testid="events-session-input-seating-plan"
              >
                <option value="">— Select a plan —</option>
                {(plansQuery.data?.seating_plans ?? []).map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
              {values.venue_id === "" ? (
                <span style={mutedHintStyle}>
                  Select a venue to list its seating plans.
                </span>
              ) : null}
            </label>
            <label style={editorFieldStyle}>
              <span style={editorLabelStyle}>Plan version *</span>
              <input
                type="number"
                min={1}
                step={1}
                value={versionN}
                onChange={(e) => setVersionN(e.target.value)}
                style={editorInputStyle}
                disabled={planId === ""}
                data-testid="events-session-input-seating-version"
              />
              {resolvedVersion !== undefined ? (
                <span
                  style={mutedHintStyle}
                  data-testid="events-session-seating-version-resolved"
                >
                  v{resolvedVersion.version_number} ·{" "}
                  {resolvedVersion.capacity_seated.toLocaleString()} seated /{" "}
                  {resolvedVersion.capacity_standing.toLocaleString()} standing
                </span>
              ) : null}
              {errors.seating_plan_version_id !== undefined ? (
                <span style={fieldErrorStyle}>
                  {errors.seating_plan_version_id}
                </span>
              ) : null}
            </label>
          </>
        ) : null}
      </div>

      {mode.kind === "create" ? (
        <div style={editorFieldStyle} data-testid="events-session-extra-dates">
          <span style={editorLabelStyle}>
            Additional dates (same venue, plan and duration)
          </span>
          {extraStarts.map((v, idx) => (
            <div key={idx} style={{ display: "flex", gap: 6 }}>
              <input
                type="datetime-local"
                value={v}
                onChange={(e) =>
                  setExtraStarts(extraStarts.map((x, i) => (i === idx ? e.target.value : x)))
                }
                style={editorInputStyle}
                data-testid={`events-session-extra-start-${idx}`}
              />
              <button
                type="button"
                style={refreshButtonStyle}
                onClick={() => setExtraStarts(extraStarts.filter((_, i) => i !== idx))}
                aria-label="Remove this extra date"
              >
                ×
              </button>
            </div>
          ))}
          <button
            type="button"
            style={refreshButtonStyle}
            onClick={() => setExtraStarts([...extraStarts, ""])}
            data-testid="events-session-extra-add"
            disabled={mutation.isPending}
          >
            + Add another date
          </button>
          {mutation.isPending && extraStarts.length > 0 ? (
            <span style={mutedHintStyle}>
              Creating… {extraCreated}/{extraStarts.length} extra dates done.
            </span>
          ) : null}
          {mutation.isError && extraCreated > 0 ? (
            <span style={fieldErrorStyle}>
              {extraCreated} extra date{extraCreated === 1 ? "" : "s"} were created before the error — refresh the list.
            </span>
          ) : null}
        </div>
      ) : null}

      {overlaps.length > 0 ? (
        <div
          style={overlapWarningStyle}
          role="status"
          data-testid="events-session-overlap-warning"
        >
          <strong>Overlap warning:</strong> this time range overlaps{" "}
          {overlaps.length} existing session
          {overlaps.length === 1 ? "" : "s"} on this event. The server will
          accept the change but the list will be flagged as overlapping.
          <ul style={overlapListStyle}>
            {overlaps.map((o) => (
              <li key={o.id}>
                {formatDateTime(o.start_at)} → {formatDateTime(o.end_at)} (
                <code style={monoStyle}>{shortenUUID(o.id)}</code>)
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      <div style={mobileFormBarStyle} data-testid="events-session-actions">
        <button
          type="button"
          style={refreshButtonStyle}
          onClick={onClose}
          disabled={mutation.isPending}
          data-testid="events-session-cancel"
        >
          Cancel
        </button>
        <button
          type="submit"
          style={primaryButtonStyle}
          disabled={mutation.isPending || Object.keys(errors).length > 0}
          data-testid="events-session-submit"
        >
          {mutation.isPending
            ? "Saving…"
            : mode.kind === "create"
              ? "Create session"
              : "Save changes"}
        </button>
      </div>

      {/* SEAT-E2 (#316): seating bind sub-form. Only rendered when
          editing an existing session — bind requires a session id and
          the associated org / event / venue context. */}
      {mode.kind === "edit" ? (
        <SessionSeatingBindPanel
          orgId={event.org_id}
          eventId={event.id}
          sessionId={mode.session.id}
          venueId={mode.session.venue_id}
          onSaved={(msg) => onSaved(msg)}
          onError={(msg) => onError(msg)}
        />
      ) : null}
    </form>
  );
}

/**
 * Translate an ApiError from a session endpoint into a human-readable
 * sentence. Mirrors the error catalogue documented in sessions.go so the
 * operator sees the same message regardless of whether the violation was
 * detected client-side or rejected by the server.
 */
export function mapSessionError(err: ApiError): string {
  switch (err.code) {
    case "session.invalid_date_range":
      return "End must be after start.";
    case "session.invalid_capacity":
      return "Capacity must be greater than zero.";
    case "session.invalid_status":
      return "Status must be one of draft, scheduled, cancelled, or completed.";
    case "session.invalid_transition":
      return err.message || "Status transition is not allowed.";
    case "session.not_found":
      return "Session no longer exists. The list will be refreshed.";
    case "session.missing_start_at":
      return "Start is required.";
    case "session.missing_end_at":
      return "End is required.";
    case "session.invalid_start_at":
    case "session.invalid_end_at":
      return "Start and end must be valid timestamps.";
    case "session.missing_venue_id":
      return "Venue is required — pick the venue the session takes place at.";
    case "session.invalid_venue_id":
      return "Venue ID is not a valid UUID.";
    case "session.venue_not_found":
      return "The selected venue no longer exists.";
    case "session.venue_org_mismatch":
      return "The selected venue belongs to a different organization.";
    case "session.invalid_admission_mode":
      return "Admission mode must be general_admission, assigned_seats, or hybrid.";
    case "session.missing_seating_plan_version":
      return "A seating plan version is required for seated sessions.";
    case "session.invalid_seating_plan_version":
      return "The selected seating plan version is not valid for this venue.";
    case "session.seating_plan_not_applicable":
      return "A general-admission session cannot carry a seating plan version.";
    case "session.invalid_currency":
      return "Currency must be a 3-letter uppercase ISO 4217 code (e.g. EUR).";
    case "session.currency_unresolvable":
      return (
        "The venue has no geography to derive a currency from — " +
        "set an explicit currency for this session."
      );
    case "session.invalid_capacity_override":
      return "Capacity override must be greater than zero.";
    case "session.capacity_unresolvable":
      return (
        "Capacity could not be resolved — set a capacity override or give " +
        "the venue a default capacity."
      );
    case "session.capacity_override_not_applicable":
      return (
        "This session is bound to a seating plan, which owns the capacity — " +
        "capacity override does not apply."
      );
    case "permissions.denied":
      return "Your account is missing the permission required for this action.";
    default:
      if (err.status === 401) {
        return "Session expired. Please sign in again.";
      }
      if (err.status === 403) {
        return "Forbidden — missing required session permission.";
      }
      return `${err.message} (${err.code})`;
  }
}

// ---------------------------------------------------------------------------
// SessionPosterUpload component (AB-47)
// ---------------------------------------------------------------------------

interface SessionPosterUploadProps {
  event: EventItem;
  session: SessionItem;
  onUploaded: () => void;
}

/**
 * Inline session-level poster upload for AB-47.
 *
 * Flow:
 *  1. Operator clicks "Upload poster" → file picker opens.
 *  2. Selected file is POSTed as multipart to POST /v1/media with
 *     owner_type=session_poster and owner_id=<session_id>.
 *  3. On success the returned media.id is PATCHed to the session via
 *     PATCH .../sessions/{id} with { poster_media_id: "<id>" }.
 *
 * A checkbox "Apply to all sessions" replaces step 3 with:
 *  3b. PATCH the event with { poster_media_id: "<id>", clear_session_overrides: true }.
 *
 * Current poster is shown as a small thumbnail when poster_media_id is set.
 */
function SessionPosterUpload({ event, session, onUploaded }: SessionPosterUploadProps) {
  const [applyToAll, setApplyToAll] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [uploadErr, setUploadErr] = useState<string | null>(null);
  const [uploadOk, setUploadOk] = useState<string | null>(null);

  async function handleFileChange(e: { target: { files: FileList | null; value: string } }) {
    const file = e.target.files?.[0];
    if (!file) return;

    setUploading(true);
    setUploadErr(null);
    setUploadOk(null);

    try {
      // Step 1: upload the media object via the existing uploadMedia helper.
      const uploaded = await uploadMedia({
        file,
        ownerType: "session_poster",
        ownerId: session.id,
      });

      const mediaID = uploaded.id;

      if (applyToAll) {
        // Step 3b: patch the event with the media id and clear all session overrides.
        await authedFetch({
          method: "PATCH",
          path: `/v1/organizations/${event.org_id}/events/${event.id}`,
          body: { poster_media_id: mediaID, clear_session_overrides: true },
        });
        setUploadOk("Poster applied to all sessions of this event.");
      } else {
        // Step 3: patch just this session.
        await authedFetch({
          method: "PATCH",
          path: `/v1/organizations/${event.org_id}/events/${event.id}/sessions/${session.id}`,
          body: { poster_media_id: mediaID },
        });
        setUploadOk("Session poster updated.");
      }
      onUploaded();
    } catch (err) {
      const msg = err instanceof ApiError
        ? `Upload failed: ${err.message} (${err.code})`
        : "Upload failed — please try again.";
      setUploadErr(msg);
    } finally {
      setUploading(false);
      // Reset the file input so the same file can be re-selected.
      e.target.value = "";
    }
  }

  const posterStyle: CSSProperties = {
    display: "flex",
    flexDirection: "column",
    gap: "6px",
    padding: "8px 0",
  };

  const posterRowStyle: CSSProperties = {
    display: "flex",
    alignItems: "center",
    gap: "10px",
    flexWrap: "wrap",
  };

  const imgStyle: CSSProperties = {
    width: "48px",
    height: "48px",
    objectFit: "cover",
    borderRadius: "4px",
    border: "1px solid #d1d5db",
  };

  const placeholderStyle: CSSProperties = {
    width: "48px",
    height: "48px",
    borderRadius: "4px",
    border: "1px dashed #d1d5db",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    color: "#9ca3af",
    fontSize: "10px",
    textAlign: "center",
  };

  return (
    <div style={posterStyle}>
      <div style={posterRowStyle}>
        {session.poster_media_id ? (
          <img
            src={`/v1/media-files/${session.poster_media_id}`}
            alt="Session poster"
            style={imgStyle}
          />
        ) : (
          <div style={placeholderStyle}>No poster</div>
        )}
        <label style={{ cursor: uploading ? "not-allowed" : "pointer" }}>
          <input
            type="file"
            accept="image/*"
            style={{ display: "none" }}
            disabled={uploading}
            onChange={handleFileChange}
          />
          <span
            style={{
              ...refreshButtonStyle,
              opacity: uploading ? 0.6 : 1,
              display: "inline-block",
            }}
          >
            {uploading ? "Uploading…" : "Upload poster"}
          </span>
        </label>
      </div>
      <label style={{ display: "flex", alignItems: "center", gap: "6px", fontSize: "12px", color: "#374151" }}>
        <input
          type="checkbox"
          checked={applyToAll}
          onChange={(e) => setApplyToAll(e.target.checked)}
          disabled={uploading}
        />
        Use for all sessions of this event
      </label>
      {uploadErr ? (
        <div style={{ ...formErrorStyle, marginTop: "4px" }}>{uploadErr}</div>
      ) : null}
      {uploadOk ? (
        <div style={{ ...successBoxStyle, marginTop: "4px" }}>{uploadOk}</div>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// SessionMediaGallery component (AB-47b, feature #435)
// ---------------------------------------------------------------------------

// Poster cap (5) mirrors backend constant MaxPostersPerGallery. Total items
// cap (20) mirrors MaxTotalItems. Keep in sync with
// apps/backend/internal/platform/httpserver/hcatalog/session_media.go.
const SESSION_MEDIA_MAX_POSTERS = 5;
const SESSION_MEDIA_MAX_ITEMS = 20;

// Video URL host allowlist mirrors backend `allowedVideoHosts`.
const SESSION_MEDIA_VIDEO_HOSTS = [
  "youtube.com",
  "youtu.be",
  "vk.com",
  "rutube.ru",
  "vimeo.com",
];

interface SessionMediaItemView {
  readonly id: string;
  readonly kind: "poster" | "video";
  readonly media_id: string | null;
  readonly video_url: string | null;
  readonly position: number;
}

interface SessionMediaGalleryResponse {
  readonly session_id: string;
  readonly items: readonly SessionMediaItemView[];
}

interface DraftItem {
  readonly localId: string;
  readonly kind: "poster" | "video";
  readonly media_id: string | null;
  readonly video_url: string | null;
}

/**
 * Session media gallery editor (AB-47b, feature #435).
 *
 * Renders the ordered per-session gallery (up to 5 posters + video URL
 * links, max 20 items total). Operator can:
 *   - upload posters (POST /v1/media owner_type=session_poster, then add
 *     to gallery draft);
 *   - paste video URLs (allowlisted hosts: YouTube, VK, RuTube, Vimeo);
 *   - reorder via up/down buttons;
 *   - remove entries;
 *   - Save → PUT /v1/sessions/{id}/media (atomic replace).
 *
 * The single per-session poster COVER (SessionPosterUpload above) is a
 * separate concept and is untouched.
 */
function SessionMediaGallery({ session }: { session: SessionItem }) {
  const queryClient = useQueryClient();
  const queryKey = ["session-media", session.id];

  const query = useQuery<SessionMediaGalleryResponse, ApiError>({
    queryKey,
    queryFn: () =>
      authedFetch<SessionMediaGalleryResponse>({
        method: "GET",
        path: `/v1/sessions/${session.id}/media`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  const [draft, setDraft] = useState<DraftItem[] | null>(null);
  const [videoInput, setVideoInput] = useState("");
  const [uploading, setUploading] = useState(false);
  const [uiError, setUiError] = useState<string | null>(null);
  const [uiOk, setUiOk] = useState<string | null>(null);

  // Sync draft with server list on first load / after save.
  useEffect(() => {
    if (query.data && draft === null) {
      setDraft(
        query.data.items.map((it, idx) => ({
          localId: `srv-${it.id}-${idx}`,
          kind: it.kind,
          media_id: it.media_id,
          video_url: it.video_url,
        })),
      );
    }
  }, [query.data, draft]);

  const saveMutation = useMutation<
    SessionMediaGalleryResponse,
    ApiError,
    DraftItem[]
  >({
    mutationFn: (items) =>
      authedFetch<SessionMediaGalleryResponse>({
        method: "PUT",
        path: `/v1/sessions/${session.id}/media`,
        body: {
          items: items.map((it) => ({
            kind: it.kind,
            media_id: it.kind === "poster" ? it.media_id : null,
            video_url: it.kind === "video" ? it.video_url : null,
          })),
        },
      }),
    onSuccess: (data) => {
      setUiOk(`Gallery saved (${data.items.length} item(s)).`);
      setUiError(null);
      setDraft(
        data.items.map((it, idx) => ({
          localId: `srv-${it.id}-${idx}`,
          kind: it.kind,
          media_id: it.media_id,
          video_url: it.video_url,
        })),
      );
      void queryClient.invalidateQueries({ queryKey });
    },
    onError: (err) => {
      setUiError(`${err.message} (${err.code})`);
      setUiOk(null);
    },
  });

  async function handlePosterUpload(e: {
    target: { files: FileList | null; value: string };
  }) {
    const file = e.target.files?.[0];
    if (!file) return;
    setUploading(true);
    setUiError(null);
    setUiOk(null);
    try {
      const uploaded = await uploadMedia({
        file,
        ownerType: "session_poster",
        ownerId: session.id,
      });
      const current = draft ?? [];
      const posterCount = current.filter((it) => it.kind === "poster").length;
      if (posterCount >= SESSION_MEDIA_MAX_POSTERS) {
        setUiError(
          `Poster cap reached (${SESSION_MEDIA_MAX_POSTERS} per session).`,
        );
        return;
      }
      if (current.length >= SESSION_MEDIA_MAX_ITEMS) {
        setUiError(
          `Gallery is full (${SESSION_MEDIA_MAX_ITEMS} items max).`,
        );
        return;
      }
      // Warn (never block) on extreme aspect ratios (AB-47b: no rigid
      // format enforcement).
      const img = new Image();
      img.onload = () => {
        const ratio = img.width / img.height;
        if (ratio < 0.3 || ratio > 3.5) {
          setUiOk(
            `Poster uploaded (unusual aspect ratio ${ratio.toFixed(2)} — will still be shown).`,
          );
        } else {
          setUiOk("Poster added to draft — click Save to persist.");
        }
      };
      img.src = URL.createObjectURL(file);
      setDraft([
        ...current,
        {
          localId: `local-${uploaded.id}-${Date.now()}`,
          kind: "poster",
          media_id: uploaded.id,
          video_url: null,
        },
      ]);
    } catch (err) {
      const msg =
        err instanceof ApiError
          ? `Upload failed: ${err.message} (${err.code})`
          : "Upload failed — please try again.";
      setUiError(msg);
    } finally {
      setUploading(false);
      e.target.value = "";
    }
  }

  function validateVideoUrl(raw: string): string | null {
    const trimmed = raw.trim();
    if (trimmed === "") return "video URL is empty";
    let parsed: URL;
    try {
      parsed = new URL(trimmed);
    } catch {
      return "not a valid URL";
    }
    if (parsed.protocol !== "https:") return "must be an https URL";
    const host = parsed.hostname.toLowerCase().replace(/^www\./, "");
    if (!SESSION_MEDIA_VIDEO_HOSTS.includes(host)) {
      return `host must be one of: ${SESSION_MEDIA_VIDEO_HOSTS.join(", ")}`;
    }
    return null;
  }

  function handleAddVideo() {
    const err = validateVideoUrl(videoInput);
    if (err) {
      setUiError(`Cannot add video: ${err}`);
      return;
    }
    const current = draft ?? [];
    if (current.length >= SESSION_MEDIA_MAX_ITEMS) {
      setUiError(`Gallery is full (${SESSION_MEDIA_MAX_ITEMS} items max).`);
      return;
    }
    setDraft([
      ...current,
      {
        localId: `local-video-${Date.now()}`,
        kind: "video",
        media_id: null,
        video_url: videoInput.trim(),
      },
    ]);
    setVideoInput("");
    setUiError(null);
    setUiOk("Video added to draft — click Save to persist.");
  }

  function move(idx: number, delta: number) {
    const current = draft ?? [];
    const target = idx + delta;
    if (target < 0 || target >= current.length) return;
    const next = [...current];
    [next[idx], next[target]] = [next[target], next[idx]];
    setDraft(next);
  }

  function remove(idx: number) {
    const current = draft ?? [];
    setDraft(current.filter((_, i) => i !== idx));
  }

  const containerStyle: CSSProperties = {
    display: "flex",
    flexDirection: "column",
    gap: "8px",
    padding: "8px 0",
  };
  const headerStyle: CSSProperties = {
    fontWeight: 600,
    fontSize: "13px",
    color: "#374151",
  };
  const listStyle: CSSProperties = {
    display: "flex",
    flexDirection: "column",
    gap: "6px",
  };
  const itemStyle: CSSProperties = {
    display: "flex",
    alignItems: "center",
    gap: "10px",
    padding: "6px 10px",
    background: "#ffffff",
    border: "1px solid #e5e7eb",
    borderRadius: "4px",
  };
  const thumbStyle: CSSProperties = {
    width: "36px",
    height: "36px",
    objectFit: "cover",
    borderRadius: "3px",
    border: "1px solid #d1d5db",
  };
  const controlsRowStyle: CSSProperties = {
    display: "flex",
    alignItems: "center",
    gap: "10px",
    flexWrap: "wrap",
  };

  if (query.isPending) {
    return <div style={containerStyle}>Loading media gallery…</div>;
  }
  if (query.isError) {
    return (
      <div style={{ ...containerStyle, ...errorBoxStyle }} role="alert">
        Failed to load gallery: {query.error.message} ({query.error.code})
      </div>
    );
  }

  const items = draft ?? [];
  const posterCount = items.filter((it) => it.kind === "poster").length;
  const posterCapReached = posterCount >= SESSION_MEDIA_MAX_POSTERS;
  const totalCapReached = items.length >= SESSION_MEDIA_MAX_ITEMS;

  return (
    <div style={containerStyle}>
      <div style={headerStyle}>
        Media gallery — {items.length} item(s) ({posterCount} poster
        {posterCount === 1 ? "" : "s"}, cap {SESSION_MEDIA_MAX_POSTERS})
      </div>
      <div style={listStyle}>
        {items.length === 0 ? (
          <div style={{ color: "#6b7280", fontSize: "12px" }}>
            Gallery is empty. Upload a poster or paste a video URL below.
          </div>
        ) : (
          items.map((it, idx) => (
            <div key={it.localId} style={itemStyle} data-media-index={idx}>
              <span
                style={{
                  fontSize: "11px",
                  fontFamily: "monospace",
                  color: "#6b7280",
                  minWidth: "22px",
                }}
              >
                #{idx}
              </span>
              {it.kind === "poster" && it.media_id ? (
                <img
                  src={`/v1/media-files/${it.media_id}`}
                  alt=""
                  style={thumbStyle}
                />
              ) : (
                <div
                  style={{
                    ...thumbStyle,
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "center",
                    background: "#f3f4f6",
                    fontSize: "10px",
                    color: "#4b5563",
                  }}
                >
                  VIDEO
                </div>
              )}
              <span style={{ flex: 1, fontSize: "12px", wordBreak: "break-all" }}>
                {it.kind === "poster"
                  ? `poster · ${it.media_id ?? "—"}`
                  : it.video_url}
              </span>
              <button
                type="button"
                aria-label={`Move up (position ${idx})`}
                onClick={() => move(idx, -1)}
                disabled={idx === 0}
                style={refreshButtonStyle}
              >
                ↑
              </button>
              <button
                type="button"
                aria-label={`Move down (position ${idx})`}
                onClick={() => move(idx, 1)}
                disabled={idx === items.length - 1}
                style={refreshButtonStyle}
              >
                ↓
              </button>
              <button
                type="button"
                aria-label={`Remove item at position ${idx}`}
                onClick={() => remove(idx)}
                style={refreshButtonStyle}
              >
                Remove
              </button>
            </div>
          ))
        )}
      </div>

      <div style={controlsRowStyle}>
        <label
          style={{
            cursor: uploading || posterCapReached || totalCapReached
              ? "not-allowed"
              : "pointer",
          }}
        >
          <input
            type="file"
            accept="image/*"
            style={{ display: "none" }}
            disabled={uploading || posterCapReached || totalCapReached}
            onChange={handlePosterUpload}
            aria-label="Add poster to gallery"
          />
          <span
            style={{
              ...refreshButtonStyle,
              opacity:
                uploading || posterCapReached || totalCapReached ? 0.6 : 1,
              display: "inline-block",
            }}
          >
            {uploading
              ? "Uploading…"
              : posterCapReached
                ? `Poster cap (${SESSION_MEDIA_MAX_POSTERS}) reached`
                : "Add poster"}
          </span>
        </label>
        <input
          type="url"
          placeholder="https://youtube.com/watch?v=…"
          value={videoInput}
          onChange={(e) => setVideoInput(e.target.value)}
          disabled={totalCapReached}
          style={{
            flex: 1,
            minWidth: "220px",
            padding: "6px 8px",
            border: "1px solid #d1d5db",
            borderRadius: "4px",
            fontSize: "12px",
          }}
          aria-label="Video URL"
        />
        <button
          type="button"
          onClick={handleAddVideo}
          disabled={totalCapReached || videoInput.trim() === ""}
          style={refreshButtonStyle}
        >
          Add video
        </button>
        <button
          type="button"
          onClick={() => saveMutation.mutate(items)}
          disabled={saveMutation.isPending}
          style={{
            ...refreshButtonStyle,
            fontWeight: 600,
            background: "#2563eb",
            color: "#ffffff",
            borderColor: "#1d4ed8",
          }}
        >
          {saveMutation.isPending ? "Saving…" : "Save gallery"}
        </button>
      </div>

      {uiError ? (
        <div style={{ ...formErrorStyle, marginTop: "4px" }}>{uiError}</div>
      ) : null}
      {uiOk ? (
        <div style={{ ...successBoxStyle, marginTop: "4px" }}>{uiOk}</div>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tiers tab (feature #283 / E-5)
// ---------------------------------------------------------------------------

type TierEditorMode =
  | { kind: "closed" }
  | { kind: "create"; sessionID: string }
  | { kind: "edit"; sessionID: string; tier: TicketTierItem };

function TiersTab({
  event,
  canCreate,
  canUpdate,
  canDelete,
}: {
  event: EventItem;
  canCreate: boolean;
  canUpdate: boolean;
  canDelete: boolean;
}) {
  const sessionsQuery = useQuery<SessionListEnvelope, ApiError>({
    queryKey: ["events", "detail", event.id, "sessions"],
    queryFn: () =>
      authedFetch<SessionListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${event.org_id}/events/${event.id}/sessions`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  if (sessionsQuery.isPending) {
    return <div style={statusBoxStyle}>Loading sessions…</div>;
  }
  if (sessionsQuery.isError) {
    return (
      <div style={errorBoxStyle} role="alert">
        <strong>Failed to load sessions.</strong>
        <div style={errorCodeStyle}>
          {sessionsQuery.error?.code ?? "unknown.error"}
        </div>
      </div>
    );
  }
  const sessions = sessionsQuery.data?.sessions ?? [];
  if (sessions.length === 0) {
    return (
      <div style={statusBoxStyle} data-testid="events-tiers-empty-sessions">
        No sessions yet -- create a session before adding ticket tiers.
      </div>
    );
  }
  return (
    <div style={tabBodyStyle} data-testid="events-tiers-tab">
      {sessions.map((s) => (
        <SessionTiersBlock
          key={s.id}
          event={event}
          session={s}
          canCreate={canCreate}
          canUpdate={canUpdate}
          canDelete={canDelete}
        />
      ))}
    </div>
  );
}

function SessionTiersBlock({
  event,
  session,
  canCreate,
  canUpdate,
  canDelete,
}: {
  event: EventItem;
  session: SessionItem;
  canCreate: boolean;
  canUpdate: boolean;
  canDelete: boolean;
}) {
  const queryClient = useQueryClient();
  const queryKey = [
    "events",
    "detail",
    event.id,
    "session",
    session.id,
    "tiers",
  ] as const;
  const query = useQuery<TicketTierListEnvelope, ApiError>({
    queryKey,
    queryFn: () =>
      authedFetch<TicketTierListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${event.org_id}/events/${event.id}/sessions/${session.id}/tiers`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  const [editor, setEditor] = useState<TierEditorMode>({ kind: "closed" });
  const [confirmDeleteID, setConfirmDeleteID] = useState<string | null>(null);
  const [actionErr, setActionErr] = useState<string | null>(null);
  const [actionOk, setActionOk] = useState<string | null>(null);
  // AB-48: which tier's price schedule editor is open.
  const [scheduleTierID, setScheduleTierID] = useState<string | null>(null);

  const deleteMutation = useMutation<void, ApiError, string>({
    mutationFn: (id) =>
      authedFetch<void>({
        method: "DELETE",
        path: `/v1/organizations/${event.org_id}/events/${event.id}/sessions/${session.id}/tiers/${id}`,
      }),
    onSuccess: (_data, id) => {
      setActionErr(null);
      setActionOk(`Deleted tier ${shortenUUID(id)}.`);
      setConfirmDeleteID(null);
      void queryClient.invalidateQueries({ queryKey });
    },
    onError: (err) => {
      setActionOk(null);
      setActionErr(mapTierError(err));
    },
  });

  const tiers = query.data?.ticket_tiers ?? query.data?.tiers ?? [];
  const sortedTiers = useMemo(
    () => [...tiers].sort((a, b) => a.sort_order - b.sort_order),
    [tiers],
  );

  return (
    <section
      style={tierBlockStyle}
      data-testid={`events-tier-block-${session.id}`}
    >
      <header style={tierBlockHeaderStyle}>
        <div>
          <div style={detailLabelStyle}>
            Session {formatDateTime(session.start_at)}
          </div>
          <div style={mutedHintStyle}>
            {session.status} · capacity {session.capacity_total.toLocaleString()}{" "}
            · {session.currency}
          </div>
        </div>
        {canCreate ? (
          <button
            type="button"
            style={primaryButtonStyle}
            onClick={() => {
              setActionErr(null);
              setActionOk(null);
              setEditor({ kind: "create", sessionID: session.id });
            }}
            disabled={editor.kind !== "closed"}
            data-testid={`events-tier-add-${session.id}`}
          >
            Add tier
          </button>
        ) : (
          <span style={mutedHintStyle}>
            <code style={monoStyle}>tier.create</code> required.
          </span>
        )}
      </header>

      {actionErr !== null ? (
        <div
          style={formErrorStyle}
          role="alert"
          data-testid={`events-tier-action-error-${session.id}`}
        >
          {actionErr}
        </div>
      ) : null}
      {actionOk !== null ? (
        <div
          style={successBoxStyle}
          role="status"
          data-testid={`events-tier-action-ok-${session.id}`}
        >
          {actionOk}
        </div>
      ) : null}

      {editor.kind === "create" && editor.sessionID === session.id ? (
        <TierEditor
          event={event}
          session={session}
          mode={editor}
          onClose={() => setEditor({ kind: "closed" })}
          onSaved={(label) => {
            setActionErr(null);
            setActionOk(label);
            setEditor({ kind: "closed" });
            void queryClient.invalidateQueries({ queryKey });
          }}
          onError={(msg) => {
            setActionErr(msg);
            setActionOk(null);
          }}
        />
      ) : null}

      {query.isPending ? (
        <div style={statusBoxStyle}>Loading tiers…</div>
      ) : query.isError ? (
        <div style={errorBoxStyle} role="alert">
          <strong>Failed to load tiers.</strong>
          <div style={errorCodeStyle}>
            {query.error?.code ?? "unknown.error"}
          </div>
        </div>
      ) : sortedTiers.length === 0 && editor.kind === "closed" ? (
        <div style={statusBoxStyle}>No tiers configured.</div>
      ) : sortedTiers.length > 0 ? (
        <div style={tableWrapStyle}>
          <table style={tableStyle}>
            <thead>
              <tr>
                <th scope="col" style={thStyle}>Name</th>
                <th scope="col" style={thStyle}>Pricing</th>
                <th scope="col" style={thStyle}>Price</th>
                <th scope="col" style={thStyle}>Currency</th>
                <th scope="col" style={thStyle}>Capacity</th>
                <th scope="col" style={thStyle}>Seats</th>
                <th scope="col" style={thStyle}>Sort</th>
                <th scope="col" style={thStyle}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {sortedTiers.map((t) => {
                const isEditing =
                  editor.kind === "edit" && editor.tier.id === t.id;
                const isScheduling = scheduleTierID === t.id;
                return (
                  <Fragment key={t.id}>
                    <tr data-testid={`events-tier-${t.id}`}>
                      <td style={tdStyle}>{t.name}</td>
                      <td style={tdStyle}>{t.pricing_mode}</td>
                      <td style={tdStyle}>
                        {t.pricing_mode === "free"
                          ? "—"
                          : t.pricing_mode === "pwyw"
                            ? `${centsToDecimal(t.pwyw_min ?? 0)} – ${
                                t.pwyw_max !== null && t.pwyw_max !== undefined
                                  ? centsToDecimal(t.pwyw_max)
                                  : "∞"
                              }`
                            : centsToDecimal(t.price_amount)}
                      </td>
                      <td style={tdStyle}>{t.currency}</td>
                      <td style={tdStyle}>
                        {t.capacity !== null && t.capacity !== undefined
                          ? t.capacity.toLocaleString()
                          : "—"}
                      </td>
                      <td style={tdStyle} data-testid={`events-tier-seats-${t.id}`}>
                        {/* AB-48 step 3: inventory beside the price. */}
                        {(t.seat_count ?? 0) > 0
                          ? `${(t.seat_count ?? 0).toLocaleString()} seats`
                          : (t.ga_unit_count ?? 0) > 0
                            ? `${(t.ga_unit_count ?? 0).toLocaleString()} GA`
                            : "—"}
                      </td>
                      <td style={tdStyle}>{t.sort_order}</td>
                      <td style={tdStyle}>
                        <div style={rowActionsStyle}>
                          {canUpdate ? (
                            <button
                              type="button"
                              style={refreshButtonStyle}
                              onClick={() => {
                                setActionErr(null);
                                setActionOk(null);
                                setEditor({
                                  kind: "edit",
                                  sessionID: session.id,
                                  tier: t,
                                });
                              }}
                              data-testid={`events-tier-edit-${t.id}`}
                              disabled={isEditing}
                            >
                              Edit
                            </button>
                          ) : null}
                          {canUpdate && t.pricing_mode === "fixed" ? (
                            <button
                              type="button"
                              style={refreshButtonStyle}
                              onClick={() =>
                                setScheduleTierID(isScheduling ? null : t.id)
                              }
                              data-testid={`events-tier-schedule-${t.id}`}
                            >
                              {isScheduling ? "Close schedule" : "Schedule"}
                            </button>
                          ) : null}
                          {canDelete ? (
                            <button
                              type="button"
                              style={dangerButtonStyle}
                              onClick={() => {
                                setActionErr(null);
                                setActionOk(null);
                                setConfirmDeleteID(t.id);
                              }}
                              data-testid={`events-tier-delete-${t.id}`}
                              disabled={deleteMutation.isPending}
                            >
                              Delete
                            </button>
                          ) : null}
                          {!canUpdate && !canDelete ? (
                            <span style={mutedHintStyle}>read-only</span>
                          ) : null}
                        </div>
                      </td>
                    </tr>
                    {confirmDeleteID === t.id ? (
                      <tr>
                        <td colSpan={7} style={tdStyle}>
                          <div
                            style={confirmDeleteStyle}
                            data-testid={`events-tier-confirm-${t.id}`}
                          >
                            <span>
                              Delete tier &quot;{t.name}&quot;? This cannot be
                              undone.
                            </span>
                            <div style={rowActionsStyle}>
                              <button
                                type="button"
                                style={dangerButtonStyle}
                                onClick={() => deleteMutation.mutate(t.id)}
                                disabled={deleteMutation.isPending}
                                data-testid={`events-tier-confirm-yes-${t.id}`}
                              >
                                {deleteMutation.isPending
                                  ? "Deleting…"
                                  : "Yes, delete"}
                              </button>
                              <button
                                type="button"
                                style={refreshButtonStyle}
                                onClick={() => setConfirmDeleteID(null)}
                                disabled={deleteMutation.isPending}
                              >
                                Cancel
                              </button>
                            </div>
                          </div>
                        </td>
                      </tr>
                    ) : null}
                    {isEditing ? (
                      <tr>
                        <td colSpan={8} style={tdStyle}>
                          <TierEditor
                            event={event}
                            session={session}
                            mode={editor}
                            onClose={() => setEditor({ kind: "closed" })}
                            onSaved={(label) => {
                              setActionErr(null);
                              setActionOk(label);
                              setEditor({ kind: "closed" });
                              void queryClient.invalidateQueries({ queryKey });
                            }}
                            onError={(msg) => {
                              setActionErr(msg);
                              setActionOk(null);
                            }}
                          />
                        </td>
                      </tr>
                    ) : null}
                    {isScheduling ? (
                      <tr data-testid={`events-tier-schedule-row-${t.id}`}>
                        <td colSpan={8} style={tdStyle}>
                          <PriceScheduleEditor
                            event={event}
                            session={session}
                            tier={t}
                            onSaved={(msg) => {
                              setActionErr(null);
                              setActionOk(msg);
                              void queryClient.invalidateQueries({ queryKey });
                            }}
                            onError={(msg) => {
                              setActionOk(null);
                              setActionErr(msg);
                            }}
                          />
                        </td>
                      </tr>
                    ) : null}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
      ) : null}
    </section>
  );
}

interface TierEnvelope {
  readonly tier: TicketTierItem;
}

// ---------------------------------------------------------------------------
// AB-48: scheduled pricing editor (per tier)
// ---------------------------------------------------------------------------

function PriceScheduleEditor({
  event,
  session,
  tier,
  onSaved,
  onError,
}: {
  event: EventItem;
  session: SessionItem;
  tier: TicketTierItem;
  onSaved: (msg: string) => void;
  onError: (msg: string) => void;
}) {
  const basePath = `/v1/organizations/${event.org_id}/events/${event.id}/sessions/${session.id}/tiers/${tier.id}/price-schedule`;
  const queryKey = ["events", "tier-schedule", tier.id] as const;
  const query = useQuery<TierPriceScheduleEnvelope, ApiError>({
    queryKey,
    queryFn: () => authedFetch<TierPriceScheduleEnvelope>({ method: "GET", path: basePath }),
    retry: false,
    refetchOnWindowFocus: false,
  });
  const [rows, setRows] = useState<PriceWindowFormRow[] | null>(null);
  const loaded = query.data?.price_schedule;
  const effectiveRows: PriceWindowFormRow[] =
    rows ??
    (loaded?.windows ?? []).map((w) => ({
      valid_from: toLocalDatetimeValue(w.valid_from),
      valid_to: w.valid_to === null ? "" : toLocalDatetimeValue(w.valid_to),
      price_amount: centsToDecimal(w.price_amount),
    }));
  const built = buildPriceWindows(effectiveRows);
  const queryClient = useQueryClient();
  const mutation = useMutation<TierPriceScheduleEnvelope, ApiError, TierPriceWindow[]>({
    mutationFn: (windows) =>
      authedFetch<TierPriceScheduleEnvelope>({ method: "PUT", path: basePath, body: { windows } }),
    onSuccess: (data) => {
      setRows(null);
      void queryClient.invalidateQueries({ queryKey });
      onSaved(
        `Saved price schedule for "${tier.name}" — current price ${centsToDecimal(data.price_schedule.current_price)} ${tier.currency}.`,
      );
    },
    onError: (err) => onError(mapTierError(err)),
  });

  if (query.isPending) {
    return <div style={statusBoxStyle}>Loading price schedule…</div>;
  }
  return (
    <div data-testid={`events-tier-schedule-editor-${tier.id}`} style={{ display: "grid", gap: 8 }}>
      <div style={detailLabelStyle}>Scheduled prices — {tier.name}</div>
      <div style={mutedHintStyle}>
        Base price {centsToDecimal(tier.price_amount)} {tier.currency} applies whenever no window
        covers the moment. Current price:{" "}
        <strong>{centsToDecimal(loaded?.current_price ?? tier.price_amount)} {tier.currency}</strong>
        {loaded?.next_price_change_at
          ? ` · changes ${formatDateTime(loaded.next_price_change_at)}`
          : ""}
        . Carts lock the price they were quoted; edits apply to new carts only.
      </div>
      {effectiveRows.map((r, idx) => (
        <div key={idx} style={editorGridStyle}>
          <label style={editorFieldStyle}>
            <span style={editorLabelStyle}>From</span>
            <input
              type="datetime-local"
              value={r.valid_from}
              onChange={(e) =>
                setRows(effectiveRows.map((x, i) => (i === idx ? { ...x, valid_from: e.target.value } : x)))
              }
              style={editorInputStyle}
              data-testid={`events-tier-window-from-${tier.id}-${idx}`}
            />
          </label>
          <label style={editorFieldStyle}>
            <span style={editorLabelStyle}>Until (empty = open-ended)</span>
            <input
              type="datetime-local"
              value={r.valid_to}
              onChange={(e) =>
                setRows(effectiveRows.map((x, i) => (i === idx ? { ...x, valid_to: e.target.value } : x)))
              }
              style={editorInputStyle}
              data-testid={`events-tier-window-to-${tier.id}-${idx}`}
            />
          </label>
          <label style={editorFieldStyle}>
            <span style={editorLabelStyle}>Price ({tier.currency})</span>
            <input
              type="text"
              inputMode="decimal"
              value={r.price_amount}
              onChange={(e) =>
                setRows(effectiveRows.map((x, i) => (i === idx ? { ...x, price_amount: e.target.value } : x)))
              }
              style={editorInputStyle}
              data-testid={`events-tier-window-price-${tier.id}-${idx}`}
            />
          </label>
          <button
            type="button"
            style={refreshButtonStyle}
            onClick={() => setRows(effectiveRows.filter((_, i) => i !== idx))}
            aria-label="Remove window"
          >
            Remove
          </button>
        </div>
      ))}
      {built.error !== undefined ? <span style={fieldErrorStyle}>{built.error}</span> : null}
      <div style={rowActionsStyle}>
        <button
          type="button"
          style={refreshButtonStyle}
          onClick={() => setRows([...effectiveRows, { valid_from: "", valid_to: "", price_amount: "" }])}
          data-testid={`events-tier-window-add-${tier.id}`}
        >
          + Add window
        </button>
        <button
          type="button"
          style={primaryButtonStyle}
          disabled={built.windows === undefined || mutation.isPending}
          onClick={() => {
            if (built.windows !== undefined) mutation.mutate(built.windows);
          }}
          data-testid={`events-tier-schedule-save-${tier.id}`}
        >
          {mutation.isPending ? "Saving…" : "Save schedule"}
        </button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// AB-48: bulk price grid across sessions
// ---------------------------------------------------------------------------

interface BulkPricingResult {
  readonly session_id: string;
  readonly applied: readonly string[];
  readonly missing_tiers: readonly string[];
  readonly error: string | null;
}

function BulkPricingPanel({
  event,
  sessions,
}: {
  event: EventItem;
  sessions: readonly SessionItem[];
}) {
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [grid, setGrid] = useState<{ tier_name: string; price_amount: string }[]>([
    { tier_name: "", price_amount: "" },
  ]);
  const [results, setResults] = useState<readonly BulkPricingResult[] | null>(null);
  const queryClient = useQueryClient();

  // Propose category names from the first selected session's tiers (AB-48
  // step A: only the categories that exist, never a fixed 15-row grid).
  const firstSelected = [...selected][0];
  const tiersQuery = useQuery<TicketTierListEnvelope, ApiError>({
    queryKey: ["events", "bulk-pricing-tiers", firstSelected ?? ""],
    enabled: firstSelected !== undefined,
    queryFn: () =>
      authedFetch<TicketTierListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${event.org_id}/events/${event.id}/sessions/${firstSelected}/tiers`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });
  const suggested = (tiersQuery.data?.ticket_tiers ?? tiersQuery.data?.tiers ?? []).filter(
    (t) => t.pricing_mode === "fixed",
  );

  const mutation = useMutation<{ results: readonly BulkPricingResult[] }, ApiError, void>({
    mutationFn: () =>
      authedFetch<{ results: readonly BulkPricingResult[] }>({
        method: "POST",
        path: `/v1/organizations/${event.org_id}/events/${event.id}/sessions/pricing-bulk`,
        body: {
          session_ids: [...selected],
          prices: grid
            .filter((g) => g.tier_name.trim() !== "")
            .map((g) => ({ tier_name: g.tier_name.trim(), price_amount: decimalToCents(g.price_amount) ?? 0 })),
        },
      }),
    onSuccess: (data) => {
      setResults(data.results);
      void queryClient.invalidateQueries({ queryKey: ["events", "detail", event.id] });
    },
  });

  const gridValid =
    grid.some((g) => g.tier_name.trim() !== "") &&
    grid.every((g) => g.tier_name.trim() === "" || (decimalToCents(g.price_amount) ?? -1) >= 0);

  if (!open) {
    return (
      <div style={{ marginTop: 8 }}>
        <button
          type="button"
          style={refreshButtonStyle}
          onClick={() => setOpen(true)}
          data-testid="events-bulk-pricing-open"
        >
          Apply one price grid to several sessions…
        </button>
      </div>
    );
  }

  return (
    <section style={tierBlockStyle} data-testid="events-bulk-pricing">
      <div style={detailLabelStyle}>Bulk pricing — apply one grid to several sessions</div>
      <div style={mutedHintStyle}>
        Categories are matched by name across the selected sessions (tiers are created per plan
        category). Every change is audited.
      </div>
      <div style={{ display: "grid", gap: 4, margin: "8px 0" }}>
        {sessions.map((sess) => (
          <label key={sess.id} style={{ fontSize: 13 }}>
            <input
              type="checkbox"
              checked={selected.has(sess.id)}
              onChange={(e) => {
                const next = new Set(selected);
                if (e.target.checked) next.add(sess.id);
                else next.delete(sess.id);
                setSelected(next);
              }}
              data-testid={`events-bulk-pricing-session-${sess.id}`}
            />{" "}
            {formatDateTime(sess.start_at)} · {sess.currency}
          </label>
        ))}
      </div>
      {suggested.length > 0 && grid.length === 1 && grid[0]!.tier_name === "" ? (
        <button
          type="button"
          style={refreshButtonStyle}
          onClick={() =>
            setGrid(
              suggested.map((t) => ({ tier_name: t.name, price_amount: centsToDecimal(t.price_amount) })),
            )
          }
          data-testid="events-bulk-pricing-prefill"
        >
          Prefill the {suggested.length} categories of the first selected session
        </button>
      ) : null}
      {grid.map((g, idx) => (
        <div key={idx} style={editorGridStyle}>
          <label style={editorFieldStyle}>
            <span style={editorLabelStyle}>Category name</span>
            <input
              type="text"
              value={g.tier_name}
              onChange={(e) =>
                setGrid(grid.map((x, i) => (i === idx ? { ...x, tier_name: e.target.value } : x)))
              }
              style={editorInputStyle}
              data-testid={`events-bulk-pricing-name-${idx}`}
            />
          </label>
          <label style={editorFieldStyle}>
            <span style={editorLabelStyle}>Price (major units)</span>
            <input
              type="text"
              inputMode="decimal"
              value={g.price_amount}
              onChange={(e) =>
                setGrid(grid.map((x, i) => (i === idx ? { ...x, price_amount: e.target.value } : x)))
              }
              style={editorInputStyle}
              data-testid={`events-bulk-pricing-price-${idx}`}
            />
          </label>
        </div>
      ))}
      <div style={rowActionsStyle}>
        <button
          type="button"
          style={refreshButtonStyle}
          onClick={() => setGrid([...grid, { tier_name: "", price_amount: "" }])}
        >
          + Add category
        </button>
        <button
          type="button"
          style={primaryButtonStyle}
          disabled={selected.size === 0 || !gridValid || mutation.isPending}
          onClick={() => mutation.mutate()}
          data-testid="events-bulk-pricing-apply"
        >
          {mutation.isPending ? "Applying…" : `Apply to ${selected.size} session${selected.size === 1 ? "" : "s"}`}
        </button>
        <button type="button" style={refreshButtonStyle} onClick={() => setOpen(false)}>
          Close
        </button>
      </div>
      {mutation.isError ? (
        <div style={formErrorStyle} role="alert">
          {mapTierError(mutation.error)}
        </div>
      ) : null}
      {results !== null ? (
        <ul style={{ margin: "8px 0", paddingLeft: 20 }} data-testid="events-bulk-pricing-results">
          {results.map((r) => (
            <li key={r.session_id}>
              <code style={monoStyle}>{shortenUUID(r.session_id)}</code>:{" "}
              {r.error !== null
                ? `error — ${r.error}`
                : `applied ${r.applied.length}${r.missing_tiers.length > 0 ? `, missing: ${r.missing_tiers.join(", ")}` : ""}`}
            </li>
          ))}
        </ul>
      ) : null}
    </section>
  );
}

interface TierEditorProps {
  event: EventItem;
  session: SessionItem;
  mode: Exclude<TierEditorMode, { kind: "closed" }>;
  onClose: () => void;
  /**
   * Called on successful create/edit. The optional `tier` argument is the
   * TicketTierItem returned by the server; the AB-42 wizard consumes it
   * to update the tier list without a full refetch. Existing callers may
   * ignore this parameter.
   */
  onSaved: (label: string, tier?: TicketTierItem) => void;
  onError: (msg: string) => void;
}

function TierEditor({
  event,
  session,
  mode,
  onClose,
  onSaved,
  onError,
}: TierEditorProps) {
  const initial =
    mode.kind === "edit" ? tierToForm(mode.tier) : emptyTierForm();
  const [values, setValues] = useState<TierFormValues>(initial);
  const errors = useMemo(() => validateTierForm(values), [values]);

  const mutation = useMutation<TierEnvelope, ApiError, TierFormValues>({
    mutationFn: (v) => {
      const basePath = `/v1/organizations/${event.org_id}/events/${event.id}/sessions/${session.id}/tiers`;
      const body = buildTierRequestBody(v);
      if (mode.kind === "create") {
        return authedFetch<TierEnvelope>({
          method: "POST",
          path: basePath,
          body,
        });
      }
      return authedFetch<TierEnvelope>({
        method: "PATCH",
        path: `${basePath}/${mode.tier.id}`,
        body,
      });
    },
    onSuccess: (data) => {
      onSaved(
        mode.kind === "create"
          ? `Created tier "${data.tier.name}".`
          : `Updated tier "${data.tier.name}".`,
        data.tier,
      );
    },
    onError: (err) => {
      onError(mapTierError(err));
    },
  });

  const submit = () => {
    if (Object.keys(errors).length > 0) {
      return;
    }
    mutation.mutate(values);
  };

  return (
    <form
      style={editorFormStyle}
      data-testid={
        mode.kind === "create"
          ? `events-tier-form-create-${session.id}`
          : `events-tier-form-edit-${mode.tier.id}`
      }
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <div style={detailLabelStyle}>
        {mode.kind === "create" ? "Add ticket tier" : "Edit ticket tier"}
      </div>
      <div style={editorGridStyle}>
        <label style={editorFieldStyle}>
          <span style={editorLabelStyle}>Name</span>
          <input
            type="text"
            value={values.name}
            onChange={(e) => setValues({ ...values, name: e.target.value })}
            style={editorInputStyle}
            maxLength={200}
            required
            data-testid="events-tier-input-name"
          />
          {errors.name !== undefined ? (
            <span style={fieldErrorStyle}>{errors.name}</span>
          ) : null}
        </label>
        <label style={editorFieldStyle}>
          <span style={editorLabelStyle}>Pricing mode</span>
          <select
            value={values.pricing_mode}
            onChange={(e) => {
              const v = e.target.value;
              setValues({
                ...values,
                pricing_mode: isTierPricingMode(v) ? v : "fixed",
              });
            }}
            style={editorInputStyle}
            data-testid="events-tier-input-mode"
          >
            {TIER_PRICING_MODES.map((m) => (
              <option key={m} value={m}>
                {m}
              </option>
            ))}
          </select>
          {errors.pricing_mode !== undefined ? (
            <span style={fieldErrorStyle}>{errors.pricing_mode}</span>
          ) : null}
          {/* AB-44: per-mode help text */}
          <span style={mutedHintStyle}>
            {values.pricing_mode === "fixed"
              ? "Attendee pays an exact price you set."
              : values.pricing_mode === "free"
                ? "Admission is free — no payment required."
                : values.pricing_mode === "pwyw"
                  ? "Pay what you want — attendee chooses an amount within optional min/max bounds."
                  : null}
          </span>
        </label>
        {values.pricing_mode === "fixed" ? (
          <label style={editorFieldStyle}>
            <span style={editorLabelStyle}>Price (major units)</span>
            <input
              type="text"
              inputMode="decimal"
              value={values.price_amount}
              onChange={(e) =>
                setValues({ ...values, price_amount: e.target.value })
              }
              placeholder="e.g. 12.50"
              style={editorInputStyle}
              required
              data-testid="events-tier-input-price"
            />
            {errors.price_amount !== undefined ? (
              <span style={fieldErrorStyle}>{errors.price_amount}</span>
            ) : null}
          </label>
        ) : null}
        <label style={editorFieldStyle}>
          <span style={editorLabelStyle}>Currency</span>
          <input
            type="text"
            value={session.currency}
            readOnly
            style={{
              ...editorInputStyle,
              background: "#f1f5f9",
              color: "#64748b",
            }}
            title="Every tier of a session is denominated in the session currency (AB-38)."
            data-testid="events-tier-currency-readonly"
          />
          <span style={mutedHintStyle}>
            Set by the session (
            {session.currency_source === "derived"
              ? "derived from venue"
              : "set manually"}
            ).
          </span>
        </label>
        {values.pricing_mode === "pwyw" ? (
          <>
            <label style={editorFieldStyle}>
              <span style={editorLabelStyle}>pwyw min (major units)</span>
              <input
                type="text"
                inputMode="decimal"
                value={values.pwyw_min}
                onChange={(e) =>
                  setValues({ ...values, pwyw_min: e.target.value })
                }
                placeholder="optional"
                style={editorInputStyle}
                data-testid="events-tier-input-pwyw-min"
              />
              {errors.pwyw_min !== undefined ? (
                <span style={fieldErrorStyle}>{errors.pwyw_min}</span>
              ) : null}
            </label>
            <label style={editorFieldStyle}>
              <span style={editorLabelStyle}>pwyw max (major units)</span>
              <input
                type="text"
                inputMode="decimal"
                value={values.pwyw_max}
                onChange={(e) =>
                  setValues({ ...values, pwyw_max: e.target.value })
                }
                placeholder="optional"
                style={editorInputStyle}
                data-testid="events-tier-input-pwyw-max"
              />
              {errors.pwyw_max !== undefined ? (
                <span style={fieldErrorStyle}>{errors.pwyw_max}</span>
              ) : null}
            </label>
          </>
        ) : null}
        <label style={editorFieldStyle}>
          <span style={editorLabelStyle}>Capacity (optional)</span>
          <input
            type="number"
            min={1}
            step={1}
            value={values.capacity}
            onChange={(e) =>
              setValues({ ...values, capacity: e.target.value })
            }
            placeholder="unlimited"
            style={editorInputStyle}
            data-testid="events-tier-input-capacity"
          />
          {errors.capacity !== undefined ? (
            <span style={fieldErrorStyle}>{errors.capacity}</span>
          ) : null}
        </label>
        <label style={editorFieldStyle}>
          <span style={editorLabelStyle}>Sale start (UTC, optional)</span>
          <input
            type="datetime-local"
            value={values.sale_window_start}
            onChange={(e) =>
              setValues({ ...values, sale_window_start: e.target.value })
            }
            style={editorInputStyle}
            data-testid="events-tier-input-sale-start"
          />
          {errors.sale_window_start !== undefined ? (
            <span style={fieldErrorStyle}>{errors.sale_window_start}</span>
          ) : null}
        </label>
        <label style={editorFieldStyle}>
          <span style={editorLabelStyle}>Sale end (UTC, optional)</span>
          <input
            type="datetime-local"
            value={values.sale_window_end}
            onChange={(e) =>
              setValues({ ...values, sale_window_end: e.target.value })
            }
            style={editorInputStyle}
            data-testid="events-tier-input-sale-end"
          />
          {errors.sale_window_end !== undefined ? (
            <span style={fieldErrorStyle}>{errors.sale_window_end}</span>
          ) : null}
        </label>
        <label style={editorFieldStyle}>
          <span style={editorLabelStyle}>Sort order</span>
          <input
            type="number"
            step={1}
            value={values.sort_order}
            onChange={(e) =>
              setValues({ ...values, sort_order: e.target.value })
            }
            style={editorInputStyle}
            required
            data-testid="events-tier-input-sort"
          />
          {errors.sort_order !== undefined ? (
            <span style={fieldErrorStyle}>{errors.sort_order}</span>
          ) : null}
        </label>
      </div>

      <div style={mobileFormBarStyle} data-testid="events-tier-actions">
        <button
          type="button"
          style={refreshButtonStyle}
          onClick={onClose}
          disabled={mutation.isPending}
          data-testid="events-tier-cancel"
        >
          Cancel
        </button>
        <button
          type="submit"
          style={primaryButtonStyle}
          disabled={mutation.isPending || Object.keys(errors).length > 0}
          data-testid="events-tier-submit"
        >
          {mutation.isPending
            ? "Saving…"
            : mode.kind === "create"
              ? "Create tier"
              : "Save changes"}
        </button>
      </div>
    </form>
  );
}

/**
 * Build the JSON request body for POST/PATCH .../tiers. Decimals are
 * converted to integer cents; optional fields are omitted (rather than
 * sent as null) so PATCH leaves them unchanged when not provided by the
 * editor. Sale-window timestamps are normalised to RFC3339 UTC.
 */
export function buildTierRequestBody(v: TierFormValues): Record<string, unknown> {
  // NOTE: currency is intentionally absent (AB-38) — the server stamps
  // the session currency on every tier and ignores a client-sent value.
  const body: Record<string, unknown> = {
    name: v.name.trim(),
    pricing_mode: v.pricing_mode,
    sort_order: Number(v.sort_order),
  };

  if (v.pricing_mode === "free") {
    body.price_amount = 0;
    body.pwyw_min = null;
    body.pwyw_max = null;
  } else if (v.pricing_mode === "fixed") {
    body.price_amount = decimalToCents(v.price_amount) ?? 0;
    body.pwyw_min = null;
    body.pwyw_max = null;
  } else {
    // pwyw: price_amount is the baseline (defaults to 0); min/max optional.
    body.price_amount = decimalToCents(v.price_amount) ?? 0;
    body.pwyw_min =
      v.pwyw_min.trim() === "" ? null : (decimalToCents(v.pwyw_min) ?? 0);
    body.pwyw_max =
      v.pwyw_max.trim() === "" ? null : (decimalToCents(v.pwyw_max) ?? 0);
  }

  if (v.capacity.trim() === "") {
    body.capacity = null;
  } else {
    body.capacity = Number(v.capacity);
  }

  body.sale_window_start =
    v.sale_window_start.trim() === "" ? null : toRFC3339(v.sale_window_start);
  body.sale_window_end =
    v.sale_window_end.trim() === "" ? null : toRFC3339(v.sale_window_end);

  return body;
}

// ---------------------------------------------------------------------------
// Publications tab (feature #284 / E-6)
//
// Manages event_publications via the existing endpoints:
//   GET    /v1/events/{event_id}/publications                        publication.read
//   POST   /v1/events/{event_id}/publications                        publication.create
//   DELETE /v1/events/{event_id}/publications/{feed_token_id}        publication.delete
//
// City list is sourced from GET /v1/geo/cities (no country_id filter applied;
// the operator can scope a publication to any city or leave it global).
// ---------------------------------------------------------------------------

// UUIDv4/v7 string shape; same loose regex used by uuid.Validate in Go.
const UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function isUUID(value: string): boolean {
  return UUID_RE.test(value.trim());
}

export function emptyPublicationForm(): PublicationFormValues {
  return {
    sales_channel_id: "",
    feed_token_id: "",
    city_id: "",
    advanced_open: false,
  };
}

export interface PublicationFormErrors {
  sales_channel_id?: string;
  feed_token_id?: string;
  city_id?: string;
}

export function validatePublicationForm(
  v: PublicationFormValues,
): PublicationFormErrors {
  const errors: PublicationFormErrors = {};
  const channel = v.sales_channel_id.trim();
  if (channel === "") {
    errors.sales_channel_id = "Sales channel is required.";
  } else if (!isUUID(channel)) {
    errors.sales_channel_id = "Sales channel must be a UUID.";
  }
  const feed = v.feed_token_id.trim();
  if (feed !== "" && !isUUID(feed)) {
    errors.feed_token_id = "Feed token must be a UUID.";
  }
  const city = v.city_id.trim();
  if (city !== "" && !isUUID(city)) {
    errors.city_id = "City must be a UUID.";
  }
  return errors;
}

export function buildPublicationRequestBody(
  v: PublicationFormValues,
  resolvedFeedTokenID: string,
): PublicationRequestBody {
  const body: PublicationRequestBody = {
    feed_token_id: resolvedFeedTokenID.trim(),
  };
  const city = v.city_id.trim();
  if (city !== "") {
    body.city_id = city;
  }
  return body;
}

/**
 * AB-43 — derive the default city scope for a new publication from the
 * event's first-in-time session's venue. Returns "" when the venue has no
 * city recorded, when sessions/venues cannot be resolved, or when the
 * event has no sessions. Exported for unit tests.
 */
export function deriveDefaultCityID(
  sessions: readonly Pick<SessionItem, "venue_id" | "start_at">[],
  venues: readonly VenueForCityLookup[],
): string {
  if (sessions.length === 0 || venues.length === 0) {
    return "";
  }
  const ordered = [...sessions].sort((a, b) =>
    a.start_at.localeCompare(b.start_at),
  );
  const venueByID = new Map(venues.map((v) => [v.id, v]));
  for (const s of ordered) {
    const v = venueByID.get(s.venue_id);
    if (v && v.city_id !== null && v.city_id !== "") {
      return v.city_id;
    }
  }
  return "";
}

export function mapPublicationError(err: ApiError): string {
  switch (err.code) {
    case "publication.invalid_event_id":
      return "Event ID is not a valid UUID.";
    case "publication.invalid_feed_token_id":
      return "Feed token ID is not a valid UUID.";
    case "publication.invalid_city_id":
      return "City ID is not a valid UUID.";
    case "publication.feed_token_id_required":
      return "Feed token ID is required.";
    case "publication.body_required":
      return "Request body is required.";
    case "publication.content_type_required":
      return "Request must be sent as JSON.";
    case "publication.invalid_json":
      return "Request body is not valid JSON.";
    // AB-43: FK violations now surface as specific 404s instead of 500.
    case "publication.feed_token_not_found":
      return "That feed token no longer exists — issue a fresh token on the sales channel and try again.";
    case "publication.city_not_found":
      return "That city is not in the geo registry — pick another city or leave the scope global.";
    case "publication.event_not_found":
      return "This event no longer exists — reload the page.";
    case "publication.internal":
      return "The server failed to apply the publication change. Try again.";
    case "feed_token.insert_failed":
    case "feed_token.generate_failed":
      return "Could not issue a feed token for the selected channel — try again.";
    case "permissions.denied":
      return "Your account is missing the permission required for this action.";
    default:
      if (err.status === 401) {
        return "Session expired. Please sign in again.";
      }
      if (err.status === 403) {
        return "Forbidden — missing required publication permission.";
      }
      return `${err.message} (${err.code})`;
  }
}

function PublicationsTab({
  event,
  canRead,
  canCreate,
  canDelete,
}: {
  event: EventItem;
  canRead: boolean;
  canCreate: boolean;
  canDelete: boolean;
}) {
  const queryClient = useQueryClient();
  const [form, setForm] = useState<PublicationFormValues>(emptyPublicationForm);
  const [formErrors, setFormErrors] = useState<PublicationFormErrors>({});
  const [actionErr, setActionErr] = useState<string | null>(null);
  const [okMsg, setOkMsg] = useState<string | null>(null);
  const [confirmDeleteID, setConfirmDeleteID] = useState<string | null>(null);

  const query = useQuery<EventPublicationListEnvelope, ApiError>({
    queryKey: ["events", "detail", event.id, "publications"],
    queryFn: () =>
      authedFetch<EventPublicationListEnvelope>({
        method: "GET",
        path: `/v1/events/${event.id}/publications`,
      }),
    enabled: canRead,
    retry: false,
    refetchOnWindowFocus: false,
  });

  const citiesQuery = useQuery<CityListEnvelope, ApiError>({
    queryKey: ["geo", "cities"],
    queryFn: () =>
      authedFetch<CityListEnvelope>({
        method: "GET",
        path: "/v1/geo/cities",
      }),
    enabled: canRead && canCreate,
    retry: false,
    refetchOnWindowFocus: false,
  });

  // AB-43: fetch the org's sales channels so the operator picks one from a
  // dropdown instead of pasting a feed-token UUID.
  const channelsQuery = useQuery<SalesChannelListEnvelope, ApiError>({
    queryKey: ["organizations", event.org_id, "channels"],
    queryFn: () =>
      authedFetch<SalesChannelListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${event.org_id}/channels`,
      }),
    enabled: canRead && canCreate,
    retry: false,
    refetchOnWindowFocus: false,
  });

  // AB-43: sessions + venues let us derive the default city scope from
  // the event's first-in-time session's venue city.
  const sessionsQueryForCity = useQuery<SessionListEnvelope, ApiError>({
    queryKey: ["events", "detail", event.id, "sessions", "publications-tab"],
    queryFn: () =>
      authedFetch<SessionListEnvelope>({
        method: "GET",
        path: `/v1/events/${event.id}/sessions`,
      }),
    enabled: canRead && canCreate,
    retry: false,
    refetchOnWindowFocus: false,
  });
  const venuesForCityQuery = useQuery<VenueLookupListEnvelope, ApiError>({
    queryKey: ["organizations", event.org_id, "venues", "publications-tab"],
    queryFn: () =>
      authedFetch<VenueLookupListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${event.org_id}/venues`,
      }),
    enabled: canRead && canCreate,
    retry: false,
    refetchOnWindowFocus: false,
  });
  const derivedCityID = deriveDefaultCityID(
    sessionsQueryForCity.data?.sessions ?? [],
    venuesForCityQuery.data?.venues ?? [],
  );

  // AB-43: when a channel is selected, list its feed tokens so the
  // mutation can pick the newest active one — or issue a fresh one when
  // the channel has none.
  const feedTokensQuery = useQuery<FeedTokenListEnvelope, ApiError>({
    queryKey: [
      "organizations",
      event.org_id,
      "channels",
      form.sales_channel_id,
      "feed-tokens",
    ],
    queryFn: () =>
      authedFetch<FeedTokenListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${event.org_id}/channels/${form.sales_channel_id}/feed-tokens`,
      }),
    enabled: canRead && canCreate && isUUID(form.sales_channel_id),
    retry: false,
    refetchOnWindowFocus: false,
  });

  const invalidate = () =>
    queryClient.invalidateQueries({
      queryKey: ["events", "detail", event.id, "publications"],
    });
  const invalidateFeedTokens = () =>
    queryClient.invalidateQueries({
      queryKey: [
        "organizations",
        event.org_id,
        "channels",
        form.sales_channel_id,
        "feed-tokens",
      ],
    });

  const publishMutation = useMutation<
    EventPublication,
    ApiError,
    { channelID: string; pinnedTokenID: string; cityID: string }
  >({
    // AB-43: publish flow resolves (or issues) the feed token for the
    // selected sales channel before calling the publications endpoint.
    mutationFn: async ({ channelID, pinnedTokenID, cityID }) => {
      let tokenID = pinnedTokenID.trim();
      if (tokenID === "") {
        // Prefer the newest active token; issue one when none exists.
        const list = feedTokensQuery.data?.feed_tokens ?? [];
        const active = list
          .filter((t) => t.is_active && t.revoked_at === null)
          .sort((a, b) => b.created_at.localeCompare(a.created_at));
        if (active.length > 0) {
          tokenID = active[0].id;
        } else {
          const issued = await authedFetch<FeedTokenEnvelope>({
            method: "POST",
            path: `/v1/organizations/${event.org_id}/channels/${channelID}/feed-tokens`,
            body: { label: "Publication (auto-issued)" },
          });
          tokenID = issued.feed_token.id;
          await invalidateFeedTokens();
        }
      }
      const body: PublicationRequestBody = { feed_token_id: tokenID };
      const c = cityID.trim();
      if (c !== "") body.city_id = c;
      return authedFetch<EventPublication>({
        method: "POST",
        path: `/v1/events/${event.id}/publications`,
        body,
      });
    },
    onSuccess: () => {
      setForm(emptyPublicationForm());
      setFormErrors({});
      setActionErr(null);
      setOkMsg("Published to feed.");
      void invalidate();
    },
    onError: (err) => {
      setOkMsg(null);
      setActionErr(mapPublicationError(err));
    },
  });

  const unpublishMutation = useMutation<void, ApiError, string>({
    mutationFn: (feedTokenID) =>
      authedFetch<void>({
        method: "DELETE",
        path: `/v1/events/${event.id}/publications/${feedTokenID}`,
      }),
    onSuccess: () => {
      setConfirmDeleteID(null);
      setActionErr(null);
      setOkMsg("Unpublished from feed.");
      void invalidate();
    },
    onError: (err) => {
      setOkMsg(null);
      setActionErr(mapPublicationError(err));
    },
  });

  if (!canRead) {
    return (
      <div style={statusBoxStyle} data-testid="events-publications-forbidden">
        Viewing publications requires the{" "}
        <code style={monoStyle}>publication.read</code> permission.
      </div>
    );
  }
  if (query.isPending) {
    return <div style={statusBoxStyle}>Loading publications…</div>;
  }
  if (query.isError) {
    return (
      <div style={errorBoxStyle} role="alert">
        <strong>Failed to load publications.</strong>
        <div style={errorCodeStyle}>{query.error?.code ?? "unknown.error"}</div>
      </div>
    );
  }
  const pubs = query.data?.publications ?? [];
  const cities = citiesQuery.data?.cities ?? [];
  const channels = channelsQuery.data?.channels ?? [];
  const feedTokens = feedTokensQuery.data?.feed_tokens ?? [];

  // AB-43: when the operator picks a channel, prefill the city with the
  // venue-derived default (only if they haven't set one manually already).
  const cityValueForRender = form.city_id !== "" ? form.city_id : derivedCityID;

  const onSubmit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    // Copy the derived default into the form on submit so the request
    // reflects what the operator saw (venue city vs. explicit global).
    const effectiveForm: PublicationFormValues = {
      ...form,
      city_id:
        form.city_id !== "" || form.advanced_open ? form.city_id : derivedCityID,
    };
    const errs = validatePublicationForm(effectiveForm);
    setFormErrors(errs);
    if (Object.keys(errs).length > 0) {
      return;
    }
    setActionErr(null);
    setOkMsg(null);
    publishMutation.mutate({
      channelID: effectiveForm.sales_channel_id,
      pinnedTokenID: effectiveForm.feed_token_id,
      cityID: effectiveForm.city_id,
    });
  };

  return (
    <div style={tabBodyStyle}>
      {okMsg !== null ? (
        <div style={successBoxStyle} data-testid="events-publications-ok">
          {okMsg}
        </div>
      ) : null}
      {actionErr !== null ? (
        <div style={formErrorStyle} role="alert" data-testid="events-publications-error">
          {actionErr}
        </div>
      ) : null}

      {canCreate ? (
        <form
          style={editorFormStyle}
          onSubmit={onSubmit}
          data-testid="events-publications-form"
        >
          <div style={editorGridStyle}>
            {/* AB-43: primary input is a sales-channel picker. The feed
                token is resolved (or auto-issued) behind the scenes so
                the operator never has to know a token UUID. */}
            <label style={editorFieldStyle}>
              <span style={editorLabelStyle}>Sales channel</span>
              <select
                value={form.sales_channel_id}
                onChange={(e) => {
                  setForm({
                    ...form,
                    sales_channel_id: e.target.value,
                    // Clear any pinned token when the channel changes.
                    feed_token_id: "",
                  });
                  if (formErrors.sales_channel_id !== undefined) {
                    setFormErrors({
                      ...formErrors,
                      sales_channel_id: undefined,
                    });
                  }
                }}
                style={editorInputStyle}
                data-testid="events-publications-sales-channel-id"
                disabled={channelsQuery.isPending}
              >
                <option value="">
                  {channelsQuery.isPending
                    ? "Loading channels…"
                    : "Select a channel"}
                </option>
                {[...channels]
                  .sort((a, b) => a.name.localeCompare(b.name))
                  .map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.name} (#{c.display_number})
                    </option>
                  ))}
              </select>
              {channelsQuery.isError ? (
                <span style={fieldErrorStyle}>
                  Could not load channels for this organization.
                </span>
              ) : null}
              {formErrors.sales_channel_id !== undefined ? (
                <span style={fieldErrorStyle}>
                  {formErrors.sales_channel_id}
                </span>
              ) : null}
              {!channelsQuery.isPending &&
              !channelsQuery.isError &&
              channels.length === 0 ? (
                <span style={mutedHintStyle}>
                  This organization has no sales channels yet — create one
                  on the Channels page first.
                </span>
              ) : null}
            </label>
            {/* AB-43: city scope is derived from the event's first
                session's venue city; the operator only touches it under
                Advanced. */}
            <div style={editorFieldStyle}>
              <span style={editorLabelStyle}>City scope</span>
              <div
                style={mutedHintStyle}
                data-testid="events-publications-city-derived"
              >
                {derivedCityID !== ""
                  ? `Defaulting to the venue city of the event's first session (${shortenUUID(
                      derivedCityID,
                    )}). Open Advanced to override or make it global.`
                  : "No venue city on the event's sessions — defaulting to global (visible in every geography)."}
              </div>
            </div>
          </div>

          {/* AB-43: Advanced disclosure. Pins a specific feed token
              (rather than newest active) and/or overrides the derived
              city scope. Hidden by default so the common case is a
              one-click publish. */}
          <details
            open={form.advanced_open}
            onToggle={(e) =>
              setForm({
                ...form,
                advanced_open: (e.target as HTMLDetailsElement).open,
              })
            }
            data-testid="events-publications-advanced"
          >
            <summary style={{ cursor: "pointer" }}>Advanced</summary>
            <div style={editorGridStyle}>
              <label style={editorFieldStyle}>
                <span style={editorLabelStyle}>
                  Feed token (leave empty to use newest active)
                </span>
                <select
                  value={form.feed_token_id}
                  onChange={(e) => {
                    setForm({ ...form, feed_token_id: e.target.value });
                    if (formErrors.feed_token_id !== undefined) {
                      setFormErrors({
                        ...formErrors,
                        feed_token_id: undefined,
                      });
                    }
                  }}
                  style={editorInputStyle}
                  data-testid="events-publications-feed-token-id"
                  disabled={
                    !isUUID(form.sales_channel_id) ||
                    feedTokensQuery.isPending
                  }
                >
                  <option value="">
                    {isUUID(form.sales_channel_id)
                      ? feedTokensQuery.isPending
                        ? "Loading tokens…"
                        : "Newest active (auto-issue if none)"
                      : "Pick a channel first"}
                  </option>
                  {feedTokens
                    .filter((t) => t.is_active && t.revoked_at === null)
                    .map((t) => (
                      <option key={t.id} value={t.id}>
                        {t.label !== ""
                          ? `${t.label} — ${shortenUUID(t.id)}`
                          : shortenUUID(t.id)}
                      </option>
                    ))}
                </select>
                {formErrors.feed_token_id !== undefined ? (
                  <span style={fieldErrorStyle}>
                    {formErrors.feed_token_id}
                  </span>
                ) : null}
              </label>
              <label style={editorFieldStyle}>
                <span style={editorLabelStyle}>City scope override</span>
                <select
                  value={cityValueForRender}
                  onChange={(e) => {
                    setForm({ ...form, city_id: e.target.value });
                    if (formErrors.city_id !== undefined) {
                      setFormErrors({ ...formErrors, city_id: undefined });
                    }
                  }}
                  style={editorInputStyle}
                  data-testid="events-publications-city-id"
                  disabled={citiesQuery.isPending}
                >
                  <option value="">Global (no geo filter)</option>
                  {[...cities]
                    .sort((a, b) => a.name.localeCompare(b.name))
                    .map((c) => (
                      <option key={c.id} value={c.id}>
                        {c.name} ({c.country_iso2})
                      </option>
                    ))}
                </select>
                {formErrors.city_id !== undefined ? (
                  <span style={fieldErrorStyle}>{formErrors.city_id}</span>
                ) : null}
              </label>
            </div>
          </details>

          <div style={rowActionsStyle}>
            <button
              type="submit"
              style={primaryButtonStyle}
              disabled={publishMutation.isPending}
              data-testid="events-publications-submit"
            >
              {publishMutation.isPending ? "Publishing…" : "Publish to feed"}
            </button>
            {citiesQuery.isError ? (
              <span style={mutedHintStyle}>
                Cities failed to load — you can still publish without a city
                scope.
              </span>
            ) : null}
          </div>
        </form>
      ) : (
        <div style={statusBoxStyle} data-testid="events-publications-noperm-create">
          Publishing requires the{" "}
          <code style={monoStyle}>publication.create</code> permission.
        </div>
      )}

      {pubs.length === 0 ? (
        <div style={statusBoxStyle} data-testid="events-publications-empty">
          This event has not been published to any feed yet.
        </div>
      ) : (
        <div style={tableWrapStyle}>
          <table style={tableStyle} data-testid="events-publications-table">
            <thead>
              <tr>
                <th scope="col" style={thStyle}>Feed token</th>
                <th scope="col" style={thStyle}>Scope</th>
                <th scope="col" style={thStyle}>Published</th>
                <th scope="col" style={thStyle}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {pubs.map((p) => (
                <Fragment key={p.id}>
                  <tr data-testid={`events-publication-${p.id}`}>
                    <td style={tdMonoStyle} title={p.feed_token_id}>
                      {shortenUUID(p.feed_token_id)}
                    </td>
                    <td style={tdStyle}>
                      {p.city_id === null ? (
                        <span style={globalScopeBadgeStyle}>global</span>
                      ) : (
                        <span style={scopedBadgeStyle} title={p.city_id}>
                          city {shortenUUID(p.city_id)}
                        </span>
                      )}
                    </td>
                    <td style={tdStyle}>{formatDateTime(p.published_at)}</td>
                    <td style={tdStyle}>
                      {canDelete ? (
                        <button
                          type="button"
                          style={linkButtonStyle}
                          onClick={() => {
                            setConfirmDeleteID(p.feed_token_id);
                            setActionErr(null);
                            setOkMsg(null);
                          }}
                          data-testid={`events-publication-unpublish-${p.id}`}
                          disabled={unpublishMutation.isPending}
                        >
                          Unpublish
                        </button>
                      ) : (
                        <span style={mutedHintStyle}>—</span>
                      )}
                    </td>
                  </tr>
                  {confirmDeleteID === p.feed_token_id ? (
                    <tr>
                      <td colSpan={4} style={tdStyle}>
                        <div style={confirmDeleteStyle}>
                          <span>
                            Unpublish from feed{" "}
                            <code style={monoStyle}>
                              {shortenUUID(p.feed_token_id)}
                            </code>
                            ?
                          </span>
                          <div style={rowActionsStyle}>
                            <button
                              type="button"
                              style={dangerButtonStyle}
                              onClick={() =>
                                unpublishMutation.mutate(p.feed_token_id)
                              }
                              disabled={unpublishMutation.isPending}
                              data-testid={`events-publication-confirm-${p.id}`}
                            >
                              {unpublishMutation.isPending
                                ? "Unpublishing…"
                                : "Confirm"}
                            </button>
                            <button
                              type="button"
                              style={refreshButtonStyle}
                              onClick={() => setConfirmDeleteID(null)}
                              disabled={unpublishMutation.isPending}
                            >
                              Cancel
                            </button>
                          </div>
                        </div>
                      </td>
                    </tr>
                  ) : null}
                </Fragment>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function ActivityTab() {
  return (
    <div style={statusBoxStyle} data-testid="events-activity-empty">
      No activity feed available for this event yet. A per-event audit reader
      will be wired in when the backend exposes one.
    </div>
  );
}

function DetailRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div style={detailRowStyle}>
      <div style={detailLabelStyle}>{label}</div>
      <div style={detailValueStyle}>{children}</div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

function shortenUUID(id: string): string {
  return id.length > 8 ? `${id.slice(0, 8)}…` : id;
}

// ---------------------------------------------------------------------------
// Styles (mirror venues.tsx)
// ---------------------------------------------------------------------------

const pageStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 16,
};

const headerStyle: CSSProperties = {
  display: "flex",
  alignItems: "flex-start",
  justifyContent: "space-between",
  gap: 16,
  flexWrap: "wrap",
};

const headingStyle: CSSProperties = {
  margin: 0,
  fontSize: 22,
  fontWeight: 600,
  letterSpacing: -0.2,
};

const subheadingStyle: CSSProperties = {
  margin: "4px 0 0 0",
  fontSize: 13,
  color: "#475569",
  maxWidth: 720,
  lineHeight: 1.45,
};

const refreshWrapStyle: CSSProperties = {
  display: "flex",
  gap: 8,
  alignItems: "center",
  flexWrap: "wrap",
};

const refreshButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

const primaryButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#0369a1",
  border: "1px solid #0369a1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#ffffff",
  fontWeight: 600,
};

const dangerButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#b91c1c",
  border: "1px solid #b91c1c",
  borderRadius: 4,
  cursor: "pointer",
  color: "#ffffff",
  fontWeight: 600,
};

const mutedHintStyle: CSSProperties = {
  fontSize: 11,
  color: "#94a3b8",
  fontStyle: "italic",
};

const tableWrapStyle: CSSProperties = {
  overflowX: "auto",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
};

const tableStyle: CSSProperties = {
  width: "100%",
  borderCollapse: "collapse",
  fontSize: 13,
};

const thStyle: CSSProperties = {
  textAlign: "left",
  padding: "10px 12px",
  borderBottom: "1px solid #e2e8f0",
  background: "#f8fafc",
  fontSize: 11,
  fontWeight: 600,
  color: "#475569",
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const tdStyle: CSSProperties = {
  padding: "10px 12px",
  borderBottom: "1px solid #f1f5f9",
  color: "#0f172a",
  verticalAlign: "middle",
};

const tdMonoStyle: CSSProperties = {
  ...tdStyle,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
  color: "#334155",
};

const linkButtonStyle: CSSProperties = {
  background: "transparent",
  border: "none",
  padding: 0,
  color: "#0369a1",
  cursor: "pointer",
  fontWeight: 600,
  fontSize: 13,
  textAlign: "left",
};

const statusBoxStyle: CSSProperties = {
  padding: 16,
  border: "1px dashed #cbd5e1",
  borderRadius: 6,
  background: "#f8fafc",
  fontSize: 12,
  color: "#475569",
};

const errorBoxStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
  padding: 16,
  border: "1px solid #fca5a5",
  borderRadius: 6,
  background: "#fef2f2",
  color: "#7f1d1d",
  fontSize: 12,
};
const errorParaStyle: CSSProperties = { margin: 0, fontSize: 12 };
const errorCodeStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 11,
};
const errorRetryStyle: CSSProperties = {
  alignSelf: "flex-start",
  fontSize: 12,
  padding: "6px 10px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

const filterBarStyle: CSSProperties = {
  display: "flex",
  gap: 12,
  flexWrap: "wrap",
  alignItems: "flex-end",
  padding: 12,
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
};

const filterFieldStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  minWidth: 140,
};

const filterLabelStyle: CSSProperties = {
  fontSize: 11,
  fontWeight: 600,
  color: "#475569",
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const filterSelectStyle: CSSProperties = {
  fontSize: 13,
  padding: "6px 8px",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  background: "#ffffff",
  color: "#0f172a",
};

const filterInputStyle: CSSProperties = {
  ...filterSelectStyle,
};

const paginationStyle: CSSProperties = {
  display: "flex",
  gap: 12,
  alignItems: "center",
  justifyContent: "flex-end",
  padding: "8px 0",
};

const posterImgStyle: CSSProperties = {
  width: 40,
  height: 40,
  borderRadius: 4,
  objectFit: "cover",
  border: "1px solid #e2e8f0",
  display: "block",
};

const posterFallbackStyle: CSSProperties = {
  width: 40,
  height: 40,
  borderRadius: 4,
  border: "1px solid #cbd5e1",
  background: "#f1f5f9",
  color: "#475569",
  display: "flex",
  alignItems: "center",
  justifyContent: "center",
  fontWeight: 700,
  fontSize: 16,
};

const statusBadgeStyle: CSSProperties = {
  display: "inline-block",
  padding: "2px 8px",
  fontSize: 11,
  fontWeight: 600,
  borderRadius: 999,
  border: "1px solid transparent",
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const drawerBackdropStyle: CSSProperties = {
  position: "fixed",
  inset: 0,
  background: "rgba(15, 23, 42, 0.4)",
  display: "flex",
  justifyContent: "flex-end",
  zIndex: 100,
};

const drawerStyle: CSSProperties = {
  background: "#ffffff",
  width: "min(560px, 100%)",
  height: "100%",
  display: "flex",
  flexDirection: "column",
  boxShadow: "-8px 0 24px rgba(15, 23, 42, 0.18)",
};

const drawerHeaderStyle: CSSProperties = {
  display: "flex",
  alignItems: "flex-start",
  justifyContent: "space-between",
  padding: "12px 16px",
  borderBottom: "1px solid #e2e8f0",
  gap: 12,
};

const drawerTitleStyle: CSSProperties = {
  margin: 0,
  fontSize: 16,
  fontWeight: 600,
  color: "#0f172a",
};

const dialogCloseStyle: CSSProperties = {
  background: "transparent",
  border: "none",
  fontSize: 22,
  lineHeight: 1,
  cursor: "pointer",
  color: "#64748b",
  padding: "0 4px",
};

const drawerTabBarStyle: CSSProperties = {
  display: "flex",
  borderBottom: "1px solid #e2e8f0",
  background: "#f8fafc",
  overflowX: "auto",
};

const tabStyle: CSSProperties = {
  padding: "10px 14px",
  fontSize: 12,
  fontWeight: 600,
  border: "none",
  background: "transparent",
  color: "#475569",
  cursor: "pointer",
  borderBottom: "2px solid transparent",
};

const activeTabStyle: CSSProperties = {
  ...tabStyle,
  color: "#0f172a",
  borderBottom: "2px solid #0369a1",
};

const drawerContentStyle: CSSProperties = {
  padding: 16,
  overflowY: "auto",
  flex: 1,
};

const tabBodyStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 12,
};

const detailRowStyle: CSSProperties = {
  display: "grid",
  gridTemplateColumns: "120px 1fr",
  gap: 8,
  alignItems: "baseline",
};

const detailLabelStyle: CSSProperties = {
  fontSize: 11,
  fontWeight: 600,
  color: "#475569",
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const detailValueStyle: CSSProperties = {
  fontSize: 13,
  color: "#0f172a",
};

const descriptionBlockStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
};

const descriptionTextStyle: CSSProperties = {
  margin: 0,
  fontSize: 13,
  color: "#334155",
  lineHeight: 1.5,
  whiteSpace: "pre-wrap",
};

const transitionSectionStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
  marginTop: 8,
  padding: 12,
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#f8fafc",
};

const transitionButtonRowStyle: CSSProperties = {
  display: "flex",
  gap: 8,
  flexWrap: "wrap",
};

const formErrorStyle: CSSProperties = {
  fontSize: 12,
  padding: 8,
  background: "#fef2f2",
  border: "1px solid #fca5a5",
  color: "#7f1d1d",
  borderRadius: 4,
};

const successBoxStyle: CSSProperties = {
  fontSize: 12,
  padding: 8,
  background: "#ecfdf5",
  border: "1px solid #86efac",
  color: "#166534",
  borderRadius: 4,
};

const tierBlockStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
  padding: 12,
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
};

const tierBlockHeaderStyle: CSSProperties = {
  display: "flex",
  alignItems: "center",
  justifyContent: "space-between",
};

const globalScopeBadgeStyle: CSSProperties = {
  ...statusBadgeStyle,
  background: "#dbeafe",
  color: "#1e3a8a",
  borderColor: "#93c5fd",
};

const scopedBadgeStyle: CSSProperties = {
  ...statusBadgeStyle,
  background: "#fef3c7",
  color: "#854d0e",
  borderColor: "#fde68a",
};

const monoStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
};

const sessionsHeaderStyle: CSSProperties = {
  display: "flex",
  alignItems: "flex-start",
  justifyContent: "space-between",
  gap: 12,
};

const rowActionsStyle: CSSProperties = {
  display: "flex",
  gap: 6,
  flexWrap: "wrap",
  alignItems: "center",
};

const editorFormStyle: CSSProperties = {
  ...singleColumnFormStyle,
  gap: 10,
  padding: 12,
  border: "1px solid #cbd5e1",
  borderRadius: 6,
  background: "#f8fafc",
};

const editorGridStyle: CSSProperties = {
  display: "grid",
  gridTemplateColumns: "repeat(auto-fit, minmax(160px, 1fr))",
  gap: 10,
};

const editorFieldStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
};

const editorLabelStyle: CSSProperties = {
  fontSize: 11,
  fontWeight: 600,
  color: "#475569",
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const editorInputStyle: CSSProperties = {
  fontSize: 13,
  padding: "6px 8px",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  background: "#ffffff",
  color: "#0f172a",
};

const fieldErrorStyle: CSSProperties = {
  fontSize: 11,
  color: "#b91c1c",
};

const overlapWarningStyle: CSSProperties = {
  padding: 10,
  border: "1px solid #fcd34d",
  borderRadius: 4,
  background: "#fffbeb",
  color: "#92400e",
  fontSize: 12,
};

const overlapListStyle: CSSProperties = {
  margin: "4px 0 0 16px",
  padding: 0,
};

const confirmDeleteStyle: CSSProperties = {
  display: "flex",
  alignItems: "center",
  justifyContent: "space-between",
  gap: 12,
  padding: 8,
  border: "1px solid #fca5a5",
  background: "#fef2f2",
  borderRadius: 4,
  fontSize: 12,
  color: "#7f1d1d",
};

const createPanelBackdropStyle: CSSProperties = {
  position: "fixed",
  inset: 0,
  background: "rgba(15, 23, 42, 0.4)",
  display: "flex",
  alignItems: "center",
  justifyContent: "center",
  zIndex: 200,
};

const createPanelStyle: CSSProperties = {
  background: "#ffffff",
  width: "min(600px, 96vw)",
  maxHeight: "90vh",
  display: "flex",
  flexDirection: "column",
  borderRadius: 8,
  boxShadow: "0 8px 32px rgba(15, 23, 42, 0.22)",
  overflow: "hidden",
};

const createPanelBodyStyle: CSSProperties = {
  padding: 16,
  overflowY: "auto",
  flex: 1,
};
