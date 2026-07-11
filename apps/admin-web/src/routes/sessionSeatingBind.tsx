/**
 * Session-editor seating binding panel (feature #316, Wave SEAT-E2).
 *
 * Embedded inside the events.tsx SessionEditor for existing sessions.
 * Lets an operator:
 *
 *   - Pick admission_mode (general_admission / assigned_seats / hybrid).
 *     GA does NOT call the bind endpoint; it is a no-op from this panel
 *     because the backend contract routes GA seating through the standard
 *     PATCH sessions surface. The selector is still shown so the operator
 *     knows which family they are configuring.
 *   - Pick a seating plan (fetched via GET /v1/venues/{venue_id}/seating-plans)
 *     scoped to the parent event's venue.
 *   - Pick a plan version (fetched via GET /v1/seating-plans/{id}/versions/{n}
 *     across n=1..plan.current_version_number; without a list-versions
 *     endpoint we probe backwards from the current pointer).
 *   - See seated / standing capacity counters from the chosen version.
 *   - Fill in a category → tier map with a per-row auto-create toggle.
 *     When auto-create is on for a row the caller does not need to pick
 *     an existing tier — the backend will provision one from the geometry
 *     category metadata.
 *   - Submit to POST
 *       /v1/organizations/{org_id}/events/{event_id}/sessions/{id}/seating
 *     with `{ seating_plan_version_id, admission_mode, category_tier_map,
 *     auto_create_tiers }`. `auto_create_tiers` is set true when ANY row
 *     is toggled to auto-create.
 *   - Handle 409 seating.rebind_forbidden with a copy-paste-safe error
 *     message directing the operator to spin up a fresh session.
 *
 * Wave M responsive rules: mirror the SessionEditor form layout —
 * columns collapse to a single column below 720px via a media query
 * baked into the parent stylesheet, and the action bar keeps the
 * mobileFormBarStyle look-and-feel. Pure helpers are exported for unit
 * tests (see sessionSeatingBind.test.ts).
 */
import { useEffect, useMemo, useState, type CSSProperties } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ApiError, authedFetch } from "@/lib/api/client";
import type {
  SeatingPlan,
  SeatingPlanVersion,
  SeatingGeometry,
} from "@/routes/venueSeatingPlans";

// ---------------------------------------------------------------------------
// Wire types (mirror openapi/clients/ts/index.d.ts)
// ---------------------------------------------------------------------------

export const ADMISSION_MODES = [
  "general_admission",
  "assigned_seats",
  "hybrid",
] as const;

export type AdmissionMode = (typeof ADMISSION_MODES)[number];

export function isAdmissionMode(v: string): v is AdmissionMode {
  return (ADMISSION_MODES as readonly string[]).includes(v);
}

/** Category → tier UUID entry, keyed by stringified geometry category index. */
export interface CategoryTierMapRow {
  readonly categoryIndex: number;
  readonly categoryName: string;
  readonly tierId: string;
  readonly autoCreate: boolean;
}

export interface BindSessionSeatingRequestBody {
  readonly seating_plan_version_id: string;
  readonly admission_mode: "assigned_seats" | "hybrid";
  readonly category_tier_map: Record<string, string | null>;
  readonly auto_create_tiers?: boolean;
}

export interface BindSessionSeatingResponse {
  readonly session: {
    readonly id: string;
    readonly event_id: string;
    readonly admission_mode: string;
    readonly seating_plan_version_id?: string;
    readonly seat_status_version: number;
    readonly capacity_total: number;
  };
  readonly seating_plan_version: SeatingPlanVersion;
  readonly materialized_seats: number;
  readonly category_tier_map: Record<string, string>;
  readonly created_tier_ids?: readonly string[];
  readonly rebound: boolean;
}

interface TicketTierItem {
  readonly id: string;
  readonly session_id: string;
  readonly name: string;
  readonly pricing_mode: string;
  readonly price_amount: number;
  readonly currency: string;
}

interface TicketTierListEnvelope {
  readonly ticket_tiers?: readonly TicketTierItem[];
  readonly tiers?: readonly TicketTierItem[];
}

interface SeatingPlanListEnvelope {
  readonly seating_plans: readonly SeatingPlan[];
}
interface SeatingPlanVersionEnvelope {
  readonly seating_plan_version: SeatingPlanVersion;
}

// ---------------------------------------------------------------------------
// Pure helpers (exported for unit tests)
// ---------------------------------------------------------------------------

/**
 * Extract the ordered list of categories from a geometry blob. If the
 * geometry has no `categories` array we synthesise one from the seats
 * (each unique category_index becomes an anonymous "Category N" entry).
 */
export function extractGeometryCategories(
  geometry: SeatingGeometry,
): readonly { index: number; name: string; color: string }[] {
  if (Array.isArray(geometry.categories) && geometry.categories.length > 0) {
    return geometry.categories.map((c) => ({
      index: c.index,
      name: c.name === "" ? `Category ${c.index}` : c.name,
      color: c.color,
    }));
  }
  const seen = new Set<number>();
  const out: { index: number; name: string; color: string }[] = [];
  for (const sec of geometry.sections ?? []) {
    for (const row of sec.rows) {
      for (const seat of row.seats) {
        if (!seen.has(seat.category_index)) {
          seen.add(seat.category_index);
          out.push({
            index: seat.category_index,
            name: `Category ${seat.category_index}`,
            color: "",
          });
        }
      }
    }
  }
  return out.sort((a, b) => a.index - b.index);
}

/**
 * Build a category_tier_map body-ready record from the UI rows. Rows
 * with auto_create=true are emitted with `null` per the OpenAPI contract
 * ("A `null` value asks the server to auto-provision a tier for that
 * category"). Rows with a non-empty tierId are emitted as-is. Rows with
 * neither are treated as unmapped and left out so validateBindForm can
 * flag them.
 */
export function buildCategoryTierMap(
  rows: readonly CategoryTierMapRow[],
): Record<string, string | null> {
  const out: Record<string, string | null> = {};
  for (const r of rows) {
    if (r.autoCreate) {
      out[String(r.categoryIndex)] = null;
    } else if (r.tierId.trim() !== "") {
      out[String(r.categoryIndex)] = r.tierId.trim();
    }
  }
  return out;
}

export interface BindFormErrors {
  readonly plan_version_id?: string;
  readonly admission_mode?: string;
  readonly unmapped_categories?: readonly number[];
}

/**
 * Client-side validation mirroring the server-side guards from bind.go:
 *   - admission_mode must be assigned_seats or hybrid
 *   - a plan version must be selected
 *   - every category must either have a tier UUID or auto-create enabled
 */
export function validateBindForm(input: {
  admissionMode: AdmissionMode;
  planVersionId: string;
  rows: readonly CategoryTierMapRow[];
}): BindFormErrors {
  const out: {
    -readonly [K in keyof BindFormErrors]?: BindFormErrors[K];
  } = {};
  if (input.admissionMode !== "assigned_seats" && input.admissionMode !== "hybrid") {
    out.admission_mode =
      "Admission mode must be assigned_seats or hybrid to bind a plan.";
  }
  if (input.planVersionId.trim() === "") {
    out.plan_version_id = "Pick a seating plan version to bind.";
  }
  const unmapped: number[] = [];
  for (const r of input.rows) {
    if (r.autoCreate) continue;
    if (r.tierId.trim() === "") unmapped.push(r.categoryIndex);
  }
  if (unmapped.length > 0) {
    out.unmapped_categories = unmapped;
  }
  return out;
}

/**
 * True when at least one row asks the server to auto-create a tier —
 * we set the top-level auto_create_tiers flag accordingly so the
 * backend accepts null values in the map.
 */
export function anyAutoCreate(rows: readonly CategoryTierMapRow[]): boolean {
  return rows.some((r) => r.autoCreate);
}

/**
 * Map an ApiError from the bind endpoint into an operator-facing
 * sentence. Recognises the SEAT-B2 error catalogue (openapi.yaml lines
 * 10540-10578) plus the 409 rebind guard. Falls back to the generic
 * "message (code)" pattern used elsewhere in events.tsx.
 */
export function mapBindError(err: ApiError): string {
  switch (err.code) {
    case "seating.invalid_body":
      return "The bind request body was rejected. Please refresh and retry.";
    case "seating.invalid_admission_mode":
      return "Admission mode must be assigned_seats or hybrid to bind a plan.";
    case "seating.version_not_found":
      return "The selected seating plan version no longer exists.";
    case "seating.invalid_category_key":
      return "One of the category → tier mapping keys is not a valid category index.";
    case "seating.unknown_category":
      return "One of the mapped categories is not present in the plan version geometry.";
    case "seating.invalid_category_tier_map":
      return "The category → tier mapping is not valid. Check each row.";
    case "seating.tier_not_found":
      return "One of the mapped ticket tiers no longer exists on this session.";
    case "seating.category_tier_map_incomplete":
      return "Every geometry category must be mapped, or auto-create must be enabled.";
    case "seating.rebind_forbidden":
      return (
        "This session already has reservations or tickets and cannot be rebound. " +
        "Create a new session to bind a different plan."
      );
    case "permissions.denied":
      return "Your account is missing event_session.assign_seating_plan.";
    default:
      if (err.status === 401) return "Session expired. Please sign in again.";
      if (err.status === 403)
        return "Forbidden — missing event_session.assign_seating_plan.";
      if (err.status === 409)
        return (
          "The session cannot be rebound while reservations or tickets exist. " +
          "Create a new session to bind a different plan."
        );
      if (err.status === 404)
        return "Session or plan not found. Refresh and retry.";
      if (err.status === 503)
        return "Seating service temporarily unavailable.";
      return `${err.message} (${err.code})`;
  }
}

/**
 * Return a friendly capacity summary string for the counters row.
 * Example: "1,200 seated / 300 standing".
 */
export function formatCapacityCounters(v: SeatingPlanVersion): string {
  const seated = v.capacity_seated.toLocaleString();
  const standing = v.capacity_standing.toLocaleString();
  return `${seated} seated / ${standing} standing`;
}

// ---------------------------------------------------------------------------
// UI component
// ---------------------------------------------------------------------------

export interface SessionSeatingBindPanelProps {
  readonly orgId: string;
  readonly eventId: string;
  readonly sessionId: string;
  readonly venueId: string | null;
  /** Called on successful bind so the outer editor can toast a message. */
  readonly onSaved: (label: string) => void;
  /** Called on any error so the outer editor can surface it. */
  readonly onError: (message: string) => void;
}

/**
 * Render the seating-bind sub-form inside the SessionEditor. Renders a
 * neutral placeholder when the parent event has no venue_id — the
 * seating plans listing is scoped to a venue and there is nothing to
 * pick otherwise.
 */
export function SessionSeatingBindPanel({
  orgId,
  eventId,
  sessionId,
  venueId,
  onSaved,
  onError,
}: SessionSeatingBindPanelProps): JSX.Element {
  const [admissionMode, setAdmissionMode] =
    useState<AdmissionMode>("assigned_seats");
  const [planId, setPlanId] = useState<string>("");
  const [versionN, setVersionN] = useState<string>("1");
  const [rows, setRows] = useState<CategoryTierMapRow[]>([]);

  const plansQuery = useQuery<SeatingPlanListEnvelope, ApiError>({
    queryKey: ["session-bind", "plans", venueId] as const,
    enabled: venueId !== null,
    queryFn: () =>
      authedFetch<SeatingPlanListEnvelope>({
        method: "GET",
        path: `/v1/venues/${venueId}/seating-plans`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  const versionQuery = useQuery<SeatingPlanVersionEnvelope, ApiError>({
    queryKey: ["session-bind", "version", planId, versionN] as const,
    enabled: planId !== "" && /^\d+$/.test(versionN) && Number(versionN) > 0,
    queryFn: () =>
      authedFetch<SeatingPlanVersionEnvelope>({
        method: "GET",
        path: `/v1/seating-plans/${planId}/versions/${versionN}`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  const tiersQuery = useQuery<TicketTierListEnvelope, ApiError>({
    queryKey: ["session-bind", "tiers", eventId, sessionId] as const,
    queryFn: () =>
      authedFetch<TicketTierListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${orgId}/events/${eventId}/sessions/${sessionId}/tiers`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  const version = versionQuery.data?.seating_plan_version;
  const geometryCategories = useMemo(
    () => (version ? extractGeometryCategories(version.geometry) : []),
    [version],
  );

  // Rebuild rows whenever the version geometry changes.
  const geometryKey = version?.id ?? "";
  useEffect(() => {
    setRows(
      geometryCategories.map((c) => ({
        categoryIndex: c.index,
        categoryName: c.name,
        tierId: "",
        autoCreate: true,
      })),
    );
    // We intentionally key on the version id string so the effect fires
    // once per geometry, not on every render of geometryCategories.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [geometryKey]);

  const availableTiers =
    tiersQuery.data?.ticket_tiers ?? tiersQuery.data?.tiers ?? [];

  const errors = useMemo(
    () => validateBindForm({ admissionMode, planVersionId: version?.id ?? "", rows }),
    [admissionMode, version, rows],
  );

  const mutation = useMutation<BindSessionSeatingResponse, ApiError, void>({
    mutationFn: () => {
      if (version === undefined) {
        throw new Error("version required");
      }
      const body: BindSessionSeatingRequestBody = {
        seating_plan_version_id: version.id,
        admission_mode:
          admissionMode === "hybrid" ? "hybrid" : "assigned_seats",
        category_tier_map: buildCategoryTierMap(rows),
        auto_create_tiers: anyAutoCreate(rows),
      };
      return authedFetch<BindSessionSeatingResponse>({
        method: "POST",
        path: `/v1/organizations/${orgId}/events/${eventId}/sessions/${sessionId}/seating`,
        body,
      });
    },
    onSuccess: (data) => {
      onSaved(
        data.rebound
          ? `Rebound session to plan version ${data.seating_plan_version.version_number}. ` +
              `Materialized ${data.materialized_seats.toLocaleString()} seats.`
          : `Bound session to plan version ${data.seating_plan_version.version_number}. ` +
              `Materialized ${data.materialized_seats.toLocaleString()} seats.`,
      );
    },
    onError: (err) => onError(mapBindError(err)),
  });

  if (venueId === null) {
    return (
      <div
        style={placeholderStyle}
        data-testid="events-session-seating-no-venue"
      >
        Attach a venue to this event before binding a seating plan.
      </div>
    );
  }

  const plans = plansQuery.data?.seating_plans ?? [];

  return (
    <section
      style={sectionStyle}
      data-testid={`events-session-seating-panel-${sessionId}`}
      aria-label="Seating binding"
    >
      <div style={detailLabelStyle}>Seating binding</div>

      <div style={gridStyle}>
        <label style={fieldStyle}>
          <span style={labelStyle}>Admission mode</span>
          <select
            value={admissionMode}
            onChange={(e) =>
              setAdmissionMode(
                isAdmissionMode(e.target.value)
                  ? e.target.value
                  : "assigned_seats",
              )
            }
            style={inputStyle}
            data-testid="events-session-seating-admission-mode"
          >
            {ADMISSION_MODES.map((m) => (
              <option key={m} value={m}>
                {m}
              </option>
            ))}
          </select>
          {errors.admission_mode !== undefined ? (
            <span style={fieldErrorStyle}>{errors.admission_mode}</span>
          ) : null}
        </label>

        <label style={fieldStyle}>
          <span style={labelStyle}>Seating plan</span>
          <select
            value={planId}
            onChange={(e) => {
              setPlanId(e.target.value);
              setVersionN("1");
            }}
            style={inputStyle}
            disabled={
              admissionMode === "general_admission" || plansQuery.isPending
            }
            data-testid="events-session-seating-plan"
          >
            <option value="">— Select a plan —</option>
            {plans.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </label>

        <label style={fieldStyle}>
          <span style={labelStyle}>Version</span>
          <input
            type="number"
            min={1}
            step={1}
            value={versionN}
            onChange={(e) => setVersionN(e.target.value)}
            style={inputStyle}
            disabled={
              admissionMode === "general_admission" || planId === ""
            }
            data-testid="events-session-seating-version"
          />
          {errors.plan_version_id !== undefined ? (
            <span style={fieldErrorStyle}>{errors.plan_version_id}</span>
          ) : null}
        </label>
      </div>

      {version !== undefined ? (
        <div
          style={countersStyle}
          data-testid="events-session-seating-counters"
        >
          Capacity: {formatCapacityCounters(version)}
        </div>
      ) : null}

      {admissionMode !== "general_admission" && version !== undefined ? (
        <div style={tableWrapStyle}>
          <table
            style={tableStyle}
            data-testid="events-session-seating-map-table"
          >
            <thead>
              <tr>
                <th scope="col" style={thStyle}>
                  Category
                </th>
                <th scope="col" style={thStyle}>
                  Ticket tier
                </th>
                <th scope="col" style={thStyle}>
                  Auto-create
                </th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr
                  key={r.categoryIndex}
                  data-testid={`events-session-seating-map-row-${r.categoryIndex}`}
                >
                  <td style={tdStyle}>{r.categoryName}</td>
                  <td style={tdStyle}>
                    <select
                      value={r.tierId}
                      onChange={(e) =>
                        setRows((prev) =>
                          prev.map((row) =>
                            row.categoryIndex === r.categoryIndex
                              ? { ...row, tierId: e.target.value }
                              : row,
                          ),
                        )
                      }
                      disabled={r.autoCreate}
                      style={inputStyle}
                      data-testid={`events-session-seating-map-tier-${r.categoryIndex}`}
                    >
                      <option value="">— Pick a tier —</option>
                      {availableTiers.map((t) => (
                        <option key={t.id} value={t.id}>
                          {t.name}
                        </option>
                      ))}
                    </select>
                  </td>
                  <td style={tdStyle}>
                    <input
                      type="checkbox"
                      checked={r.autoCreate}
                      onChange={(e) =>
                        setRows((prev) =>
                          prev.map((row) =>
                            row.categoryIndex === r.categoryIndex
                              ? { ...row, autoCreate: e.target.checked }
                              : row,
                          ),
                        )
                      }
                      data-testid={`events-session-seating-map-auto-${r.categoryIndex}`}
                    />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {errors.unmapped_categories !== undefined ? (
            <div
              style={fieldErrorStyle}
              data-testid="events-session-seating-unmapped-error"
            >
              Unmapped categories: {errors.unmapped_categories.join(", ")}
            </div>
          ) : null}
        </div>
      ) : null}

      <div style={actionsStyle}>
        <button
          type="button"
          style={primaryButtonStyle}
          disabled={
            admissionMode === "general_admission" ||
            Object.keys(errors).length > 0 ||
            mutation.isPending ||
            version === undefined
          }
          onClick={() => mutation.mutate()}
          data-testid="events-session-seating-submit"
        >
          {mutation.isPending ? "Binding…" : "Bind seating plan"}
        </button>
      </div>
    </section>
  );
}

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

const sectionStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 12,
  marginTop: 16,
  padding: 12,
  border: "1px solid #e5e7eb",
  borderRadius: 8,
};

const detailLabelStyle: CSSProperties = {
  fontSize: 14,
  fontWeight: 600,
  color: "#111827",
};

const gridStyle: CSSProperties = {
  display: "grid",
  gap: 12,
  gridTemplateColumns: "repeat(auto-fit, minmax(220px, 1fr))",
};

const fieldStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
};

const labelStyle: CSSProperties = {
  fontSize: 12,
  color: "#6b7280",
};

const inputStyle: CSSProperties = {
  padding: "6px 8px",
  borderRadius: 6,
  border: "1px solid #d1d5db",
  minHeight: 32,
};

const countersStyle: CSSProperties = {
  padding: "6px 8px",
  background: "#f9fafb",
  borderRadius: 6,
  fontSize: 13,
  color: "#374151",
};

const tableWrapStyle: CSSProperties = {
  overflowX: "auto",
};

const tableStyle: CSSProperties = {
  width: "100%",
  borderCollapse: "collapse",
  fontSize: 13,
};

const thStyle: CSSProperties = {
  textAlign: "left",
  padding: "6px 8px",
  borderBottom: "1px solid #e5e7eb",
  fontWeight: 600,
  color: "#374151",
};

const tdStyle: CSSProperties = {
  padding: "6px 8px",
  borderBottom: "1px solid #f3f4f6",
  verticalAlign: "middle",
};

const fieldErrorStyle: CSSProperties = {
  fontSize: 12,
  color: "#b91c1c",
};

const actionsStyle: CSSProperties = {
  display: "flex",
  gap: 8,
  justifyContent: "flex-end",
};

const primaryButtonStyle: CSSProperties = {
  padding: "8px 12px",
  borderRadius: 6,
  border: "1px solid #2563eb",
  background: "#2563eb",
  color: "#fff",
  cursor: "pointer",
};

const placeholderStyle: CSSProperties = {
  marginTop: 16,
  padding: 12,
  background: "#fff7ed",
  borderRadius: 8,
  fontSize: 13,
  color: "#7c2d12",
};
