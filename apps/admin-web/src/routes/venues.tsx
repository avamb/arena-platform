/**
 * Venues CRUD module (feature #242).
 *
 * Replaces the legacy /venues SAUI-12 placeholder shell with a real
 * end-to-end CRUD screen backed by the venues API in
 * apps/backend/internal/platform/httpserver/venues.go:
 *
 *   GET    /v1/venues                                  list (venue.read)
 *   GET    /v1/venues/{id}                             get  (venue.read)
 *   POST   /v1/organizations/{org_id}/venues           create (venue.create, owner-gated)
 *   PATCH  /v1/organizations/{org_id}/venues/{id}      update (venue.update, owner-gated)
 *   DELETE /v1/organizations/{org_id}/venues/{id}      soft-delete (venue.delete, owner-gated)
 *
 * Reads are platform-wide (any authenticated org may read any active
 * venue); writes are owner-gated -- the org_id in the URL path MUST
 * match the venue's owning org. For create, the form requires the
 * caller to supply org_id (prefilled from the active scope when it is
 * an "organization" scope). For edit/delete, the venue itself carries
 * org_id so the UI never has to ask.
 *
 * Permission gating:
 *   - Route wrapped in <RequirePermission /> using the "venues" nav
 *     entry (anyOf venue.read | venue.create | venue.update |
 *     venue.delete | superadmin.read).
 *   - Create / Edit / Delete buttons are individually gated against
 *     venue.create / venue.update / venue.delete so an operator with
 *     only venue.read sees a read-only view.
 *
 * Delete uses DELETE /v1/organizations/{org_id}/venues/{id} which is a
 * SOFT-DELETE on the backend (sets deleted_at). A confirm dialog is
 * shown before the call. "Archive" in operator language ==
 * soft-delete on the wire.
 *
 * Mock data: NONE. List, form, and delete all hit the live backend.
 * No globalThis / devStore / mockDb.
 */
import { createRoute } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type FormEvent,
  type ReactNode,
} from "react";
import { Route as RootRoute } from "./__root";
import { ApiError, authedFetch } from "@/lib/api/client";
import { RequirePermission } from "@/components/RequirePermission";
import { useAuth } from "@/lib/auth/useAuth";
import { useScope } from "@/lib/auth/ScopeContext";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";
import { orgContextFromLocation } from "@/lib/routing/orgContext";
import {
  ResponsiveTable,
  type ResponsiveTableColumn,
  mobileFormBarStyle,
  singleColumnFormStyle,
} from "@/components/layout";
import { VenueSeatingPlansDrawer } from "./venueSeatingPlans";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/venues",
  component: VenuesRoute,
});

// ---------------------------------------------------------------------------
// Backend response shapes
// ---------------------------------------------------------------------------

export interface Venue {
  readonly id: string;
  readonly display_number: number;
  readonly org_id: string;
  readonly city_id: string | null;
  readonly name: string;
  /**
   * Legacy free-form address column. Preserved for backward-compat reads
   * (older venues created before migration 0050 carry only this field).
   * New writes prefer the structured fields below.
   */
  readonly address: string | null;
  readonly capacity_default: number | null;
  readonly created_at: string;
  readonly updated_at: string;
  // V-1 structured address & metadata (migration 0050 / OpenAPI #258).
  // All optional on the wire — older responses may omit them entirely.
  readonly address_line1?: string | null;
  readonly address_line2?: string | null;
  readonly postal_code?: string | null;
  readonly country?: string | null;
  readonly geo_lat?: number | null;
  readonly geo_lng?: number | null;
  readonly timezone?: string | null;
  readonly contact_phone?: string | null;
  readonly contact_email?: string | null;
  readonly website_url?: string | null;
  readonly status?: VenueStatus;
}

export const VENUE_STATUSES = ["active", "draft", "archived"] as const;
export type VenueStatus = (typeof VENUE_STATUSES)[number];

interface OrganizationSummary {
  readonly id: string;
  readonly name: string;
  readonly slug?: string;
  readonly country?: string | null;
}
interface OrganizationEnvelope {
  readonly organization: OrganizationSummary;
}

interface OrganizationListEnvelope {
  readonly organizations: readonly OrganizationSummary[];
}

interface CityItem {
  readonly id: string;
  readonly country_id: string;
  readonly country_iso2: string;
  readonly slug: string;
  readonly name: string;
}

interface CityListEnvelope {
  readonly cities: readonly CityItem[];
}

interface CountryItem {
  readonly id: string;
  readonly iso2: string;
  readonly iso3: string;
  readonly slug: string;
  readonly name: string;
}

interface CountryListEnvelope {
  readonly countries: readonly CountryItem[];
}

interface NominatimResult {
  readonly display_name: string;
  readonly lat: string;
  readonly lon: string;
  readonly name?: string;
  readonly address?: Record<string, string>;
}

export function nominatimSearchURL(query: string): string {
  const params = new URLSearchParams({
    format: "jsonv2",
    addressdetails: "1",
    limit: "5",
    q: query.trim(),
  });
  return `https://nominatim.openstreetmap.org/search?${params.toString()}`;
}

function slugifyCity(value: string): string {
  return value
    .trim()
    .toLowerCase()
    .normalize("NFD")
    .replace(/[\u0300-\u036f]/g, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/(^-|-$)/g, "")
    .slice(0, 100);
}

interface VenueListEnvelope {
  readonly venues: readonly Venue[];
}

interface VenueEnvelope {
  readonly venue: Venue;
}

// ---------------------------------------------------------------------------
// UUID validation (mirrors backend uuid.Parse contract)
// ---------------------------------------------------------------------------

export const UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function validateVenueName(name: string): string | null {
  const trimmed = name.trim();
  if (trimmed === "") {
    return "Name is required";
  }
  if (trimmed.length > 200) {
    return "Name must be at most 200 characters";
  }
  return null;
}

export function validateVenueOrgID(orgID: string): string | null {
  if (orgID.trim() === "") {
    return "Organization ID is required";
  }
  if (!UUID_RE.test(orgID.trim())) {
    return "Organization ID must be a UUID";
  }
  return null;
}

export function validateVenueCityID(cityID: string): string | null {
  // Optional; empty is fine.
  if (cityID.trim() === "") {
    return null;
  }
  if (!UUID_RE.test(cityID.trim())) {
    return "City ID must be a UUID";
  }
  return null;
}

export function validateVenueCapacity(raw: string): string | null {
  if (raw.trim() === "") {
    return null;
  }
  const parsed = Number(raw);
  if (!Number.isInteger(parsed)) {
    return "Capacity must be a whole number";
  }
  if (parsed < 0) {
    return "Capacity cannot be negative";
  }
  if (parsed > 2_000_000_000) {
    return "Capacity is unreasonably large";
  }
  return null;
}

// ---------------------------------------------------------------------------
// V-1 structured address validators (feature #259).
//
// All structured fields are OPTIONAL at the field level — the server is
// authoritative for cross-field invariants. We only enforce shape and
// safety bounds client-side so the operator sees instant feedback.
// ---------------------------------------------------------------------------

export const ISO_COUNTRY_RE = /^[A-Z]{2}$/;

export function normalizeCountry(raw: string): string {
  return raw.trim().toUpperCase();
}

export function validateVenueCountry(raw: string): string | null {
  if (raw.trim() === "") {
    return null;
  }
  if (!ISO_COUNTRY_RE.test(normalizeCountry(raw))) {
    return "Country must be a 2-letter ISO-3166-1 code (e.g., RU, US, GB)";
  }
  return null;
}

export function validateVenuePostalCode(raw: string): string | null {
  if (raw.trim() === "") {
    return null;
  }
  if (raw.trim().length > 32) {
    return "Postal code must be at most 32 characters";
  }
  return null;
}

export function validateVenueAddressLine(raw: string, n: 1 | 2): string | null {
  if (raw.trim() === "") {
    return null;
  }
  if (raw.trim().length > 200) {
    return `Address line ${n} must be at most 200 characters`;
  }
  return null;
}

export function validateVenueGeoLat(raw: string): string | null {
  if (raw.trim() === "") {
    return null;
  }
  const n = Number(raw);
  if (!Number.isFinite(n)) {
    return "Latitude must be a number";
  }
  if (n < -90 || n > 90) {
    return "Latitude must be between -90 and 90";
  }
  return null;
}

export function validateVenueGeoLng(raw: string): string | null {
  if (raw.trim() === "") {
    return null;
  }
  const n = Number(raw);
  if (!Number.isFinite(n)) {
    return "Longitude must be a number";
  }
  if (n < -180 || n > 180) {
    return "Longitude must be between -180 and 180";
  }
  return null;
}

// Lat/lng must be supplied together (an isolated half-coordinate is a
// data-integrity bug). The server also rejects this, but instant feedback
// is friendlier in the form.
export function validateVenueGeoPair(
  latRaw: string,
  lngRaw: string,
): string | null {
  const latSet = latRaw.trim() !== "";
  const lngSet = lngRaw.trim() !== "";
  if (latSet !== lngSet) {
    return "Latitude and longitude must be provided together";
  }
  return null;
}

export function validateVenueTimezone(raw: string): string | null {
  if (raw.trim() === "") {
    return null;
  }
  // Conservative IANA shape: Region/Sub[/SubSub] OR a bare token like UTC.
  // The server runs time.LoadLocation as the authoritative check and
  // emits venue.invalid_timezone (422) on rejection.
  if (!/^[A-Za-z_+\-]+(?:\/[A-Za-z0-9_+\-]+){0,2}$/.test(raw.trim())) {
    return "Timezone must be an IANA name (e.g., Europe/Moscow, UTC)";
  }
  return null;
}

export function validateVenueContactEmail(raw: string): string | null {
  if (raw.trim() === "") {
    return null;
  }
  if (raw.trim().length > 320) {
    return "Email must be at most 320 characters";
  }
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(raw.trim())) {
    return "Enter a valid email address";
  }
  return null;
}

export function validateVenueContactPhone(raw: string): string | null {
  if (raw.trim() === "") {
    return null;
  }
  if (raw.trim().length > 40) {
    return "Phone must be at most 40 characters";
  }
  if (!/^[+0-9 ()\-]+$/.test(raw.trim())) {
    return "Phone may contain digits, spaces, +, -, and parentheses only";
  }
  return null;
}

export function validateVenueWebsiteUrl(raw: string): string | null {
  if (raw.trim() === "") {
    return null;
  }
  let parsed: URL;
  try {
    parsed = new URL(raw.trim());
  } catch {
    return "Website must be a valid URL (https://example.com)";
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    return "Website must use http or https";
  }
  return null;
}

export function isVenueStatus(value: string): value is VenueStatus {
  return (VENUE_STATUSES as readonly string[]).includes(value);
}

// ---------------------------------------------------------------------------
// IANA timezone catalogue
//
// Browsers ship the canonical list via Intl.supportedValuesOf("timeZone").
// We expose it via a small helper that falls back to a curated subset for
// engines that haven't implemented Stage-3 yet (older Safari, jsdom, node
// 18 in some environments) so the autocomplete is never empty.
// ---------------------------------------------------------------------------

const IANA_FALLBACK: readonly string[] = [
  "UTC",
  "Europe/Moscow",
  "Europe/Kaliningrad",
  "Europe/Samara",
  "Europe/London",
  "Europe/Berlin",
  "Europe/Paris",
  "Europe/Madrid",
  "Europe/Rome",
  "Europe/Amsterdam",
  "Europe/Warsaw",
  "Europe/Istanbul",
  "Europe/Helsinki",
  "Asia/Jerusalem",
  "Asia/Dubai",
  "Asia/Tokyo",
  "Asia/Singapore",
  "Asia/Shanghai",
  "Asia/Hong_Kong",
  "Asia/Kolkata",
  "Asia/Bangkok",
  "Asia/Seoul",
  "Asia/Yekaterinburg",
  "Asia/Novosibirsk",
  "Asia/Krasnoyarsk",
  "Asia/Irkutsk",
  "Asia/Vladivostok",
  "America/New_York",
  "America/Chicago",
  "America/Denver",
  "America/Los_Angeles",
  "America/Toronto",
  "America/Sao_Paulo",
  "America/Mexico_City",
  "Australia/Sydney",
  "Australia/Perth",
  "Africa/Johannesburg",
  "Africa/Cairo",
  "Pacific/Auckland",
];

export function listIanaTimezones(): readonly string[] {
  const intl = Intl as typeof Intl & {
    supportedValuesOf?: (key: string) => string[];
  };
  if (typeof intl.supportedValuesOf === "function") {
    try {
      const v = intl.supportedValuesOf("timeZone");
      if (Array.isArray(v) && v.length > 0) {
        return v;
      }
    } catch {
      /* fall through */
    }
  }
  return IANA_FALLBACK;
}

// ---------------------------------------------------------------------------
// Nav entry binding
// ---------------------------------------------------------------------------

const VENUES_NAV_ENTRY = NAV_BY_PATH["/venues"];
if (VENUES_NAV_ENTRY === undefined) {
  throw new Error("venues route: NAV_BY_PATH['/venues'] missing");
}

// ---------------------------------------------------------------------------
// Page shell
// ---------------------------------------------------------------------------

function VenuesRoute() {
  return (
    <RequirePermission entry={VENUES_NAV_ENTRY}>
      <VenuesModule />
    </RequirePermission>
  );
}

type FormMode =
  | { kind: "closed" }
  | { kind: "create" }
  | { kind: "edit"; venue: Venue };

function VenuesModule() {
  const { permissions } = useAuth();
  const { activeScope } = useScope();
  const canCreate = permissions.has("venue.create");
  const canUpdate = permissions.has("venue.update");
  const canDelete = permissions.has("venue.delete");

  const defaultOrgID =
    orgContextFromLocation() ||
    (activeScope?.kind === "organization" && activeScope.id !== null
      ? activeScope.id
      : "");

  const [form, setForm] = useState<FormMode>({ kind: "closed" });
  const [pendingDelete, setPendingDelete] = useState<Venue | null>(null);
  const [plansVenue, setPlansVenue] = useState<Venue | null>(null);

  const query = useQuery<VenueListEnvelope, ApiError>({
    queryKey: ["venues", "list"],
    queryFn: () =>
      authedFetch<VenueListEnvelope>({
        method: "GET",
        path: "/v1/venues",
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

  const rows = query.data?.venues ?? [];
  const sorted = useMemo(
    () => [...rows].sort((a, b) => a.name.localeCompare(b.name)),
    [rows],
  );

  return (
    <section aria-labelledby="venues-heading" style={pageStyle}>
      <header style={headerStyle}>
        <div>
          <h1 id="venues-heading" style={headingStyle}>
            Venues
          </h1>
          <p style={subheadingStyle}>
            Physical event locations. Reads are shared across organizations;
            writes are owner-gated -- a venue can only be created, edited, or
            archived by the organization that owns it. The visual seating
            editor remains explicitly deferred and is not part of this CRUD
            scope.
          </p>
        </div>
        <div style={refreshWrapStyle}>
          <button
            type="button"
            onClick={() => query.refetch()}
            style={refreshButtonStyle}
            disabled={query.isFetching}
            data-testid="venues-refresh"
          >
            {query.isFetching ? "Refreshing…" : "Refresh"}
          </button>
          {canCreate ? (
            <button
              type="button"
              onClick={() => setForm({ kind: "create" })}
              style={primaryButtonStyle}
              data-testid="venues-new"
            >
              New venue
            </button>
          ) : (
            <span style={mutedHintStyle} title="Requires venue.create">
              Create requires venue.create
            </span>
          )}
        </div>
      </header>

      <VenuesBody
        query={query}
        rows={sorted}
        canUpdate={canUpdate}
        canDelete={canDelete}
        onEdit={(venue) => setForm({ kind: "edit", venue })}
        onDelete={(venue) => setPendingDelete(venue)}
        onOpenPlans={(venue) => setPlansVenue(venue)}
      />

      {plansVenue !== null ? (
        <VenueSeatingPlansDrawer
          venue={plansVenue}
          open={true}
          onClose={() => setPlansVenue(null)}
        />
      ) : null}

      {form.kind !== "closed" ? (
        <VenueFormDialog
          mode={form}
          defaultOrgID={defaultOrgID}
          onClose={() => setForm({ kind: "closed" })}
        />
      ) : null}

      {pendingDelete !== null ? (
        <DeleteConfirmDialog
          venue={pendingDelete}
          onClose={() => setPendingDelete(null)}
        />
      ) : null}
    </section>
  );
}

// ---------------------------------------------------------------------------
// Table body and states
// ---------------------------------------------------------------------------

interface BodyProps {
  query: ReturnType<typeof useQuery<VenueListEnvelope, ApiError>>;
  rows: readonly Venue[];
  canUpdate: boolean;
  canDelete: boolean;
  onEdit: (venue: Venue) => void;
  onDelete: (venue: Venue) => void;
  onOpenPlans: (venue: Venue) => void;
}

function VenuesBody({
  query,
  rows,
  canUpdate,
  canDelete,
  onEdit,
  onDelete,
  onOpenPlans,
}: BodyProps) {
  if (query.isPending) {
    return (
      <div style={statusBoxStyle} role="status" aria-live="polite">
        Loading venues from /v1/venues…
      </div>
    );
  }
  if (query.isError) {
    return <VenuesErrorState error={query.error} onRetry={() => query.refetch()} />;
  }
  if (rows.length === 0) {
    return (
      <div style={statusBoxStyle} role="status" data-testid="venues-empty">
        No venues exist yet. Create the first venue to begin attaching events
        and seating plans.
      </div>
    );
  }
  const columns: ResponsiveTableColumn<Venue>[] = [
    {
      id: "name",
      header: "Name",
      primary: true,
      renderCell: (v) => (
        <span data-testid={`venues-row-${v.id}`}>{v.name} · #{v.display_number}</span>
      ),
    },
    {
      id: "org",
      header: "Organization",
      renderCell: (v) => <span title={v.org_id}>{shortenUUID(v.org_id)}</span>,
    },
    {
      id: "city",
      header: "City",
      renderCell: (v) => (
        <span title={v.city_id ?? ""}>
          {v.city_id !== null ? shortenUUID(v.city_id) : "—"}
        </span>
      ),
    },
    {
      id: "country",
      header: "Country",
      renderCell: (v) =>
        v.country !== null && v.country !== undefined && v.country !== ""
          ? v.country
          : "—",
    },
    {
      id: "capacity",
      header: "Capacity",
      renderCell: (v) =>
        v.capacity_default !== null ? v.capacity_default.toLocaleString() : "—",
    },
    {
      id: "status",
      header: "Status",
      renderCell: (v) => <VenueStatusBadge status={v.status ?? "active"} />,
    },
    {
      id: "updated",
      header: "Updated",
      renderCell: (v) => formatDate(v.updated_at),
    },
    {
      id: "actions",
      header: "Actions",
      hideOnMobile: true,
      renderCell: (v) => (
        <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
          <button
            type="button"
            style={rowActionButtonStyle}
            onClick={() => onOpenPlans(v)}
            data-testid={`venues-plans-${v.id}`}
          >
            Seating plans
          </button>
          {canUpdate ? (
            <button
              type="button"
              style={rowActionButtonStyle}
              onClick={() => onEdit(v)}
              data-testid={`venues-edit-${v.id}`}
            >
              Edit
            </button>
          ) : null}
          {canDelete ? (
            <button
              type="button"
              style={rowDangerButtonStyle}
              onClick={() => onDelete(v)}
              data-testid={`venues-delete-${v.id}`}
            >
              Archive
            </button>
          ) : null}
          {!canUpdate && !canDelete ? (
            <span style={mutedHintStyle}>read-only</span>
          ) : null}
        </div>
      ),
    },
  ];
  return (
    <div style={tableWrapStyle} role="region" aria-label="Venues">
      <ResponsiveTable<Venue>
        id="venues-table"
        columns={columns}
        rows={rows}
        rowKey={(v) => v.id}
      />
    </div>
  );
}

function VenuesErrorState({
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
      <div style={errorBoxStyle} role="alert" data-testid="venues-forbidden">
        <strong>Forbidden.</strong>
        <p style={errorParaStyle}>
          Your account is missing <code style={monoStyle}>venue.read</code>.
          Ask a platform administrator to grant the permission.
        </p>
      </div>
    );
  }
  if (error instanceof ApiError && error.status === 401) {
    return (
      <div style={errorBoxStyle} role="status" data-testid="venues-session-expired">
        <strong>Session expired.</strong>
        <p style={errorParaStyle}>Sign in again to reload venues.</p>
      </div>
    );
  }
  return (
    <div style={errorBoxStyle} role="alert" data-testid="venues-error">
      <strong>Failed to load venues.</strong>
      <div style={errorCodeStyle}>{error?.code ?? "unknown.error"}</div>
      {error?.message ? <div style={errorParaStyle}>{error.message}</div> : null}
      <button type="button" style={errorRetryStyle} onClick={onRetry}>
        Retry
      </button>
    </div>
  );
}

function VenueStatusBadge({ status }: { status: VenueStatus }) {
  const palette: Record<VenueStatus, CSSProperties> = {
    active: { background: "#dcfce7", color: "#166534", borderColor: "#86efac" },
    draft: { background: "#fef3c7", color: "#854d0e", borderColor: "#fde68a" },
    archived: { background: "#f1f5f9", color: "#475569", borderColor: "#cbd5e1" },
  };
  return (
    <span
      style={{ ...statusBadgeStyle, ...palette[status] }}
      data-testid={`venues-status-${status}`}
    >
      {status}
    </span>
  );
}

// ---------------------------------------------------------------------------
// Create / Edit modal
// ---------------------------------------------------------------------------

interface FormDialogProps {
  mode: Extract<FormMode, { kind: "create" } | { kind: "edit" }>;
  defaultOrgID: string;
  onClose: () => void;
}

interface ServerFieldErrors {
  name?: string;
  org_id?: string;
  city_id?: string;
  address?: string;
  address_line1?: string;
  address_line2?: string;
  postal_code?: string;
  country?: string;
  geo_lat?: string;
  geo_lng?: string;
  timezone?: string;
  contact_phone?: string;
  contact_email?: string;
  website_url?: string;
  status?: string;
  capacity_default?: string;
  form?: string;
}

function VenueFormDialog({ mode, defaultOrgID, onClose }: FormDialogProps) {
  const queryClient = useQueryClient();
  const isEdit = mode.kind === "edit";
  const tzListId = useId();

  const initialOrgID = isEdit ? mode.venue.org_id : defaultOrgID;
  const initialName = isEdit ? mode.venue.name : "";
  const initialCityID = isEdit ? (mode.venue.city_id ?? "") : "";
  const initialAddressLegacy = isEdit ? (mode.venue.address ?? "") : "";
  const initialAddressLine1 = isEdit ? (mode.venue.address_line1 ?? "") : "";
  const initialAddressLine2 = isEdit ? (mode.venue.address_line2 ?? "") : "";
  const initialPostal = isEdit ? (mode.venue.postal_code ?? "") : "";
  const initialCountry = isEdit ? (mode.venue.country ?? "") : "";
  const initialGeoLat =
    isEdit && mode.venue.geo_lat !== null && mode.venue.geo_lat !== undefined
      ? String(mode.venue.geo_lat)
      : "";
  const initialGeoLng =
    isEdit && mode.venue.geo_lng !== null && mode.venue.geo_lng !== undefined
      ? String(mode.venue.geo_lng)
      : "";
  const initialTimezone = isEdit ? (mode.venue.timezone ?? "") : "";
  const initialPhone = isEdit ? (mode.venue.contact_phone ?? "") : "";
  const initialEmail = isEdit ? (mode.venue.contact_email ?? "") : "";
  const initialWebsite = isEdit ? (mode.venue.website_url ?? "") : "";
  const initialStatus: VenueStatus = isEdit
    ? (mode.venue.status ?? "active")
    : "active";
  const initialCapacity =
    isEdit && mode.venue.capacity_default !== null
      ? String(mode.venue.capacity_default)
      : "";

  const [orgID, setOrgID] = useState(initialOrgID);
  const [name, setName] = useState(initialName);
  const [cityID, setCityID] = useState(initialCityID);
  const [addressLine1, setAddressLine1] = useState(initialAddressLine1);
  const [addressLine2, setAddressLine2] = useState(initialAddressLine2);
  const [postalCode, setPostalCode] = useState(initialPostal);
  const [country, setCountry] = useState(initialCountry);
  const [geoLat, setGeoLat] = useState(initialGeoLat);
  const [geoLng, setGeoLng] = useState(initialGeoLng);
  const [timezone, setTimezone] = useState(initialTimezone);
  const [phone, setPhone] = useState(initialPhone);
  const [email, setEmail] = useState(initialEmail);
  const [website, setWebsite] = useState(initialWebsite);
  const [status, setStatus] = useState<VenueStatus>(initialStatus);
  const [capacity, setCapacity] = useState(initialCapacity);
  const [serverErrors, setServerErrors] = useState<ServerFieldErrors>({});
  const [addressSearch, setAddressSearch] = useState("");
  const [geocodeResults, setGeocodeResults] = useState<readonly NominatimResult[]>([]);
  const [geocodeError, setGeocodeError] = useState<string | null>(null);
  const [isGeocoding, setIsGeocoding] = useState(false);
  const [newCityName, setNewCityName] = useState("");
  const [isCreatingCity, setIsCreatingCity] = useState(false);
  const [cityCreateError, setCityCreateError] = useState<string | null>(null);
  // Inline "+ Add city" toggle (AB-35: always available, not just from geocoding)
  const [addCityOpen, setAddCityOpen] = useState(false);
  // Inline country creation (AB-35)
  const [countryCreateOpen, setCountryCreateOpen] = useState(false);
  const [newCountryName, setNewCountryName] = useState("");
  const [newCountryIso2, setNewCountryIso2] = useState("");
  const [newCountryIso3, setNewCountryIso3] = useState("");
  const [isCreatingCountry, setIsCreatingCountry] = useState(false);
  const [countryCreateError, setCountryCreateError] = useState<string | null>(null);

  // Track whether the operator has hand-edited the country so the
  // org-country prefill effect doesn't clobber their input. Refs avoid
  // re-rendering on every keystroke.
  const countryTouchedRef = useRef<boolean>(isEdit || initialCountry !== "");

  // Preselect country from the owning organization on create. We only
  // run this when orgID is a valid UUID and the user hasn't typed yet.
  const orgIdValid = validateVenueOrgID(orgID) === null;
  const organizationsQuery = useQuery<OrganizationListEnvelope, ApiError>({
    queryKey: ["organizations", "venue-picker"],
    queryFn: () =>
      authedFetch<OrganizationListEnvelope>({
        method: "GET",
        path: "/v1/organizations",
      }),
    enabled: !isEdit,
    retry: false,
    refetchOnWindowFocus: false,
  });
  const citiesQuery = useQuery<CityListEnvelope, ApiError>({
    queryKey: ["geo", "cities", "venue-picker"],
    queryFn: () =>
      authedFetch<CityListEnvelope>({ method: "GET", path: "/v1/geo/cities" }),
    retry: false,
    refetchOnWindowFocus: false,
  });
  const countriesQuery = useQuery<CountryListEnvelope, ApiError>({
    queryKey: ["geo", "countries", "venue-picker"],
    queryFn: () =>
      authedFetch<CountryListEnvelope>({ method: "GET", path: "/v1/geo/countries" }),
    retry: false,
    refetchOnWindowFocus: false,
  });
  const orgQuery = useQuery<OrganizationEnvelope, ApiError>({
    queryKey: ["venues", "form", "org", orgID.trim()],
    queryFn: () =>
      authedFetch<OrganizationEnvelope>({
        method: "GET",
        path: `/v1/organizations/${orgID.trim()}`,
      }),
    enabled: !isEdit && orgIdValid,
    retry: false,
    refetchOnWindowFocus: false,
  });
  useEffect(() => {
    if (isEdit) {
      return;
    }
    if (countryTouchedRef.current) {
      return;
    }
    const c = orgQuery.data?.organization.country;
    if (typeof c === "string" && ISO_COUNTRY_RE.test(c.toUpperCase())) {
      setCountry(c.toUpperCase());
    }
  }, [orgQuery.data, isEdit]);

  async function searchAddress() {
    const query = addressSearch.trim();
    if (query.length < 3) {
      setGeocodeError("Enter at least three characters to search for an address.");
      return;
    }
    setIsGeocoding(true);
    setGeocodeError(null);
    setGeocodeResults([]);
    try {
      const response = await fetch(nominatimSearchURL(query), {
        headers: { Accept: "application/json" },
      });
      if (!response.ok) {
        throw new Error(`Geocoding service returned ${response.status}`);
      }
      const results: unknown = await response.json();
      setGeocodeResults(Array.isArray(results) ? (results as NominatimResult[]) : []);
    } catch {
      setGeocodeError("Address suggestions are unavailable. You can enter all fields manually.");
    } finally {
      setIsGeocoding(false);
    }
  }

  function applyGeocodeResult(result: NominatimResult) {
    const address = result.address ?? {};
    const matchedCityName = address.city ?? address.town ?? address.village ?? address.municipality ?? "";
    const matchedCountry = (address.country_code ?? "").toUpperCase();
    const street = [address.road, address.house_number].filter(Boolean).join(" ");
    setAddressLine1(street || result.display_name.split(",")[0] || "");
    setPostalCode(address.postcode ?? "");
    setCountry(matchedCountry);
    countryTouchedRef.current = true;
    setGeoLat(result.lat);
    setGeoLng(result.lon);
    if (name.trim() === "" && result.name !== undefined && result.name.trim() !== "") {
      setName(result.name.trim());
    }
    const foundCity = (citiesQuery.data?.cities ?? []).find(
      (city) =>
        city.country_iso2.toUpperCase() === matchedCountry &&
        city.name.localeCompare(matchedCityName, undefined, { sensitivity: "accent" }) === 0,
    );
    setCityID(foundCity?.id ?? "");
    setNewCityName(foundCity === undefined ? matchedCityName : "");
    setGeocodeResults([]);
  }

  async function createCity() {
    const normalizedName = newCityName.trim();
    const parent = (countriesQuery.data?.countries ?? []).find(
      (candidate) => candidate.iso2.toUpperCase() === normalizeCountry(country),
    );
    const slug = slugifyCity(normalizedName);
    if (parent === undefined || normalizedName === "" || slug === "") {
      setCityCreateError("Choose a registry country and provide a city name before creating it.");
      return;
    }
    setIsCreatingCity(true);
    setCityCreateError(null);
    try {
      const response = await authedFetch<{ city: CityItem }>({
        method: "POST",
        path: "/v1/admin/geo/cities",
        body: { country_id: parent.id, slug, name_en: normalizedName },
      });
      setCityID(response.city.id);
      setNewCityName("");
      await queryClient.invalidateQueries({ queryKey: ["geo", "cities"] });
    } catch (error) {
      setCityCreateError(error instanceof ApiError ? error.message : "Unable to create the city.");
    } finally {
      setIsCreatingCity(false);
    }
  }

  async function createCountry() {
    const nameEn = newCountryName.trim();
    const iso2 = newCountryIso2.trim().toUpperCase();
    const iso3 = newCountryIso3.trim().toUpperCase();
    if (nameEn === "" || iso2.length !== 2 || iso3.length !== 3) {
      setCountryCreateError("Provide a 2-letter ISO-2, 3-letter ISO-3, and country name.");
      return;
    }
    setIsCreatingCountry(true);
    setCountryCreateError(null);
    try {
      const slug = slugifyCity(nameEn); // reuse slug builder for simplicity
      await authedFetch<{ country: CountryItem }>({
        method: "POST",
        path: "/v1/admin/geo/countries",
        body: { iso2, iso3, slug, name_en: nameEn },
      });
      setCountry(iso2);
      countryTouchedRef.current = true;
      setNewCountryName("");
      setNewCountryIso2("");
      setNewCountryIso3("");
      setCountryCreateOpen(false);
      await queryClient.invalidateQueries({ queryKey: ["geo", "countries"] });
    } catch (error) {
      setCountryCreateError(error instanceof ApiError ? error.message : "Unable to create the country.");
    } finally {
      setIsCreatingCountry(false);
    }
  }

  const nameErr = validateVenueName(name);
  const orgIDLocalErr = validateVenueOrgID(orgID);
  const cityIDErr = validateVenueCityID(cityID);
  const capacityErr = validateVenueCapacity(capacity);
  const addr1Err = validateVenueAddressLine(addressLine1, 1);
  const addr2Err = validateVenueAddressLine(addressLine2, 2);
  const postalErr = validateVenuePostalCode(postalCode);
  const countryErr = validateVenueCountry(country);
  const latErr = validateVenueGeoLat(geoLat);
  const lngErr = validateVenueGeoLng(geoLng);
  const geoPairErr = validateVenueGeoPair(geoLat, geoLng);
  const tzErr = validateVenueTimezone(timezone);
  const emailErr = validateVenueContactEmail(email);
  const phoneErr = validateVenueContactPhone(phone);
  const websiteErr = validateVenueWebsiteUrl(website);

  const localValid =
    nameErr === null &&
    orgIDLocalErr === null &&
    cityIDErr === null &&
    capacityErr === null &&
    addr1Err === null &&
    addr2Err === null &&
    postalErr === null &&
    countryErr === null &&
    latErr === null &&
    lngErr === null &&
    geoPairErr === null &&
    tzErr === null &&
    emailErr === null &&
    phoneErr === null &&
    websiteErr === null;

  // For edit, allow submission when at least one field changed.
  const dirty =
    !isEdit ||
    name.trim() !== initialName ||
    cityID.trim() !== initialCityID ||
    addressLine1.trim() !== initialAddressLine1 ||
    addressLine2.trim() !== initialAddressLine2 ||
    postalCode.trim() !== initialPostal ||
    normalizeCountry(country) !== initialCountry ||
    geoLat.trim() !== initialGeoLat ||
    geoLng.trim() !== initialGeoLng ||
    timezone.trim() !== initialTimezone ||
    phone.trim() !== initialPhone ||
    email.trim() !== initialEmail ||
    website.trim() !== initialWebsite ||
    status !== initialStatus ||
    capacity.trim() !== initialCapacity;

  const mutation = useMutation<VenueEnvelope, ApiError, void>({
    mutationFn: () => {
      const trimmedOrgID = orgID.trim();
      if (isEdit) {
        const body = buildUpdateVenueBody(
          {
            name,
            cityID,
            addressLine1,
            addressLine2,
            postalCode,
            country,
            geoLat,
            geoLng,
            timezone,
            phone,
            email,
            website,
            status,
            capacity,
          },
          {
            name: initialName,
            cityID: initialCityID,
            addressLine1: initialAddressLine1,
            addressLine2: initialAddressLine2,
            postalCode: initialPostal,
            country: initialCountry,
            geoLat: initialGeoLat,
            geoLng: initialGeoLng,
            timezone: initialTimezone,
            phone: initialPhone,
            email: initialEmail,
            website: initialWebsite,
            status: initialStatus,
            capacity: initialCapacity,
          },
        );
        return authedFetch<VenueEnvelope>({
          method: "PATCH",
          path: `/v1/organizations/${trimmedOrgID}/venues/${mode.venue.id}`,
          body,
        });
      }
      const body = buildCreateVenueBody({
        name,
        cityID,
        addressLine1,
        addressLine2,
        postalCode,
        country,
        geoLat,
        geoLng,
        timezone,
        phone,
        email,
        website,
        status,
        capacity,
      });
      return authedFetch<VenueEnvelope>({
        method: "POST",
        path: `/v1/organizations/${trimmedOrgID}/venues`,
        body,
      });
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["venues"] });
      onClose();
    },
    onError: (err) => {
      setServerErrors(mapServerError(err));
    },
  });

  function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setServerErrors({});
    if (!localValid || !dirty) {
      return;
    }
    mutation.mutate();
  }

  const tzOptions = useMemo(() => listIanaTimezones(), []);
  const title = isEdit ? `Edit ${mode.venue.name}` : "New venue";
  const submitLabel = isEdit ? "Save changes" : "Create venue";
  // Legacy `address` value is preserved on the wire automatically — show
  // a read-only callout in edit mode so operators understand provenance.
  void initialAddressLegacy;

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="venues-form-title"
      style={dialogBackdropStyle}
      data-testid="venues-form-dialog"
    >
      <div style={dialogStyle}>
        <header style={dialogHeaderStyle}>
          <h2 id="venues-form-title" style={dialogTitleStyle}>
            {title}
          </h2>
          <button
            type="button"
            onClick={onClose}
            style={dialogCloseStyle}
            aria-label="Close"
            data-testid="venues-form-close"
          >
            ×
          </button>
        </header>
        <form onSubmit={onSubmit} style={formStyle} noValidate>
          {!isEdit ? (
            <FieldRow
              label="Organization"
              htmlFor="venue-org-id"
              error={serverErrors.org_id ?? null}
              localError={orgID.length > 0 ? orgIDLocalErr : null}
              hint="Choose the organization that owns this venue."
            >
              <select
                id="venue-org-id"
                value={orgID}
                onChange={(e) => {
                  setOrgID(e.target.value);
                  if (serverErrors.org_id !== undefined) {
                    setServerErrors({ ...serverErrors, org_id: undefined });
                  }
                }}
                style={inputStyle}
                required
                data-testid="venues-form-org-id"
                disabled={organizationsQuery.isPending}
              >
                <option value="">
                  {organizationsQuery.isPending ? "Loading organizations…" : "Select an organization"}
                </option>
                {[...(organizationsQuery.data?.organizations ?? [])]
                  .sort((a, b) => a.name.localeCompare(b.name))
                  .map((organization) => (
                    <option key={organization.id} value={organization.id}>
                      {organization.name}{organization.slug !== undefined ? ` · ${organization.slug}` : ""}
                    </option>
                  ))}
              </select>
            </FieldRow>
          ) : null}
          <FieldRow
            label="Name"
            htmlFor="venue-name"
            error={serverErrors.name ?? null}
            localError={name.length > 0 ? nameErr : null}
            hint="Operator-visible venue name. Required."
          >
            <input
              id="venue-name"
              type="text"
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                if (serverErrors.name !== undefined) {
                  setServerErrors({ ...serverErrors, name: undefined });
                }
              }}
              style={inputStyle}
              required
              maxLength={200}
              autoFocus={!isEdit}
              data-testid="venues-form-name"
            />
          </FieldRow>

          <FieldRow
            label="Status"
            htmlFor="venue-status"
            error={serverErrors.status ?? null}
            localError={null}
            hint="active = visible to operators and events; draft = hidden from event creation; archived = legacy/closed."
          >
            <select
              id="venue-status"
              value={status}
              onChange={(e) => {
                if (isVenueStatus(e.target.value)) {
                  setStatus(e.target.value);
                }
                if (serverErrors.status !== undefined) {
                  setServerErrors({ ...serverErrors, status: undefined });
                }
              }}
              style={inputStyle}
              data-testid="venues-form-status"
            >
              {VENUE_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </FieldRow>

          <fieldset style={fieldsetStyle}>
            <legend style={legendStyle}>Address</legend>
            <FieldRow
              label="Find an address"
              htmlFor="venue-address-search"
              error={null}
              localError={null}
              hint="Searches OpenStreetMap Nominatim. Selecting a suggestion fills the address, city, country, and coordinates; manual entry always remains available."
            >
              <div style={searchRowStyle}>
                <input
                  id="venue-address-search"
                  type="search"
                  value={addressSearch}
                  onChange={(e) => setAddressSearch(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      e.preventDefault();
                      void searchAddress();
                    }
                  }}
                  style={inputStyle}
                  placeholder="e.g. 28 Kubelíkova, Prague"
                  data-testid="venues-form-address-search"
                />
                <button
                  type="button"
                  style={refreshButtonStyle}
                  onClick={() => void searchAddress()}
                  disabled={isGeocoding}
                  data-testid="venues-form-address-search-submit"
                >
                  {isGeocoding ? "Searching…" : "Search"}
                </button>
              </div>
              {geocodeError !== null ? <div style={fieldErrorStyle} role="status">{geocodeError}</div> : null}
              {geocodeResults.length > 0 ? (
                <div style={geocodeResultsStyle} data-testid="venues-form-address-suggestions">
                  {geocodeResults.map((result, index) => (
                    <button
                      type="button"
                      key={`${result.lat}-${result.lon}-${index}`}
                      onClick={() => applyGeocodeResult(result)}
                      style={geocodeResultButtonStyle}
                      data-testid={`venues-form-address-suggestion-${index}`}
                    >
                      {result.display_name}
                    </button>
                  ))}
                </div>
              ) : null}
            </FieldRow>
            <FieldRow
              label="Country (ISO-3166-1 alpha-2)"
              htmlFor="venue-country"
              error={serverErrors.country ?? null}
              localError={country.length > 0 ? countryErr : null}
              hint={
                orgQuery.data?.organization.country !== undefined &&
                orgQuery.data.organization.country !== null &&
                !isEdit
                  ? `Prefilled from the owning organization (${orgQuery.data.organization.country.toUpperCase()}). Override if the venue lives elsewhere.`
                  : "Choose a country from the registry, or create one if it's not listed yet."
              }
            >
              {/* AB-35: select from geo registry + inline create affordance */}
              <div style={{ display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap" }}>
                <select
                  id="venue-country"
                  value={country}
                  onChange={(e) => {
                    countryTouchedRef.current = true;
                    setCountry(e.target.value);
                    setCityID(""); // reset city when country changes
                    if (serverErrors.country !== undefined) {
                      setServerErrors({ ...serverErrors, country: undefined });
                    }
                  }}
                  style={{ ...inputStyle, flex: 1 }}
                  data-testid="venues-form-country"
                  disabled={countriesQuery.isPending}
                >
                  <option value="">
                    {countriesQuery.isPending ? "Loading countries…" : "No country selected"}
                  </option>
                  {[...(countriesQuery.data?.countries ?? [])]
                    .sort((a, b) => a.name.localeCompare(b.name))
                    .map((c) => (
                      <option key={c.id} value={c.iso2}>{c.name} ({c.iso2})</option>
                    ))}
                </select>
                <button
                  type="button"
                  style={inlineCreateButtonStyle}
                  onClick={() => setCountryCreateOpen((o) => !o)}
                  data-testid="venues-form-country-create-toggle"
                  title="Create a new country in the geo registry"
                >
                  {countryCreateOpen ? "- Cancel" : "+ Country"}
                </button>
              </div>
              {countriesQuery.isError ? (
                <div style={fieldErrorStyle}>Country registry unavailable; try refreshing the page.</div>
              ) : null}
              {countryCreateOpen && (
                <div style={inlineCreateFormStyle} data-testid="venues-form-create-country">
                  <strong style={{ fontSize: 11, color: "#334155" }}>New country</strong>
                  <div style={{ display: "flex", gap: 4, flexWrap: "wrap" }}>
                    <input
                      type="text"
                      placeholder="Name (English)"
                      value={newCountryName}
                      onChange={(e) => setNewCountryName(e.target.value)}
                      style={{ ...inputStyle, flex: 2, minWidth: 120 }}
                      maxLength={100}
                      data-testid="venues-form-country-name"
                    />
                    <input
                      type="text"
                      placeholder="ISO-2 (e.g., IL)"
                      value={newCountryIso2}
                      onChange={(e) => setNewCountryIso2(e.target.value.toUpperCase())}
                      style={{ ...inputMonoStyle, width: 72 }}
                      maxLength={2}
                      autoCapitalize="characters"
                      data-testid="venues-form-country-iso2"
                    />
                    <input
                      type="text"
                      placeholder="ISO-3 (e.g., ISR)"
                      value={newCountryIso3}
                      onChange={(e) => setNewCountryIso3(e.target.value.toUpperCase())}
                      style={{ ...inputMonoStyle, width: 84 }}
                      maxLength={3}
                      autoCapitalize="characters"
                      data-testid="venues-form-country-iso3"
                    />
                    <button
                      type="button"
                      style={primaryButtonStyle}
                      onClick={() => void createCountry()}
                      disabled={isCreatingCountry || newCountryName.trim() === "" || newCountryIso2.trim().length !== 2 || newCountryIso3.trim().length !== 3}
                      data-testid="venues-form-country-create-submit"
                    >
                      {isCreatingCountry ? "Creating…" : "Create"}
                    </button>
                  </div>
                  {countryCreateError !== null ? (
                    <span style={fieldErrorStyle}>{countryCreateError}</span>
                  ) : null}
                </div>
              )}
            </FieldRow>
            <FieldRow
              label="City"
              htmlFor="venue-city-id"
              error={serverErrors.city_id ?? null}
              localError={null}
              hint="Optional city from the geo registry. Use '+ City' to create a missing city without leaving this form."
            >
              {/* AB-35: city select filtered by country, with persistent + Add city button */}
              <div style={{ display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap" }}>
                <select
                  id="venue-city-id"
                  value={cityID}
                  onChange={(e) => {
                    setCityID(e.target.value);
                    if (serverErrors.city_id !== undefined) {
                      setServerErrors({ ...serverErrors, city_id: undefined });
                    }
                  }}
                  style={{ ...inputStyle, flex: 1 }}
                  data-testid="venues-form-city-id"
                  disabled={citiesQuery.isPending}
                >
                  <option value="">{citiesQuery.isPending ? "Loading cities…" : "No city selected"}</option>
                  {[...(citiesQuery.data?.cities ?? [])]
                    .filter((city) => country.trim() === "" || city.country_iso2 === normalizeCountry(country))
                    .sort((a, b) => a.name.localeCompare(b.name))
                    .map((city) => (
                      <option key={city.id} value={city.id}>{city.name} ({city.country_iso2})</option>
                    ))}
                </select>
                <button
                  type="button"
                  style={inlineCreateButtonStyle}
                  onClick={() => {
                    setAddCityOpen((o) => !o);
                    if (!addCityOpen) {
                      setNewCityName("");
                      setCityCreateError(null);
                    }
                  }}
                  data-testid="venues-form-city-create-toggle"
                  title="Create a new city in the geo registry"
                >
                  {addCityOpen ? "- Cancel" : "+ City"}
                </button>
              </div>
              {citiesQuery.isError ? <div style={fieldErrorStyle}>City registry unavailable; retry the page or enter address details manually.</div> : null}
              {(newCityName !== "" || addCityOpen) ? (
                <div style={inlineCityStyle} data-testid="venues-form-create-city">
                  {newCityName !== "" && !addCityOpen ? (
                    <span>"{newCityName}" is not in the registry.</span>
                  ) : null}
                  <div style={{ display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap" }}>
                    <input
                      type="text"
                      placeholder="City name (English)"
                      value={newCityName}
                      onChange={(e) => setNewCityName(e.target.value)}
                      style={{ ...inputStyle, flex: 1, minWidth: 120 }}
                      maxLength={100}
                      data-testid="venues-form-city-name"
                    />
                    <button type="button" style={primaryButtonStyle} onClick={() => void createCity()} disabled={isCreatingCity || newCityName.trim() === ""}>
                      {isCreatingCity ? "Creating…" : "Create city"}
                    </button>
                  </div>
                  {cityCreateError !== null ? <span style={fieldErrorStyle}>{cityCreateError}</span> : null}
                </div>
              ) : null}
            </FieldRow>
            <FieldRow
              label="Postal code"
              htmlFor="venue-postal"
              error={serverErrors.postal_code ?? null}
              localError={postalCode.length > 0 ? postalErr : null}
              hint="Optional. Free-form per-country format."
            >
              <input
                id="venue-postal"
                type="text"
                value={postalCode}
                onChange={(e) => {
                  setPostalCode(e.target.value);
                  if (serverErrors.postal_code !== undefined) {
                    setServerErrors({ ...serverErrors, postal_code: undefined });
                  }
                }}
                style={inputStyle}
                maxLength={32}
                autoCapitalize="characters"
                autoCorrect="off"
                spellCheck={false}
                data-testid="venues-form-postal"
              />
            </FieldRow>
            <FieldRow
              label="Address line 1"
              htmlFor="venue-address-line1"
              error={serverErrors.address_line1 ?? null}
              localError={addressLine1.length > 0 ? addr1Err : null}
              hint="Street, building number."
            >
              <input
                id="venue-address-line1"
                type="text"
                value={addressLine1}
                onChange={(e) => {
                  setAddressLine1(e.target.value);
                  if (serverErrors.address_line1 !== undefined) {
                    setServerErrors({
                      ...serverErrors,
                      address_line1: undefined,
                    });
                  }
                }}
                style={inputStyle}
                maxLength={200}
                data-testid="venues-form-address-line1"
              />
            </FieldRow>
            <FieldRow
              label="Address line 2"
              htmlFor="venue-address-line2"
              error={serverErrors.address_line2 ?? null}
              localError={addressLine2.length > 0 ? addr2Err : null}
              hint="Apartment, floor, district, etc. Optional."
            >
              <input
                id="venue-address-line2"
                type="text"
                value={addressLine2}
                onChange={(e) => {
                  setAddressLine2(e.target.value);
                  if (serverErrors.address_line2 !== undefined) {
                    setServerErrors({
                      ...serverErrors,
                      address_line2: undefined,
                    });
                  }
                }}
                style={inputStyle}
                maxLength={200}
                data-testid="venues-form-address-line2"
              />
            </FieldRow>
            <div style={twoColRowStyle}>
              <FieldRow
                label="Latitude"
                htmlFor="venue-geo-lat"
                error={serverErrors.geo_lat ?? null}
                localError={
                  geoLat.length > 0
                    ? latErr
                    : geoPairErr !== null && geoLng.length > 0
                      ? geoPairErr
                      : null
                }
                hint="WGS-84 decimal degrees, -90 to 90."
              >
                <input
                  id="venue-geo-lat"
                  type="text"
                  inputMode="decimal"
                  value={geoLat}
                  onChange={(e) => {
                    setGeoLat(e.target.value);
                    if (serverErrors.geo_lat !== undefined) {
                      setServerErrors({ ...serverErrors, geo_lat: undefined });
                    }
                  }}
                  style={inputStyle}
                  maxLength={16}
                  data-testid="venues-form-geo-lat"
                />
              </FieldRow>
              <FieldRow
                label="Longitude"
                htmlFor="venue-geo-lng"
                error={serverErrors.geo_lng ?? null}
                localError={
                  geoLng.length > 0
                    ? lngErr
                    : geoPairErr !== null && geoLat.length > 0
                      ? geoPairErr
                      : null
                }
                hint="WGS-84 decimal degrees, -180 to 180."
              >
                <input
                  id="venue-geo-lng"
                  type="text"
                  inputMode="decimal"
                  value={geoLng}
                  onChange={(e) => {
                    setGeoLng(e.target.value);
                    if (serverErrors.geo_lng !== undefined) {
                      setServerErrors({ ...serverErrors, geo_lng: undefined });
                    }
                  }}
                  style={inputStyle}
                  maxLength={16}
                  data-testid="venues-form-geo-lng"
                />
              </FieldRow>
            </div>
            {geoLat.trim() !== "" && geoLng.trim() !== "" && latErr === null && lngErr === null ? (
              <div style={mapPreviewStyle} data-testid="venues-form-map-preview">
                <strong>Map pin preview</strong>
                <span>Drag is not required: adjust latitude and longitude above to move the pin.</span>
                <iframe
                  title="Venue location preview"
                  style={mapFrameStyle}
                  src={`https://www.openstreetmap.org/export/embed.html?layer=mapnik&marker=${encodeURIComponent(`${geoLat},${geoLng}`)}#map=16/${encodeURIComponent(geoLat)}/${encodeURIComponent(geoLng)}`}
                />
              </div>
            ) : null}
          </fieldset>

          <FieldRow
            label="Timezone (IANA)"
            htmlFor="venue-timezone"
            error={serverErrors.timezone ?? null}
            localError={timezone.length > 0 ? tzErr : null}
            hint="Type to autocomplete. Server validates via time.LoadLocation."
          >
            <input
              id="venue-timezone"
              type="text"
              value={timezone}
              onChange={(e) => {
                setTimezone(e.target.value);
                if (serverErrors.timezone !== undefined) {
                  setServerErrors({ ...serverErrors, timezone: undefined });
                }
              }}
              style={inputMonoStyle}
              list={tzListId}
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              data-testid="venues-form-timezone"
            />
            <datalist id={tzListId} data-testid="venues-form-timezone-list">
              {tzOptions.map((tz) => (
                <option key={tz} value={tz} />
              ))}
            </datalist>
          </FieldRow>

          <fieldset style={fieldsetStyle}>
            <legend style={legendStyle}>Contacts</legend>
            <FieldRow
              label="Contact phone"
              htmlFor="venue-phone"
              error={serverErrors.contact_phone ?? null}
              localError={phone.length > 0 ? phoneErr : null}
              hint="Operator-facing. Optional."
            >
              <input
                id="venue-phone"
                type="tel"
                value={phone}
                onChange={(e) => {
                  setPhone(e.target.value);
                  if (serverErrors.contact_phone !== undefined) {
                    setServerErrors({
                      ...serverErrors,
                      contact_phone: undefined,
                    });
                  }
                }}
                style={inputStyle}
                maxLength={40}
                data-testid="venues-form-phone"
              />
            </FieldRow>
            <FieldRow
              label="Contact email"
              htmlFor="venue-email"
              error={serverErrors.contact_email ?? null}
              localError={email.length > 0 ? emailErr : null}
              hint="Operator-facing. Optional."
            >
              <input
                id="venue-email"
                type="email"
                value={email}
                onChange={(e) => {
                  setEmail(e.target.value);
                  if (serverErrors.contact_email !== undefined) {
                    setServerErrors({
                      ...serverErrors,
                      contact_email: undefined,
                    });
                  }
                }}
                style={inputStyle}
                maxLength={320}
                autoCapitalize="off"
                autoCorrect="off"
                spellCheck={false}
                data-testid="venues-form-email"
              />
            </FieldRow>
            <FieldRow
              label="Website"
              htmlFor="venue-website"
              error={serverErrors.website_url ?? null}
              localError={website.length > 0 ? websiteErr : null}
              hint="Full URL including https://"
            >
              <input
                id="venue-website"
                type="url"
                value={website}
                onChange={(e) => {
                  setWebsite(e.target.value);
                  if (serverErrors.website_url !== undefined) {
                    setServerErrors({
                      ...serverErrors,
                      website_url: undefined,
                    });
                  }
                }}
                style={inputStyle}
                maxLength={500}
                autoCapitalize="off"
                autoCorrect="off"
                spellCheck={false}
                data-testid="venues-form-website"
              />
            </FieldRow>
          </fieldset>

          <FieldRow
            label="Default capacity"
            htmlFor="venue-capacity"
            error={serverErrors.capacity_default ?? null}
            localError={capacity.length > 0 ? capacityErr : null}
            hint="Optional whole number. Leave blank if unknown."
          >
            <input
              id="venue-capacity"
              type="number"
              value={capacity}
              onChange={(e) => {
                setCapacity(e.target.value);
                if (serverErrors.capacity_default !== undefined) {
                  setServerErrors({
                    ...serverErrors,
                    capacity_default: undefined,
                  });
                }
              }}
              style={inputStyle}
              min={0}
              step={1}
              data-testid="venues-form-capacity"
            />
          </FieldRow>

          {serverErrors.form !== undefined ? (
            <div style={formErrorStyle} role="alert" data-testid="venues-form-error">
              {serverErrors.form}
            </div>
          ) : null}

          <div style={mobileFormBarStyle} data-testid="venues-form-actions">
            <button
              type="button"
              onClick={onClose}
              style={secondaryButtonStyle}
              data-testid="venues-form-cancel"
            >
              Cancel
            </button>
            <button
              type="submit"
              style={primaryButtonStyle}
              disabled={!localValid || !dirty || mutation.isPending}
              data-testid="venues-form-submit"
            >
              {mutation.isPending ? "Saving…" : submitLabel}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

function FieldRow({
  label,
  htmlFor,
  error,
  localError,
  hint,
  children,
}: {
  label: string;
  htmlFor: string;
  error: string | null;
  localError: string | null;
  hint: ReactNode;
  children: ReactNode;
}) {
  const visibleError = error ?? localError;
  return (
    <div style={fieldRowStyle}>
      <label htmlFor={htmlFor} style={fieldLabelStyle}>
        {label}
      </label>
      {children}
      {visibleError !== null ? (
        <div style={fieldErrorStyle} role="alert" data-testid={`${htmlFor}-error`}>
          {visibleError}
        </div>
      ) : (
        <div style={fieldHintStyle}>{hint}</div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Delete confirm dialog (soft-delete on the backend)
// ---------------------------------------------------------------------------

function DeleteConfirmDialog({
  venue,
  onClose,
}: {
  venue: Venue;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [error, setError] = useState<ApiError | null>(null);

  const mutation = useMutation<unknown, ApiError, void>({
    mutationFn: () =>
      authedFetch<unknown>({
        method: "DELETE",
        path: `/v1/organizations/${venue.org_id}/venues/${venue.id}`,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["venues"] });
      onClose();
    },
    onError: (err) => setError(err),
  });

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="venues-delete-title"
      style={dialogBackdropStyle}
      data-testid="venues-delete-dialog"
    >
      <div style={dialogStyle}>
        <header style={dialogHeaderStyle}>
          <h2 id="venues-delete-title" style={dialogTitleStyle}>
            Archive {venue.name}?
          </h2>
          <button
            type="button"
            onClick={onClose}
            style={dialogCloseStyle}
            aria-label="Close"
            data-testid="venues-delete-close"
          >
            ×
          </button>
        </header>
        <div style={archiveBodyStyle}>
          <p style={archiveParaStyle}>
            Archiving a venue removes it from the active list. This is a{" "}
            <strong>soft-delete</strong>: the row is preserved with{" "}
            <code style={monoStyle}>deleted_at</code> set and a{" "}
            <code style={monoStyle}>v1.venue.delete</code> audit event is
            written atomically. Existing events that reference this venue
            keep their reference.
          </p>
          {error !== null ? (
            <div style={formErrorStyle} role="alert" data-testid="venues-delete-error">
              {error.message} (<code style={monoStyle}>{error.code}</code>)
            </div>
          ) : null}
          <div style={formActionsStyle}>
            <button
              type="button"
              onClick={onClose}
              style={secondaryButtonStyle}
              data-testid="venues-delete-cancel"
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={() => mutation.mutate()}
              style={dangerButtonStyle}
              disabled={mutation.isPending}
              data-testid="venues-delete-confirm"
            >
              {mutation.isPending ? "Archiving…" : "Archive"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Server error mapping
// ---------------------------------------------------------------------------

/**
 * Map an envelope from venues.go onto field-level errors. The handler
 * emits `details.field = "name" | "city_id"` for invalid_name /
 * invalid_city_id; duplicate is unconditionally a name-field error
 * (uniqueness is per (org_id, name)). V-1 / feature #258 added the
 * structured-address codes (invalid_country, invalid_postal_code,
 * invalid_address_line, invalid_geo, invalid_timezone, invalid_email,
 * invalid_phone, invalid_website, invalid_status) — each carries
 * `details.field` so the handler can grow new codes without touching
 * this mapper.
 */
export function mapServerError(err: ApiError): ServerFieldErrors {
  const out: ServerFieldErrors = {};
  const field =
    err.details !== undefined && typeof err.details.field === "string"
      ? err.details.field
      : undefined;
  switch (err.code) {
    case "venue.invalid_name":
      out.name = err.message;
      return out;
    case "venue.invalid_city_id":
      out.city_id = err.message;
      return out;
    case "venue.duplicate":
      out.name = err.message;
      return out;
    case "venue.invalid_country":
      out.country = err.message;
      return out;
    case "venue.invalid_postal_code":
      out.postal_code = err.message;
      return out;
    case "venue.invalid_address_line":
      if (field === "address_line2") {
        out.address_line2 = err.message;
      } else {
        out.address_line1 = err.message;
      }
      return out;
    case "venue.invalid_geo":
    case "venue.invalid_geo_lat":
      out.geo_lat = err.message;
      return out;
    case "venue.invalid_geo_lng":
      out.geo_lng = err.message;
      return out;
    case "venue.invalid_timezone":
      out.timezone = err.message;
      return out;
    case "venue.invalid_email":
    case "venue.invalid_contact_email":
      out.contact_email = err.message;
      return out;
    case "venue.invalid_phone":
    case "venue.invalid_contact_phone":
      out.contact_phone = err.message;
      return out;
    case "venue.invalid_website":
    case "venue.invalid_website_url":
      out.website_url = err.message;
      return out;
    case "venue.invalid_status":
      out.status = err.message;
      return out;
    case "venue.not_found":
      out.form = err.message;
      return out;
    case "venue.empty_body":
    case "venue.invalid_body":
    case "venue.invalid_json":
      out.form = err.message;
      return out;
    case "permissions.denied":
      out.form =
        "Your account is missing the required permission. Ask a platform administrator.";
      return out;
    case "superadmin.missing_reason":
    case "superadmin.reason_required":
      out.form =
        "An audit reason is required for cross-tenant changes. Provide a reason in the prompt and retry.";
      return out;
    default:
      if (field !== undefined && FIELD_KEYS.has(field)) {
        (out as Record<string, string>)[field] = err.message;
      } else {
        out.form = `${err.message} (${err.code})`;
      }
      return out;
  }
}

const FIELD_KEYS: ReadonlySet<string> = new Set([
  "name",
  "org_id",
  "city_id",
  "address",
  "address_line1",
  "address_line2",
  "postal_code",
  "country",
  "geo_lat",
  "geo_lng",
  "timezone",
  "contact_phone",
  "contact_email",
  "website_url",
  "status",
  "capacity_default",
]);

// ---------------------------------------------------------------------------
// Request body builders
//
// Pulled out of the dialog so we can unit-test them without rendering.
// Trim semantics: empty optional strings serialise as `null` on PATCH so
// the operator can clear a field; on POST they are simply omitted so the
// server can apply its defaults.
// ---------------------------------------------------------------------------

export interface VenueFormState {
  readonly name: string;
  readonly cityID: string;
  readonly addressLine1: string;
  readonly addressLine2: string;
  readonly postalCode: string;
  readonly country: string;
  readonly geoLat: string;
  readonly geoLng: string;
  readonly timezone: string;
  readonly phone: string;
  readonly email: string;
  readonly website: string;
  readonly status: VenueStatus;
  readonly capacity: string;
}

export function buildCreateVenueBody(
  s: VenueFormState,
): Record<string, unknown> {
  const body: Record<string, unknown> = {
    name: s.name.trim(),
    city_id: s.cityID.trim(),
    status: s.status,
  };
  setIfFilled(body, "address_line1", s.addressLine1);
  setIfFilled(body, "address_line2", s.addressLine2);
  setIfFilled(body, "postal_code", s.postalCode);
  const c = normalizeCountry(s.country);
  if (c !== "") {
    body.country = c;
  }
  if (s.geoLat.trim() !== "") {
    body.geo_lat = Number(s.geoLat);
  }
  if (s.geoLng.trim() !== "") {
    body.geo_lng = Number(s.geoLng);
  }
  setIfFilled(body, "timezone", s.timezone);
  setIfFilled(body, "contact_phone", s.phone);
  setIfFilled(body, "contact_email", s.email);
  setIfFilled(body, "website_url", s.website);
  if (s.capacity.trim() !== "") {
    body.capacity_default = Number(s.capacity);
  }
  return body;
}

export function buildUpdateVenueBody(
  next: VenueFormState,
  prev: VenueFormState,
): Record<string, unknown> {
  const body: Record<string, unknown> = {};
  if (next.name.trim() !== prev.name) {
    body.name = next.name.trim();
  }
  if (next.cityID.trim() !== prev.cityID) {
    body.city_id = next.cityID.trim();
  }
  diffOptionalString(body, "address_line1", next.addressLine1, prev.addressLine1);
  diffOptionalString(body, "address_line2", next.addressLine2, prev.addressLine2);
  diffOptionalString(body, "postal_code", next.postalCode, prev.postalCode);
  const nextCountry = normalizeCountry(next.country);
  if (nextCountry !== prev.country) {
    body.country = nextCountry === "" ? null : nextCountry;
  }
  if (next.geoLat.trim() !== prev.geoLat) {
    body.geo_lat = next.geoLat.trim() === "" ? null : Number(next.geoLat);
  }
  if (next.geoLng.trim() !== prev.geoLng) {
    body.geo_lng = next.geoLng.trim() === "" ? null : Number(next.geoLng);
  }
  diffOptionalString(body, "timezone", next.timezone, prev.timezone);
  diffOptionalString(body, "contact_phone", next.phone, prev.phone);
  diffOptionalString(body, "contact_email", next.email, prev.email);
  diffOptionalString(body, "website_url", next.website, prev.website);
  if (next.status !== prev.status) {
    body.status = next.status;
  }
  if (next.capacity.trim() !== prev.capacity) {
    body.capacity_default =
      next.capacity.trim() === "" ? null : Number(next.capacity);
  }
  return body;
}

function setIfFilled(
  body: Record<string, unknown>,
  key: string,
  raw: string,
): void {
  const trimmed = raw.trim();
  if (trimmed !== "") {
    body[key] = trimmed;
  }
}

function diffOptionalString(
  body: Record<string, unknown>,
  key: string,
  nextRaw: string,
  prevRaw: string,
): void {
  const trimmed = nextRaw.trim();
  if (trimmed === prevRaw) {
    return;
  }
  body[key] = trimmed === "" ? null : trimmed;
}

// ---------------------------------------------------------------------------
// Format helpers
// ---------------------------------------------------------------------------

function formatDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return iso;
  }
  return d.toISOString().slice(0, 10);
}

function shortenUUID(id: string): string {
  return id.length > 8 ? `${id.slice(0, 8)}…` : id;
}

// ---------------------------------------------------------------------------
// Styles (mirror networks.tsx)
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

const secondaryButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
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

const rowActionButtonStyle: CSSProperties = {
  fontSize: 11,
  padding: "4px 10px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

const rowDangerButtonStyle: CSSProperties = {
  fontSize: 11,
  padding: "4px 10px",
  background: "#ffffff",
  border: "1px solid #fca5a5",
  borderRadius: 4,
  cursor: "pointer",
  color: "#7f1d1d",
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

const dialogBackdropStyle: CSSProperties = {
  position: "fixed",
  inset: 0,
  background: "rgba(15, 23, 42, 0.4)",
  display: "flex",
  alignItems: "center",
  justifyContent: "center",
  padding: 16,
  zIndex: 100,
};

const dialogStyle: CSSProperties = {
  background: "#ffffff",
  borderRadius: 8,
  border: "1px solid #e2e8f0",
  boxShadow: "0 10px 25px rgba(15, 23, 42, 0.2)",
  width: "min(520px, 100%)",
  maxHeight: "90vh",
  overflowY: "auto",
};

const dialogHeaderStyle: CSSProperties = {
  display: "flex",
  alignItems: "center",
  justifyContent: "space-between",
  padding: "12px 16px",
  borderBottom: "1px solid #e2e8f0",
};

const dialogTitleStyle: CSSProperties = {
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

// V-3 mobile contract: forms collapse to a single column < md and host
// their Save / Cancel row inside the shared mobileFormBarStyle.
const formStyle: CSSProperties = {
  ...singleColumnFormStyle,
  gap: 16,
  padding: 16,
};

const fieldRowStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
};

const fieldLabelStyle: CSSProperties = {
  fontSize: 12,
  fontWeight: 600,
  color: "#334155",
};

const inputStyle: CSSProperties = {
  fontSize: 13,
  padding: "8px 10px",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  background: "#ffffff",
  color: "#0f172a",
};

const inputMonoStyle: CSSProperties = {
  ...inputStyle,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
};

const fieldHintStyle: CSSProperties = {
  fontSize: 11,
  color: "#64748b",
  lineHeight: 1.4,
};

const fieldErrorStyle: CSSProperties = {
  fontSize: 11,
  color: "#b91c1c",
  fontWeight: 500,
};

const formErrorStyle: CSSProperties = {
  fontSize: 12,
  padding: 8,
  background: "#fef2f2",
  border: "1px solid #fca5a5",
  color: "#7f1d1d",
  borderRadius: 4,
};

const formActionsStyle: CSSProperties = {
  display: "flex",
  justifyContent: "flex-end",
  gap: 8,
};

const archiveBodyStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 12,
  padding: 16,
};

const archiveParaStyle: CSSProperties = {
  margin: 0,
  fontSize: 13,
  color: "#334155",
  lineHeight: 1.5,
};

const monoStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
};

const fieldsetStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 12,
  margin: 0,
  padding: "12px 14px 14px",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#f8fafc",
};

const legendStyle: CSSProperties = {
  padding: "0 6px",
  fontSize: 11,
  fontWeight: 600,
  color: "#475569",
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const twoColRowStyle: CSSProperties = {
  display: "grid",
  gridTemplateColumns: "1fr 1fr",
  gap: 12,
};

const searchRowStyle: CSSProperties = {
  display: "flex",
  gap: 8,
  alignItems: "center",
};

const geocodeResultsStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  marginTop: 4,
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  background: "#ffffff",
  padding: 4,
};

const geocodeResultButtonStyle: CSSProperties = {
  border: "none",
  background: "transparent",
  textAlign: "left",
  cursor: "pointer",
  padding: "7px 8px",
  borderRadius: 3,
  color: "#0f172a",
  fontSize: 12,
};

const inlineCityStyle: CSSProperties = {
  display: "flex",
  gap: 8,
  alignItems: "center",
  flexWrap: "wrap",
  marginTop: 6,
  fontSize: 11,
  color: "#475569",
};

const inlineCreateButtonStyle: CSSProperties = {
  fontSize: 11,
  padding: "3px 8px",
  border: "1px dashed #94a3b8",
  borderRadius: 3,
  background: "transparent",
  color: "#475569",
  cursor: "pointer",
  whiteSpace: "nowrap",
};

const inlineCreateFormStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 6,
  marginTop: 6,
  padding: "8px 10px",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  background: "#f8fafc",
};

const mapPreviewStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 6,
  padding: 10,
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  fontSize: 11,
  color: "#475569",
  background: "#ffffff",
};

const mapFrameStyle: CSSProperties = {
  width: "100%",
  height: 220,
  border: 0,
  borderRadius: 3,
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
