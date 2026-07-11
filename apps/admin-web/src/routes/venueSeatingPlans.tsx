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
 *   GET  /v1/seating-plans/{id}/versions/{n}                (used for preview)
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
import { ApiError, authedFetch } from "@/lib/api/client";
import { ResponsiveDrawer } from "@/components/layout";
import type { Venue } from "./venues";

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
interface SeatingPlanVersionEnvelope {
  readonly seating_plan_version: SeatingPlanVersion;
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
  return (
    <div style={planBodyStyle}>
      <UploadSVGForm plan={plan} venue={venue} />
      <PlanPreview plan={plan} />
      <PlanActions plan={plan} venue={venue} />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Upload SVG form
// ---------------------------------------------------------------------------

function UploadSVGForm({ plan, venue }: { plan: SeatingPlan; venue: Venue }) {
  const qc = useQueryClient();
  const [issues, setIssues] = useState<readonly SeatingImportIssue[]>([]);
  const [warnings, setWarnings] = useState<readonly SeatingImportIssue[]>([]);
  const [uploadError, setUploadError] = useState<ApiError | null>(null);
  const [okMessage, setOkMessage] = useState<string | null>(null);

  const mutation = useMutation<CreateVersionResponse, ApiError, File>({
    mutationFn: async (file: File) => {
      const svg = await readFileAsText(file);
      return authedFetch<CreateVersionResponse>({
        method: "POST",
        path: `/v1/seating-plans/${plan.id}/versions`,
        body: { svg },
      });
    },
    onSuccess: (res) => {
      setIssues([]);
      setUploadError(null);
      setWarnings(res.warnings ?? []);
      setOkMessage(
        `Uploaded as version ${res.seating_plan_version.version_number} ` +
          `(${res.seating_plan_version.capacity_seated} seats).`,
      );
      qc.invalidateQueries({ queryKey: ["seating-plans", "by-venue", venue.id] });
      qc.invalidateQueries({ queryKey: ["seating-plan-version", plan.id] });
    },
    onError: (err) => {
      setOkMessage(null);
      setWarnings([]);
      setIssues(parseVersionValidationErrors(err));
      setUploadError(err);
    },
  });

  const onFile = (e: ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0];
    if (f === undefined) return;
    setIssues([]);
    setWarnings([]);
    setUploadError(null);
    setOkMessage(null);
    mutation.mutate(f);
    e.target.value = "";
  };

  return (
    <div style={sectionStyle} data-testid={`venues-plan-upload-${plan.id}`}>
      <div style={sectionTitleStyle}>Upload SVG</div>
      <p style={hintStyle}>
        The importer canonicalises geometry and rejects §6 rule violations.
        Per-element errors below cite the offending SVG element.
      </p>
      <label style={fileButtonStyle}>
        <input
          type="file"
          accept="image/svg+xml,.svg"
          onChange={onFile}
          disabled={mutation.isPending}
          style={{ display: "none" }}
          data-testid={`venues-plan-upload-input-${plan.id}`}
        />
        <span>{mutation.isPending ? "Uploading…" : "Choose SVG file"}</span>
      </label>
      {okMessage !== null ? (
        <div
          style={okBoxStyle}
          role="status"
          data-testid={`venues-plan-upload-ok-${plan.id}`}
        >
          {okMessage}
        </div>
      ) : null}
      {issues.length > 0 ? (
        <ul
          style={issueListStyle}
          role="alert"
          data-testid={`venues-plan-upload-errors-${plan.id}`}
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
          data-testid={`venues-plan-upload-warnings-${plan.id}`}
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
          data-testid={`venues-plan-upload-fatal-${plan.id}`}
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
// Preview: fetch current version and render inline SVG
// ---------------------------------------------------------------------------

function PlanPreview({ plan }: { plan: SeatingPlan }) {
  // Fetch the plan's CURRENT version: the list payload carries
  // current_version_number alongside current_version_id, so the preview
  // addresses /versions/{n} directly instead of hard-coding n=1 (which
  // silently rendered stale geometry once a second version was
  // uploaded). When a new version is uploaded the plans list query is
  // invalidated (see UploadSVGForm.onSuccess), the plan re-renders with
  // the bumped number, and this query keys off it.
  const versionN = resolveCurrentVersionNumber(plan);
  const query = useQuery<SeatingPlanVersionEnvelope, ApiError>({
    queryKey: ["seating-plan-version", plan.id, versionN],
    enabled: versionN !== null,
    queryFn: () =>
      authedFetch<SeatingPlanVersionEnvelope>({
        method: "GET",
        path: `/v1/seating-plans/${plan.id}/versions/${versionN}`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  if (plan.current_version_id === null) {
    return (
      <div style={sectionStyle} data-testid={`venues-plan-preview-${plan.id}`}>
        <div style={sectionTitleStyle}>Preview</div>
        <p style={hintStyle}>Upload an SVG above to render a preview.</p>
      </div>
    );
  }

  return (
    <div style={sectionStyle} data-testid={`venues-plan-preview-${plan.id}`}>
      <div style={sectionTitleStyle}>Preview</div>
      {query.isPending ? (
        <div style={statusBoxStyle}>Loading current version…</div>
      ) : query.isError ? (
        <div style={errorBoxStyle} role="alert">
          <strong>Could not load version.</strong>
          <div style={errorCodeStyle}>{query.error?.code ?? "unknown.error"}</div>
        </div>
      ) : query.data !== undefined ? (
        <GeometrySVG geometry={query.data.seating_plan_version.geometry} />
      ) : null}
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

function CreatePlanForm({ venue }: { venue: Venue }) {
  const qc = useQueryClient();
  const [name, setName] = useState<string>("");
  const [planType, setPlanType] = useState<SeatingPlanType>("assigned_seats");
  const [ownerOrgID, setOwnerOrgID] = useState<string>(venue.org_id);
  const [error, setError] = useState<ApiError | null>(null);

  const mutation = useMutation<SeatingPlanEnvelope, ApiError, void>({
    mutationFn: () =>
      authedFetch<SeatingPlanEnvelope>({
        method: "POST",
        path: `/v1/venues/${venue.id}/seating-plans`,
        body: {
          owner_org_id: ownerOrgID.trim(),
          name: name.trim(),
          plan_type: planType,
        },
      }),
    onSuccess: () => {
      setError(null);
      setName("");
      qc.invalidateQueries({ queryKey: ["seating-plans", "by-venue", venue.id] });
    },
    onError: (err) => setError(err),
  });

  const disabled =
    name.trim() === "" || ownerOrgID.trim() === "" || mutation.isPending;

  return (
    <form
      style={createFormStyle}
      onSubmit={(e) => {
        e.preventDefault();
        if (!disabled) mutation.mutate();
      }}
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
          onChange={(e) => setName(e.target.value)}
          style={inputStyle}
          data-testid="venues-plan-create-name"
        />
      </div>
      <div style={fieldRowStyle}>
        <label style={miniLabelStyle} htmlFor="venue-plan-type">
          Plan type
        </label>
        <select
          id="venue-plan-type"
          value={planType}
          onChange={(e) => setPlanType(e.target.value as SeatingPlanType)}
          style={inputStyle}
          data-testid="venues-plan-create-type"
        >
          {PLAN_TYPES.map((t) => (
            <option key={t} value={t}>
              {t}
            </option>
          ))}
        </select>
      </div>
      <div style={fieldRowStyle}>
        <label style={miniLabelStyle} htmlFor="venue-plan-owner">
          Owner org UUID
        </label>
        <input
          id="venue-plan-owner"
          type="text"
          value={ownerOrgID}
          onChange={(e) => setOwnerOrgID(e.target.value)}
          style={monoInputStyle}
          data-testid="venues-plan-create-owner"
          spellCheck={false}
        />
      </div>
      {error !== null ? (
        <div style={errorBoxStyle} role="alert" data-testid="venues-plan-create-error">
          <div style={errorCodeStyle}>{error.code}</div>
          <div style={errorParaStyle}>{error.message}</div>
        </div>
      ) : null}
      <button
        type="submit"
        style={primaryButtonStyle}
        disabled={disabled}
        data-testid="venues-plan-create-submit"
      >
        {mutation.isPending ? "Creating…" : "Create plan"}
      </button>
    </form>
  );
}

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

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
