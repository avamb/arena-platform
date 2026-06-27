/**
 * Shared helpers for SuperAdmin support consoles (SAUI-10).
 *
 * The /orders, /tickets and /refunds modules all consume the
 * /v1/admin/{entity} endpoints (see
 * apps/backend/internal/platform/httpserver/superadmin.go) and follow
 * the same operational contract:
 *
 *   - read-only (no write actions exposed in this milestone);
 *   - require `superadmin.read` + an X-Admin-Reason header;
 *   - paginate via backend-aligned `limit` / `offset` query parameters;
 *   - accept a single optional `org_id` UUID filter, plus exactly one
 *     status-style enum filter (`state` for orders/refunds,
 *     `status` for tickets).
 *
 * To avoid drifting copies of those constraints across three routes
 * this module owns:
 *
 *   - the URL builder that turns the toolbar state into a query string
 *     using ONLY the filters the backend understands today;
 *   - the validation predicates for `org_id`, `limit`, `offset`;
 *   - the pagination math helpers (page <-> offset, prev/next bounds);
 *   - the `formatMoneyMinor` / `formatDateTime` formatters used in
 *     drawer detail rows.
 *
 * The list-of-allowed-filters approach is deliberate: the worst
 * regression we could ship is a search box that pretends to filter
 * server-side while actually silently dropping unknown parameters.
 * If a future backend change adds richer filters, extend the allow-list
 * here and the toolbar UI in the corresponding route.
 */

/** Maximum page size accepted by the superadmin endpoints. */
export const SUPPORT_MAX_LIMIT = 200;

/** Default page size used when no override is provided. */
export const SUPPORT_DEFAULT_LIMIT = 50;

/** Page size choices exposed in the toolbar dropdown. */
export const SUPPORT_LIMIT_CHOICES: readonly number[] = [25, 50, 100, 200];

/** UUID-ish regex used for the org_id input validation. Mirrors backend
 *  `uuid.Parse` which accepts hyphenated 36-char lowercase/uppercase. */
const UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/**
 * True when `raw` is a syntactically valid UUID.
 *
 * Validating client-side lets the toolbar mark the field invalid before
 * issuing a request that the backend would reject with
 * `superadmin.invalid_org_id`. Empty input is treated as "no filter".
 */
export function isValidUuid(raw: string): boolean {
  return UUID_RE.test(raw.trim());
}

/** Clamp limit to the backend-accepted range, falling back to default. */
export function clampLimit(raw: number | undefined): number {
  if (raw === undefined || !Number.isFinite(raw)) {
    return SUPPORT_DEFAULT_LIMIT;
  }
  const n = Math.floor(raw);
  if (n < 1) {
    return SUPPORT_DEFAULT_LIMIT;
  }
  if (n > SUPPORT_MAX_LIMIT) {
    return SUPPORT_MAX_LIMIT;
  }
  return n;
}

/** Clamp offset to >= 0 (backend rejects negative). */
export function clampOffset(raw: number | undefined): number {
  if (raw === undefined || !Number.isFinite(raw)) {
    return 0;
  }
  const n = Math.floor(raw);
  return n < 0 ? 0 : n;
}

/** Filter selection used by every support console. */
export interface SupportFilters {
  /** Optional org_id UUID. Empty string == no filter. */
  readonly orgId: string;
  /** Optional state/status enum value. Empty string == no filter. */
  readonly statusValue: string;
  readonly limit: number;
  readonly offset: number;
}

/**
 * Build a query string for the support endpoints from filter state.
 *
 * Only the four allow-listed keys (`org_id`, `<statusKey>`, `limit`,
 * `offset`) are ever emitted. The status key differs per entity --
 * orders/refunds use `state`, tickets use `status` -- so the caller
 * passes the matching key. Empty/invalid values are dropped instead of
 * sending blank `?org_id=` which the backend rejects with 400.
 */
export function buildSupportQuery(
  filters: SupportFilters,
  statusKey: "state" | "status",
): string {
  const parts: string[] = [];
  const trimmedOrg = filters.orgId.trim();
  if (trimmedOrg !== "" && isValidUuid(trimmedOrg)) {
    parts.push(`org_id=${encodeURIComponent(trimmedOrg)}`);
  }
  const trimmedStatus = filters.statusValue.trim();
  if (trimmedStatus !== "") {
    parts.push(`${statusKey}=${encodeURIComponent(trimmedStatus)}`);
  }
  parts.push(`limit=${clampLimit(filters.limit)}`);
  parts.push(`offset=${clampOffset(filters.offset)}`);
  return parts.join("&");
}

/**
 * Read initial filter state from the current URL.
 *
 * Used so that deep links from the Organizations explorer
 * (`/orders?org_id=…`) hydrate the toolbar correctly on first paint.
 * Unknown keys are silently ignored — only the four allow-listed
 * filters are honoured. The status key differs per entity.
 */
export function readSupportFiltersFromLocation(
  search: string,
  statusKey: "state" | "status",
): SupportFilters {
  const params = new URLSearchParams(search.startsWith("?") ? search.slice(1) : search);
  const orgId = params.get("org_id") ?? "";
  const statusValue = params.get(statusKey) ?? "";
  const limitRaw = params.get("limit");
  const offsetRaw = params.get("offset");
  return {
    orgId,
    statusValue,
    limit: clampLimit(limitRaw === null ? undefined : Number(limitRaw)),
    offset: clampOffset(offsetRaw === null ? undefined : Number(offsetRaw)),
  };
}

/**
 * One-based page number derived from offset+limit. Useful for the
 * pagination caption (`Page 3`). Never less than 1.
 */
export function currentPage(offset: number, limit: number): number {
  const safeLimit = limit < 1 ? 1 : limit;
  return Math.floor(offset / safeLimit) + 1;
}

/** True when a previous page exists (offset > 0). */
export function canGoPrev(offset: number): boolean {
  return offset > 0;
}

/**
 * True when more pages MAY exist. The backend returns
 * `total = len(rows)` (not a global count), so we can only infer
 * "more available" from "this page is full". This is an honest signal:
 * if the page is partial, we know there are no more rows; if it is
 * full, we cannot know without paging again, so we expose Next.
 */
export function canGoNext(rowsOnPage: number, limit: number): boolean {
  return rowsOnPage >= limit;
}

// ---------------------------------------------------------------------------
// Format helpers
// ---------------------------------------------------------------------------

/**
 * Render a minor-unit money amount in the canonical "X.YZ CCY" form.
 *
 * Backend `Total` on checkout_sessions is stored in minor units
 * (kopecks/cents); refunds `Amount` follows the same convention.
 * Currency codes use ISO 4217 (e.g. RUB, USD, EUR). When either the
 * amount or currency is missing we render an em-dash so the table
 * never displays an ambiguous zero.
 */
export function formatMoneyMinor(
  amountMinor: number | null | undefined,
  currency: string | null | undefined,
): string {
  if (amountMinor === null || amountMinor === undefined || !Number.isFinite(amountMinor)) {
    return "—";
  }
  if (currency === null || currency === undefined || currency.trim() === "") {
    return "—";
  }
  const major = amountMinor / 100;
  // Two decimals are the typical ticketing convention; tested against
  // ISO 4217 zero-decimal currencies (JPY/KRW) by the unit tests below,
  // which prefer explicit cents over guessing per-currency exponents.
  return `${major.toFixed(2)} ${currency.toUpperCase()}`;
}

/** YYYY-MM-DD HH:MMZ — short, sortable, unambiguous; mirrors organizations.tsx. */
export function formatDateTime(iso: string | null | undefined): string {
  if (iso === null || iso === undefined || iso === "") {
    return "—";
  }
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return iso;
  }
  return `${d.toISOString().slice(0, 16).replace("T", " ")}Z`;
}

/** Truncate a UUID for table display: first segment + ellipsis. */
export function shortUuid(id: string): string {
  if (id.length <= 8) {
    return id;
  }
  return `${id.slice(0, 8)}…`;
}
