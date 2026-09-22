/**
 * Promo codes admin screen — org-scoped discount vouchers.
 *
 * Backed by apps/backend/internal/platform/httpserver/hpromo (see
 * openapi.yaml `PromoCodeItem` / `CreatePromoCodeRequest` /
 * `PromoRedemptionItem`):
 *
 *   GET    /v1/organizations/{org_id}/promo-codes
 *   POST   /v1/organizations/{org_id}/promo-codes
 *   PATCH  /v1/organizations/{org_id}/promo-codes/{id}
 *   DELETE /v1/organizations/{org_id}/promo-codes/{id}
 *   GET    /v1/organizations/{org_id}/promo-code-redemptions[?promo_code_id=&format=csv]
 *
 * The "applies to sessions" picker reuses the events/sessions/venues APIs
 * the Events screen already wires:
 *
 *   GET /v1/organizations/{org_id}/events
 *   GET /v1/organizations/{org_id}/events/{event_id}/sessions
 *   GET /v1/organizations/{org_id}/venues
 *
 * The route takes org_id from the URL (`/organizations/$orgId/promo-codes`)
 * rather than a picker — the screen is reached from an org context (the
 * session overview's "Manage promo codes" link, or a direct URL), never
 * from a bare top-level nav click with no org selected.
 *
 * CSV downloads (both the all-codes and the per-code usage report) go
 * through an authenticated fetch → Blob → object-URL anchor click, because
 * the endpoint requires the Authorization (and, per SAUI-04, sometimes
 * X-Admin-Reason) header a plain <a href> cannot carry.
 *
 * Mock data: NONE. Everything here hits the live backend.
 */
import { createRoute, Link, useParams } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Fragment, useState, type CSSProperties, type FormEvent } from "react";
import { Route as RootRoute } from "./__root";
import { ApiError, authedFetch } from "@/lib/api/client";
import { getAccessToken } from "@/lib/api/tokenStore";
import {
  getActiveReason,
  requiresAdminReason,
  resolveReasonFor,
} from "@/lib/api/reason";
import { config } from "@/lib/config";
import { RequirePermission } from "@/components/RequirePermission";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";
import { formatDateTime, formatMoneyMinor } from "@/lib/admin/supportConsole";
import { decimalToCents, parseLocalDatetime } from "@/routes/events";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/organizations/$orgId/promo-codes",
  component: PromoCodesRoute,
});

// ---------------------------------------------------------------------------
// Wire types (mirror openapi.yaml PromoCodeItem / PromoRedemptionItem)
// ---------------------------------------------------------------------------

export type PromoDiscountType = "percent" | "fixed_amount";
export type PromoStatus = "active" | "paused";

export interface PromoCode {
  readonly id: string;
  readonly org_id: string;
  readonly code: string;
  readonly discount_type: PromoDiscountType;
  readonly discount_value: number;
  readonly currency: string | null;
  readonly applies_to_tier_ids: readonly string[];
  readonly applies_to_session_ids: readonly string[];
  readonly uses: number;
  readonly discount_total: number;
  readonly last_used_at: string | null;
  readonly max_uses: number | null;
  readonly max_uses_per_customer: number | null;
  readonly valid_from: string | null;
  readonly valid_until: string | null;
  readonly min_order_amount: number;
  readonly status: PromoStatus;
  readonly created_at: string;
  readonly updated_at: string;
}

interface PromoCodeListEnvelope {
  readonly promo_codes: readonly PromoCode[];
}

interface PromoCodeEnvelope {
  readonly promo_code: PromoCode;
}

interface PromoCodeDeleteEnvelope {
  readonly promo_code: PromoCode;
  readonly deleted: true;
}

export interface PromoRedemption {
  readonly id: string;
  readonly promo_code_id: string;
  readonly code: string;
  readonly redeemed_at: string;
  readonly discount_amount: number;
  readonly order_amount: number;
  readonly order_id: string | null;
  readonly order_number: number | null;
  readonly order_status: string | null;
  readonly currency: string | null;
  readonly buyer_email: string | null;
  readonly session_id: string | null;
  readonly channel_id: string | null;
  readonly channel_name: string | null;
}

interface PromoRedemptionListEnvelope {
  readonly redemptions: readonly PromoRedemption[];
}

interface OrgEventItem {
  readonly id: string;
  readonly name: string;
}

interface OrgEventListEnvelope {
  readonly events: readonly OrgEventItem[];
}

interface OrgSessionItem {
  readonly id: string;
  readonly event_id: string;
  readonly venue_id: string;
  readonly start_at: string;
}

interface OrgSessionListEnvelope {
  readonly sessions: readonly OrgSessionItem[];
}

interface VenueSummary {
  readonly id: string;
  readonly name: string;
}

interface VenueListEnvelope {
  readonly venues: readonly VenueSummary[];
}

// ---------------------------------------------------------------------------
// Pure helpers (exported for unit tests)
// ---------------------------------------------------------------------------

/** Deep link into this screen from an org context (session overview etc). */
export function promoCodesLink(orgId: string): string {
  return `/organizations/${encodeURIComponent(orgId)}/promo-codes`;
}

/** "10 %" for a percent code, "50.00 CZK" for a fixed-amount one. */
export function discountLabel(code: {
  discount_type: PromoDiscountType;
  discount_value: number;
  currency: string | null;
}): string {
  if (code.discount_type === "percent") {
    return `${code.discount_value} %`;
  }
  return formatMoneyMinor(code.discount_value, code.currency);
}

/**
 * "1350.00 CZK" for a code with a currency, "450.00" (two decimals, no
 * currency suffix) for a percent code, "—" when discount_total is missing.
 * Mirrors formatMoneyMinor's minor-unit conversion for the currency case;
 * a percent code has no currency to report so it prints the bare amount.
 */
export function discountTotalCellLabel(
  discountTotal: number | null | undefined,
  currency: string | null | undefined,
): string {
  if (
    discountTotal === null ||
    discountTotal === undefined ||
    !Number.isFinite(discountTotal)
  ) {
    return "—";
  }
  if (currency !== null && currency !== undefined && currency.trim() !== "") {
    return formatMoneyMinor(discountTotal, currency);
  }
  return (discountTotal / 100).toFixed(2);
}

/** "2026-06-01 00:00Z – 2026-08-31 23:59Z" / "always" / one-sided ranges. */
export function validityLabel(
  validFrom: string | null,
  validUntil: string | null,
): string {
  if (validFrom === null && validUntil === null) {
    return "always";
  }
  const from = validFrom !== null ? formatDateTime(validFrom) : "any time";
  const until = validUntil !== null ? formatDateTime(validUntil) : "no end";
  return `${from} – ${until}`;
}

/** "5" or "∞" for a usage-cap field. */
export function limitLabel(value: number | null): string {
  return value === null ? "∞" : String(value);
}

/** "any" or "N sessions" for the table's Sessions column. */
export function sessionsCellLabel(sessionIds: readonly string[]): string {
  if (sessionIds.length === 0) {
    return "any";
  }
  return `${sessionIds.length} session${sessionIds.length === 1 ? "" : "s"}`;
}

/** Hover title listing the session names for the Sessions column. */
export function sessionsCellTitle(
  sessionIds: readonly string[],
  labelsById: ReadonlyMap<string, string>,
): string {
  if (sessionIds.length === 0) {
    return "Applies to any session of the organization.";
  }
  return sessionIds.map((id) => labelsById.get(id) ?? id).join("\n");
}

export interface SessionOption {
  readonly id: string;
  readonly label: string;
}

/** "Event name — 2026-10-13 17:00Z (Venue)", sorted for a stable picker. */
export function buildSessionOptions(
  events: readonly OrgEventItem[],
  sessions: readonly OrgSessionItem[],
  venues: readonly VenueSummary[],
): readonly SessionOption[] {
  const eventNameById = new Map(events.map((e) => [e.id, e.name] as const));
  const venueNameById = new Map(venues.map((v) => [v.id, v.name] as const));
  const options = sessions.map((s) => {
    const eventName = eventNameById.get(s.event_id) ?? "Unknown event";
    const venueName = venueNameById.get(s.venue_id) ?? "Unknown venue";
    return {
      id: s.id,
      label: `${eventName} — ${formatDateTime(s.start_at)} (${venueName})`,
    };
  });
  return [...options].sort((a, b) => a.label.localeCompare(b.label));
}

/** `<input type="datetime-local">` -> RFC3339 UTC, or null when blank. */
export function localDatetimeToRFC3339(value: string): string | null {
  const trimmed = value.trim();
  if (trimmed === "") {
    return null;
  }
  return `${trimmed}:00Z`;
}

/** RFC3339 -> `<input type="datetime-local">` value, "" when null/invalid. */
export function rfc3339ToLocalDatetime(iso: string | null): string {
  if (iso === null) {
    return "";
  }
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

export interface PromoFormValues {
  readonly code: string;
  readonly discount_type: PromoDiscountType;
  /** Integer string (percent) or decimal string (fixed_amount major units). */
  readonly value: string;
  readonly currency: string;
  readonly session_ids: readonly string[];
  readonly max_uses: string;
  readonly max_uses_per_customer: string;
  readonly valid_from: string;
  readonly valid_until: string;
  readonly status: PromoStatus;
}

export function emptyPromoForm(): PromoFormValues {
  return {
    code: "",
    discount_type: "percent",
    value: "",
    currency: "",
    session_ids: [],
    max_uses: "",
    max_uses_per_customer: "",
    valid_from: "",
    valid_until: "",
    status: "active",
  };
}

export interface PromoFormErrors {
  code?: string;
  value?: string;
  currency?: string;
  max_uses?: string;
  max_uses_per_customer?: string;
  valid_from?: string;
  valid_until?: string;
}

/** Positive-or-blank integer field (max_uses / max_uses_per_customer). */
function validatePositiveIntOrBlank(raw: string): string | undefined {
  const trimmed = raw.trim();
  if (trimmed === "") {
    return undefined;
  }
  if (!/^\d+$/.test(trimmed) || Number(trimmed) <= 0) {
    return "Must be a whole number greater than zero, or left blank for unlimited.";
  }
  return undefined;
}

/**
 * Client-side validation mirroring CreatePromoCodeRequest's server rules:
 * code required, discount_value > 0 (percent 1-100), currency required for
 * fixed_amount, valid_until (if set) after valid_from (when both set).
 */
export function validatePromoForm(v: PromoFormValues): PromoFormErrors {
  const errors: PromoFormErrors = {};

  if (v.code.trim() === "") {
    errors.code = "Code is required.";
  }

  if (v.discount_type === "percent") {
    const trimmed = v.value.trim();
    if (trimmed === "" || !/^\d+$/.test(trimmed)) {
      errors.value = "Percent must be a whole number between 1 and 100.";
    } else {
      const n = Number(trimmed);
      if (n < 1 || n > 100) {
        errors.value = "Percent must be between 1 and 100.";
      }
    }
  } else {
    const cents = decimalToCents(v.value);
    if (cents === null || cents <= 0) {
      errors.value = "Amount must be a decimal greater than zero (e.g. 12.50).";
    }
    if (v.currency.trim() === "") {
      errors.currency = "Currency is required for a fixed-amount code.";
    } else if (!/^[A-Za-z]{3}$/.test(v.currency.trim())) {
      errors.currency = "Currency must be a 3-letter ISO 4217 code (e.g. EUR).";
    }
  }

  const maxUsesErr = validatePositiveIntOrBlank(v.max_uses);
  if (maxUsesErr !== undefined) {
    errors.max_uses = maxUsesErr;
  }
  const maxPerCustomerErr = validatePositiveIntOrBlank(v.max_uses_per_customer);
  if (maxPerCustomerErr !== undefined) {
    errors.max_uses_per_customer = maxPerCustomerErr;
  }

  const from = v.valid_from.trim() === "" ? null : parseLocalDatetime(v.valid_from);
  if (v.valid_from.trim() !== "" && from === null) {
    errors.valid_from = "Valid-from must be a valid date/time.";
  }
  const until = v.valid_until.trim() === "" ? null : parseLocalDatetime(v.valid_until);
  if (v.valid_until.trim() !== "" && until === null) {
    errors.valid_until = "Valid-until must be a valid date/time.";
  }
  if (from !== null && until !== null && until.getTime() <= from.getTime()) {
    errors.valid_until = "Valid-until must be after valid-from.";
  }

  return errors;
}

/**
 * Builds the CreatePromoCodeRequest body. tier ids are always sent empty
 * (this screen has no tier picker) and min_order_amount is always 0 (no
 * form field for it) — both match the Goal's "send []" / "send 0" contract.
 */
export function buildCreatePromoCodeBody(v: PromoFormValues): Record<string, unknown> {
  const value =
    v.discount_type === "percent" ? Number(v.value.trim()) : decimalToCents(v.value)!;
  const body: Record<string, unknown> = {
    code: v.code.trim(),
    discount_type: v.discount_type,
    discount_value: value,
    applies_to_tier_ids: [],
    applies_to_session_ids: [...v.session_ids],
    max_uses: v.max_uses.trim() === "" ? null : Number(v.max_uses.trim()),
    max_uses_per_customer:
      v.max_uses_per_customer.trim() === "" ? null : Number(v.max_uses_per_customer.trim()),
    valid_from: localDatetimeToRFC3339(v.valid_from),
    valid_until: localDatetimeToRFC3339(v.valid_until),
    min_order_amount: 0,
    status: v.status,
  };
  if (v.discount_type === "fixed_amount") {
    body.currency = v.currency.trim().toUpperCase();
  }
  return body;
}

/** PATCH body toggling a code between active and paused. */
export function buildStatusPatchBody(code: PromoCode): { status: PromoStatus } {
  return { status: code.status === "active" ? "paused" : "active" };
}

/** RFC 4180 field escaping — unused for the server CSV, handy for filenames. */
function sanitizeForFilename(raw: string): string {
  return raw.replace(/[^a-zA-Z0-9_-]+/g, "-").replace(/^-+|-+$/g, "");
}

/** Download filename for the redemptions CSV, scoped or org-wide. */
export function redemptionsCsvFilename(code?: string | null): string {
  const stamp = new Date().toISOString().slice(0, 10);
  if (code === undefined || code === null || code.trim() === "") {
    return `promo-codes-redemptions-${stamp}.csv`;
  }
  return `promo-code-${sanitizeForFilename(code)}-redemptions-${stamp}.csv`;
}

/** Translate an ApiError from a promo-code endpoint into readable text. */
export function mapPromoError(err: ApiError): string {
  switch (err.code) {
    case "promo.invalid_code":
      return "Code is required.";
    case "promo.invalid_discount_type":
      return "Discount type must be percent or fixed amount.";
    case "promo.invalid_discount_value":
      return "Discount value is invalid.";
    case "promo.invalid_currency":
      return "Currency must be a 3-letter ISO 4217 code.";
    case "promo.currency_required":
      return "Currency is required for a fixed-amount code.";
    case "promo.invalid_session_id":
      return "One of the selected sessions is invalid.";
    case "promo.invalid_session":
      return "One of the selected sessions does not belong to this organization.";
    case "promo.invalid_valid_from":
      return "Valid-from must be a valid RFC3339 timestamp.";
    case "promo.invalid_valid_until":
      return "Valid-until must be a valid RFC3339 timestamp.";
    case "promo.duplicate":
      return "A promo code with this code already exists in this organization.";
    case "permissions.denied":
      return "Your account is missing the required permission.";
    default:
      if (err.status === 401) {
        return "Session expired. Please sign in again.";
      }
      if (err.status === 403) {
        return "Forbidden — missing the required promo permission.";
      }
      return `${err.message} (${err.code})`;
  }
}

// ---------------------------------------------------------------------------
// Authenticated CSV download (Blob, not a plain href — needs auth headers)
// ---------------------------------------------------------------------------

async function fetchRedemptionsCsvBlob(
  orgId: string,
  promoCodeId: string | null,
): Promise<Blob> {
  const params = new URLSearchParams({ format: "csv" });
  if (promoCodeId !== null) {
    params.set("promo_code_id", promoCodeId);
  }
  const path = `/v1/organizations/${orgId}/promo-code-redemptions?${params.toString()}`;
  const headers: Record<string, string> = {};
  const token = getAccessToken();
  if (token !== null) {
    headers.Authorization = `Bearer ${token}`;
  }
  if (requiresAdminReason(path, "GET")) {
    const cached = getActiveReason();
    const reason = cached !== null ? cached : await resolveReasonFor(path);
    headers["X-Admin-Reason"] = reason;
  }
  const res = await fetch(`${config.apiBaseUrl}${path}`, {
    method: "GET",
    headers,
    credentials: "omit",
  });
  if (!res.ok) {
    let message = `HTTP ${res.status}`;
    try {
      const body: unknown = await res.json();
      if (
        body !== null &&
        typeof body === "object" &&
        "error" in body &&
        typeof (body as { error: unknown }).error === "object" &&
        (body as { error: { message?: unknown } }).error !== null
      ) {
        const em = (body as { error: { message?: unknown } }).error.message;
        if (typeof em === "string" && em !== "") {
          message = em;
        }
      }
    } catch {
      // non-JSON error body — keep the generic HTTP status message
    }
    throw new Error(message);
  }
  return res.blob();
}

function triggerBlobDownload(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}

// ---------------------------------------------------------------------------
// Nav entry binding
// ---------------------------------------------------------------------------

const PROMO_CODES_NAV_ENTRY = NAV_BY_PATH["/organizations/$orgId/promo-codes"];
if (PROMO_CODES_NAV_ENTRY === undefined) {
  throw new Error(
    "promoCodes route: NAV_BY_PATH['/organizations/$orgId/promo-codes'] missing",
  );
}

// ---------------------------------------------------------------------------
// Page shell
// ---------------------------------------------------------------------------

function PromoCodesRoute() {
  const { orgId } = useParams({ from: "/organizations/$orgId/promo-codes" });
  return (
    <RequirePermission entry={PROMO_CODES_NAV_ENTRY}>
      <PromoCodesModule orgId={orgId} />
    </RequirePermission>
  );
}

function PromoCodesModule({ orgId }: { orgId: string }) {
  const queryClient = useQueryClient();

  const listQuery = useQuery<PromoCodeListEnvelope, ApiError>({
    queryKey: ["promo-codes", "list", orgId],
    queryFn: () =>
      authedFetch<PromoCodeListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${orgId}/promo-codes`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  const eventsQuery = useQuery<OrgEventListEnvelope, ApiError>({
    queryKey: ["promo-codes", "events", orgId],
    queryFn: () =>
      authedFetch<OrgEventListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${orgId}/events`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  const venuesQuery = useQuery<VenueListEnvelope, ApiError>({
    queryKey: ["promo-codes", "venues", orgId],
    queryFn: () =>
      authedFetch<VenueListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${orgId}/venues`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  const events = eventsQuery.data?.events ?? [];
  const sessionsQuery = useQuery<OrgSessionListEnvelope, ApiError>({
    queryKey: ["promo-codes", "sessions", orgId, events.map((e) => e.id).join(",")],
    enabled: eventsQuery.isSuccess,
    queryFn: async () => {
      const perEvent = await Promise.all(
        events.map((e) =>
          authedFetch<OrgSessionListEnvelope>({
            method: "GET",
            path: `/v1/organizations/${orgId}/events/${e.id}/sessions`,
          }),
        ),
      );
      return { sessions: perEvent.flatMap((r) => r.sessions) };
    },
    retry: false,
    refetchOnWindowFocus: false,
  });

  const sessionOptions = buildSessionOptions(
    events,
    sessionsQuery.data?.sessions ?? [],
    venuesQuery.data?.venues ?? [],
  );
  const sessionLabelsById = new Map(sessionOptions.map((o) => [o.id, o.label] as const));

  const [formOpen, setFormOpen] = useState(false);
  const [form, setForm] = useState<PromoFormValues>(emptyPromoForm());
  const formErrors = validatePromoForm(form);

  const createMutation = useMutation<PromoCodeEnvelope, ApiError, Record<string, unknown>>({
    mutationFn: (body) =>
      authedFetch<PromoCodeEnvelope>({
        method: "POST",
        path: `/v1/organizations/${orgId}/promo-codes`,
        body,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["promo-codes", "list", orgId] });
      setFormOpen(false);
      setForm(emptyPromoForm());
    },
  });

  const statusMutation = useMutation<
    PromoCodeEnvelope,
    ApiError,
    { id: string; body: { status: PromoStatus } }
  >({
    mutationFn: ({ id, body }) =>
      authedFetch<PromoCodeEnvelope>({
        method: "PATCH",
        path: `/v1/organizations/${orgId}/promo-codes/${id}`,
        body,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["promo-codes", "list", orgId] });
    },
  });

  const deleteMutation = useMutation<PromoCodeDeleteEnvelope, ApiError, string>({
    mutationFn: (id) =>
      authedFetch<PromoCodeDeleteEnvelope>({
        method: "DELETE",
        path: `/v1/organizations/${orgId}/promo-codes/${id}`,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["promo-codes", "list", orgId] });
    },
  });

  const [expandedId, setExpandedId] = useState<string | null>(null);
  const redemptionsQuery = useQuery<PromoRedemptionListEnvelope, ApiError>({
    queryKey: ["promo-codes", "redemptions", orgId, expandedId],
    enabled: expandedId !== null,
    queryFn: () =>
      authedFetch<PromoRedemptionListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${orgId}/promo-code-redemptions?promo_code_id=${expandedId}`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  const [csvBusy, setCsvBusy] = useState<string | null>(null); // "*" for the all-codes button, or a code id
  const [csvError, setCsvError] = useState<string | null>(null);

  async function downloadCsv(promoCodeId: string | null, code: string | null): Promise<void> {
    const busyKey = promoCodeId ?? "*";
    setCsvBusy(busyKey);
    setCsvError(null);
    try {
      const blob = await fetchRedemptionsCsvBlob(orgId, promoCodeId);
      triggerBlobDownload(blob, redemptionsCsvFilename(code));
    } catch (err) {
      setCsvError(err instanceof Error ? err.message : "Could not download the CSV.");
    } finally {
      setCsvBusy(null);
    }
  }

  const rows = listQuery.data?.promo_codes ?? [];

  return (
    <section aria-labelledby="promo-codes-heading" style={pageStyle}>
      <div style={{ marginBottom: 8, fontSize: 13 }}>
        <Link to="/events" data-testid="promo-codes-back">
          ← Events and sessions
        </Link>
      </div>

      <header style={headerStyle}>
        <div>
          <h1 id="promo-codes-heading" style={headingStyle}>
            Promo codes
          </h1>
          <p style={subheadingStyle}>
            Organization <code style={monoStyle}>{orgId}</code>. Discount
            vouchers redeemable at checkout, optionally restricted to
            specific sessions.
          </p>
        </div>
        <div style={refreshWrapStyle}>
          <button
            type="button"
            onClick={() => void downloadCsv(null, null)}
            style={refreshButtonStyle}
            disabled={csvBusy !== null}
            data-testid="promo-codes-download-all-csv"
          >
            {csvBusy === "*" ? "Downloading…" : "Download CSV"}
          </button>
          <button
            type="button"
            onClick={() => setFormOpen((v) => !v)}
            style={primaryButtonStyle}
            data-testid="promo-codes-new-toggle"
          >
            {formOpen ? "Close" : "New promo code"}
          </button>
          <button
            type="button"
            onClick={() => listQuery.refetch()}
            style={refreshButtonStyle}
            disabled={listQuery.isFetching}
            data-testid="promo-codes-refresh"
          >
            {listQuery.isFetching ? "Refreshing…" : "Refresh"}
          </button>
        </div>
      </header>

      {csvError !== null ? (
        <div style={errorBoxStyle} role="alert" data-testid="promo-codes-csv-error">
          {csvError}
        </div>
      ) : null}

      {formOpen ? (
        <PromoCodeForm
          values={form}
          errors={formErrors}
          sessionOptions={sessionOptions}
          sessionsLoading={eventsQuery.isPending || sessionsQuery.isFetching}
          onChange={setForm}
          onCancel={() => {
            setFormOpen(false);
            setForm(emptyPromoForm());
          }}
          onSubmit={() => {
            if (Object.keys(formErrors).length > 0) {
              return;
            }
            createMutation.mutate(buildCreatePromoCodeBody(form));
          }}
          submitting={createMutation.isPending}
          submitError={
            createMutation.isError ? mapPromoError(createMutation.error) : null
          }
        />
      ) : null}

      {listQuery.isPending ? (
        <div style={statusBoxStyle} role="status" aria-live="polite">
          Loading promo codes…
        </div>
      ) : listQuery.isError ? (
        <ListErrorState error={listQuery.error} onRetry={() => listQuery.refetch()} />
      ) : (
        <PromoCodesTable
          codes={rows}
          sessionLabelsById={sessionLabelsById}
          expandedId={expandedId}
          onToggleUsage={(id) => setExpandedId((cur) => (cur === id ? null : id))}
          onPauseToggle={(code) =>
            statusMutation.mutate({ id: code.id, body: buildStatusPatchBody(code) })
          }
          onDelete={(code) => {
            if (
              window.confirm(
                `Delete promo code "${code.code}"? This cannot be undone.`,
              )
            ) {
              deleteMutation.mutate(code.id);
            }
          }}
          onDownloadCodeCsv={(code) => void downloadCsv(code.id, code.code)}
          csvBusyId={csvBusy}
          redemptions={redemptionsQuery.data?.redemptions ?? []}
          redemptionsLoading={redemptionsQuery.isFetching}
          redemptionsError={
            redemptionsQuery.isError ? redemptionsQuery.error.message : null
          }
        />
      )}
    </section>
  );
}

function ListErrorState({
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
      <div style={errorBoxStyle} role="alert" data-testid="promo-codes-forbidden">
        <strong>Forbidden.</strong>
        <p style={errorParaStyle}>
          Your account is missing <code style={monoStyle}>promo.read</code>.
        </p>
      </div>
    );
  }
  return (
    <div style={errorBoxStyle} role="alert" data-testid="promo-codes-error">
      <strong>Failed to load promo codes.</strong>
      <div style={errorCodeStyle}>{error?.code ?? "unknown.error"}</div>
      {error?.message ? <div style={errorParaStyle}>{error.message}</div> : null}
      <button type="button" style={errorRetryStyle} onClick={onRetry}>
        Retry
      </button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Create form
// ---------------------------------------------------------------------------

interface PromoCodeFormProps {
  readonly values: PromoFormValues;
  readonly errors: PromoFormErrors;
  readonly sessionOptions: readonly SessionOption[];
  readonly sessionsLoading: boolean;
  readonly onChange: (v: PromoFormValues) => void;
  readonly onCancel: () => void;
  readonly onSubmit: () => void;
  readonly submitting: boolean;
  readonly submitError: string | null;
}

function PromoCodeForm({
  values,
  errors,
  sessionOptions,
  sessionsLoading,
  onChange,
  onCancel,
  onSubmit,
  submitting,
  submitError,
}: PromoCodeFormProps) {
  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    onSubmit();
  }

  function toggleSession(id: string) {
    const has = values.session_ids.includes(id);
    onChange({
      ...values,
      session_ids: has
        ? values.session_ids.filter((s) => s !== id)
        : [...values.session_ids, id],
    });
  }

  return (
    <form onSubmit={submit} style={formStyle} noValidate data-testid="promo-codes-form">
      <div style={formGridStyle}>
        <label style={fieldGroupStyle}>
          <span style={fieldLabelStyle}>Code</span>
          <input
            type="text"
            value={values.code}
            onChange={(e) => onChange({ ...values, code: e.target.value })}
            style={inputStyle}
            data-testid="promo-codes-field-code"
          />
          {errors.code !== undefined ? (
            <span style={fieldErrorStyle}>{errors.code}</span>
          ) : null}
        </label>

        <label style={fieldGroupStyle}>
          <span style={fieldLabelStyle}>Type</span>
          <select
            value={values.discount_type}
            onChange={(e) =>
              onChange({
                ...values,
                discount_type: e.target.value as PromoDiscountType,
              })
            }
            style={inputStyle}
            data-testid="promo-codes-field-discount-type"
          >
            <option value="percent">Percent</option>
            <option value="fixed_amount">Fixed amount</option>
          </select>
        </label>

        <label style={fieldGroupStyle}>
          <span style={fieldLabelStyle}>
            {values.discount_type === "percent" ? "Value (%)" : "Value (decimal)"}
          </span>
          <input
            type="text"
            inputMode="decimal"
            value={values.value}
            onChange={(e) => onChange({ ...values, value: e.target.value })}
            placeholder={values.discount_type === "percent" ? "25" : "12.50"}
            style={inputStyle}
            data-testid="promo-codes-field-value"
          />
          {errors.value !== undefined ? (
            <span style={fieldErrorStyle}>{errors.value}</span>
          ) : null}
        </label>

        {values.discount_type === "fixed_amount" ? (
          <label style={fieldGroupStyle}>
            <span style={fieldLabelStyle}>Currency</span>
            <input
              type="text"
              value={values.currency}
              onChange={(e) => onChange({ ...values, currency: e.target.value })}
              placeholder="EUR"
              maxLength={3}
              style={inputStyle}
              data-testid="promo-codes-field-currency"
            />
            {errors.currency !== undefined ? (
              <span style={fieldErrorStyle}>{errors.currency}</span>
            ) : null}
          </label>
        ) : null}

        <label style={fieldGroupStyle}>
          <span style={fieldLabelStyle}>Max uses</span>
          <input
            type="text"
            inputMode="numeric"
            value={values.max_uses}
            onChange={(e) => onChange({ ...values, max_uses: e.target.value })}
            placeholder="unlimited"
            style={inputStyle}
            data-testid="promo-codes-field-max-uses"
          />
          {errors.max_uses !== undefined ? (
            <span style={fieldErrorStyle}>{errors.max_uses}</span>
          ) : null}
        </label>

        <label style={fieldGroupStyle}>
          <span style={fieldLabelStyle}>Max uses per customer</span>
          <input
            type="text"
            inputMode="numeric"
            value={values.max_uses_per_customer}
            onChange={(e) =>
              onChange({ ...values, max_uses_per_customer: e.target.value })
            }
            placeholder="unlimited"
            style={inputStyle}
            data-testid="promo-codes-field-max-uses-per-customer"
          />
          {errors.max_uses_per_customer !== undefined ? (
            <span style={fieldErrorStyle}>{errors.max_uses_per_customer}</span>
          ) : null}
        </label>

        <label style={fieldGroupStyle}>
          <span style={fieldLabelStyle}>Valid from (UTC)</span>
          <input
            type="datetime-local"
            value={values.valid_from}
            onChange={(e) => onChange({ ...values, valid_from: e.target.value })}
            style={inputStyle}
            data-testid="promo-codes-field-valid-from"
          />
          {errors.valid_from !== undefined ? (
            <span style={fieldErrorStyle}>{errors.valid_from}</span>
          ) : null}
        </label>

        <label style={fieldGroupStyle}>
          <span style={fieldLabelStyle}>Valid until (UTC)</span>
          <input
            type="datetime-local"
            value={values.valid_until}
            onChange={(e) => onChange({ ...values, valid_until: e.target.value })}
            style={inputStyle}
            data-testid="promo-codes-field-valid-until"
          />
          {errors.valid_until !== undefined ? (
            <span style={fieldErrorStyle}>{errors.valid_until}</span>
          ) : null}
        </label>

        <label style={fieldGroupStyle}>
          <span style={fieldLabelStyle}>Status</span>
          <select
            value={values.status}
            onChange={(e) =>
              onChange({ ...values, status: e.target.value as PromoStatus })
            }
            style={inputStyle}
            data-testid="promo-codes-field-status"
          >
            <option value="active">Active</option>
            <option value="paused">Paused</option>
          </select>
        </label>
      </div>

      <div style={fieldGroupStyle}>
        <span style={fieldLabelStyle}>Sessions</span>
        {sessionsLoading ? (
          <p style={mutedStyle}>Loading sessions…</p>
        ) : sessionOptions.length === 0 ? (
          <p style={mutedStyle}>
            No sessions found for this organization — the code will apply to
            any session.
          </p>
        ) : (
          <div style={sessionPickerStyle} data-testid="promo-codes-field-sessions">
            {sessionOptions.map((opt) => (
              <label key={opt.id} style={sessionOptionStyle}>
                <input
                  type="checkbox"
                  checked={values.session_ids.includes(opt.id)}
                  onChange={() => toggleSession(opt.id)}
                  data-testid={`promo-codes-session-checkbox-${opt.id}`}
                />
                <span>{opt.label}</span>
              </label>
            ))}
          </div>
        )}
        <span style={fieldHintStyle}>
          Leave every box unchecked to apply the code to any session.
        </span>
      </div>

      {submitError !== null ? (
        <div style={fieldErrorStyle} data-testid="promo-codes-form-error">
          {submitError}
        </div>
      ) : null}

      <div style={formButtonsStyle}>
        <button
          type="submit"
          style={primaryButtonStyle}
          disabled={submitting}
          data-testid="promo-codes-form-submit"
        >
          {submitting ? "Creating…" : "Create promo code"}
        </button>
        <button
          type="button"
          style={secondaryButtonStyle}
          onClick={onCancel}
          disabled={submitting}
          data-testid="promo-codes-form-cancel"
        >
          Cancel
        </button>
      </div>
    </form>
  );
}

// ---------------------------------------------------------------------------
// Table (presentational — router/query-free so it renders with
// renderToStaticMarkup in tests, mirroring SessionOverviewBody)
// ---------------------------------------------------------------------------

export interface PromoCodesTableProps {
  readonly codes: readonly PromoCode[];
  readonly sessionLabelsById: ReadonlyMap<string, string>;
  readonly expandedId: string | null;
  readonly onToggleUsage: (id: string) => void;
  readonly onPauseToggle: (code: PromoCode) => void;
  readonly onDelete: (code: PromoCode) => void;
  readonly onDownloadCodeCsv: (code: PromoCode) => void;
  readonly csvBusyId: string | null;
  readonly redemptions: readonly PromoRedemption[];
  readonly redemptionsLoading: boolean;
  readonly redemptionsError: string | null;
}

export function PromoCodesTable({
  codes,
  sessionLabelsById,
  expandedId,
  onToggleUsage,
  onPauseToggle,
  onDelete,
  onDownloadCodeCsv,
  csvBusyId,
  redemptions,
  redemptionsLoading,
  redemptionsError,
}: PromoCodesTableProps): JSX.Element {
  if (codes.length === 0) {
    return (
      <div style={statusBoxStyle} role="status" data-testid="promo-codes-empty">
        No promo codes yet.
      </div>
    );
  }
  return (
    <div style={tableWrapStyle} role="region" aria-label="Promo codes">
      <table style={tableStyle} data-testid="promo-codes-table">
        <thead>
          <tr>
            <th scope="col" style={thStyle}>Code</th>
            <th scope="col" style={thStyle}>Discount</th>
            <th scope="col" style={thStyle}>Sessions</th>
            <th scope="col" style={thStyle}>Limits</th>
            <th scope="col" style={thStyle}>Valid</th>
            <th scope="col" style={thStyle}>Status</th>
            <th scope="col" style={thNumStyle}>Uses</th>
            <th scope="col" style={thNumStyle}>Discount total</th>
            <th scope="col" style={thStyle}>Last used</th>
            <th scope="col" style={thStyle} aria-label="Actions" />
          </tr>
        </thead>
        <tbody>
          {codes.map((code) => (
            <Fragment key={code.id}>
              <tr data-testid={`promo-codes-row-${code.id}`}>
                <td style={tdMonoStyle}>{code.code}</td>
                <td style={tdStyle}>{discountLabel(code)}</td>
                <td
                  style={tdStyle}
                  title={sessionsCellTitle(code.applies_to_session_ids, sessionLabelsById)}
                >
                  {sessionsCellLabel(code.applies_to_session_ids)}
                </td>
                <td style={tdStyle}>
                  {limitLabel(code.max_uses)} / {limitLabel(code.max_uses_per_customer)}
                </td>
                <td style={tdStyle}>{validityLabel(code.valid_from, code.valid_until)}</td>
                <td style={tdStyle}>
                  <StatusBadge status={code.status} />
                </td>
                <td style={tdNumStyle}>{code.uses}</td>
                <td style={tdNumStyle}>
                  {discountTotalCellLabel(code.discount_total, code.currency)}
                </td>
                <td style={tdStyle}>
                  {code.last_used_at !== null ? formatDateTime(code.last_used_at) : "—"}
                </td>
                <td style={tdActionsStyle}>
                  <button
                    type="button"
                    style={rowActionButtonStyle}
                    onClick={() => onPauseToggle(code)}
                    data-testid={`promo-codes-toggle-status-${code.id}`}
                  >
                    {code.status === "active" ? "Pause" : "Activate"}
                  </button>
                  <button
                    type="button"
                    style={rowActionButtonStyle}
                    onClick={() => onToggleUsage(code.id)}
                    data-testid={`promo-codes-usage-${code.id}`}
                  >
                    {expandedId === code.id ? "Hide usage" : "Usage"}
                  </button>
                  <button
                    type="button"
                    style={dangerRowButtonStyle}
                    onClick={() => onDelete(code)}
                    data-testid={`promo-codes-delete-${code.id}`}
                  >
                    Delete
                  </button>
                </td>
              </tr>
              {expandedId === code.id ? (
                <tr data-testid={`promo-codes-usage-row-${code.id}`}>
                  <td colSpan={10} style={usageCellStyle}>
                    <UsagePanel
                      code={code}
                      redemptions={redemptions}
                      loading={redemptionsLoading}
                      error={redemptionsError}
                      csvBusy={csvBusyId === code.id}
                      onDownloadCsv={() => onDownloadCodeCsv(code)}
                    />
                  </td>
                </tr>
              ) : null}
            </Fragment>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function StatusBadge({ status }: { status: PromoStatus }) {
  return (
    <span style={status === "active" ? badgeActiveStyle : badgeNeutralStyle}>
      {status}
    </span>
  );
}

function UsagePanel({
  code,
  redemptions,
  loading,
  error,
  csvBusy,
  onDownloadCsv,
}: {
  code: PromoCode;
  redemptions: readonly PromoRedemption[];
  loading: boolean;
  error: string | null;
  csvBusy: boolean;
  onDownloadCsv: () => void;
}): JSX.Element {
  return (
    <div style={usagePanelStyle} data-testid={`promo-codes-usage-panel-${code.id}`}>
      <div style={usageHeaderStyle}>
        <h3 style={usageTitleStyle}>Usage — {code.code}</h3>
        <button
          type="button"
          style={refreshButtonStyle}
          onClick={onDownloadCsv}
          disabled={csvBusy}
          data-testid={`promo-codes-download-code-csv-${code.id}`}
        >
          {csvBusy ? "Downloading…" : "Download CSV"}
        </button>
      </div>
      {loading ? (
        <p style={mutedStyle}>Loading redemptions…</p>
      ) : error !== null ? (
        <div style={fieldErrorStyle} data-testid={`promo-codes-usage-error-${code.id}`}>
          Could not load usage: {error}
        </div>
      ) : redemptions.length === 0 ? (
        <p style={mutedStyle} data-testid={`promo-codes-usage-empty-${code.id}`}>
          This code has not been redeemed yet.
        </p>
      ) : (
        <table style={miniTableStyle} data-testid={`promo-codes-usage-table-${code.id}`}>
          <thead>
            <tr>
              <th style={thStyle}>Date</th>
              <th style={thStyle}>Order #</th>
              <th style={thStyle}>Status</th>
              <th style={thStyle}>Buyer</th>
              <th style={thStyle}>Channel</th>
              <th style={thNumStyle}>Order amount</th>
              <th style={thNumStyle}>Discount</th>
            </tr>
          </thead>
          <tbody>
            {redemptions.map((r) => (
              <tr key={r.id} data-testid={`promo-codes-redemption-${r.id}`}>
                <td style={tdStyle}>{formatDateTime(r.redeemed_at)}</td>
                <td style={tdMonoStyle}>{r.order_number ?? "—"}</td>
                <td style={tdStyle}>{r.order_status ?? "—"}</td>
                <td style={tdStyle}>{r.buyer_email ?? "—"}</td>
                <td style={tdStyle}>{r.channel_name ?? "—"}</td>
                <td style={tdNumStyle}>{formatMoneyMinor(r.order_amount, r.currency)}</td>
                <td style={tdNumStyle}>{formatMoneyMinor(r.discount_amount, r.currency)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

const pageStyle: CSSProperties = { padding: 24, maxWidth: 1280, color: "#0f172a" };

const headerStyle: CSSProperties = {
  display: "flex",
  alignItems: "flex-start",
  justifyContent: "space-between",
  gap: 16,
  flexWrap: "wrap",
  marginBottom: 16,
};

const headingStyle: CSSProperties = { margin: 0, fontSize: 22, fontWeight: 600, letterSpacing: -0.2 };

const subheadingStyle: CSSProperties = {
  margin: "4px 0 0 0",
  fontSize: 13,
  color: "#475569",
  maxWidth: 720,
  lineHeight: 1.45,
};

const refreshWrapStyle: CSSProperties = { display: "flex", gap: 8, alignItems: "center", flexWrap: "wrap" };

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

const secondaryButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

const rowActionButtonStyle: CSSProperties = {
  fontSize: 11,
  padding: "4px 10px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

const dangerRowButtonStyle: CSSProperties = {
  ...rowActionButtonStyle,
  background: "#fef2f2",
  border: "1px solid #fca5a5",
  color: "#991b1b",
};

const formStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 12,
  padding: 16,
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
  marginBottom: 16,
};

const formGridStyle: CSSProperties = {
  display: "grid",
  gridTemplateColumns: "repeat(auto-fit, minmax(200px, 1fr))",
  gap: 12,
};

const formButtonsStyle: CSSProperties = { display: "flex", gap: 8 };

const fieldGroupStyle: CSSProperties = { display: "flex", flexDirection: "column", gap: 4 };

const fieldLabelStyle: CSSProperties = { fontSize: 12, fontWeight: 600, color: "#334155" };

const inputStyle: CSSProperties = {
  fontSize: 13,
  padding: "8px 10px",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  background: "#ffffff",
  color: "#0f172a",
};

const fieldHintStyle: CSSProperties = { fontSize: 11, color: "#64748b", lineHeight: 1.4 };

const fieldErrorStyle: CSSProperties = { fontSize: 11, color: "#b91c1c", fontWeight: 500 };

const sessionPickerStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  maxHeight: 220,
  overflowY: "auto",
  padding: 8,
  border: "1px solid #e2e8f0",
  borderRadius: 4,
  background: "#f8fafc",
};

const sessionOptionStyle: CSSProperties = {
  display: "flex",
  alignItems: "center",
  gap: 8,
  fontSize: 12,
  color: "#0f172a",
};

const tableWrapStyle: CSSProperties = {
  overflowX: "auto",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
};

const tableStyle: CSSProperties = { width: "100%", borderCollapse: "collapse", fontSize: 13 };

const miniTableStyle: CSSProperties = { ...tableStyle, fontSize: 12 };

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
  whiteSpace: "nowrap",
};

const thNumStyle: CSSProperties = { ...thStyle, textAlign: "right" };

const tdStyle: CSSProperties = {
  padding: "10px 12px",
  borderBottom: "1px solid #f1f5f9",
  color: "#0f172a",
  verticalAlign: "middle",
};

const tdNumStyle: CSSProperties = { ...tdStyle, textAlign: "right", fontVariantNumeric: "tabular-nums", whiteSpace: "nowrap" };

const tdMonoStyle: CSSProperties = {
  ...tdStyle,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
};

const tdActionsStyle: CSSProperties = { ...tdStyle, display: "flex", gap: 6, flexWrap: "wrap" };

const usageCellStyle: CSSProperties = { padding: 0, borderBottom: "1px solid #f1f5f9", background: "#f8fafc" };

const usagePanelStyle: CSSProperties = { padding: 16, display: "flex", flexDirection: "column", gap: 8 };

const usageHeaderStyle: CSSProperties = {
  display: "flex",
  justifyContent: "space-between",
  alignItems: "center",
  gap: 12,
  flexWrap: "wrap",
};

const usageTitleStyle: CSSProperties = { margin: 0, fontSize: 13, fontWeight: 700 };

const statusBoxStyle: CSSProperties = {
  padding: 16,
  border: "1px dashed #cbd5e1",
  borderRadius: 6,
  background: "#f8fafc",
  fontSize: 12,
  color: "#475569",
};

const mutedStyle: CSSProperties = { margin: 0, fontSize: 12, color: "#64748b" };

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
  marginBottom: 16,
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

const monoStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
};

const badgeBaseStyle: CSSProperties = {
  fontSize: 10,
  padding: "2px 6px",
  borderRadius: 999,
  fontWeight: 600,
  textTransform: "uppercase",
  letterSpacing: 0.4,
  display: "inline-block",
};
const badgeActiveStyle: CSSProperties = { ...badgeBaseStyle, background: "#dcfce7", color: "#166534" };
const badgeNeutralStyle: CSSProperties = { ...badgeBaseStyle, background: "#e2e8f0", color: "#334155" };
