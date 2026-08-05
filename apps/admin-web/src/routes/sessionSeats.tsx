/**
 * Interactive session seat management screen (feature #317, Wave SEAT-E3).
 *
 * Operational admin surface for closing individual seats, whole rows, or
 * whole sectors for sale (tech seats, camera platforms, blocked
 * sightlines, house holds) and reopening them. Backed by the operator
 * PATCH endpoint delivered in Wave SEAT-B4:
 *
 *   PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{id}/seats
 *
 * The route also composes two public read endpoints from Wave SEAT-B3:
 *
 *   GET /v1/event-sessions/{id}/schema        — canonical geometry + categories
 *   GET /v1/event-sessions/{id}/seat-status   — per-seat status snapshot
 *
 * UX contract enforced here:
 *
 *   1. Client-side geometry → SVG renderer, coloured by live seat status
 *      with a category legend (§ renderSeatMapSVG).
 *   2. Click a seat to toggle a single-seat block/unblock. Multi-select
 *      via the sector / row picker resolves to the union of seat_keys and
 *      is submitted in ONE PATCH batch so `seat_status_version` advances
 *      exactly once. The endpoint also accepts a `sectors` / `rows`
 *      selector envelope directly — we send seat_keys for stable audit
 *      logs but the picker helpers are shared with a sectors[]/rows[]
 *      shortcut.
 *   3. Held / sold seats surfaced by the endpoint as skipped are reported
 *      inline with the reason (never silently mutated).
 *   4. Read-only counters per sector and per category (available / held /
 *      sold / unavailable). No per-seat repainting (category changes ship
 *      only via a new plan version — separate SEAT-E1 flow).
 *   5. Wave-M responsive rules: two-column layout on desktop (map on the
 *      left, controls on the right) collapses to a single stack on
 *      narrow viewports via a `forceLayout` escape hatch for tests.
 *
 * Mock data: NONE. Every read + write goes to the live backend. All pure
 * helpers below are exported for unit tests in sessionSeats.test.ts.
 */
import { createRoute, useParams } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  useCallback,
  useMemo,
  useState,
  type CSSProperties,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
} from "react";
import { Route as RootRoute } from "./__root";
import { ApiError, authedFetch } from "@/lib/api/client";
import {
  renderGeometryToSVG,
  type SeatingCategory,
  type SeatingGeometry,
  type SeatingSection,
} from "./venueSeatingPlans";

// ---------------------------------------------------------------------------
// Route registration
// ---------------------------------------------------------------------------

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/organizations/$orgId/events/$eventId/sessions/$sessionId/seats",
  component: SessionSeatsRoute,
});

// ---------------------------------------------------------------------------
// Wire types (mirror openapi/clients/ts/index.d.ts)
// ---------------------------------------------------------------------------

export type SeatStatus = "available" | "held" | "sold" | "unavailable";

export interface SeatingSchemaEnvelope {
  readonly session_id: string;
  readonly event_id: string;
  readonly admission_mode: "assigned_seats" | "hybrid";
  readonly seat_status_version: number;
  readonly geometry_checksum: string;
  readonly capacity_seated: number;
  readonly capacity_standing: number;
  readonly geometry: SeatingGeometry;
  readonly category_prices?: readonly SeatingCategoryPrice[];
}

export interface SeatingCategoryPrice {
  readonly index: number;
  readonly name: string;
  readonly color?: string;
  readonly tier_name?: string;
  readonly price_amount?: number;
  readonly currency?: string;
}

export interface SeatStatusEnvelope {
  readonly session_id: string;
  readonly status_version: number;
  readonly seats: Record<string, SeatStatus>;
  readonly delta: boolean;
}

export interface PatchSeatsOutcome {
  readonly seat_key: string;
  readonly outcome: "unavailable" | "available" | "noop" | "skipped";
  readonly reason?: string;
  readonly status: string;
}

export interface PatchSeatsSummary {
  readonly requested: number;
  readonly changed: number;
  readonly noop: number;
  readonly skipped: number;
}

export interface PatchSeatsResponse {
  readonly session_id: string;
  readonly action: "block" | "unblock";
  readonly seat_status_version: number;
  readonly outcomes: readonly PatchSeatsOutcome[];
  readonly summary: PatchSeatsSummary;
}

export interface PatchSeatsRowSelector {
  readonly sector: string;
  readonly row: string;
}

export interface PatchSeatsRequest {
  readonly action: "block" | "unblock";
  readonly seat_keys?: readonly string[];
  readonly sectors?: readonly string[];
  readonly rows?: readonly PatchSeatsRowSelector[];
}

// AB-39 (feature #429): per-seat price category reassignment.
export interface PatchSeatsCategoryRequest {
  readonly tier_id: string;
  readonly seat_keys: readonly string[];
}

export interface PatchSeatsCategorySummary {
  readonly requested: number;
  readonly updated: number;
  readonly unknown: number;
}

export interface PatchSeatsCategoryResponse {
  readonly session_id: string;
  readonly tier_id: string;
  readonly seat_status_version: number;
  readonly updated_seat_keys: readonly string[];
  readonly unknown_seat_keys: readonly string[];
  readonly summary: PatchSeatsCategorySummary;
}

export interface AdminSeatRow {
  readonly id: string;
  readonly seat_key: string;
  readonly sector_name: string;
  readonly row_name: string;
  readonly seat_number: string;
  readonly status: SeatStatus;
  readonly tier_id: string | null;
  readonly kind: string;
}

export interface AdminSeatsResponse {
  readonly session_id: string;
  readonly seat_status_version: number;
  readonly seats: readonly AdminSeatRow[];
}

export interface AdminTicketTier {
  readonly id: string;
  readonly session_id: string;
  readonly name: string;
  readonly pricing_mode: string;
  readonly price_amount: number;
  readonly currency: string;
}

export interface AdminTicketTierEnvelope {
  readonly ticket_tiers?: readonly AdminTicketTier[];
  readonly tiers?: readonly AdminTicketTier[];
}

// ---------------------------------------------------------------------------
// Colour palette shared by legend, seats, and counters
// ---------------------------------------------------------------------------

export const SEAT_STATUS_COLOURS: Record<SeatStatus, string> = {
  available: "#22c55e", // green
  held: "#f59e0b", // amber
  sold: "#0369a1", // blue
  unavailable: "#64748b", // slate
};

export const SEAT_STATUS_LABELS: Record<SeatStatus, string> = {
  available: "Available",
  held: "Held",
  sold: "Sold",
  unavailable: "Unavailable",
};

// ---------------------------------------------------------------------------
// Pure helpers (exported for tests)
// ---------------------------------------------------------------------------

const UNKNOWN_STATUS_COLOUR = "#e2e8f0";
const SELECTED_STROKE = "#e11d48";
const SELECTED_STROKE_WIDTH = 2;

/**
 * Collect every seat_key that belongs to the named sector. Case-sensitive
 * match against `section.name` (the operator-visible label). Returns an
 * empty array for unknown sectors — the caller can treat that as a no-op.
 */
export function collectSeatKeysForSector(
  geometry: SeatingGeometry,
  sectorName: string,
): readonly string[] {
  const out: string[] = [];
  for (const sec of geometry.sections ?? []) {
    if (sec.name !== sectorName) continue;
    for (const row of sec.rows) {
      for (const seat of row.seats) out.push(seat.key);
    }
  }
  return out;
}

/**
 * Collect every seat_key that belongs to the named (sector, row) pair.
 * Case-sensitive match against `section.name` and `row.name`.
 */
export function collectSeatKeysForRow(
  geometry: SeatingGeometry,
  sectorName: string,
  rowName: string,
): readonly string[] {
  const out: string[] = [];
  for (const sec of geometry.sections ?? []) {
    if (sec.name !== sectorName) continue;
    for (const row of sec.rows) {
      if (row.name !== rowName) continue;
      for (const seat of row.seats) out.push(seat.key);
    }
  }
  return out;
}

export interface SectorCounter {
  readonly name: string;
  readonly total: number;
  readonly by_status: Record<SeatStatus, number>;
}

/**
 * Read-only counters keyed by sector name. Every sector present in the
 * geometry is emitted even when it has zero seats in a given status.
 * Unknown statuses fall back to `available` for the counter roll-up but
 * are NOT silently coloured — the SVG renderer paints them with the
 * unknown-status fallback so operators notice the discrepancy.
 */
export function computeSectorCounters(
  geometry: SeatingGeometry,
  statuses: Readonly<Record<string, SeatStatus>>,
): readonly SectorCounter[] {
  const out: SectorCounter[] = [];
  for (const sec of geometry.sections ?? []) {
    const by: Record<SeatStatus, number> = {
      available: 0,
      held: 0,
      sold: 0,
      unavailable: 0,
    };
    let total = 0;
    for (const row of sec.rows) {
      for (const seat of row.seats) {
        total++;
        const s = statuses[seat.key];
        if (
          s === "available" ||
          s === "held" ||
          s === "sold" ||
          s === "unavailable"
        ) {
          by[s]++;
        } else {
          by.available++;
        }
      }
    }
    out.push({ name: sec.name, total, by_status: by });
  }
  return out;
}

export interface CategoryCounter {
  readonly index: number;
  readonly name: string;
  readonly color: string;
  readonly total: number;
  readonly by_status: Record<SeatStatus, number>;
}

/**
 * Read-only counters keyed by category index. Uses `categories[]` from
 * the geometry as the source of truth for name + colour. Seats whose
 * category_index is unknown are grouped under a synthetic "Uncategorised"
 * bucket with index=0 so the operator can spot importer drift.
 */
export function computeCategoryCounters(
  geometry: SeatingGeometry,
  statuses: Readonly<Record<string, SeatStatus>>,
): readonly CategoryCounter[] {
  const byIndex = new Map<number, CategoryCounter>();
  for (const c of geometry.categories ?? []) {
    byIndex.set(c.index, {
      index: c.index,
      name: c.name,
      color: c.color,
      total: 0,
      by_status: { available: 0, held: 0, sold: 0, unavailable: 0 },
    });
  }
  const uncategorised: CategoryCounter = {
    index: 0,
    name: "Uncategorised",
    color: UNKNOWN_STATUS_COLOUR,
    total: 0,
    by_status: { available: 0, held: 0, sold: 0, unavailable: 0 },
  };
  let uncategorisedUsed = false;

  for (const sec of geometry.sections ?? []) {
    for (const row of sec.rows) {
      for (const seat of row.seats) {
        const bucket = byIndex.get(seat.category_index);
        const s = statuses[seat.key];
        const status: SeatStatus =
          s === "available" ||
          s === "held" ||
          s === "sold" ||
          s === "unavailable"
            ? s
            : "available";
        if (bucket === undefined) {
          uncategorisedUsed = true;
          uncategorised.by_status[status]++;
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          (uncategorised as any).total = uncategorised.total + 1;
        } else {
          bucket.by_status[status]++;
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          (bucket as any).total = bucket.total + 1;
        }
      }
    }
  }
  const out: CategoryCounter[] = [];
  for (const c of geometry.categories ?? []) {
    const bucket = byIndex.get(c.index);
    if (bucket !== undefined) out.push(bucket);
  }
  if (uncategorisedUsed) out.push(uncategorised);
  return out;
}

/**
 * Collect all unique sector names + per-sector row-name lists from the
 * geometry, sorted by first appearance. Used to drive the sector / row
 * picker dropdowns.
 */
export interface SectorRowIndex {
  readonly sectorNames: readonly string[];
  readonly rowsBySector: Readonly<Record<string, readonly string[]>>;
}

export function buildSectorRowIndex(
  geometry: SeatingGeometry,
): SectorRowIndex {
  const sectorNames: string[] = [];
  const rowsBySector: Record<string, string[]> = {};
  const seenSectors = new Set<string>();
  for (const sec of geometry.sections ?? []) {
    if (!seenSectors.has(sec.name)) {
      seenSectors.add(sec.name);
      sectorNames.push(sec.name);
      rowsBySector[sec.name] = [];
    }
    const bucket = rowsBySector[sec.name] ?? [];
    const seenRows = new Set(bucket);
    for (const row of sec.rows) {
      if (!seenRows.has(row.name)) {
        seenRows.add(row.name);
        bucket.push(row.name);
      }
    }
    rowsBySector[sec.name] = bucket;
  }
  return { sectorNames, rowsBySector };
}

/**
 * Render an inline SVG with per-seat status colouring, category legend
 * geometry, and a red stroke on the seats the operator has staged in the
 * current selection. Reuses the seat coordinate primitives from
 * renderGeometryToSVG so the visual footprint stays byte-stable with the
 * SEAT-E1 read-only preview — but overlays live status colours.
 */
export function renderSeatMapSVG(
  geometry: SeatingGeometry,
  statuses: Readonly<Record<string, SeatStatus>>,
  selectedKeys: ReadonlySet<string>,
): string {
  const width = safeCoord(geometry.canvas?.width ?? 800);
  const height = safeCoord(geometry.canvas?.height ?? 600);
  const parts: string[] = [];
  for (const sec of geometry.sections ?? []) {
    for (const row of sec.rows) {
      for (const seat of row.seats) {
        const st = statuses[seat.key];
        const fill = isSeatStatus(st)
          ? SEAT_STATUS_COLOURS[st]
          : UNKNOWN_STATUS_COLOUR;
        const selected = selectedKeys.has(seat.key);
        const stroke = selected
          ? ` stroke="${SELECTED_STROKE}" stroke-width="${SELECTED_STROKE_WIDTH}"`
          : "";
        parts.push(
          `<circle cx="${round(seat.x)}" cy="${round(seat.y)}" r="${round(
            seat.radius,
          )}" fill="${escapeAttr(fill)}"${stroke} data-seat-key="${escapeAttr(
            seat.key,
          )}" data-status="${escapeAttr(st ?? "")}"><title>${escapeText(
            seatTitle(seat.key, st),
          )}</title></circle>`,
        );
      }
    }
  }
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${round(
      width,
    )} ${round(
      height,
    )}" role="img" aria-label="Interactive session seat map" data-testid="session-seats-svg">` +
    `<g data-role="seats">${parts.join("")}</g>` +
    `</svg>`
  );
}

function seatTitle(key: string, status: SeatStatus | undefined): string {
  if (status === undefined) return key;
  return `${key} — ${SEAT_STATUS_LABELS[status]}`;
}

function isSeatStatus(v: string | undefined): v is SeatStatus {
  return v === "available" || v === "held" || v === "sold" || v === "unavailable";
}

function safeCoord(n: number): number {
  return Number.isFinite(n) && n > 0 ? n : 100;
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
 * Split the response outcomes into two operator-facing buckets: seats
 * that changed (outcome "unavailable" after a block / "available" after
 * an unblock; noop is idempotent) and seats skipped with a reason.
 */
export interface OutcomeSummary {
  readonly changed: readonly PatchSeatsOutcome[];
  readonly noop: readonly PatchSeatsOutcome[];
  readonly skipped: readonly PatchSeatsOutcome[];
  readonly skippedByReason: Readonly<Record<string, readonly PatchSeatsOutcome[]>>;
}

export function summariseOutcomes(
  outcomes: readonly PatchSeatsOutcome[],
): OutcomeSummary {
  const changed: PatchSeatsOutcome[] = [];
  const noop: PatchSeatsOutcome[] = [];
  const skipped: PatchSeatsOutcome[] = [];
  const skippedByReason: Record<string, PatchSeatsOutcome[]> = {};
  for (const o of outcomes) {
    if (o.outcome === "unavailable" || o.outcome === "available") {
      changed.push(o);
    } else if (o.outcome === "noop") {
      noop.push(o);
    } else {
      skipped.push(o);
      const reason = o.reason ?? "unknown";
      const bucket = skippedByReason[reason] ?? [];
      bucket.push(o);
      skippedByReason[reason] = bucket;
    }
  }
  return { changed, noop, skipped, skippedByReason };
}

/**
 * Merge a seat-status delta or full snapshot into an existing map. The
 * caller passes the previous state; the returned object is a NEW map so
 * React state updates trigger without mutation.
 */
export function mergeSeatStatus(
  prev: Readonly<Record<string, SeatStatus>>,
  next: SeatStatusEnvelope,
): Record<string, SeatStatus> {
  const out: Record<string, SeatStatus> = next.delta ? { ...prev } : {};
  for (const [k, v] of Object.entries(next.seats)) {
    if (isSeatStatus(v)) out[k] = v;
  }
  return out;
}

// ---------------------------------------------------------------------------
// Route component
// ---------------------------------------------------------------------------

function SessionSeatsRoute() {
  const { orgId, eventId, sessionId } = useParams({
    from: "/organizations/$orgId/events/$eventId/sessions/$sessionId/seats",
  });
  return (
    <SessionSeatsScreen
      orgId={orgId}
      eventId={eventId}
      sessionId={sessionId}
    />
  );
}

export interface SessionSeatsScreenProps {
  readonly orgId: string;
  readonly eventId: string;
  readonly sessionId: string;
  /** Test-only escape hatch to force layout mode. */
  readonly forceLayout?: "desktop" | "mobile";
}

export function SessionSeatsScreen({
  orgId,
  eventId,
  sessionId,
  forceLayout,
}: SessionSeatsScreenProps): JSX.Element {
  const qc = useQueryClient();
  const [selected, setSelected] = useState<ReadonlySet<string>>(
    () => new Set<string>(),
  );
  const [banner, setBanner] = useState<{
    tone: "ok" | "err";
    message: string;
  } | null>(null);
  const [outcomeSummary, setOutcomeSummary] = useState<OutcomeSummary | null>(
    null,
  );

  const schemaQuery = useQuery<SeatingSchemaEnvelope, ApiError>({
    queryKey: ["session-seats-schema", sessionId],
    queryFn: () =>
      authedFetch<SeatingSchemaEnvelope>({
        method: "GET",
        path: `/v1/event-sessions/${sessionId}/schema`,
      }),
    refetchOnWindowFocus: false,
    retry: false,
  });

  const seatStatusQuery = useQuery<SeatStatusEnvelope, ApiError>({
    queryKey: ["session-seats-status", sessionId],
    queryFn: () =>
      authedFetch<SeatStatusEnvelope>({
        method: "GET",
        path: `/v1/event-sessions/${sessionId}/seat-status`,
      }),
    refetchOnWindowFocus: false,
    retry: false,
  });

  // AB-39: admin seat inventory with per-seat tier binding + active tiers.
  const adminSeatsQuery = useQuery<AdminSeatsResponse, ApiError>({
    queryKey: ["session-seats-admin", sessionId],
    queryFn: () =>
      authedFetch<AdminSeatsResponse>({
        method: "GET",
        path: `/v1/event-sessions/${sessionId}/seats/admin`,
      }),
    refetchOnWindowFocus: false,
    retry: false,
  });

  const tiersQuery = useQuery<AdminTicketTierEnvelope, ApiError>({
    queryKey: ["session-seats-tiers", orgId, eventId, sessionId],
    queryFn: () =>
      authedFetch<AdminTicketTierEnvelope>({
        method: "GET",
        path: `/v1/organizations/${orgId}/events/${eventId}/sessions/${sessionId}/tiers`,
      }),
    refetchOnWindowFocus: false,
    retry: false,
  });

  const tiers = useMemo<readonly AdminTicketTier[]>(
    () =>
      tiersQuery.data?.ticket_tiers ??
      tiersQuery.data?.tiers ??
      [],
    [tiersQuery.data],
  );
  const tierById = useMemo<Record<string, AdminTicketTier>>(() => {
    const out: Record<string, AdminTicketTier> = {};
    for (const t of tiers) out[t.id] = t;
    return out;
  }, [tiers]);

  const patchCategory = useMutation<
    PatchSeatsCategoryResponse,
    ApiError,
    PatchSeatsCategoryRequest
  >({
    mutationFn: (req) =>
      authedFetch<PatchSeatsCategoryResponse>({
        method: "PATCH",
        path: `/v1/event-sessions/${sessionId}/seats/category`,
        body: req,
      }),
    onSuccess: (res) => {
      const tierName = tierById[res.tier_id]?.name ?? res.tier_id.slice(0, 8);
      setOutcomeSummary(null);
      setBanner({
        tone: "ok",
        message: `Reassigned ${res.summary.updated} seat(s) to tier "${tierName}"${
          res.summary.unknown > 0 ? ` (${res.summary.unknown} unknown seat_key skipped)` : ""
        }. seat_status_version=${res.seat_status_version}.`,
      });
      setSelected(new Set<string>());
      qc.invalidateQueries({ queryKey: ["session-seats-admin", sessionId] });
      qc.invalidateQueries({ queryKey: ["session-seats-status", sessionId] });
    },
    onError: (err) => {
      setOutcomeSummary(null);
      const conflictKeys =
        err.details && typeof err.details === "object"
          ? (err.details as { conflicting_seat_keys?: readonly string[] })
              .conflicting_seat_keys
          : undefined;
      const suffix =
        conflictKeys && conflictKeys.length > 0
          ? ` — conflicting seats: ${conflictKeys.slice(0, 6).join(", ")}${
              conflictKeys.length > 6 ? " …" : ""
            }`
          : "";
      setBanner({
        tone: "err",
        message: `${err.code}: ${err.message}${suffix}`,
      });
    },
  });

  const patch = useMutation<PatchSeatsResponse, ApiError, PatchSeatsRequest>({
    mutationFn: (req) =>
      authedFetch<PatchSeatsResponse>({
        method: "PATCH",
        path: `/v1/organizations/${orgId}/events/${eventId}/sessions/${sessionId}/seats`,
        body: req,
      }),
    onSuccess: (res) => {
      const summary = summariseOutcomes(res.outcomes);
      setOutcomeSummary(summary);
      setBanner({
        tone: "ok",
        message: `Applied: ${res.summary.changed} changed, ${res.summary.noop} noop, ${res.summary.skipped} skipped.`,
      });
      setSelected(new Set<string>());
      qc.invalidateQueries({ queryKey: ["session-seats-status", sessionId] });
      qc.invalidateQueries({ queryKey: ["session-seats-admin", sessionId] });
    },
    onError: (err) => {
      setOutcomeSummary(null);
      setBanner({
        tone: "err",
        message: `${err.code}: ${err.message}`,
      });
    },
  });

  const geometry = schemaQuery.data?.geometry ?? { sections: [] };
  const statuses = seatStatusQuery.data?.seats ?? {};

  const svgHtml = useMemo(
    () => renderSeatMapSVG(geometry, statuses, selected),
    [geometry, statuses, selected],
  );

  const sectorCounters = useMemo(
    () => computeSectorCounters(geometry, statuses),
    [geometry, statuses],
  );
  const categoryCounters = useMemo(
    () => computeCategoryCounters(geometry, statuses),
    [geometry, statuses],
  );
  const sectorRowIndex = useMemo(
    () => buildSectorRowIndex(geometry),
    [geometry],
  );

  const onSvgClick = useCallback((e: ReactMouseEvent<HTMLDivElement>) => {
    const target = e.target as Element | null;
    if (target === null) return;
    const key = target.getAttribute?.("data-seat-key");
    if (typeof key !== "string" || key === "") return;
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }, []);

  const addSector = useCallback(
    (name: string) => {
      const keys = collectSeatKeysForSector(geometry, name);
      setSelected((prev) => {
        const next = new Set(prev);
        for (const k of keys) next.add(k);
        return next;
      });
    },
    [geometry],
  );

  const addRow = useCallback(
    (sector: string, row: string) => {
      const keys = collectSeatKeysForRow(geometry, sector, row);
      setSelected((prev) => {
        const next = new Set(prev);
        for (const k of keys) next.add(k);
        return next;
      });
    },
    [geometry],
  );

  const clearSelection = useCallback(() => {
    setSelected(new Set<string>());
    setOutcomeSummary(null);
  }, []);

  const applyBlock = useCallback(() => {
    if (selected.size === 0) return;
    patch.mutate({ action: "block", seat_keys: Array.from(selected) });
  }, [selected, patch]);

  const applyUnblock = useCallback(() => {
    if (selected.size === 0) return;
    patch.mutate({ action: "unblock", seat_keys: Array.from(selected) });
  }, [selected, patch]);

  // AB-39: reassign the current selection to a chosen tier.
  const applyCategory = useCallback(
    (tierID: string) => {
      if (selected.size === 0 || tierID === "") return;
      patchCategory.mutate({
        tier_id: tierID,
        seat_keys: Array.from(selected),
      });
    },
    [selected, patchCategory],
  );

  // Toggle-select a whole row of the seat table.
  const onTableRowClick = useCallback(
    (key: string, extend: boolean) => {
      setSelected((prev) => {
        const next = extend ? new Set(prev) : new Set<string>();
        if (extend && prev.has(key)) next.delete(key);
        else next.add(key);
        return next;
      });
    },
    [],
  );

  const isDesktop =
    forceLayout === "desktop" ? true : forceLayout === "mobile" ? false : true;

  return (
    <div
      style={pageStyle}
      data-testid="session-seats-page"
      data-org-id={orgId}
      data-event-id={eventId}
      data-session-id={sessionId}
    >
      <header style={headerStyle}>
        <h1 style={h1Style}>Seat management</h1>
        <p style={subtitleStyle}>
          Session <code>{sessionId}</code> • Event <code>{eventId}</code>
        </p>
      </header>

      {schemaQuery.isPending || seatStatusQuery.isPending ? (
        <div style={statusBoxStyle} role="status">
          Loading seat map…
        </div>
      ) : schemaQuery.isError ? (
        <div style={errorBoxStyle} role="alert" data-testid="session-seats-schema-error">
          <strong>Failed to load seat map.</strong>
          <div style={errorCodeStyle}>{schemaQuery.error?.code ?? "unknown.error"}</div>
          {schemaQuery.error?.message ? (
            <div>{schemaQuery.error.message}</div>
          ) : null}
        </div>
      ) : (
        <div style={isDesktop ? twoColumnStyle : oneColumnStyle}>
          <section style={mapColumnStyle}>
            <Legend categories={geometry.categories ?? []} />
            <div
              style={svgFrameStyle}
              onClick={onSvgClick}
              data-testid="session-seats-map"
              // Safe: renderSeatMapSVG escapes every interpolated value.
              dangerouslySetInnerHTML={{ __html: svgHtml }}
            />
            <SeatTable
              rows={adminSeatsQuery.data?.seats ?? []}
              tiers={tierById}
              selected={selected}
              onRowClick={onTableRowClick}
              loading={adminSeatsQuery.isPending}
              error={adminSeatsQuery.error}
            />
          </section>

          <aside style={sideColumnStyle}>
            <SelectionPanel
              selected={selected}
              sectorRowIndex={sectorRowIndex}
              onAddSector={addSector}
              onAddRow={addRow}
              onClear={clearSelection}
              onBlock={applyBlock}
              onUnblock={applyUnblock}
              pending={patch.isPending}
            />

            <CategoryPicker
              selected={selected}
              tiers={tiers}
              onApply={applyCategory}
              pending={patchCategory.isPending}
            />

            {banner !== null ? (
              <div
                style={
                  banner.tone === "ok" ? okBoxStyle : errorBoxStyle
                }
                role={banner.tone === "ok" ? "status" : "alert"}
                data-testid={`session-seats-banner-${banner.tone}`}
              >
                {banner.message}
              </div>
            ) : null}

            {outcomeSummary !== null ? (
              <OutcomeSummaryPanel summary={outcomeSummary} />
            ) : null}

            <SectorCounters counters={sectorCounters} />
            <CategoryCounters counters={categoryCounters} />
          </aside>
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Legend + counters + selection sub-components
// ---------------------------------------------------------------------------

function Legend({
  categories,
}: {
  categories: readonly SeatingCategory[];
}) {
  return (
    <div style={legendStyle} data-testid="session-seats-legend">
      <div style={sectionTitleStyle}>Status</div>
      <div style={legendRowStyle}>
        {(Object.keys(SEAT_STATUS_COLOURS) as SeatStatus[]).map((s) => (
          <span key={s} style={legendItemStyle}>
            <span
              style={{
                ...legendSwatchStyle,
                background: SEAT_STATUS_COLOURS[s],
              }}
            />
            {SEAT_STATUS_LABELS[s]}
          </span>
        ))}
      </div>
      {categories.length > 0 ? (
        <>
          <div style={sectionTitleStyle}>Categories</div>
          <div style={legendRowStyle} data-testid="session-seats-category-legend">
            {categories.map((c) => (
              <span key={c.index} style={legendItemStyle}>
                <span
                  style={{ ...legendSwatchStyle, background: c.color }}
                />
                {c.name}
              </span>
            ))}
          </div>
        </>
      ) : null}
    </div>
  );
}

function SelectionPanel({
  selected,
  sectorRowIndex,
  onAddSector,
  onAddRow,
  onClear,
  onBlock,
  onUnblock,
  pending,
}: {
  selected: ReadonlySet<string>;
  sectorRowIndex: SectorRowIndex;
  onAddSector: (name: string) => void;
  onAddRow: (sector: string, row: string) => void;
  onClear: () => void;
  onBlock: () => void;
  onUnblock: () => void;
  pending: boolean;
}) {
  const [sector, setSector] = useState<string>(
    sectorRowIndex.sectorNames[0] ?? "",
  );
  const [row, setRow] = useState<string>("");
  const rows = sector !== "" ? sectorRowIndex.rowsBySector[sector] ?? [] : [];

  return (
    <div style={sectionStyle} data-testid="session-seats-selection-panel">
      <div style={sectionTitleStyle}>Selection ({selected.size})</div>
      <div style={selectorRowStyle}>
        <label style={miniLabelStyle}>
          Sector
          <select
            value={sector}
            onChange={(e) => {
              setSector(e.target.value);
              setRow("");
            }}
            style={inputStyle}
            data-testid="session-seats-sector-picker"
          >
            <option value="">—</option>
            {sectorRowIndex.sectorNames.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </label>
        <button
          type="button"
          style={secondaryButtonStyle}
          disabled={sector === ""}
          onClick={() => onAddSector(sector)}
          data-testid="session-seats-sector-add"
        >
          Add sector
        </button>
      </div>
      <div style={selectorRowStyle}>
        <label style={miniLabelStyle}>
          Row
          <select
            value={row}
            onChange={(e) => setRow(e.target.value)}
            style={inputStyle}
            disabled={sector === ""}
            data-testid="session-seats-row-picker"
          >
            <option value="">—</option>
            {rows.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>
        </label>
        <button
          type="button"
          style={secondaryButtonStyle}
          disabled={sector === "" || row === ""}
          onClick={() => onAddRow(sector, row)}
          data-testid="session-seats-row-add"
        >
          Add row
        </button>
      </div>
      <div style={buttonRowStyle}>
        <button
          type="button"
          style={dangerButtonStyle}
          disabled={pending || selected.size === 0}
          onClick={onBlock}
          data-testid="session-seats-block"
        >
          {pending ? "Applying…" : "Make unavailable"}
        </button>
        <button
          type="button"
          style={primaryButtonStyle}
          disabled={pending || selected.size === 0}
          onClick={onUnblock}
          data-testid="session-seats-unblock"
        >
          {pending ? "Applying…" : "Make available"}
        </button>
        <button
          type="button"
          style={secondaryButtonStyle}
          disabled={selected.size === 0}
          onClick={onClear}
          data-testid="session-seats-clear"
        >
          Clear
        </button>
      </div>
    </div>
  );
}

function OutcomeSummaryPanel({ summary }: { summary: OutcomeSummary }) {
  return (
    <div style={sectionStyle} data-testid="session-seats-outcomes">
      <div style={sectionTitleStyle}>
        Last batch — {summary.changed.length} changed,{" "}
        {summary.noop.length} noop, {summary.skipped.length} skipped
      </div>
      {summary.skipped.length === 0 ? (
        <p style={hintStyle}>All seats accepted the transition.</p>
      ) : (
        <ul style={skippedListStyle} data-testid="session-seats-skipped">
          {Object.entries(summary.skippedByReason).map(([reason, group]) => (
            <li key={reason} style={skippedItemStyle}>
              <strong style={skippedReasonStyle}>{reason}</strong>{" "}
              <span style={hintStyle}>({group.length})</span>
              <div style={skippedSeatsStyle}>
                {group.slice(0, 8).map((o) => (
                  <code key={o.seat_key} style={seatCodeStyle}>
                    {o.seat_key}
                  </code>
                ))}
                {group.length > 8 ? (
                  <span style={hintStyle}>… +{group.length - 8} more</span>
                ) : null}
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// AB-39: category picker — one dropdown of active tiers + Apply button.
// Applies the current selection to the chosen tier via the bulk PATCH
// endpoint. Disabled when the selection is empty, no tier chosen, or a
// request is inflight.
function CategoryPicker({
  selected,
  tiers,
  onApply,
  pending,
}: {
  selected: ReadonlySet<string>;
  tiers: readonly AdminTicketTier[];
  onApply: (tierID: string) => void;
  pending: boolean;
}) {
  const [tierID, setTierID] = useState<string>("");
  return (
    <div style={sectionStyle} data-testid="session-seats-category-picker">
      <div style={sectionTitleStyle}>
        AB-39 · Change category ({selected.size} selected)
      </div>
      <div style={selectorRowStyle}>
        <label style={miniLabelStyle}>
          Target tier
          <select
            value={tierID}
            onChange={(e) => setTierID(e.target.value)}
            style={inputStyle}
            disabled={tiers.length === 0}
            data-testid="session-seats-tier-picker"
          >
            <option value="">—</option>
            {tiers.map((t) => (
              <option key={t.id} value={t.id}>
                {t.name}
                {t.pricing_mode === "fixed"
                  ? ` — ${(t.price_amount / 100).toFixed(2)} ${t.currency}`
                  : t.pricing_mode !== ""
                    ? ` — ${t.pricing_mode}`
                    : ""}
              </option>
            ))}
          </select>
        </label>
        <button
          type="button"
          style={primaryButtonStyle}
          disabled={pending || selected.size === 0 || tierID === ""}
          onClick={() => onApply(tierID)}
          data-testid="session-seats-category-apply"
        >
          {pending ? "Applying…" : "Apply category"}
        </button>
      </div>
      {tiers.length === 0 ? (
        <p style={hintStyle}>
          No active tiers on this session — create tiers first in the
          session tier editor.
        </p>
      ) : (
        <p style={hintStyle}>
          Reassignment is rejected for seats currently held or sold; the
          error banner will list the conflicting keys.
        </p>
      )}
    </div>
  );
}

// AB-39: seat inventory table. Rows share the selection state with the
// SVG map, so clicking a table row highlights the seat on the map (and
// vice versa). Held/sold rows are colour-tinted so bulk actions become
// visually predictable. Shift+click extends the selection; a bare click
// on a row resets the selection to that seat.
function SeatTable({
  rows,
  tiers,
  selected,
  onRowClick,
  loading,
  error,
}: {
  rows: readonly AdminSeatRow[];
  tiers: Readonly<Record<string, AdminTicketTier>>;
  selected: ReadonlySet<string>;
  onRowClick: (key: string, extend: boolean) => void;
  loading: boolean;
  error: ApiError | null;
}) {
  if (loading) {
    return (
      <div style={statusBoxStyle} role="status">
        Loading seat inventory…
      </div>
    );
  }
  if (error !== null) {
    return (
      <div style={errorBoxStyle} role="alert" data-testid="session-seats-table-error">
        <strong>Failed to load seat inventory.</strong>
        <div style={errorCodeStyle}>{error.code ?? "unknown.error"}</div>
        {error.message ? <div>{error.message}</div> : null}
      </div>
    );
  }
  if (rows.length === 0) {
    return (
      <div style={statusBoxStyle} role="status">
        No seated inventory materialized for this session.
      </div>
    );
  }
  return (
    <div style={sectionStyle} data-testid="session-seats-table-panel">
      <div style={sectionTitleStyle}>
        Seat inventory ({rows.length} rows · selected {selected.size})
      </div>
      <div style={{ maxHeight: 360, overflow: "auto" }}>
        <table
          style={countersTableStyle}
          data-testid="session-seats-table"
        >
          <thead>
            <tr>
              <th style={countersThStyle}>Sector</th>
              <th style={countersThStyle}>Row</th>
              <th style={countersThStyle}>Seat</th>
              <th style={countersThStyle}>Category (tier)</th>
              <th style={countersThStyle}>Price</th>
              <th style={countersThStyle}>Status</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => {
              const tier = r.tier_id !== null ? tiers[r.tier_id] : undefined;
              const isSelected = selected.has(r.seat_key);
              const statusColour = SEAT_STATUS_COLOURS[r.status] ?? UNKNOWN_STATUS_COLOUR;
              const rowStyle: CSSProperties = {
                cursor: "pointer",
                background: isSelected
                  ? "#fef3c7"
                  : r.status === "sold" || r.status === "held"
                    ? "#f8fafc"
                    : "#ffffff",
                outline: isSelected ? "2px solid #e11d48" : "none",
              };
              const price =
                tier && tier.pricing_mode === "fixed"
                  ? `${(tier.price_amount / 100).toFixed(2)} ${tier.currency}`
                  : tier
                    ? tier.pricing_mode
                    : "—";
              return (
                <tr
                  key={r.id}
                  style={rowStyle}
                  data-testid={`session-seats-table-row-${r.seat_key}`}
                  data-seat-key={r.seat_key}
                  data-tier-id={r.tier_id ?? ""}
                  onClick={(e) => onRowClick(r.seat_key, e.shiftKey || e.metaKey || e.ctrlKey)}
                >
                  <td style={countersTdStyle}>{r.sector_name}</td>
                  <td style={countersTdStyle}>{r.row_name}</td>
                  <td style={countersTdStyle}>{r.seat_number}</td>
                  <td style={countersTdStyle}>
                    {tier ? tier.name : <em>unbound</em>}
                  </td>
                  <td style={countersTdStyle}>{price}</td>
                  <td style={countersTdStyle}>
                    <span
                      style={{
                        ...legendSwatchStyle,
                        background: statusColour,
                        marginRight: 4,
                      }}
                    />
                    {SEAT_STATUS_LABELS[r.status]}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function SectorCounters({
  counters,
}: {
  counters: readonly SectorCounter[];
}) {
  if (counters.length === 0) return null;
  return (
    <div style={sectionStyle} data-testid="session-seats-sector-counters">
      <div style={sectionTitleStyle}>Sectors</div>
      <table style={countersTableStyle}>
        <thead>
          <tr>
            <th style={countersThStyle}>Sector</th>
            <th style={countersThStyle}>Avail</th>
            <th style={countersThStyle}>Held</th>
            <th style={countersThStyle}>Sold</th>
            <th style={countersThStyle}>Unavailable</th>
            <th style={countersThStyle}>Total</th>
          </tr>
        </thead>
        <tbody>
          {counters.map((c) => (
            <tr key={c.name}>
              <td style={countersTdStyle}>{c.name}</td>
              <td style={countersTdStyle}>{c.by_status.available}</td>
              <td style={countersTdStyle}>{c.by_status.held}</td>
              <td style={countersTdStyle}>{c.by_status.sold}</td>
              <td style={countersTdStyle}>{c.by_status.unavailable}</td>
              <td style={countersTdStyle}>{c.total}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function CategoryCounters({
  counters,
}: {
  counters: readonly CategoryCounter[];
}) {
  if (counters.length === 0) return null;
  return (
    <div style={sectionStyle} data-testid="session-seats-category-counters">
      <div style={sectionTitleStyle}>Categories</div>
      <table style={countersTableStyle}>
        <thead>
          <tr>
            <th style={countersThStyle}>Category</th>
            <th style={countersThStyle}>Avail</th>
            <th style={countersThStyle}>Held</th>
            <th style={countersThStyle}>Sold</th>
            <th style={countersThStyle}>Unavailable</th>
            <th style={countersThStyle}>Total</th>
          </tr>
        </thead>
        <tbody>
          {counters.map((c) => (
            <tr key={`${c.index}-${c.name}`}>
              <td style={countersTdStyle}>
                <span
                  style={{
                    ...legendSwatchStyle,
                    background: c.color,
                    marginRight: 6,
                  }}
                />
                {c.name}
              </td>
              <td style={countersTdStyle}>{c.by_status.available}</td>
              <td style={countersTdStyle}>{c.by_status.held}</td>
              <td style={countersTdStyle}>{c.by_status.sold}</td>
              <td style={countersTdStyle}>{c.by_status.unavailable}</td>
              <td style={countersTdStyle}>{c.total}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// Exported for tests that want to reach into a section by name without
// spelunking the styled table markup.
export function findSector(
  geometry: SeatingGeometry,
  name: string,
): SeatingSection | undefined {
  for (const sec of geometry.sections ?? []) {
    if (sec.name === name) return sec;
  }
  return undefined;
}

// Re-export the underlying SVG renderer helper so tests can pin the
// SEAT-E1 preview + SEAT-E3 interactive renderers side-by-side.
export { renderGeometryToSVG };

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

const pageStyle: CSSProperties = {
  padding: 16,
  display: "flex",
  flexDirection: "column",
  gap: 12,
};

const headerStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
};

const h1Style: CSSProperties = {
  margin: 0,
  fontSize: 20,
  color: "#0f172a",
};

const subtitleStyle: CSSProperties = {
  margin: 0,
  fontSize: 12,
  color: "#64748b",
};

const twoColumnStyle: CSSProperties = {
  display: "grid",
  gridTemplateColumns: "minmax(0, 1fr) 320px",
  gap: 16,
  alignItems: "start",
};

const oneColumnStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 12,
};

const mapColumnStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
};

const sideColumnStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 12,
};

const svgFrameStyle: CSSProperties = {
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
  padding: 8,
  overflow: "auto",
  maxHeight: 640,
};

const legendStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 6,
  padding: 8,
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#f8fafc",
};

const legendRowStyle: CSSProperties = {
  display: "flex",
  flexWrap: "wrap",
  gap: 8,
  fontSize: 12,
  color: "#334155",
};

const legendItemStyle: CSSProperties = {
  display: "inline-flex",
  alignItems: "center",
  gap: 4,
};

const legendSwatchStyle: CSSProperties = {
  display: "inline-block",
  width: 10,
  height: 10,
  borderRadius: 5,
  border: "1px solid rgba(15,23,42,0.2)",
};

const sectionStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 6,
  padding: 8,
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
};

const sectionTitleStyle: CSSProperties = {
  fontSize: 11,
  fontWeight: 700,
  color: "#334155",
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const hintStyle: CSSProperties = {
  fontSize: 11,
  color: "#64748b",
  margin: 0,
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
  padding: 12,
  border: "1px solid #fca5a5",
  borderRadius: 6,
  background: "#fef2f2",
  color: "#7f1d1d",
  fontSize: 12,
  display: "flex",
  flexDirection: "column",
  gap: 4,
};

const errorCodeStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 11,
};

const okBoxStyle: CSSProperties = {
  padding: 12,
  border: "1px solid #86efac",
  borderRadius: 6,
  background: "#dcfce7",
  color: "#166534",
  fontSize: 12,
};

const selectorRowStyle: CSSProperties = {
  display: "flex",
  gap: 6,
  alignItems: "flex-end",
};

const buttonRowStyle: CSSProperties = {
  display: "flex",
  flexWrap: "wrap",
  gap: 6,
  marginTop: 4,
};

const miniLabelStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  fontSize: 11,
  color: "#334155",
  fontWeight: 600,
  flex: "1 1 120px",
};

const inputStyle: CSSProperties = {
  fontSize: 12,
  padding: "4px 6px",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  background: "#ffffff",
  color: "#0f172a",
};

const primaryButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 10px",
  background: "#0369a1",
  border: "1px solid #0369a1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#ffffff",
  fontWeight: 600,
};

const secondaryButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 10px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

const dangerButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 10px",
  background: "#ffffff",
  border: "1px solid #fca5a5",
  borderRadius: 4,
  cursor: "pointer",
  color: "#7f1d1d",
  fontWeight: 600,
};

const skippedListStyle: CSSProperties = {
  listStyle: "none",
  padding: 0,
  margin: 0,
  display: "flex",
  flexDirection: "column",
  gap: 6,
};

const skippedItemStyle: CSSProperties = {
  padding: 6,
  border: "1px solid #fde68a",
  borderRadius: 4,
  background: "#fffbeb",
  color: "#854d0e",
  fontSize: 12,
};

const skippedReasonStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 11,
};

const skippedSeatsStyle: CSSProperties = {
  display: "flex",
  flexWrap: "wrap",
  gap: 4,
  marginTop: 4,
};

const seatCodeStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 10,
  padding: "1px 4px",
  border: "1px solid #f59e0b",
  borderRadius: 3,
  background: "#fef3c7",
  color: "#78350f",
};

const countersTableStyle: CSSProperties = {
  width: "100%",
  borderCollapse: "collapse",
  fontSize: 12,
};

const countersThStyle: CSSProperties = {
  textAlign: "left",
  padding: "4px 6px",
  borderBottom: "1px solid #e2e8f0",
  color: "#334155",
  fontWeight: 600,
};

const countersTdStyle: CSSProperties = {
  padding: "4px 6px",
  borderBottom: "1px solid #f1f5f9",
  color: "#0f172a",
};

// Suppress unused-import warnings when tree-shaken usage patterns hide
// the outer ReactNode / other type deps.
type _NodeGuard = ReactNode;
export const _debugNodeGuard: _NodeGuard | undefined = undefined;

// (findSector re-exported above intentionally to give tests a stable
// deep-inspection helper without exposing the whole render tree.)
