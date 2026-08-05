/**
 * Venue Seating-plans drawer (feature #315, Wave SEAT-E1).
 *
 * The venues list already ships a create/edit modal; this module adds a
 * separate "Seating plans" drawer opened from the row actions column. It
 * hosts a tabbed UI:
 *
 *   - Details tab (read-only summary of the venue)
 *   - Seating plans tab (default): list of plans attached to the venue,
 *     an SVG uploader that POSTs to /v1/seating-plans/{id}/versions and
 *     surfaces 422 per-element errors inline, a client-side geometry
 *     preview renderer, plus Fork / Archive row actions.
 *
 * Backend contract (see 09_autoforge/seating_backlog.md §5 + openapi.yaml):
 *
 *   GET  /v1/venues/{venue_id}/seating-plans                (SEAT-A3 list)
 *   POST /v1/venues/{venue_id}/seating-plans                (SEAT-A3 create)
 *   PATCH  /v1/seating-plans/{id}          (archive via status="archived")
 *   POST /v1/seating-plans/{id}/fork                        (SEAT-A3 fork)
 *   POST /v1/seating-plans/{id}/versions                    (SEAT-A3 upload)
 *   GET  /v1/seating-plans/{id}/versions      (AB-25c history + preview src)
 *   GET  /v1/media/{id}              (signed URL for the stored SVG asset)
 *
 * 422 error shape from POST versions:
 *
 *   { code: "seating_plan.version_validation_failed",
 *     details: { errors: [{ code, element?, detail? }, ...] } }
 *
 * No mock data. All list / mutation actions hit the live backend. Pure
 * helpers (renderGeometryToSVG, parseVersionValidationErrors, ...) are
 * exported for unit tests.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  useMemo,
  useState,
  type CSSProperties,
  type ChangeEvent,
  type ReactNode,
} from "react";
import {
  ApiError,
  authedFetch,
  uploadMedia,
  type MediaOwnerType,
} from "@/lib/api/client";
import {
  OWNER_TYPE_MAX_BYTES,
  acceptedExtensionsLabel,
  formatBytes,
  validateFile,
} from "@/components/ImageUpload";
import { config } from "@/lib/config";
import { ResponsiveDrawer } from "@/components/layout";
import type { Venue } from "./venues";

/**
 * media_objects.owner_type discriminator for seating-plan authoring assets
 * (migration 0078). Pinned to a constant so the upload call, the client-side
 * validator, and the hint copy cannot drift apart.
 */
const SEATING_PLAN_SVG_OWNER_TYPE: MediaOwnerType = "seating_plan_svg";

// ---------------------------------------------------------------------------
// Wire types (mirror openapi/clients/ts/index.d.ts)
// ---------------------------------------------------------------------------

export type SeatingPlanType =
  | "assigned_seats"
  | "general_admission"
  | "tables"
  | "mixed";

export type SeatingPlanVisibility =
  | "private"
  | "shared_read"
  | "public_template"
  | "operator_verified";

export type SeatingPlanStatus = "draft" | "active" | "archived";

export interface SeatingPlan {
  readonly id: string;
  readonly venue_id: string;
  readonly owner_org_id: string;
  readonly name: string;
  readonly plan_type: SeatingPlanType;
  readonly visibility: SeatingPlanVisibility;
  readonly status: SeatingPlanStatus;
  readonly source_seating_plan_id: string | null;
  readonly current_version_id: string | null;
  /**
   * 1-based positional number of the version current_version_id points
   * at (null until the first version exists). Optional because servers
   * predating the field omit it — see resolveCurrentVersionNumber.
   */
  readonly current_version_number?: number | null;
  /**
   * AB-40 A3: display-name overrides keyed by category index ("1".."15")
   * applied over geometry category names — renaming never re-imports.
   */
  readonly category_name_overrides?: Readonly<Record<string, string>>;
  readonly created_at: string;
  readonly updated_at: string;
}

interface SeatingPlanListEnvelope {
  readonly seating_plans: readonly SeatingPlan[];
}
interface SeatingPlanEnvelope {
  readonly seating_plan: SeatingPlan;
}

export interface SeatingPlanVersion {
  readonly id: string;
  readonly seating_plan_id: string;
  readonly version_number: number;
  readonly geometry: SeatingGeometry;
  readonly geometry_checksum: string;
  readonly svg_asset_media_id: string | null;
  readonly capacity_seated: number;
  readonly capacity_standing: number;
  readonly locked_at: string | null;
  readonly created_at: string;
}
interface CreateVersionResponse {
  readonly seating_plan: SeatingPlan;
  readonly seating_plan_version: SeatingPlanVersion;
  readonly warnings?: readonly SeatingImportIssue[];
}

// ---------------------------------------------------------------------------
// Client-side geometry model (matches domain/seating.Geometry §5.3)
// ---------------------------------------------------------------------------

export interface SeatingCanvas {
  readonly width: number;
  readonly height: number;
}
export interface SeatingCategory {
  readonly index: number;
  readonly name: string;
  readonly color: string;
  /** Price hint imported from the SVG (minor units as text), if any. */
  readonly price_hint?: string;
  /**
   * AB-40: "" / "seated" — seats bind by colour; "general_admission" —
   * the category carries its own bulk capacity and no seats.
   */
  readonly kind?: "" | "seated" | "general_admission";
  /** Declared bulk capacity of a GA category (absent for seated). */
  readonly capacity?: number;
}
export interface SeatingSeat {
  readonly key: string;
  readonly number: string;
  readonly x: number;
  readonly y: number;
  readonly radius: number;
  readonly category_index: number;
}
export interface SeatingRow {
  readonly key: string;
  readonly name: string;
  readonly seats: readonly SeatingSeat[];
}
export interface SeatingSection {
  readonly key: string;
  readonly name: string;
  readonly rows: readonly SeatingRow[];
}
export interface SeatingGeometry {
  readonly schema_version?: number;
  readonly canvas?: SeatingCanvas;
  readonly categories?: readonly SeatingCategory[];
  readonly sections?: readonly SeatingSection[];
  readonly decor_svg?: string;
}

export interface SeatingImportIssue {
  readonly code: string;
  readonly element?: string;
  readonly detail?: string;
}

// Organization summary used by the owner-org picker in CreatePlanForm.
export interface OrganizationSummary {
  readonly id: string;
  readonly name: string;
}
interface OrganizationListEnvelope {
  readonly organizations: readonly OrganizationSummary[];
}

// Version list returned by GET /v1/seating-plans/{id}/versions (AB-25).
interface SeatingPlanVersionListEnvelope {
  readonly seating_plan_versions: readonly SeatingPlanVersion[];
}

// ---------------------------------------------------------------------------
// Pure helpers (exported for tests)
// ---------------------------------------------------------------------------

const DEFAULT_SEAT_COLOR = "#94a3b8";

/**
 * Render a canonical geometry object into an inline SVG string suitable
 * for injection via dangerouslySetInnerHTML. Deliberately restricted to
 * primitives the backend importer emits (circle seats) with every
 * interpolated string escaped, so no server- or author-supplied markup
 * ever reaches the DOM. decor_svg is intentionally NEVER emitted: raw
 * decor markup injected through dangerouslySetInnerHTML would be an XSS
 * channel, and pre-render sanitisation is out of scope for this preview.
 */
export function renderGeometryToSVG(g: SeatingGeometry): string {
  const width = g.canvas?.width ?? 800;
  const height = g.canvas?.height ?? 600;
  const catByIndex = new Map<number, string>();
  for (const c of g.categories ?? []) {
    if (typeof c.color === "string" && c.color !== "") {
      catByIndex.set(c.index, c.color);
    }
  }
  const seatCircles: string[] = [];
  for (const sec of g.sections ?? []) {
    for (const row of sec.rows) {
      for (const seat of row.seats) {
        const color = catByIndex.get(seat.category_index) ?? DEFAULT_SEAT_COLOR;
        seatCircles.push(
          `<circle cx="${round(seat.x)}" cy="${round(seat.y)}" r="${round(
            seat.radius,
          )}" fill="${escapeAttr(color)}" data-seat-key="${escapeAttr(
            seat.key,
          )}"><title>${escapeText(seat.key)}</title></circle>`,
        );
      }
    }
  }
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${round(width)} ${round(
      height,
    )}" role="img" aria-label="Seating plan preview">` +
    `<g data-role="seats">${seatCircles.join("")}</g>` +
    `</svg>`
  );
}

function round(n: number): number {
  if (!Number.isFinite(n)) return 0;
  return Math.round(n * 100) / 100;
}

function escapeAttr(v: string): string {
  return v
    .replace(/&/g, "&amp;")
    .replace(/"/g, "&quot;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}
function escapeText(v: string): string {
  return v.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

/**
 * Extract per-element validation errors from a 422 ApiError raised by
 * POST /v1/seating-plans/{id}/versions. The server sends them under
 * details.errors[]; malformed shapes are dropped rather than throwing.
 */
export function parseVersionValidationErrors(
  err: ApiError,
): readonly SeatingImportIssue[] {
  if (err.status !== 422) return [];
  const raw = err.details?.errors;
  if (!Array.isArray(raw)) return [];
  const out: SeatingImportIssue[] = [];
  for (const e of raw) {
    if (e === null || typeof e !== "object") continue;
    const rec = e as Record<string, unknown>;
    const code = typeof rec.code === "string" ? rec.code : null;
    if (code === null) continue;
    const element = typeof rec.element === "string" ? rec.element : undefined;
    const detail = typeof rec.detail === "string" ? rec.detail : undefined;
    const issue: SeatingImportIssue = { code, element, detail };
    out.push(issue);
  }
  return out;
}

/**
 * Read a File as a UTF-8 text string. Returns a Promise so the caller
 * can await it in a mutation. Wraps FileReader in a stable Promise to
 * avoid the callback-style event API.
 */
export function readFileAsText(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      const v = reader.result;
      if (typeof v === "string") {
        resolve(v);
        return;
      }
      reject(new Error("expected text result from FileReader"));
    };
    reader.onerror = () => reject(reader.error ?? new Error("FileReader error"));
    reader.readAsText(file);
  });
}

/** Convert a status to an operator-facing label. */
export function statusLabel(s: SeatingPlanStatus): string {
  return s;
}

/**
 * Returns true when a plan type supports General Admission (GA) capacity.
 * GA types: general_admission, mixed. Non-GA types: assigned_seats, tables.
 */
export function planHasGeneralAdmission(planType: SeatingPlanType): boolean {
  return planType === "general_admission" || planType === "mixed";
}

/** Field discriminator for create-plan validation issues. */
export type CreatePlanField = "name" | "owner_org_id" | "ga_categories";

export interface CreatePlanFormIssue {
  readonly field: CreatePlanField;
  readonly message: string;
}

/**
 * Collect EVERY unmet requirement of the create-plan form, in field order.
 *
 * Returning the full list (rather than the first failure) is what lets the
 * form explain itself: repo UI guidance forbids a silently disabled submit
 * button, so the operator must be able to see all the reasons the form is
 * not ready at once instead of fixing them one round-trip at a time.
 */
export function createPlanFormIssues(
  name: string,
  ownerOrgID: string,
): readonly CreatePlanFormIssue[] {
  const issues: CreatePlanFormIssue[] = [];
  if (name.trim() === "") {
    issues.push({ field: "name", message: "Plan name is required." });
  }
  if (ownerOrgID.trim() === "") {
    issues.push({
      field: "owner_org_id",
      message: "Owner organization is required.",
    });
  }
  return issues;
}

/** First issue attached to `field`, or null when that field is satisfied. */
export function issueForField(
  issues: readonly CreatePlanFormIssue[],
  field: CreatePlanField,
): string | null {
  return issues.find((i) => i.field === field)?.message ?? null;
}

/**
 * Validate the create-plan form inputs. Returns a human-readable error
 * string when invalid, or null when the form is ready to submit. Retained
 * as the single-message projection of createPlanFormIssues for callers that
 * only need a yes/no answer. Exported for Vitest coverage.
 */
export function validateCreatePlanForm(name: string, ownerOrgID: string): string | null {
  return createPlanFormIssues(name, ownerOrgID)[0]?.message ?? null;
}

/**
 * Resolve which /versions/{n} slot holds the plan's CURRENT version.
 * Returns null when the plan has no version yet (nothing to preview).
 * Falls back to 1 when the server payload predates the
 * current_version_number field — versions are append-only and version 1
 * always exists once current_version_id is set, so the fallback renders
 * the oldest geometry rather than nothing.
 */
export function resolveCurrentVersionNumber(plan: SeatingPlan): number | null {
  if (plan.current_version_id === null) return null;
  const n = plan.current_version_number;
  if (typeof n === "number" && Number.isInteger(n) && n >= 1) return n;
  return 1;
}

// ---------------------------------------------------------------------------
// Drawer component
// ---------------------------------------------------------------------------

export interface VenueSeatingPlansDrawerProps {
  readonly venue: Venue;
  readonly open: boolean;
  readonly onClose: () => void;
  /** Escape hatch used by tests to bypass matchMedia. */
  readonly forceLayout?: "desktop" | "mobile";
}

type TabKey = "details" | "plans";

export function VenueSeatingPlansDrawer({
  venue,
  open,
  onClose,
  forceLayout,
}: VenueSeatingPlansDrawerProps): JSX.Element | null {
  const [tab, setTab] = useState<TabKey>("plans");
  return (
    <ResponsiveDrawer
      open={open}
      onClose={onClose}
      title={venue.name}
      subtitle={`Venue • ${venue.id}`}
      id="venues-plans-drawer"
      forceLayout={forceLayout}
    >
      <div style={tabsBarStyle} role="tablist" aria-label="Venue detail tabs">
        <TabButton
          active={tab === "details"}
          onClick={() => setTab("details")}
          testId="venues-plans-tab-details"
        >
          Details
        </TabButton>
        <TabButton
          active={tab === "plans"}
          onClick={() => setTab("plans")}
          testId="venues-plans-tab-plans"
        >
          Seating plans
        </TabButton>
      </div>
      {tab === "details" ? (
        <VenueDetailsPanel venue={venue} />
      ) : (
        <SeatingPlansPanel venue={venue} />
      )}
    </ResponsiveDrawer>
  );
}

function TabButton({
  active,
  onClick,
  testId,
  children,
}: {
  active: boolean;
  onClick: () => void;
  testId: string;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      onClick={onClick}
      style={active ? tabButtonActiveStyle : tabButtonStyle}
      data-testid={testId}
    >
      {children}
    </button>
  );
}

function VenueDetailsPanel({ venue }: { venue: Venue }) {
  return (
    <dl style={detailListStyle} data-testid="venues-plans-details">
      <DetailRow label="Organization" value={venue.org_id} mono />
      <DetailRow
        label="Country"
        value={
          venue.country !== null && venue.country !== undefined && venue.country !== ""
            ? venue.country
            : "—"
        }
      />
      <DetailRow
        label="City ID"
        value={venue.city_id ?? "—"}
        mono={venue.city_id !== null}
      />
      <DetailRow
        label="Capacity"
        value={
          venue.capacity_default !== null
            ? venue.capacity_default.toLocaleString()
            : "—"
        }
      />
      <DetailRow label="Status" value={venue.status ?? "active"} />
    </dl>
  );
}

function DetailRow({
  label,
  value,
  mono,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <>
      <dt style={detailLabelStyle}>{label}</dt>
      <dd style={mono === true ? detailValueMonoStyle : detailValueStyle}>
        {value}
      </dd>
    </>
  );
}

// ---------------------------------------------------------------------------
// Seating plans panel
// ---------------------------------------------------------------------------

function SeatingPlansPanel({ venue }: { venue: Venue }) {
  const query = useQuery<SeatingPlanListEnvelope, ApiError>({
    queryKey: ["seating-plans", "by-venue", venue.id],
    queryFn: () =>
      authedFetch<SeatingPlanListEnvelope>({
        method: "GET",
        path: `/v1/venues/${venue.id}/seating-plans`,
      }),
    enabled: true,
    retry: (failureCount, err) => {
      if (err instanceof ApiError && (err.status === 401 || err.status === 403)) {
        return false;
      }
      return failureCount < 2;
    },
    refetchOnWindowFocus: false,
  });

  const [selectedPlanID, setSelectedPlanID] = useState<string | null>(null);

  const plans = query.data?.seating_plans ?? [];

  return (
    <div style={panelStyle}>
      {query.isPending ? (
        <div style={statusBoxStyle} role="status">
          Loading seating plans…
        </div>
      ) : query.isError ? (
        <div style={errorBoxStyle} role="alert" data-testid="venues-plans-error">
          <strong>Failed to load seating plans.</strong>
          <div style={errorCodeStyle}>{query.error?.code ?? "unknown.error"}</div>
          {query.error?.message ? (
            <div style={errorParaStyle}>{query.error.message}</div>
          ) : null}
          <button
            type="button"
            style={secondaryButtonStyle}
            onClick={() => query.refetch()}
          >
            Retry
          </button>
        </div>
      ) : plans.length === 0 ? (
        <div style={statusBoxStyle} role="status" data-testid="venues-plans-empty">
          No seating plans yet. Create the first plan below.
        </div>
      ) : (
        <ul style={planListStyle} data-testid="venues-plans-list">
          {plans.map((p) => (
            <SeatingPlanRow
              key={p.id}
              plan={p}
              venue={venue}
              selected={selectedPlanID === p.id}
              onSelect={() =>
                setSelectedPlanID(selectedPlanID === p.id ? null : p.id)
              }
            />
          ))}
        </ul>
      )}
      <CreatePlanForm venue={venue} />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Single-plan row: header + expandable body (upload / preview / actions)
// ---------------------------------------------------------------------------

function SeatingPlanRow({
  plan,
  venue,
  selected,
  onSelect,
}: {
  plan: SeatingPlan;
  venue: Venue;
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <li style={planRowStyle} data-testid={`venues-plan-${plan.id}`}>
      <button
        type="button"
        onClick={onSelect}
        style={planHeaderStyle}
        aria-expanded={selected}
        data-testid={`venues-plan-toggle-${plan.id}`}
      >
        <span style={planNameStyle}>{plan.name}</span>
        <span style={planMetaStyle}>
          {plan.plan_type} • {plan.status} • {plan.visibility}
        </span>
      </button>
      {selected ? <SeatingPlanBody plan={plan} venue={venue} /> : null}
    </li>
  );
}

function SeatingPlanBody({ plan, venue }: { plan: SeatingPlan; venue: Venue }) {
  // The version history and the preview address the same version, so the
  // selection lives here rather than in either child. `null` means "follow
  // the plan's current version", which is what a freshly-opened drawer — and
  // a drawer that has just published a new version — should show.
  const [selectedVersionID, setSelectedVersionID] = useState<string | null>(null);

  const versionsQuery = useQuery<SeatingPlanVersionListEnvelope, ApiError>({
    queryKey: ["seating-plan-versions", plan.id],
    queryFn: () =>
      authedFetch<SeatingPlanVersionListEnvelope>({
        method: "GET",
        path: `/v1/seating-plans/${plan.id}/versions`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  const versions = versionsQuery.data?.seating_plan_versions ?? [];
  const selectedVersion = resolveSelectedVersion(plan, versions, selectedVersionID);

  return (
    <div style={planBodyStyle}>
      {plan.plan_type === "general_admission" ? (
        // AB-40 C1: a GA-only plan needs no SVG at all — new versions
        // are hand-entered category tables.
        <GAVersionForm
          plan={plan}
          baseVersion={selectedVersion}
          onVersionCreated={() => setSelectedVersionID(null)}
        />
      ) : (
        <UploadSVGForm
          plan={plan}
          venue={venue}
          // Snap back to the (new) current version rather than leaving the
          // preview pinned to whichever historical version was selected.
          onVersionCreated={() => setSelectedVersionID(null)}
        />
      )}
      <div style={sectionStyle} data-testid={`venues-plan-versions-${plan.id}`}>
        <div style={sectionTitleStyle}>Version history</div>
        {versionsQuery.isPending ? (
          <div style={statusBoxStyle} role="status">
            Loading versions…
          </div>
        ) : versionsQuery.isError ? (
          <div style={errorBoxStyle} role="alert">
            <strong>Could not load version history.</strong>
            <div style={errorCodeStyle}>
              {versionsQuery.error?.code ?? "unknown.error"}
            </div>
          </div>
        ) : (
          <VersionHistoryTable
            versions={versions}
            currentVersionID={plan.current_version_id}
            selectedVersionID={selectedVersion?.id ?? null}
            onSelect={(id) => setSelectedVersionID(id)}
            hasGA={planHasGeneralAdmission(plan.plan_type)}
          />
        )}
      </div>
      <CategoryTablePanel plan={plan} version={selectedVersion} />
      <PlanPreviewSection version={selectedVersion} hasGA={planHasGeneralAdmission(plan.plan_type)} />
      <PlanActions plan={plan} venue={venue} />
    </div>
  );
}

/**
 * Decide which version the drawer previews.
 *
 * Explicit operator selection wins. Otherwise the plan's current version is
 * used, resolved first by `current_version_id` and — for server payloads that
 * predate that field being echoed — by `current_version_number`. The newest
 * row is the final fallback so a plan whose pointer is somehow unresolvable
 * still previews something rather than nothing.
 */
export function resolveSelectedVersion(
  plan: SeatingPlan,
  versions: readonly SeatingPlanVersion[],
  selectedVersionID: string | null,
): SeatingPlanVersion | null {
  if (versions.length === 0) return null;
  if (selectedVersionID !== null) {
    const picked = versions.find((v) => v.id === selectedVersionID);
    if (picked !== undefined) return picked;
  }
  const current = versions.find((v) => v.id === plan.current_version_id);
  if (current !== undefined) return current;
  const currentNumber = resolveCurrentVersionNumber(plan);
  if (currentNumber !== null) {
    const byNumber = versions.find((v) => v.version_number === currentNumber);
    if (byNumber !== undefined) return byNumber;
  }
  return [...versions].sort((a, b) => b.version_number - a.version_number)[0] ?? null;
}

/**
 * Render a version's created_at as a stable, locale-independent UTC stamp.
 * Admin operators compare these against server logs, so a fixed format beats
 * a browser-localised one.
 */
export function formatVersionTimestamp(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const s = d.toISOString();
  return `${s.slice(0, 10)} ${s.slice(11, 16)} UTC`;
}

// ---------------------------------------------------------------------------
// Category table (AB-40 C3) + GA-only version form (AB-40 C1)
// ---------------------------------------------------------------------------

/**
 * The Bil24 plan table — `Category | Seats | Starting price` — with GA
 * rows inline, plus per-category display-name renaming stored as the
 * plan's category_name_overrides (AB-40 A3: renaming never re-imports).
 */
function CategoryTablePanel({
  plan,
  version,
}: {
  plan: SeatingPlan;
  version: SeatingPlanVersion | null;
}) {
  const qc = useQueryClient();
  const [edits, setEdits] = useState<Record<string, string>>({});
  const [apiError, setApiError] = useState<ApiError | null>(null);

  const mutation = useMutation<SeatingPlanEnvelope, ApiError, void>({
    mutationFn: () => {
      const merged: Record<string, string> = {
        ...(plan.category_name_overrides ?? {}),
      };
      for (const [idx, v] of Object.entries(edits)) {
        if (v.trim() === "") {
          delete merged[idx];
        } else {
          merged[idx] = v.trim();
        }
      }
      return authedFetch<SeatingPlanEnvelope>({
        method: "PATCH",
        path: `/v1/seating-plans/${plan.id}`,
        body: { category_name_overrides: merged },
      });
    },
    onSuccess: () => {
      setApiError(null);
      setEdits({});
      qc.invalidateQueries({ queryKey: ["seating-plans", "by-venue", plan.venue_id] });
    },
    onError: (err) => setApiError(err),
  });

  const categories = version?.geometry.categories ?? [];
  if (version === null || categories.length === 0) return <></>;

  const dirty = Object.keys(edits).length > 0;
  return (
    <div style={sectionStyle} data-testid={`venues-plan-categories-${plan.id}`}>
      <div style={sectionTitleStyle}>Categories</div>
      <table style={gaEditorTableStyle}>
        <thead>
          <tr>
            <th style={gaEditorHeadStyle}>Category</th>
            <th style={gaEditorHeadStyle}>Kind</th>
            <th style={gaEditorHeadStyle}>Seats</th>
            <th style={gaEditorHeadStyle}>Starting price</th>
          </tr>
        </thead>
        <tbody>
          {categories.map((cat) => {
            const idxKey = String(cat.index);
            const effective = effectiveCategoryName(cat, plan.category_name_overrides);
            const value = edits[idxKey] ?? effective;
            return (
              <tr key={cat.index} data-testid={`venues-plan-category-row-${plan.id}-${cat.index}`}>
                <td style={gaEditorCellStyle}>
                  <input
                    type="text"
                    value={value}
                    onChange={(e) =>
                      setEdits((prev) => ({ ...prev, [idxKey]: e.target.value }))
                    }
                    style={inputStyle}
                    aria-label={`Display name for category ${cat.index}`}
                    data-testid={`venues-plan-category-name-${plan.id}-${cat.index}`}
                  />
                  {effective !== cat.name ? (
                    <div style={mutedHintTextStyle}>imported as “{cat.name}”</div>
                  ) : null}
                </td>
                <td style={gaEditorCellStyle}>
                  <span
                    style={{
                      display: "inline-block",
                      width: 10,
                      height: 10,
                      borderRadius: 5,
                      background: cat.color,
                      marginRight: 6,
                    }}
                  />
                  {cat.kind === "general_admission" ? "general admission" : "seated"}
                </td>
                <td style={gaEditorCellStyle} data-testid={`venues-plan-category-seats-${plan.id}-${cat.index}`}>
                  {categorySeatCount(cat, version.geometry).toLocaleString()}
                </td>
                <td style={gaEditorCellStyle}>{cat.price_hint ?? "—"}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {apiError !== null ? (
        <div style={errorBoxStyle} role="alert" data-testid={`venues-plan-categories-error-${plan.id}`}>
          <div style={errorCodeStyle}>{apiError.code}</div>
          <div style={errorParaStyle}>{apiError.message}</div>
        </div>
      ) : null}
      {dirty ? (
        <button
          type="button"
          style={primaryButtonStyle}
          disabled={mutation.isPending}
          onClick={() => mutation.mutate()}
          data-testid={`venues-plan-categories-save-${plan.id}`}
        >
          {mutation.isPending ? "Saving…" : "Save names"}
        </button>
      ) : null}
    </div>
  );
}

/**
 * New-version form for GA-only plans (AB-40 C1): the operator edits the
 * named-capacity table and publishes it as the next version — no SVG
 * involved. Pre-fills from the currently selected version.
 */
function GAVersionForm({
  plan,
  baseVersion,
  onVersionCreated,
}: {
  plan: SeatingPlan;
  baseVersion: SeatingPlanVersion | null;
  onVersionCreated: () => void;
}) {
  const qc = useQueryClient();
  const initial: readonly GACategoryDraft[] =
    baseVersion === null
      ? [emptyGADraft]
      : (baseVersion.geometry.categories ?? []).map((c) => ({
          name: effectiveCategoryName(c, plan.category_name_overrides),
          capacity: String(c.capacity ?? ""),
          price: c.price_hint ?? "",
        }));
  const [drafts, setDrafts] = useState<readonly GACategoryDraft[]>(
    initial.length > 0 ? initial : [emptyGADraft],
  );
  const [submitAttempted, setSubmitAttempted] = useState(false);
  const [apiError, setApiError] = useState<ApiError | null>(null);

  const mutation = useMutation<CreateVersionResponse, ApiError, void>({
    mutationFn: () =>
      authedFetch<CreateVersionResponse>({
        method: "POST",
        path: `/v1/seating-plans/${plan.id}/versions`,
        body: { geometry: buildGAOnlyGeometry(drafts) },
      }),
    onSuccess: () => {
      setApiError(null);
      setSubmitAttempted(false);
      qc.invalidateQueries({ queryKey: ["seating-plan-versions", plan.id] });
      qc.invalidateQueries({ queryKey: ["seating-plans", "by-venue", plan.venue_id] });
      onVersionCreated();
    },
    onError: (err) => setApiError(err),
  });

  const issues = gaCategoryIssues(drafts);
  return (
    <div style={sectionStyle} data-testid={`venues-plan-ga-version-${plan.id}`}>
      <div style={sectionTitleStyle}>New version (general admission)</div>
      <GACategoryEditorView drafts={drafts} onChange={setDrafts} />
      {submitAttempted && issues.length > 0 ? (
        <div style={validationMsgStyle} role="alert">
          <ul style={validationListStyle}>
            {issues.map((m) => (
              <li key={m}>{m}</li>
            ))}
          </ul>
        </div>
      ) : null}
      {apiError !== null ? (
        <div style={errorBoxStyle} role="alert">
          <div style={errorCodeStyle}>{apiError.code}</div>
          <div style={errorParaStyle}>{apiError.message}</div>
        </div>
      ) : null}
      <button
        type="button"
        style={primaryButtonStyle}
        disabled={mutation.isPending}
        onClick={() => {
          setSubmitAttempted(true);
          if (issues.length > 0) return;
          mutation.mutate();
        }}
        data-testid={`venues-plan-ga-version-submit-${plan.id}`}
      >
        {mutation.isPending ? "Publishing…" : "Publish version"}
      </button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Upload SVG form
// ---------------------------------------------------------------------------

/**
 * Build the exact request body for POST /v1/seating-plans/{id}/versions.
 *
 * The endpoint accepts exactly one of `svg` or `geometry`; this flow always
 * sends `svg` and lets the server-side importer canonicalise, checksum, and
 * count seats. `svg_asset_media_id` links the already-uploaded binary so the
 * drawer can preview the original artwork.
 *
 * AB-40: capacity_standing is no longer sent — the server derives it
 * from the geometry's GA categories and ignores the legacy field.
 *
 * The endpoint bumps the plan's current_version_id to the new row inside the
 * same transaction, so no follow-up PATCH is required to publish it.
 */
export function buildCreateVersionBody(params: {
  readonly svg: string;
  readonly svgAssetMediaID: string;
}): Record<string, unknown> {
  return {
    svg: params.svg,
    svg_asset_media_id: params.svgAssetMediaID,
  };
}

function UploadSVGForm({
  plan,
  venue,
  onVersionCreated,
}: {
  plan: SeatingPlan;
  venue: Venue;
  onVersionCreated: () => void;
}) {
  const qc = useQueryClient();
  const [issues, setIssues] = useState<readonly SeatingImportIssue[]>([]);
  const [warnings, setWarnings] = useState<readonly SeatingImportIssue[]>([]);
  const [uploadError, setUploadError] = useState<ApiError | null>(null);
  const [okMessage, setOkMessage] = useState<string | null>(null);
  const [uploadStep, setUploadStep] = useState<string | null>(null);
  // Client-side rejection (bad MIME, oversized file) that never reaches
  // the network. AB-40: the standing-capacity input is gone — the server
  // derives GA capacity from the geometry's #GA-labeled categories.
  const [fileError, setFileError] = useState<string | null>(null);

  const mutation = useMutation<CreateVersionResponse, ApiError, File>({
    mutationFn: async (file: File) => {
      // Step 1: store the SVG as a binary media object so the signed URL
      // can be used for preview without re-parsing geometry.
      setUploadStep("Uploading SVG asset (1/2)…");
      const mediaObj = await uploadMedia({
        file,
        ownerType: SEATING_PLAN_SVG_OWNER_TYPE,
        orgId: plan.owner_org_id,
        ownerId: plan.id,
      });
      // Step 2: read SVG as text, import geometry, and persist a new version
      // row that carries both the canonical geometry and the media asset ID.
      // The handler makes this version current in the same transaction.
      setUploadStep("Creating version (2/2)…");
      const svg = await readFileAsText(file);
      return authedFetch<CreateVersionResponse>({
        method: "POST",
        path: `/v1/seating-plans/${plan.id}/versions`,
        body: buildCreateVersionBody({
          svg,
          svgAssetMediaID: mediaObj.id,
        }),
      });
    },
    onSuccess: (res) => {
      setUploadStep(null);
      setIssues([]);
      setUploadError(null);
      setWarnings(res.warnings ?? []);
      setOkMessage(
        `Uploaded as version ${res.seating_plan_version.version_number} ` +
          `(${res.seating_plan_version.capacity_seated} seats). ` +
          `Version ${res.seating_plan_version.version_number} is now current.`,
      );
      // Refetch the plan (current_version_id / _number) and the version
      // history, which also feeds the preview, so the drawer reflects the
      // newly-published version.
      qc.invalidateQueries({ queryKey: ["seating-plans", "by-venue", venue.id] });
      qc.invalidateQueries({ queryKey: ["seating-plan-versions", plan.id] });
      onVersionCreated();
    },
    onError: (err) => {
      setUploadStep(null);
      setOkMessage(null);
      setWarnings([]);
      setIssues(parseVersionValidationErrors(err));
      setUploadError(err);
    },
  });

  const onFile = (file: File) => {
    setIssues([]);
    setWarnings([]);
    setUploadError(null);
    setOkMessage(null);
    setFileError(null);
    // Reject bad input before any bytes leave the machine: an SVG that fails
    // the MIME/size gate would otherwise be stored as an orphan media object
    // whose version-create call then fails.
    const failure = validateFile(file, SEATING_PLAN_SVG_OWNER_TYPE);
    if (failure !== null) {
      setFileError(failure.message);
      return;
    }
    mutation.mutate(file);
  };

  return (
    <UploadSVGFormView
      planID={plan.id}
      pending={mutation.isPending}
      step={uploadStep}
      okMessage={okMessage}
      fileError={fileError}
      issues={issues}
      warnings={warnings}
      uploadError={uploadError}
      onFileSelected={onFile}
      gaHint={planHasGeneralAdmission(plan.plan_type)}
    />
  );
}

export interface UploadSVGFormViewProps {
  readonly planID: string;
  readonly pending: boolean;
  /** Which of the two upload steps is running, shown on the picker button. */
  readonly step: string | null;
  readonly okMessage: string | null;
  /** Client-side rejection that never reached the network. */
  readonly fileError: string | null;
  readonly issues: readonly SeatingImportIssue[];
  readonly warnings: readonly SeatingImportIssue[];
  readonly uploadError: ApiError | null;
  /**
   * AB-40: for GA-capable plan types the form shows an authoring hint
   * about #GA-labeled areas instead of a capacity input — GA capacity
   * is derived server-side from the geometry.
   */
  readonly gaHint: boolean;
  readonly onFileSelected: (file: File) => void;
}

/**
 * Presentational half of the SVG upload / version-create control. Stateless
 * and query-free so it renders under renderToStaticMarkup in tests.
 */
export function UploadSVGFormView({
  planID,
  pending,
  step,
  okMessage,
  fileError,
  issues,
  warnings,
  uploadError,
  gaHint,
  onFileSelected,
}: UploadSVGFormViewProps): JSX.Element {
  const onChange = (e: ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0];
    // Allow re-picking the same path after a rejection.
    e.target.value = "";
    if (f === undefined) return;
    onFileSelected(f);
  };

  return (
    <div style={sectionStyle} data-testid={`venues-plan-upload-${planID}`}>
      <div style={sectionTitleStyle}>Upload SVG · new version</div>
      <p style={hintStyle}>
        Upload an SVG file to create a new version and publish it as the plan&apos;s
        current version. The importer canonicalises geometry and derives seated
        capacity from the parsed seats; the original SVG is stored for preview.
        Accepted: {acceptedExtensionsLabel(SEATING_PLAN_SVG_OWNER_TYPE)} up to{" "}
        {formatBytes(OWNER_TYPE_MAX_BYTES[SEATING_PLAN_SVG_OWNER_TYPE])}.
        Per-element validation errors appear below.
      </p>
      {gaHint ? (
        <p style={hintStyle} data-testid={`venues-plan-upload-ga-${planID}`}>
          General-admission areas: label an element &quot;#GA &lt;name&gt;&quot; whose
          &lt;title&gt; carries the capacity and whose fill matches a category
          swatch. Both capacities are derived from the file — nothing to
          enter by hand.
        </p>
      ) : null}
      <label style={fileButtonStyle}>
        <input
          type="file"
          accept="image/svg+xml,.svg"
          onChange={onChange}
          disabled={pending}
          style={{ display: "none" }}
          data-testid={`venues-plan-upload-input-${planID}`}
        />
        <span>{pending ? (step ?? "Uploading…") : "Choose SVG file"}</span>
      </label>
      {fileError !== null ? (
        <div
          style={errorBoxStyle}
          role="alert"
          data-testid={`venues-plan-upload-file-error-${planID}`}
        >
          <strong>File rejected.</strong>
          <div style={errorParaStyle}>{fileError}</div>
        </div>
      ) : null}
      {okMessage !== null ? (
        <div
          style={okBoxStyle}
          role="status"
          data-testid={`venues-plan-upload-ok-${planID}`}
        >
          {okMessage}
        </div>
      ) : null}
      {issues.length > 0 ? (
        <ul
          style={issueListStyle}
          role="alert"
          data-testid={`venues-plan-upload-errors-${planID}`}
        >
          {issues.map((i, idx) => (
            <li key={`${i.code}-${idx}`} style={issueRowStyle}>
              <code style={issueCodeStyle}>{i.code}</code>
              {i.element !== undefined ? (
                <span style={issueElementStyle}> [{i.element}]</span>
              ) : null}
              {i.detail !== undefined ? (
                <div style={issueDetailStyle}>{i.detail}</div>
              ) : null}
            </li>
          ))}
        </ul>
      ) : null}
      {warnings.length > 0 ? (
        <ul
          style={warningListStyle}
          data-testid={`venues-plan-upload-warnings-${planID}`}
        >
          {warnings.map((i, idx) => (
            <li key={`${i.code}-${idx}`} style={issueRowStyle}>
              <code style={warningCodeStyle}>{i.code}</code>
              {i.element !== undefined ? (
                <span style={issueElementStyle}> [{i.element}]</span>
              ) : null}
              {i.detail !== undefined ? (
                <div style={issueDetailStyle}>{i.detail}</div>
              ) : null}
            </li>
          ))}
        </ul>
      ) : null}
      {uploadError !== null && issues.length === 0 ? (
        <div
          style={errorBoxStyle}
          role="alert"
          data-testid={`venues-plan-upload-fatal-${planID}`}
        >
          <strong>Upload failed.</strong>
          <div style={errorCodeStyle}>{uploadError.code}</div>
          <div style={errorParaStyle}>{uploadError.message}</div>
        </div>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Version history list (AB-25)
// ---------------------------------------------------------------------------

export interface VersionHistoryTableProps {
  /** Versions as returned by the API: version_number descending. */
  readonly versions: readonly SeatingPlanVersion[];
  readonly currentVersionID: string | null;
  readonly selectedVersionID: string | null;
  readonly onSelect: (versionID: string) => void;
  readonly hasGA: boolean;
}

/**
 * Version history for a plan: one row per version, newest first, carrying
 * both capacities, the creation stamp, the lock state, and a marker for the
 * plan's published (current) version. Selecting a row targets it for preview.
 *
 * Stateless and query-free so it renders under renderToStaticMarkup in tests.
 * The rows are sorted defensively rather than trusting the transport order.
 */
export function VersionHistoryTable({
  versions,
  currentVersionID,
  selectedVersionID,
  onSelect,
  hasGA,
}: VersionHistoryTableProps): JSX.Element {
  if (versions.length === 0) {
    return (
      <p style={hintStyle} data-testid="venues-plan-versions-empty">
        No versions yet. A plan is only a container — Upload an SVG above to
        create version 1, which carries the parsed geometry, the derived seat
        capacity, and the original SVG asset stored in the media library.
      </p>
    );
  }

  const ordered = [...versions].sort((a, b) => b.version_number - a.version_number);

  return (
    <div style={versionTableWrapStyle}>
      <table style={versionTableStyle}>
        <caption style={versionCaptionStyle}>
          {ordered.length} version{ordered.length === 1 ? "" : "s"}, newest first.
          Select a row to preview it.
        </caption>
        <thead>
          <tr>
            <th style={versionThStyle} scope="col">
              Version
            </th>
            <th style={versionThNumStyle} scope="col">
              Seated
            </th>
            <th style={versionThNumStyle} scope="col">
              GA
            </th>
            <th style={versionThStyle} scope="col">
              Created
            </th>
            <th style={versionThStyle} scope="col">
              State
            </th>
          </tr>
        </thead>
        <tbody>
          {ordered.map((v) => {
            const isCurrent = currentVersionID !== null && currentVersionID === v.id;
            const isSelected = selectedVersionID === v.id;
            return (
              <tr key={v.id} style={isSelected ? versionTrSelectedStyle : undefined}>
                <th style={versionRowHeaderStyle} scope="row">
                  <button
                    type="button"
                    onClick={() => onSelect(v.id)}
                    aria-pressed={isSelected}
                    aria-label={`Preview version ${v.version_number}`}
                    style={isSelected ? versionSelectButtonActiveStyle : versionSelectButtonStyle}
                    data-testid={`venues-plan-version-row-${v.id}`}
                  >
                    v{v.version_number}
                  </button>
                  {isSelected ? (
                    <span
                      style={selectedBadgeStyle}
                      data-testid={`venues-plan-version-selected-${v.id}`}
                    >
                      previewing
                    </span>
                  ) : null}
                </th>
                <td style={versionTdNumStyle}>{v.capacity_seated.toLocaleString()}</td>
                <td style={versionTdNumStyle}>
                  {hasGA ? v.capacity_standing.toLocaleString() : "—"}
                </td>
                <td style={versionTdStyle}>
                  <time dateTime={v.created_at}>
                    {formatVersionTimestamp(v.created_at)}
                  </time>
                </td>
                <td style={versionTdStyle}>
                  {isCurrent ? (
                    <span
                      style={currentBadgeStyle}
                      data-testid={`venues-plan-version-current-${v.id}`}
                    >
                      current
                    </span>
                  ) : null}
                  {v.locked_at !== null ? (
                    <span
                      style={lockedBadgeStyle}
                      title={`Locked ${formatVersionTimestamp(v.locked_at)}`}
                      data-testid={`venues-plan-version-locked-${v.id}`}
                    >
                      locked
                    </span>
                  ) : null}
                  {!isCurrent && v.locked_at === null ? (
                    <span style={versionMetaStyle}>—</span>
                  ) : null}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Preview: fetch current version and render inline SVG
// ---------------------------------------------------------------------------

/**
 * Container for the inline preview. The version object itself comes from the
 * history list (which already carries the geometry), so the only request made
 * here is for the short-lived signed URL of the stored SVG asset.
 */
function PlanPreviewSection({ version, hasGA }: { version: SeatingPlanVersion | null; hasGA: boolean }) {
  const svgMediaID = version?.svg_asset_media_id ?? null;

  // Fetch a signed download URL for the SVG media asset when it exists.
  // The URL expires after 7 minutes; react-query will refetch on remount.
  const mediaQuery = useQuery<{ signed_url: string }, ApiError>({
    queryKey: ["media-signed-url", svgMediaID],
    enabled: svgMediaID !== null,
    queryFn: async () => {
      const res = await authedFetch<{
        media_object: { signed_url?: string };
      }>({
        method: "GET",
        path: `/v1/media/${svgMediaID}`,
      });
      const raw = res.media_object.signed_url ?? "";
      // AB-13: local backend returns a relative URL — anchor to the API base.
      const signed_url = raw.startsWith("/")
        ? `${config.apiBaseUrl}${raw}`
        : raw;
      return { signed_url };
    },
    retry: false,
    refetchOnWindowFocus: false,
    staleTime: 5 * 60 * 1000, // 5 min — well within the 7-min signed URL TTL
  });

  return (
    <div style={sectionStyle} data-testid="venues-plan-preview">
      <div style={sectionTitleStyle}>Preview</div>
      {version === null ? (
        <p style={hintStyle}>
          No version yet. Upload an SVG above to create version 1. A seating
          plan is a container — the version carries the geometry, seat
          capacity, and the original SVG asset.
        </p>
      ) : (
        <VersionPreview
          version={version}
          signedURL={mediaQuery.data?.signed_url ?? null}
          loading={svgMediaID !== null && mediaQuery.isPending}
          error={mediaQuery.error ?? null}
          hasGA={hasGA}
        />
      )}
    </div>
  );
}

export interface VersionPreviewProps {
  readonly version: SeatingPlanVersion;
  /** Signed download URL for version.svg_asset_media_id, when resolved. */
  readonly signedURL: string | null;
  readonly loading?: boolean;
  readonly error?: ApiError | null;
  readonly hasGA?: boolean;
}

/**
 * Inline preview of a single version.
 *
 * Prefers the stored SVG binary rendered through an `<img>`: the browser
 * treats an SVG image resource as inert — no script execution, no external
 * fetches — so operator-supplied markup can never become an XSS channel here.
 * Versions predating the media pipeline (or whose asset URL cannot be signed)
 * fall back to the client-side geometry renderer, which emits only circle
 * primitives built from numeric coordinates.
 *
 * Stateless and query-free so it renders under renderToStaticMarkup in tests.
 */
export function VersionPreview({
  version,
  signedURL,
  loading = false,
  error = null,
  hasGA = false,
}: VersionPreviewProps): JSX.Element {
  const hasAsset = version.svg_asset_media_id !== null;

  return (
    <div style={previewWrapStyle}>
      <div style={previewCaptionStyle}>
        Version {version.version_number} ·{" "}
        {version.capacity_seated.toLocaleString()} seated
        {hasGA && version.capacity_standing > 0
          ? ` · ${version.capacity_standing.toLocaleString()} GA`
          : ""}
      </div>
      {hasAsset && loading ? (
        <div style={statusBoxStyle} role="status">
          Loading SVG asset…
        </div>
      ) : hasAsset && signedURL !== null ? (
        <div style={svgWrapStyle} data-testid="venues-plan-preview-svg-img">
          <img
            src={signedURL}
            alt={`Seating plan SVG preview, version ${version.version_number}`}
            style={{ maxWidth: "100%", height: "auto", display: "block" }}
          />
        </div>
      ) : (
        <>
          {hasAsset && error !== null ? (
            <div style={hintStyle} data-testid="venues-plan-preview-asset-error">
              Stored SVG could not be loaded ({error.code}); showing the parsed
              geometry instead.
            </div>
          ) : null}
          <GeometrySVG geometry={version.geometry} />
        </>
      )}
    </div>
  );
}

/**
 * Renders the SVG string produced by renderGeometryToSVG. Uses
 * dangerouslySetInnerHTML because we produce the markup ourselves from
 * numeric coordinates + a fixed circle primitive — no user-supplied
 * markup is interpolated.
 */
export function GeometrySVG({ geometry }: { geometry: SeatingGeometry }) {
  const html = useMemo(() => renderGeometryToSVG(geometry), [geometry]);
  return (
    <div
      style={svgWrapStyle}
      data-testid="venues-plan-preview-svg"
      // Safe: renderGeometryToSVG escapes all interpolated string values.
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
}

// ---------------------------------------------------------------------------
// Fork / Archive actions
// ---------------------------------------------------------------------------

function PlanActions({ plan, venue }: { plan: SeatingPlan; venue: Venue }) {
  const qc = useQueryClient();
  const [forkError, setForkError] = useState<ApiError | null>(null);
  const [archiveError, setArchiveError] = useState<ApiError | null>(null);
  const [forkOrgID, setForkOrgID] = useState<string>(plan.owner_org_id);

  const fork = useMutation<SeatingPlanEnvelope, ApiError, void>({
    mutationFn: () =>
      authedFetch<SeatingPlanEnvelope>({
        method: "POST",
        path: `/v1/seating-plans/${plan.id}/fork`,
        body: { owner_org_id: forkOrgID.trim() },
      }),
    onSuccess: () => {
      setForkError(null);
      qc.invalidateQueries({ queryKey: ["seating-plans", "by-venue", venue.id] });
    },
    onError: (err) => setForkError(err),
  });

  const archive = useMutation<SeatingPlanEnvelope, ApiError, void>({
    mutationFn: () =>
      authedFetch<SeatingPlanEnvelope>({
        method: "PATCH",
        path: `/v1/seating-plans/${plan.id}`,
        body: { status: "archived" },
      }),
    onSuccess: () => {
      setArchiveError(null);
      qc.invalidateQueries({ queryKey: ["seating-plans", "by-venue", venue.id] });
    },
    onError: (err) => setArchiveError(err),
  });

  const alreadyArchived = plan.status === "archived";

  return (
    <div style={sectionStyle}>
      <div style={sectionTitleStyle}>Actions</div>
      <div style={actionRowStyle}>
        <label style={forkInputWrapStyle}>
          <span style={miniLabelStyle}>Fork owner org UUID</span>
          <input
            type="text"
            value={forkOrgID}
            onChange={(e) => setForkOrgID(e.target.value)}
            style={monoInputStyle}
            data-testid={`venues-plan-fork-org-${plan.id}`}
            spellCheck={false}
          />
        </label>
        <button
          type="button"
          style={secondaryButtonStyle}
          onClick={() => fork.mutate()}
          disabled={fork.isPending || forkOrgID.trim() === ""}
          data-testid={`venues-plan-fork-${plan.id}`}
        >
          {fork.isPending ? "Forking…" : "Fork"}
        </button>
        <button
          type="button"
          style={dangerButtonStyle}
          onClick={() => archive.mutate()}
          disabled={archive.isPending || alreadyArchived}
          data-testid={`venues-plan-archive-${plan.id}`}
        >
          {archive.isPending ? "Archiving…" : alreadyArchived ? "Archived" : "Archive"}
        </button>
      </div>
      {forkError !== null ? (
        <div
          style={errorBoxStyle}
          role="alert"
          data-testid={`venues-plan-fork-error-${plan.id}`}
        >
          <div style={errorCodeStyle}>{forkError.code}</div>
          <div style={errorParaStyle}>{forkError.message}</div>
        </div>
      ) : null}
      {archiveError !== null ? (
        <div
          style={errorBoxStyle}
          role="alert"
          data-testid={`venues-plan-archive-error-${plan.id}`}
        >
          <div style={errorCodeStyle}>{archiveError.code}</div>
          <div style={errorParaStyle}>{archiveError.message}</div>
        </div>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Create plan form (inline at the bottom of the plans list)
// ---------------------------------------------------------------------------

const PLAN_TYPES: readonly SeatingPlanType[] = [
  "assigned_seats",
  "general_admission",
  "tables",
  "mixed",
];

// Per-type hint mirroring the Bil24 create dialog (AB-40 C1): the form
// reshapes with the choice instead of assuming an SVG is the only path.
export const PLAN_TYPE_HINTS: Readonly<Record<SeatingPlanType, string>> = {
  assigned_seats:
    "Assigned seats: upload the hall SVG after creating — seats and categories come from the file.",
  general_admission:
    "General admission: no file needed — enter the named capacities below; they become the plan's categories.",
  tables: "Tables: reserved plan type (not yet supported end-to-end).",
  mixed:
    "Combined: upload an SVG whose #GA-labeled area carries the floor capacity alongside the seats.",
};

// The 15-swatch Bil24 palette (First..Fifteenth), used to auto-colour
// hand-entered GA categories so they stay within the SVG convention.
export const CATEGORY_PALETTE: readonly string[] = [
  "#ff0000", "#ffa000", "#ffff00", "#00ff00", "#00ffff",
  "#0000ff", "#800080", "#008000", "#a9ff2d", "#aa2800",
  "#0096ff", "#808000", "#3caa00", "#d4aa00", "#00d455",
];

/** One hand-entered GA category row (AB-40 C1). */
export interface GACategoryDraft {
  readonly name: string;
  readonly capacity: string; // raw input; validated to a positive int
  readonly price: string; // optional starting price in minor units
}

export const emptyGADraft: GACategoryDraft = { name: "", capacity: "", price: "" };

/**
 * Validates the hand-entered GA category table. Empty rows (all fields
 * blank) are ignored so an untouched trailing row never blocks submit.
 */
export function gaCategoryIssues(
  drafts: readonly GACategoryDraft[],
): readonly string[] {
  const active = drafts.filter(
    (d) => d.name.trim() !== "" || d.capacity.trim() !== "" || d.price.trim() !== "",
  );
  const issues: string[] = [];
  if (active.length === 0) {
    issues.push("Add at least one general-admission category (name + capacity).");
    return issues;
  }
  if (active.length > 15) {
    issues.push("At most 15 categories are allowed (the Bil24 ceiling).");
  }
  const seen = new Set<string>();
  active.forEach((d, i) => {
    const label = d.name.trim() === "" ? `row ${i + 1}` : `"${d.name.trim()}"`;
    if (d.name.trim() === "") {
      issues.push(`Category ${label} needs a name.`);
    } else if (seen.has(d.name.trim().toLowerCase())) {
      issues.push(`Category ${label} is listed twice.`);
    } else {
      seen.add(d.name.trim().toLowerCase());
    }
    const cap = Number(d.capacity.trim());
    if (
      d.capacity.trim() === "" ||
      !Number.isInteger(cap) ||
      cap <= 0
    ) {
      issues.push(`Category ${label} needs a positive whole-number capacity.`);
    }
    if (d.price.trim() !== "" && (!/^\d+$/.test(d.price.trim()) || Number(d.price.trim()) < 0)) {
      issues.push(`Category ${label} starting price must be a whole number of minor units.`);
    }
  });
  return issues;
}

/**
 * Builds the canonical GA-only geometry (§5.3) from the hand-entered
 * table: categories with kind=general_admission and palette colours, no
 * sections, no canvas dependency. The server derives capacity_standing
 * from these categories and validates them against plan_type.
 */
export function buildGAOnlyGeometry(drafts: readonly GACategoryDraft[]): {
  readonly schema_version: number;
  readonly canvas: SeatingCanvas;
  readonly categories: readonly SeatingCategory[];
} {
  const active = drafts.filter(
    (d) => d.name.trim() !== "" || d.capacity.trim() !== "" || d.price.trim() !== "",
  );
  return {
    schema_version: 1,
    canvas: { width: 1000, height: 400 },
    categories: active.map((d, i) => ({
      index: i + 1,
      name: d.name.trim(),
      color: CATEGORY_PALETTE[i % CATEGORY_PALETTE.length],
      kind: "general_admission" as const,
      capacity: Number(d.capacity.trim()),
      ...(d.price.trim() !== "" ? { price_hint: d.price.trim() } : {}),
    })),
  };
}

/**
 * Effective display name of a category: the per-plan override (AB-40
 * A3) wins over the geometry name.
 */
export function effectiveCategoryName(
  cat: SeatingCategory,
  overrides: Readonly<Record<string, string>> | undefined,
): string {
  const o = overrides?.[String(cat.index)];
  return o !== undefined && o.trim() !== "" ? o : cat.name;
}

/**
 * Seats column of the Bil24 category table: seated categories count
 * their bound seats; GA categories show their declared capacity.
 */
export function categorySeatCount(
  cat: SeatingCategory,
  geometry: SeatingGeometry,
): number {
  if (cat.kind === "general_admission") return cat.capacity ?? 0;
  let n = 0;
  for (const sec of geometry.sections ?? []) {
    for (const row of sec.rows) {
      for (const seat of row.seats) {
        if (seat.category_index === cat.index) n++;
      }
    }
  }
  return n;
}

function CreatePlanForm({ venue }: { venue: Venue }) {
  const qc = useQueryClient();
  const [name, setName] = useState<string>("");
  const [planType, setPlanType] = useState<SeatingPlanType>("assigned_seats");
  const [ownerOrgID, setOwnerOrgID] = useState<string>(venue.org_id);
  // AB-40 C1: hand-entered GA categories for a general_admission plan —
  // the plan needs no SVG at all; the table becomes version 1.
  const [gaDrafts, setGADrafts] = useState<readonly GACategoryDraft[]>([
    emptyGADraft,
  ]);
  const [apiError, setApiError] = useState<ApiError | null>(null);
  // Issues are surfaced only after the operator has tried to submit once,
  // then kept live so each message disappears as its requirement is met.
  const [submitAttempted, setSubmitAttempted] = useState<boolean>(false);

  // Fetch the organization list so the operator can pick by name instead of
  // pasting a raw UUID — the same picker pattern used by Sales Channels and
  // Payment Configs (AB-17 / AB-22).
  const orgsQuery = useQuery<OrganizationListEnvelope, ApiError>({
    queryKey: ["organizations", "seating-create-picker"],
    queryFn: () =>
      authedFetch<OrganizationListEnvelope>({
        method: "GET",
        path: "/v1/organizations",
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });
  const orgsSorted = useMemo(
    () =>
      [...(orgsQuery.data?.organizations ?? [])].sort((a, b) =>
        a.name.localeCompare(b.name),
      ),
    [orgsQuery.data],
  );

  const mutation = useMutation<SeatingPlanEnvelope, ApiError, void>({
    mutationFn: async () => {
      const created = await authedFetch<SeatingPlanEnvelope>({
        method: "POST",
        path: `/v1/venues/${venue.id}/seating-plans`,
        body: {
          owner_org_id: ownerOrgID.trim(),
          name: name.trim(),
          plan_type: planType,
        },
      });
      // AB-40 C1: a GA-only plan is complete at creation — the
      // hand-entered categories become version 1 in the same submit,
      // so the operator never faces an "upload an SVG" dead end.
      if (planType === "general_admission") {
        await authedFetch<CreateVersionResponse>({
          method: "POST",
          path: `/v1/seating-plans/${created.seating_plan.id}/versions`,
          body: { geometry: buildGAOnlyGeometry(gaDrafts) },
        });
      }
      return created;
    },
    onSuccess: () => {
      setApiError(null);
      setSubmitAttempted(false);
      setName("");
      setGADrafts([emptyGADraft]);
      qc.invalidateQueries({ queryKey: ["seating-plans", "by-venue", venue.id] });
    },
    onError: (err) => setApiError(err),
  });

  const issues: CreatePlanFormIssue[] = [
    ...createPlanFormIssues(name, ownerOrgID),
    ...(planType === "general_admission"
      ? gaCategoryIssues(gaDrafts).map((message) => ({
          field: "ga_categories" as const,
          message,
        }))
      : []),
  ];

  return (
    <CreatePlanFormView
      name={name}
      planType={planType}
      ownerOrgID={ownerOrgID}
      organizations={orgsSorted}
      organizationsPending={orgsQuery.isPending}
      gaDrafts={gaDrafts}
      // Before the first submit the operator has not asked for anything yet,
      // so an empty form is "not yet filled in", not "wrong".
      issues={submitAttempted ? issues : []}
      submitting={mutation.isPending}
      apiError={apiError}
      onNameChange={setName}
      onPlanTypeChange={setPlanType}
      onOwnerOrgChange={setOwnerOrgID}
      onGADraftsChange={setGADrafts}
      onSubmit={() => {
        // Validate on submit — the button is never silently disabled, so the
        // operator always learns which requirements are unmet.
        setSubmitAttempted(true);
        if (issues.length > 0) return;
        mutation.mutate();
      }}
    />
  );
}

export interface CreatePlanFormViewProps {
  readonly name: string;
  readonly planType: SeatingPlanType;
  readonly ownerOrgID: string;
  readonly organizations: readonly OrganizationSummary[];
  readonly organizationsPending: boolean;
  /** AB-40 C1: hand-entered GA categories (general_admission type only). */
  readonly gaDrafts: readonly GACategoryDraft[];
  /** Unmet requirements to display; empty renders no validation feedback. */
  readonly issues: readonly CreatePlanFormIssue[];
  readonly submitting: boolean;
  readonly apiError: ApiError | null;
  readonly onNameChange: (v: string) => void;
  readonly onPlanTypeChange: (v: SeatingPlanType) => void;
  readonly onOwnerOrgChange: (v: string) => void;
  readonly onGADraftsChange: (v: readonly GACategoryDraft[]) => void;
  readonly onSubmit: () => void;
}

/**
 * Presentational half of the create-plan form. Holds no state and issues no
 * queries so it can be rendered directly in a Vitest (node environment,
 * renderToStaticMarkup) test to assert the validation display.
 *
 * The submit button is deliberately enabled whenever a request is not in
 * flight: a disabled button with no explanation is the defect AB-25a fixes.
 */
export function CreatePlanFormView({
  name,
  planType,
  ownerOrgID,
  organizations,
  organizationsPending,
  gaDrafts,
  issues,
  submitting,
  apiError,
  onNameChange,
  onPlanTypeChange,
  onOwnerOrgChange,
  onGADraftsChange,
  onSubmit,
}: CreatePlanFormViewProps): JSX.Element {
  const nameIssue = issueForField(issues, "name");
  const ownerIssue = issueForField(issues, "owner_org_id");

  return (
    <form
      style={createFormStyle}
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit();
      }}
      noValidate
      data-testid="venues-plan-create-form"
    >
      <div style={sectionTitleStyle}>New seating plan</div>
      <div style={fieldRowStyle}>
        <label style={miniLabelStyle} htmlFor="venue-plan-name">
          Name
        </label>
        <input
          id="venue-plan-name"
          type="text"
          value={name}
          onChange={(e) => onNameChange(e.target.value)}
          style={nameIssue !== null ? inputInvalidStyle : inputStyle}
          placeholder="e.g. Main Hall — Assigned Seating"
          aria-invalid={nameIssue !== null}
          aria-describedby={
            nameIssue !== null ? "venue-plan-name-error" : undefined
          }
          data-testid="venues-plan-create-name"
        />
        {nameIssue !== null ? (
          <div
            id="venue-plan-name-error"
            style={fieldErrorStyle}
            data-testid="venues-plan-create-name-error"
          >
            {nameIssue}
          </div>
        ) : null}
      </div>
      <div style={fieldRowStyle}>
        <label style={miniLabelStyle} htmlFor="venue-plan-type">
          Plan type
        </label>
        <select
          id="venue-plan-type"
          value={planType}
          onChange={(e) => onPlanTypeChange(e.target.value as SeatingPlanType)}
          style={inputStyle}
          data-testid="venues-plan-create-type"
        >
          {PLAN_TYPES.map((t) => (
            <option key={t} value={t}>
              {t}
            </option>
          ))}
        </select>
        <div style={mutedHintTextStyle} data-testid="venues-plan-create-type-hint">
          {PLAN_TYPE_HINTS[planType]}
        </div>
      </div>
      {planType === "general_admission" ? (
        <GACategoryEditorView drafts={gaDrafts} onChange={onGADraftsChange} />
      ) : null}
      <div style={fieldRowStyle}>
        <label style={miniLabelStyle} htmlFor="venue-plan-owner">
          Owner organization
        </label>
        <select
          id="venue-plan-owner"
          value={ownerOrgID}
          onChange={(e) => onOwnerOrgChange(e.target.value)}
          style={ownerIssue !== null ? inputInvalidStyle : inputStyle}
          disabled={organizationsPending}
          aria-invalid={ownerIssue !== null}
          aria-describedby={
            ownerIssue !== null ? "venue-plan-owner-error" : undefined
          }
          data-testid="venues-plan-create-owner"
        >
          {organizationsPending ? (
            <option value="">Loading organizations…</option>
          ) : (
            <>
              <option value="">Select organization</option>
              {organizations.map((org) => (
                <option key={org.id} value={org.id}>
                  {org.name}
                </option>
              ))}
            </>
          )}
        </select>
        {ownerIssue !== null ? (
          <div
            id="venue-plan-owner-error"
            style={fieldErrorStyle}
            data-testid="venues-plan-create-owner-error"
          >
            {ownerIssue}
          </div>
        ) : null}
      </div>
      {issues.length > 0 ? (
        <div
          style={validationMsgStyle}
          role="alert"
          data-testid="venues-plan-create-validation"
        >
          <strong>Cannot create this plan yet:</strong>
          <ul style={validationListStyle}>
            {issues.map((i) => (
              <li key={i.field}>{i.message}</li>
            ))}
          </ul>
        </div>
      ) : null}
      {apiError !== null ? (
        <div style={errorBoxStyle} role="alert" data-testid="venues-plan-create-error">
          <div style={errorCodeStyle}>{apiError.code}</div>
          <div style={errorParaStyle}>{apiError.message}</div>
        </div>
      ) : null}
      <button
        type="submit"
        style={primaryButtonStyle}
        disabled={submitting}
        data-testid="venues-plan-create-submit"
      >
        {submitting ? "Creating…" : "Create plan"}
      </button>
    </form>
  );
}

/**
 * Hand-entered GA category table (AB-40 C1): No. | Category | Seats |
 * Starting price with add/remove rows, mirroring the Bil24 create
 * dialog for GA-only plans. Presentational — exported for Vitest.
 */
export function GACategoryEditorView({
  drafts,
  onChange,
}: {
  readonly drafts: readonly GACategoryDraft[];
  readonly onChange: (v: readonly GACategoryDraft[]) => void;
}): JSX.Element {
  const update = (i: number, patch: Partial<GACategoryDraft>): void => {
    onChange(drafts.map((d, j) => (j === i ? { ...d, ...patch } : d)));
  };
  return (
    <div style={fieldRowStyle} data-testid="venues-plan-create-ga-editor">
      <div style={miniLabelStyle}>General-admission categories</div>
      <table style={gaEditorTableStyle}>
        <thead>
          <tr>
            <th style={gaEditorHeadStyle}>No.</th>
            <th style={gaEditorHeadStyle}>Category</th>
            <th style={gaEditorHeadStyle}>Seats</th>
            <th style={gaEditorHeadStyle}>Starting price</th>
            <th style={gaEditorHeadStyle} aria-label="Row actions" />
          </tr>
        </thead>
        <tbody>
          {drafts.map((d, i) => (
            // Row identity is positional by design: rows are a transient
            // editing buffer, never reordered, only appended/removed.
            // eslint-disable-next-line react/no-array-index-key
            <tr key={i} data-testid={`venues-plan-create-ga-row-${i}`}>
              <td style={gaEditorCellStyle}>{i + 1}</td>
              <td style={gaEditorCellStyle}>
                <input
                  type="text"
                  value={d.name}
                  placeholder="e.g. Dance floor"
                  onChange={(e) => update(i, { name: e.target.value })}
                  style={inputStyle}
                  aria-label={`Category ${i + 1} name`}
                  data-testid={`venues-plan-create-ga-name-${i}`}
                />
              </td>
              <td style={gaEditorCellStyle}>
                <input
                  type="text"
                  inputMode="numeric"
                  value={d.capacity}
                  placeholder="500"
                  onChange={(e) => update(i, { capacity: e.target.value })}
                  style={inputStyle}
                  aria-label={`Category ${i + 1} capacity`}
                  data-testid={`venues-plan-create-ga-capacity-${i}`}
                />
              </td>
              <td style={gaEditorCellStyle}>
                <input
                  type="text"
                  inputMode="numeric"
                  value={d.price}
                  placeholder="minor units, optional"
                  onChange={(e) => update(i, { price: e.target.value })}
                  style={inputStyle}
                  aria-label={`Category ${i + 1} starting price`}
                  data-testid={`venues-plan-create-ga-price-${i}`}
                />
              </td>
              <td style={gaEditorCellStyle}>
                <button
                  type="button"
                  style={ghostButtonStyle}
                  onClick={() => onChange(drafts.filter((_, j) => j !== i))}
                  disabled={drafts.length === 1}
                  aria-label={`Remove category row ${i + 1}`}
                  data-testid={`venues-plan-create-ga-remove-${i}`}
                >
                  −
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <button
        type="button"
        style={ghostButtonStyle}
        onClick={() => onChange([...drafts, emptyGADraft])}
        disabled={drafts.length >= 15}
        data-testid="venues-plan-create-ga-add"
      >
        + Add category
      </button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

const mutedHintTextStyle: CSSProperties = {
  fontSize: 12,
  color: "#64748b",
  marginTop: 4,
};

const gaEditorTableStyle: CSSProperties = {
  width: "100%",
  borderCollapse: "collapse",
  marginTop: 4,
};

const gaEditorHeadStyle: CSSProperties = {
  textAlign: "left",
  fontSize: 12,
  color: "#64748b",
  padding: "2px 6px 2px 0",
};

const gaEditorCellStyle: CSSProperties = {
  padding: "2px 6px 2px 0",
  verticalAlign: "top",
};

const ghostButtonStyle: CSSProperties = {
  background: "transparent",
  border: "1px solid #cbd5e1",
  borderRadius: 6,
  padding: "4px 10px",
  cursor: "pointer",
  fontSize: 13,
  marginTop: 4,
};

const tabsBarStyle: CSSProperties = {
  display: "flex",
  gap: 4,
  borderBottom: "1px solid #e2e8f0",
  marginBottom: 12,
};

const tabButtonStyle: CSSProperties = {
  background: "transparent",
  border: 0,
  padding: "8px 12px",
  color: "#475569",
  cursor: "pointer",
  borderBottom: "2px solid transparent",
  fontSize: 13,
  fontWeight: 500,
};

const tabButtonActiveStyle: CSSProperties = {
  ...tabButtonStyle,
  color: "#0369a1",
  borderBottom: "2px solid #0369a1",
};

const panelStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 12,
};

const detailListStyle: CSSProperties = {
  display: "grid",
  gridTemplateColumns: "auto 1fr",
  gap: "4px 12px",
  margin: 0,
  fontSize: 12,
};

const detailLabelStyle: CSSProperties = {
  color: "#64748b",
  fontWeight: 600,
};

const detailValueStyle: CSSProperties = {
  margin: 0,
  color: "#0f172a",
};

const detailValueMonoStyle: CSSProperties = {
  ...detailValueStyle,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 11,
};

const planListStyle: CSSProperties = {
  listStyle: "none",
  padding: 0,
  margin: 0,
  display: "flex",
  flexDirection: "column",
  gap: 8,
};

const planRowStyle: CSSProperties = {
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
};

const planHeaderStyle: CSSProperties = {
  width: "100%",
  textAlign: "left",
  background: "transparent",
  border: 0,
  padding: "10px 12px",
  cursor: "pointer",
  display: "flex",
  flexDirection: "column",
  gap: 2,
};

const planNameStyle: CSSProperties = {
  fontSize: 13,
  fontWeight: 600,
  color: "#0f172a",
};

const planMetaStyle: CSSProperties = {
  fontSize: 11,
  color: "#64748b",
};

const planBodyStyle: CSSProperties = {
  borderTop: "1px solid #e2e8f0",
  padding: 12,
  display: "flex",
  flexDirection: "column",
  gap: 12,
};

const sectionStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 6,
};

const sectionTitleStyle: CSSProperties = {
  fontSize: 12,
  fontWeight: 700,
  color: "#334155",
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const hintStyle: CSSProperties = {
  margin: 0,
  fontSize: 11,
  color: "#64748b",
  lineHeight: 1.4,
};

const statusBoxStyle: CSSProperties = {
  padding: 12,
  border: "1px dashed #cbd5e1",
  borderRadius: 6,
  background: "#f8fafc",
  fontSize: 12,
  color: "#475569",
};

const errorBoxStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  padding: 12,
  border: "1px solid #fca5a5",
  borderRadius: 6,
  background: "#fef2f2",
  color: "#7f1d1d",
  fontSize: 12,
};

const okBoxStyle: CSSProperties = {
  padding: 8,
  border: "1px solid #86efac",
  borderRadius: 6,
  background: "#dcfce7",
  color: "#166534",
  fontSize: 12,
};

const errorParaStyle: CSSProperties = { margin: 0, fontSize: 12 };
const errorCodeStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 11,
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
  alignSelf: "flex-start",
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
  background: "#ffffff",
  border: "1px solid #fca5a5",
  borderRadius: 4,
  cursor: "pointer",
  color: "#7f1d1d",
};

const fileButtonStyle: CSSProperties = {
  ...secondaryButtonStyle,
  alignSelf: "flex-start",
  display: "inline-block",
};

const issueListStyle: CSSProperties = {
  listStyle: "none",
  padding: 8,
  margin: 0,
  border: "1px solid #fca5a5",
  borderRadius: 6,
  background: "#fef2f2",
  color: "#7f1d1d",
  display: "flex",
  flexDirection: "column",
  gap: 6,
};

const warningListStyle: CSSProperties = {
  listStyle: "none",
  padding: 8,
  margin: 0,
  border: "1px solid #fde68a",
  borderRadius: 6,
  background: "#fffbeb",
  color: "#854d0e",
  display: "flex",
  flexDirection: "column",
  gap: 6,
};

const issueRowStyle: CSSProperties = { fontSize: 12 };
const issueCodeStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 11,
  fontWeight: 600,
};
const warningCodeStyle: CSSProperties = { ...issueCodeStyle, color: "#854d0e" };
const issueElementStyle: CSSProperties = { fontSize: 11, color: "#475569" };
const issueDetailStyle: CSSProperties = { fontSize: 11, marginTop: 2 };

const actionRowStyle: CSSProperties = {
  display: "flex",
  flexWrap: "wrap",
  gap: 8,
  alignItems: "flex-end",
};

const forkInputWrapStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  flex: "1 1 200px",
};

const miniLabelStyle: CSSProperties = {
  fontSize: 11,
  fontWeight: 600,
  color: "#334155",
};

const monoInputStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 11,
  padding: "6px 8px",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  background: "#ffffff",
  color: "#0f172a",
};

const inputStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 8px",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  background: "#ffffff",
  color: "#0f172a",
};

const fieldRowStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
};

const createFormStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
  padding: 12,
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#f8fafc",
};

const svgWrapStyle: CSSProperties = {
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
  padding: 8,
  overflow: "auto",
  maxHeight: 320,
};

// Version history table styles (AB-25c)
const versionTableWrapStyle: CSSProperties = {
  // Narrow drawers must scroll the table rather than the page body.
  overflowX: "auto",
};

const versionTableStyle: CSSProperties = {
  width: "100%",
  borderCollapse: "collapse",
  fontSize: 12,
};

const versionCaptionStyle: CSSProperties = {
  captionSide: "bottom",
  textAlign: "left",
  fontSize: 11,
  color: "#64748b",
  paddingTop: 4,
};

const versionThStyle: CSSProperties = {
  textAlign: "left",
  fontSize: 11,
  fontWeight: 700,
  color: "#475569",
  borderBottom: "1px solid #e2e8f0",
  padding: "4px 6px",
  whiteSpace: "nowrap",
};

const versionThNumStyle: CSSProperties = {
  ...versionThStyle,
  textAlign: "right",
};

const versionRowHeaderStyle: CSSProperties = {
  textAlign: "left",
  padding: "4px 6px",
  borderBottom: "1px solid #f1f5f9",
  whiteSpace: "nowrap",
};

const versionTdStyle: CSSProperties = {
  padding: "4px 6px",
  borderBottom: "1px solid #f1f5f9",
  color: "#334155",
  whiteSpace: "nowrap",
};

const versionTdNumStyle: CSSProperties = {
  ...versionTdStyle,
  textAlign: "right",
  fontVariantNumeric: "tabular-nums",
};

const versionTrSelectedStyle: CSSProperties = {
  background: "#eff6ff",
};

const versionSelectButtonStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
  fontWeight: 700,
  color: "#0369a1",
  background: "transparent",
  border: 0,
  padding: 0,
  cursor: "pointer",
  textDecoration: "underline",
};

const versionSelectButtonActiveStyle: CSSProperties = {
  ...versionSelectButtonStyle,
  color: "#0f172a",
  textDecoration: "none",
};

const selectedBadgeStyle: CSSProperties = {
  marginLeft: 6,
  fontSize: 10,
  fontWeight: 700,
  padding: "1px 5px",
  borderRadius: 3,
  background: "#e2e8f0",
  color: "#0f172a",
  letterSpacing: 0.3,
};

const previewWrapStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 6,
};

const previewCaptionStyle: CSSProperties = {
  fontSize: 11,
  color: "#475569",
  fontWeight: 600,
};

const currentBadgeStyle: CSSProperties = {
  fontSize: 10,
  fontWeight: 700,
  padding: "1px 5px",
  borderRadius: 3,
  background: "#2563eb",
  color: "#ffffff",
  letterSpacing: 0.3,
};

const lockedBadgeStyle: CSSProperties = {
  fontSize: 10,
  fontWeight: 700,
  padding: "1px 5px",
  borderRadius: 3,
  background: "#f97316",
  color: "#ffffff",
  letterSpacing: 0.3,
};

const versionMetaStyle: CSSProperties = {
  color: "#64748b",
  fontSize: 11,
  marginLeft: 4,
};

// Inline validation message for the create-plan form (AB-25a)
const validationMsgStyle: CSSProperties = {
  fontSize: 12,
  color: "#b45309",
  background: "#fffbeb",
  border: "1px solid #fde68a",
  borderRadius: 4,
  padding: "6px 8px",
};

const validationListStyle: CSSProperties = {
  margin: "4px 0 0 0",
  paddingLeft: 18,
};

// Per-field message rendered directly beneath the offending control.
const fieldErrorStyle: CSSProperties = {
  fontSize: 11,
  color: "#b45309",
};

const inputInvalidStyle: CSSProperties = {
  ...inputStyle,
  border: "1px solid #f59e0b",
};
