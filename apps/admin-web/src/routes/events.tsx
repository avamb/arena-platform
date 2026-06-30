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
 * "Next session" column:
 *   The EventItem shape does not currently expose an aggregated
 *   next-session timestamp. We approximate by rendering the event's
 *   own `start_at` (events represent the umbrella; their start_at is
 *   the earliest scheduled time). When an `events.next_session_at`
 *   field is added server-side, replace the column source here.
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
import { ApiError, authedFetch } from "@/lib/api/client";
import { RequirePermission } from "@/components/RequirePermission";
import { useAuth } from "@/lib/auth/useAuth";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";
import {
  ResponsiveTable,
  type ResponsiveTableColumn,
  mobileFormBarStyle,
  singleColumnFormStyle,
} from "@/components/layout";

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
  readonly org_id: string;
  readonly venue_id: string | null;
  readonly name: string;
  readonly description: string | null;
  readonly status: EventStatus;
  readonly start_at: string;
  readonly end_at: string;
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
}

interface OrganizationListEnvelope {
  readonly organizations: readonly OrganizationSummary[];
}

export interface SessionItem {
  readonly id: string;
  readonly event_id: string;
  readonly start_at: string;
  readonly end_at: string;
  readonly capacity_total: number;
  readonly status: "draft" | "scheduled" | "cancelled" | "completed" | string;
  readonly created_at: string;
  readonly updated_at: string;
  readonly has_overlapping_sessions?: boolean;
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
  feed_token_id: string;
  city_id: string;
}

export interface PublicationRequestBody {
  feed_token_id: string;
  city_id?: string | null;
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
 * Filter events whose `start_at` falls inside an inclusive date range.
 * Both bounds are optional ("" = unbounded). Inputs are
 * `<input type="date">` strings (yyyy-MM-dd, local TZ-naive); we compare
 * by ISO date prefix so an off-by-one timezone shift in the client does
 * not silently drop events near midnight UTC.
 */
export function filterEventsByDateRange<T extends { start_at: string }>(
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
    const day = e.start_at.slice(0, 10);
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

export interface SessionFormValues {
  readonly start_at: string;
  readonly end_at: string;
  readonly capacity_total: string;
  readonly status: SessionStatus;
}

export interface SessionFormErrors {
  readonly start_at?: string;
  readonly end_at?: string;
  readonly capacity_total?: string;
  readonly status?: string;
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
    start_at: "",
    end_at: "",
    capacity_total: "",
    status: "draft",
  };
}

/** Populate a form from an existing session for editing. */
export function sessionToForm(s: {
  start_at: string;
  end_at: string;
  capacity_total: number;
  status: string;
}): SessionFormValues {
  return {
    start_at: toLocalDatetimeValue(s.start_at),
    end_at: toLocalDatetimeValue(s.end_at),
    capacity_total: String(s.capacity_total),
    status: isSessionStatus(s.status) ? s.status : "draft",
  };
}

export function isSessionStatus(value: string): value is SessionStatus {
  return (SESSION_STATUSES as readonly string[]).includes(value);
}

/**
 * Client-side validation mirroring the server-side guards from
 * sessions.go: start_at and end_at are required RFC3339 timestamps,
 * end_at must be strictly after start_at, capacity_total must be a
 * positive integer, and status must belong to the catalog state
 * machine. Returns an errors map keyed by field; empty map means valid.
 */
export function validateSessionForm(
  values: SessionFormValues,
): SessionFormErrors {
  const errors: { -readonly [K in keyof SessionFormErrors]?: string } = {};

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

  const capStr = values.capacity_total.trim();
  if (capStr === "") {
    errors.capacity_total = "Capacity is required.";
  } else if (!/^\d+$/.test(capStr)) {
    errors.capacity_total = "Capacity must be a whole number.";
  } else {
    const cap = Number(capStr);
    if (cap <= 0) {
      errors.capacity_total = "Capacity must be greater than zero.";
    } else if (cap > 2_000_000_000) {
      // The backend stores capacity_total as int32; refuse anything that
      // would overflow before we hit the wire.
      errors.capacity_total = "Capacity is too large.";
    }
  }

  if (!isSessionStatus(values.status)) {
    errors.status = "Status is invalid.";
  }

  return errors;
}

// ---------------------------------------------------------------------------
// Ticket-tier form helpers (feature #283; exported for unit tests)
// ---------------------------------------------------------------------------

export const TIER_PRICING_MODES = ["fixed", "free", "pwyw"] as const;
export type TierPricingMode = (typeof TIER_PRICING_MODES)[number];

export function isTierPricingMode(value: string): value is TierPricingMode {
  return (TIER_PRICING_MODES as readonly string[]).includes(value);
}

/**
 * Currency capabilities by payment provider. The intent is to surface
 * only currencies the organization can actually accept end-to-end:
 * Stripe Charges accepts ~135 currencies, and the AllPay (Israeli)
 * processor primarily clears ILS with secondary USD/EUR support. We
 * keep this map intentionally pragmatic (top-20 Stripe currencies plus
 * the AllPay set) so the dropdown is short enough to scan; legacy
 * processors are not represented here because they're not wired into
 * the platform.
 *
 * The values are ISO 4217 codes in uppercase, the same format
 * ticket_tiers.go stores in the `currency` column.
 */
export const PROVIDER_CURRENCIES: Record<string, readonly string[]> = {
  stripe: [
    "USD", "EUR", "GBP", "ILS", "CAD", "AUD", "JPY", "CHF",
    "SEK", "NOK", "DKK", "PLN", "CZK", "HUF", "BGN", "RON",
    "SGD", "HKD", "NZD", "BRL", "MXN", "ZAR", "INR", "RUB",
  ],
  allpay: ["ILS", "USD", "EUR"],
};

/**
 * Compute the currency set the organization can sell in by taking the
 * UNION of every connected provider's supported currencies. Empty
 * `providers` returns `defaultCurrencies` so the editor remains usable
 * before the first channel is connected (the form still ships the
 * value to the API, which enforces its own validation).
 */
export function allowedCurrenciesForProviders(
  providers: readonly string[],
  defaultCurrencies: readonly string[] = ["USD", "EUR", "ILS"],
): readonly string[] {
  if (providers.length === 0) {
    return defaultCurrencies;
  }
  const set = new Set<string>();
  for (const p of providers) {
    const caps = PROVIDER_CURRENCIES[p];
    if (caps === undefined) {
      continue;
    }
    for (const c of caps) {
      set.add(c);
    }
  }
  if (set.size === 0) {
    return defaultCurrencies;
  }
  return Array.from(set).sort();
}

export interface TierFormValues {
  readonly name: string;
  readonly pricing_mode: TierPricingMode;
  /** Decimal string (major units, e.g. "12.50"). Converted to cents on submit. */
  readonly price_amount: string;
  readonly currency: string;
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
  readonly currency?: string;
  readonly pwyw_min?: string;
  readonly pwyw_max?: string;
  readonly capacity?: string;
  readonly sale_window_start?: string;
  readonly sale_window_end?: string;
  readonly sort_order?: string;
}

export function emptyTierForm(defaultCurrency: string = "USD"): TierFormValues {
  return {
    name: "",
    pricing_mode: "fixed",
    price_amount: "",
    currency: defaultCurrency,
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
    currency: t.currency,
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
 * The `allowedCurrencies` argument is the org-scoped currency menu the
 * editor renders; we reject values outside that menu so an operator
 * cannot bypass the dropdown by hand-editing the DOM and ship a
 * currency the org cannot actually settle.
 */
export function validateTierForm(
  values: TierFormValues,
  allowedCurrencies: readonly string[],
): TierFormErrors {
  const errors: { -readonly [K in keyof TierFormErrors]?: string } = {};

  if (values.name.trim() === "") {
    errors.name = "Name is required.";
  } else if (values.name.length > 200) {
    errors.name = "Name must be at most 200 characters.";
  }

  if (!isTierPricingMode(values.pricing_mode)) {
    errors.pricing_mode = "Pricing mode must be fixed, free, or pwyw.";
  }

  const currency = values.currency.trim().toUpperCase();
  if (currency === "") {
    errors.currency = "Currency is required.";
  } else if (
    allowedCurrencies.length > 0 &&
    !allowedCurrencies.includes(currency)
  ) {
    errors.currency =
      "Currency is not supported by this organization's payment providers.";
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
    return [...byDate].sort((a, b) => a.start_at.localeCompare(b.start_at));
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
            <code style={monoStyle}>event.publish</code> permission. Full
            create / edit / delete will land in a later wave.
          </p>
        </div>
        <div style={refreshWrapStyle}>
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
          canPublish={canPublish}
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
              <option key={o.id} value={o.id}>
                {o.name}
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
            {ev.name}
          </button>
          <div style={mutedHintStyle}>
            {orgsByID.get(ev.org_id)?.name ?? shortenUUID(ev.org_id)}
          </div>
        </span>
      ),
    },
    {
      id: "venue",
      header: "Venue",
      renderCell: (ev) => (
        <span title={ev.venue_id ?? ""}>
          {ev.venue_id !== null ? shortenUUID(ev.venue_id) : "—"}
        </span>
      ),
    },
    {
      id: "next_session",
      header: "Next session",
      renderCell: (ev) => formatDateTime(ev.start_at),
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
  canPublish: boolean;
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
}

function EventDrawer({
  event,
  canPublish,
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
}: DrawerProps) {
  const [tab, setTab] = useState<DrawerTab>("overview");
  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="events-drawer-title"
      style={drawerBackdropStyle}
      data-testid="events-drawer"
      onClick={onClose}
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
            <OverviewTab event={event} canPublish={canPublish} />
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
  canPublish,
}: {
  event: EventItem;
  canPublish: boolean;
}) {
  const queryClient = useQueryClient();
  const [errMsg, setErrMsg] = useState<string | null>(null);
  const [okMsg, setOkMsg] = useState<string | null>(null);
  const transitions = allowedTransitions(event.status);

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
      <DetailRow label="Status">
        <EventStatusBadge status={event.status} />
      </DetailRow>
      <DetailRow label="Visibility">{event.visibility}</DetailRow>
      <DetailRow label="Organization">
        <code style={monoStyle}>{event.org_id}</code>
      </DetailRow>
      <DetailRow label="Venue">
        {event.venue_id !== null ? (
          <code style={monoStyle}>{event.venue_id}</code>
        ) : (
          <span style={mutedHintStyle}>no fixed venue</span>
        )}
      </DetailRow>
      <DetailRow label="Starts">{formatDateTime(event.start_at)}</DetailRow>
      <DetailRow label="Ends">{formatDateTime(event.end_at)}</DetailRow>
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
                <th scope="col" style={thStyle}>Capacity</th>
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
                      <td style={tdStyle}>{s.capacity_total.toLocaleString()}</td>
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
                          {!canUpdate && !canDelete ? (
                            <span style={mutedHintStyle}>read-only</span>
                          ) : null}
                        </div>
                      </td>
                    </tr>
                    {confirmDeleteID === s.id ? (
                      <tr>
                        <td colSpan={5} style={tdStyle}>
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
                        <td colSpan={5} style={tdStyle}>
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
  onSaved: (successLabel: string) => void;
  onError: (msg: string) => void;
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

  const mutation = useMutation<SessionEnvelope, ApiError, SessionFormValues>({
    mutationFn: (v) => {
      const body = {
        start_at: toRFC3339(v.start_at),
        end_at: toRFC3339(v.end_at),
        capacity_total: Number(v.capacity_total),
        status: v.status,
      };
      if (mode.kind === "create") {
        return authedFetch<SessionEnvelope>({
          method: "POST",
          path: `/v1/organizations/${event.org_id}/events/${event.id}/sessions`,
          body,
        });
      }
      return authedFetch<SessionEnvelope>({
        method: "PATCH",
        path: `/v1/organizations/${event.org_id}/events/${event.id}/sessions/${mode.session.id}`,
        body,
      });
    },
    onSuccess: (data) => {
      onSaved(
        mode.kind === "create"
          ? `Created session ${formatDateTime(data.session.start_at)}.`
          : `Updated session ${shortenUUID(data.session.id)}.`,
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
        <label style={editorFieldStyle}>
          <span style={editorLabelStyle}>Capacity</span>
          <input
            type="number"
            min={1}
            step={1}
            value={values.capacity_total}
            onChange={(e) =>
              setValues({ ...values, capacity_total: e.target.value })
            }
            style={editorInputStyle}
            required
            data-testid="events-session-input-capacity"
          />
          {errors.capacity_total !== undefined ? (
            <span style={fieldErrorStyle}>{errors.capacity_total}</span>
          ) : null}
        </label>
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
      </div>

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
// Tiers tab (feature #283 / E-5)
// ---------------------------------------------------------------------------

interface ChannelSummary {
  readonly id: string;
  readonly org_id: string;
  readonly provider: string;
}

interface ChannelListEnvelope {
  readonly channels: readonly ChannelSummary[];
}

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

  // Channels query feeds the currency-capability dropdown. We fetch
  // once per drawer open at the org scope. If the operator lacks
  // channel.read the query 403s; we degrade to the default currency
  // list rather than block the editor.
  const channelsQuery = useQuery<ChannelListEnvelope, ApiError>({
    queryKey: ["events", "detail", event.id, "org-channels"],
    queryFn: () =>
      authedFetch<ChannelListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${event.org_id}/channels`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  const allowedCurrencies = useMemo(() => {
    const providers = new Set<string>();
    for (const c of channelsQuery.data?.channels ?? []) {
      providers.add(c.provider);
    }
    return allowedCurrenciesForProviders(Array.from(providers));
  }, [channelsQuery.data]);

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
      {channelsQuery.isError && channelsQuery.error?.status !== 403 ? (
        <div style={statusBoxStyle}>
          Could not load payment channels — currency menu defaults to USD/EUR/ILS.
        </div>
      ) : null}
      {sessions.map((s) => (
        <SessionTiersBlock
          key={s.id}
          event={event}
          session={s}
          canCreate={canCreate}
          canUpdate={canUpdate}
          canDelete={canDelete}
          allowedCurrencies={allowedCurrencies}
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
  allowedCurrencies,
}: {
  event: EventItem;
  session: SessionItem;
  canCreate: boolean;
  canUpdate: boolean;
  canDelete: boolean;
  allowedCurrencies: readonly string[];
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
            {session.status} · capacity {session.capacity_total.toLocaleString()}
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
          allowedCurrencies={allowedCurrencies}
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
                <th scope="col" style={thStyle}>Sort</th>
                <th scope="col" style={thStyle}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {sortedTiers.map((t) => {
                const isEditing =
                  editor.kind === "edit" && editor.tier.id === t.id;
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
                        <td colSpan={7} style={tdStyle}>
                          <TierEditor
                            event={event}
                            session={session}
                            mode={editor}
                            allowedCurrencies={allowedCurrencies}
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

interface TierEditorProps {
  event: EventItem;
  session: SessionItem;
  mode: Exclude<TierEditorMode, { kind: "closed" }>;
  allowedCurrencies: readonly string[];
  onClose: () => void;
  onSaved: (label: string) => void;
  onError: (msg: string) => void;
}

function TierEditor({
  event,
  session,
  mode,
  allowedCurrencies,
  onClose,
  onSaved,
  onError,
}: TierEditorProps) {
  const initial =
    mode.kind === "edit"
      ? tierToForm(mode.tier)
      : emptyTierForm(allowedCurrencies[0] ?? "USD");
  const [values, setValues] = useState<TierFormValues>(initial);
  const errors = useMemo(
    () => validateTierForm(values, allowedCurrencies),
    [values, allowedCurrencies],
  );

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
          <select
            value={values.currency}
            onChange={(e) =>
              setValues({ ...values, currency: e.target.value })
            }
            style={editorInputStyle}
            data-testid="events-tier-input-currency"
          >
            {allowedCurrencies.map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </select>
          {errors.currency !== undefined ? (
            <span style={fieldErrorStyle}>{errors.currency}</span>
          ) : null}
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
  const body: Record<string, unknown> = {
    name: v.name.trim(),
    pricing_mode: v.pricing_mode,
    currency: v.currency.trim().toUpperCase(),
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
  return { feed_token_id: "", city_id: "" };
}

export interface PublicationFormErrors {
  feed_token_id?: string;
  city_id?: string;
}

export function validatePublicationForm(
  v: PublicationFormValues,
): PublicationFormErrors {
  const errors: PublicationFormErrors = {};
  const feed = v.feed_token_id.trim();
  if (feed === "") {
    errors.feed_token_id = "Feed token ID is required.";
  } else if (!isUUID(feed)) {
    errors.feed_token_id = "Feed token ID must be a UUID.";
  }
  const city = v.city_id.trim();
  if (city !== "" && !isUUID(city)) {
    errors.city_id = "City ID must be a UUID.";
  }
  return errors;
}

export function buildPublicationRequestBody(
  v: PublicationFormValues,
): PublicationRequestBody {
  const body: PublicationRequestBody = {
    feed_token_id: v.feed_token_id.trim(),
  };
  const city = v.city_id.trim();
  if (city !== "") {
    body.city_id = city;
  }
  return body;
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
    case "publication.internal":
      return "The server failed to apply the publication change. Try again.";
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

  const invalidate = () =>
    queryClient.invalidateQueries({
      queryKey: ["events", "detail", event.id, "publications"],
    });

  const publishMutation = useMutation<
    EventPublication,
    ApiError,
    PublicationRequestBody
  >({
    mutationFn: (body) =>
      authedFetch<EventPublication>({
        method: "POST",
        path: `/v1/events/${event.id}/publications`,
        body,
      }),
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

  const onSubmit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const errs = validatePublicationForm(form);
    setFormErrors(errs);
    if (Object.keys(errs).length > 0) {
      return;
    }
    setActionErr(null);
    setOkMsg(null);
    publishMutation.mutate(buildPublicationRequestBody(form));
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
            <label style={editorFieldStyle}>
              <span style={editorLabelStyle}>Feed token ID</span>
              <input
                type="text"
                value={form.feed_token_id}
                onChange={(e) => {
                  setForm({ ...form, feed_token_id: e.target.value });
                  if (formErrors.feed_token_id !== undefined) {
                    setFormErrors({ ...formErrors, feed_token_id: undefined });
                  }
                }}
                style={editorInputStyle}
                placeholder="00000000-0000-0000-0000-000000000000"
                data-testid="events-publications-feed-token-id"
                spellCheck={false}
                autoComplete="off"
              />
              {formErrors.feed_token_id !== undefined ? (
                <span style={fieldErrorStyle}>{formErrors.feed_token_id}</span>
              ) : null}
            </label>
            <label style={editorFieldStyle}>
              <span style={editorLabelStyle}>City scope (optional)</span>
              <select
                value={form.city_id}
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
