/**
 * The seat manager of a seated session (tasks S2b and S2c, spec 26).
 *
 * The hall is the working surface; the panel holds the brushes. The organizer
 * picks a brush (a category, or "close" / "open" for sale), touches seats,
 * rows, sections, an area or a lasso on the map, and every touch adds a stroke
 * to the TRAY. Nothing reaches the server until "Apply": the tray then goes as
 * ONE atomic batch, so a buyer's widget never sees a half painted hall. A
 * batch can be undone from the history with one press.
 *
 * Built for a phone (it opens inside Telegram): no hover, finger-sized
 * controls, pinch to zoom, the panel is a bottom sheet. On a desktop the same
 * markup is a map with a panel at its right.
 *
 * The map is the seat map core of the widget (pan and zoom, the decor
 * sanitizer, the geometry types), so the organizer sees the hall as the buyer.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createSeatBatch,
  fetchSessionSchema,
  fetchSessionSeatsAdmin,
  listSeatBatches,
  listTiers,
  revertSeatBatch,
} from "@/lib/api/endpoints";
import { ApiError } from "@/lib/api/http";
import type { SeatBatch } from "@/lib/api/types";
import { formatNumber } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { ErrorNotice } from "@/components/ErrorNotice";
import { Loading } from "@/components/ui";
import { STATUS_COLORS, tierColors, orderTiers } from "@/lib/seatManager/colors";
import { buildHallIndex, rowRange, seatsOfRow, seatsOfSection, type Point } from "@/lib/seatManager/model";
import {
  MAX_BATCH_CHANGES,
  batchKind,
  clearTray,
  countByTier,
  effectiveState,
  makeStroke,
  mergeStrokes,
  pushStroke,
  trayChanges,
  trayTotals,
  undoStroke,
  type Brush,
  type SeatState,
  type SeatStatus,
  type Stroke,
  type Tray,
} from "@/lib/seatManager/pending";
import { HATCH_FILL, type SeatLook } from "@/lib/seatManager/render";
import { loadTray, saveTray } from "@/lib/seatManager/trayStore";
import { SeatMapCanvas, type MapTool, type SeatMapHandle } from "@/components/seatManager/SeatMapCanvas";
import {
  ApplyBar,
  BrushPalette,
  ChangesPanel,
  HistoryPanel,
  ModeSwitch,
  RowsPanel,
  ToolBar,
  type PanelMode,
  type TierView,
} from "@/components/seatManager/SeatPanels";
import { SeatLayoutPanel, type LayoutPreview } from "@/components/seatManager/SeatLayoutPanel";

type Tab = "brush" | "changes" | "rows" | "history" | "layout";

interface Note {
  readonly kind: "picked" | "noBrush" | "applied" | "reverted" | "transferred";
  readonly changed: number;
  readonly skipped: number;
  readonly unchanged: number;
}

export interface SeatManagerProps {
  orgId: string;
  eventId: string;
  sessionId: string;
  /** Inside the Telegram mini app: no page header, no way "back" of our own. */
  embed?: boolean;
}

const STAGE_KEY = "arena.seats.stage.";

function loadStage(sessionId: string): Point | null {
  try {
    const raw = sessionStorage.getItem(STAGE_KEY + sessionId);
    if (raw === null) return null;
    const v: unknown = JSON.parse(raw);
    if (typeof v === "object" && v !== null) {
      const { x, y } = v as { x?: unknown; y?: unknown };
      if (typeof x === "number" && typeof y === "number" && Number.isFinite(x) && Number.isFinite(y)) return { x, y };
    }
  } catch {
    // Storage is blocked or holds something else: no stage.
  }
  return null;
}

function saveStage(sessionId: string, p: Point | null): void {
  try {
    if (p === null) sessionStorage.removeItem(STAGE_KEY + sessionId);
    else sessionStorage.setItem(STAGE_KEY + sessionId, JSON.stringify(p));
  } catch {
    // The stage then lives until the page reloads.
  }
}

export function SeatManager({ orgId, eventId, sessionId, embed = false }: SeatManagerProps) {
  const { t, locale } = useI18n();
  const qc = useQueryClient();
  const mapRef = useRef<SeatMapHandle>(null);

  const schemaQ = useQuery({
    queryKey: ["seat-schema", sessionId],
    queryFn: () => fetchSessionSchema(sessionId),
    staleTime: 5 * 60_000,
    retry: false,
  });
  const seatsQ = useQuery({
    queryKey: ["seats-admin", sessionId],
    queryFn: () => fetchSessionSeatsAdmin(sessionId),
    refetchInterval: 8000,
  });
  const tiersQ = useQuery({
    queryKey: ["tiers", orgId, eventId, sessionId],
    queryFn: () => listTiers(orgId, eventId, sessionId),
  });
  const batchesQ = useQuery({
    queryKey: ["seat-batches", orgId, sessionId],
    queryFn: () => listSeatBatches(orgId, sessionId),
  });

  const geometry = schemaQ.data?.geometry;
  const index = useMemo(() => (geometry !== undefined ? buildHallIndex(geometry) : null), [geometry]);

  const base = useMemo(() => {
    const m = new Map<string, SeatState>();
    for (const row of seatsQ.data?.seats ?? []) {
      if (row.kind !== "seat") continue;
      m.set(row.seat_key, { tierId: row.tier_id, status: row.status as SeatStatus });
    }
    return m;
  }, [seatsQ.data]);

  // Categories that sell seats (a hybrid session also has general-admission ones).
  const seatTiers = useMemo(
    () =>
      (tiersQ.data?.tiers ?? []).filter((x) => !((x.ga_unit_count ?? 0) > 0 && (x.seat_count ?? 0) === 0)),
    [tiersQ.data],
  );
  const colors = useMemo(() => tierColors(seatTiers), [seatTiers]);

  // ── state ───────────────────────────────────────────────────────────────
  const [tray, setTrayState] = useState<Tray>(() => loadTray(sessionId));
  const setTray = useCallback(
    (next: Tray) => {
      setTrayState(next);
      saveTray(sessionId, next);
    },
    [sessionId],
  );
  const [mode, setMode] = useState<PanelMode>("categories");
  const [brush, setBrush] = useState<Brush | null>(null);
  const [tool, setTool] = useState<MapTool>("seat");
  const [tab, setTab] = useState<Tab>("brush");
  const [note, setNote] = useState<Note | null>(null);
  const [preview, setPreview] = useState<LayoutPreview | null>(null);
  const [stage, setStageState] = useState<Point | null>(() => loadStage(sessionId));
  const [collapsed, setCollapsed] = useState(false);
  const lastRow = useRef<string | null>(null);
  // The tray as the last render saw it, and the strokes the batch in flight carries: a touch made while the
  // request is on its way must stay in the tray after the answer.
  const trayRef = useRef(tray);
  trayRef.current = tray;
  const sentStrokes = useRef<ReadonlySet<number>>(new Set());

  const merged = useMemo(() => mergeStrokes(tray.strokes), [tray.strokes]);
  const changes = useMemo(() => trayChanges(base, merged), [base, merged]);
  const totals = useMemo(() => trayTotals(changes), [changes]);

  // A default brush: the first category, or "close" in sale mode.
  useEffect(() => {
    if (brush !== null) return;
    if (mode === "sale") setBrush({ kind: "close" });
    else {
      const first = orderTiers(seatTiers)[0];
      if (first !== undefined) setBrush({ kind: "tier", tierId: first.id });
    }
  }, [brush, mode, seatTiers]);

  const switchMode = (m: PanelMode) => {
    setMode(m);
    setBrush(null);
    lastRow.current = null;
  };

  // ── counters ────────────────────────────────────────────────────────────
  const seatKeys = useMemo(() => index?.seats.map((s) => s.key) ?? [], [index]);
  const counts = useMemo(() => countByTier(base, merged, seatKeys), [base, merged, seatKeys]);
  const tierViews: TierView[] = useMemo(
    () =>
      orderTiers(seatTiers).map((x) => ({
        id: x.id,
        name: x.name,
        price: x.current_price ?? x.price_amount,
        currency: x.currency,
        color: colors.get(x.id) ?? STATUS_COLORS.unknown,
        open: x.is_open !== false,
        counts: counts.get(x.id),
      })),
    [seatTiers, colors, counts],
  );
  const overall = useMemo(() => {
    let sold = 0;
    let held = 0;
    let closed = 0;
    for (const k of seatKeys) {
      const s = effectiveState(base, merged, k);
      if (s === undefined) continue;
      if (s.status === "sold") sold++;
      else if (s.status === "held") held++;
      else if (s.status === "unavailable") closed++;
    }
    return { total: seatKeys.length, sold, held, closed };
  }, [base, merged, seatKeys]);

  // ── how a seat looks ────────────────────────────────────────────────────
  const look = useCallback(
    (key: string): SeatLook => {
      const b = base.get(key);
      if (b === undefined) return { fill: STATUS_COLORS.unknown, stroke: "none", strokeWidth: 0, dashed: false };
      const tierColor = (id: string | null) => (id === null ? STATUS_COLORS.unknown : (colors.get(id) ?? STATUS_COLORS.unknown));
      if (b.status === "sold") return { fill: STATUS_COLORS.sold, stroke: tierColor(b.tierId), strokeWidth: 2, dashed: false };
      if (b.status === "held") return { fill: STATUS_COLORS.held, stroke: tierColor(b.tierId), strokeWidth: 2, dashed: false };
      const previewColor = preview?.colors.get(key);
      if (previewColor !== undefined) return { fill: previewColor, stroke: "#111827", strokeWidth: 1.5, dashed: true };
      const eff = effectiveState(base, merged, key) ?? b;
      const tr = merged.get(key);
      const changed = tr !== undefined && ((tr.tierId !== undefined && tr.tierId !== b.tierId) || (tr.status !== undefined && tr.status !== b.status));
      const stroke = changed ? "#111827" : "none";
      const sw = changed ? 2.5 : 0;
      if (eff.status === "unavailable") return { fill: HATCH_FILL, stroke: changed ? "#111827" : tierColor(eff.tierId), strokeWidth: changed ? 2.5 : 1.5, dashed: false };
      return { fill: tierColor(eff.tierId), stroke, strokeWidth: sw, dashed: false };
    },
    [base, merged, colors, preview],
  );

  // ── painting ────────────────────────────────────────────────────────────
  const paint = useCallback(
    (keys: Iterable<string>) => {
      if (brush === null) {
        setNote({ kind: "noBrush", changed: 0, skipped: 0, unchanged: 0 });
        return;
      }
      const res = makeStroke(tray.nextId, brush, keys, base, merged);
      if (res.stroke !== null) setTray(pushStroke(tray, res.stroke));
      setNote({ kind: "picked", changed: res.changed, skipped: res.skippedFrozen, unchanged: res.unchanged });
    },
    [brush, tray, base, merged, setTray],
  );

  const onSeats = useCallback((keys: string[]) => paint(keys), [paint]);
  const onRowHandle = useCallback(
    (rowId: string, additive: boolean) => {
      if (index === null) return;
      let keys: readonly string[] = seatsOfRow(index, rowId);
      if (additive && lastRow.current !== null) {
        const range = rowRange(index, lastRow.current, rowId);
        if (range.length > 0) keys = range;
      }
      lastRow.current = rowId;
      paint(keys);
    },
    [index, paint],
  );
  const onStage = useCallback(
    (p: Point) => {
      setStageState(p);
      saveStage(sessionId, p);
      setTool("seat");
      setCollapsed(false);
    },
    [sessionId],
  );

  // ── apply / revert ──────────────────────────────────────────────────────
  const refreshAll = () => {
    void qc.invalidateQueries({ queryKey: ["seats-admin", sessionId] });
    void qc.invalidateQueries({ queryKey: ["seat-batches", orgId, sessionId] });
    void qc.invalidateQueries({ queryKey: ["tiers", orgId, eventId, sessionId] });
    void qc.invalidateQueries({ queryKey: ["sessionSummary", orgId, sessionId] });
  };

  const apply = useMutation({
    mutationFn: () =>
      createSeatBatch(orgId, sessionId, {
        kind: batchKind(changes),
        changes: changes.map((c) => ({ seat_key: c.seat_key, tier_id: c.tier_id, status: c.status })),
      }),
    onSuccess: (batch: SeatBatch) => {
      const sent = sentStrokes.current;
      const now = trayRef.current;
      setTray({ strokes: now.strokes.filter((x) => !sent.has(x.id)), nextId: now.nextId });
      setNote({ kind: "applied", changed: batch.changes.length, skipped: batch.skipped.length, unchanged: batch.unchanged_seat_keys?.length ?? 0 });
      refreshAll();
    },
    onError: () => refreshAll(),
  });
  const revert = useMutation({
    mutationFn: (batchId: string) => revertSeatBatch(orgId, sessionId, batchId),
    onSuccess: (batch: SeatBatch) => {
      setNote({ kind: "reverted", changed: batch.changes.length, skipped: batch.skipped.length, unchanged: 0 });
      refreshAll();
    },
    onError: () => refreshAll(),
  });

  const tooMany = changes.length > MAX_BATCH_CHANGES;

  const onTransfer = (strokes: Stroke[], n: { changed: number; leftOut: number }) => {
    let next = tray;
    for (const s of strokes) next = pushStroke(next, s);
    setTray(next);
    setPreview(null);
    setNote({ kind: "transferred", changed: n.changed, skipped: n.leftOut, unchanged: 0 });
    setTab("changes");
  };

  // ── render ──────────────────────────────────────────────────────────────
  if (schemaQ.isLoading || seatsQ.isLoading || tiersQ.isLoading) return <Loading />;

  if (schemaQ.error !== null && schemaQ.error !== undefined) {
    const notFound = schemaQ.error instanceof ApiError && schemaQ.error.status === 404;
    return notFound ? (
      <p className="notice notice-warn" role="status" data-testid="seat-no-map">
        {t("seats.noMap")}
      </p>
    ) : (
      <ErrorNotice error={schemaQ.error} />
    );
  }
  if (seatsQ.error !== null && seatsQ.error !== undefined) return <ErrorNotice error={seatsQ.error} />;
  if (tiersQ.error !== null && tiersQ.error !== undefined) return <ErrorNotice error={tiersQ.error} />;
  if (geometry === undefined || index === null) return <Loading />;
  if (index.seats.length === 0) {
    return (
      <p className="notice" role="status" data-testid="seat-no-seats">
        {t("seats.noSeats")}
      </p>
    );
  }

  const pointingStage = tool === "stage";
  const tabs: { id: Tab; label: string; badge?: number }[] = [
    { id: "brush", label: t("seats.tab.brush") },
    { id: "changes", label: t("seats.tab.changes"), badge: totals.seats },
    { id: "rows", label: t("seats.tab.rows") },
    { id: "history", label: t("seats.tab.history") },
    { id: "layout", label: t("seats.tab.layout") },
  ];

  return (
    <section className={embed ? "sm sm-embed" : "sm"} data-testid="seat-manager" aria-label={t("seats.title")}>
      {!embed && (
        <p className="sm-summary muted" data-testid="seat-summary">
          {t("seats.summary", {
            total: formatNumber(overall.total, locale),
            sold: formatNumber(overall.sold, locale),
            held: formatNumber(overall.held, locale),
            closed: formatNumber(overall.closed, locale),
          })}
        </p>
      )}
      <div className="sm-body">
        <div className="sm-map">
          <SeatMapCanvas
            ref={mapRef}
            geometry={geometry}
            index={index}
            label={t("seats.mapLabel")}
            look={look}
            tool={tool}
            stage={stage}
            onSeats={onSeats}
            onRowHandle={onRowHandle}
            onStage={onStage}
          />
          <div className="sm-zoom" role="group" aria-label={t("seats.zoom")}>
            <button type="button" className="sm-zoom-btn" onClick={() => mapRef.current?.zoomBy(1.4)} aria-label={t("seats.zoomIn")} data-testid="seat-zoom-in">
              +
            </button>
            <button type="button" className="sm-zoom-btn" onClick={() => mapRef.current?.zoomBy(1 / 1.4)} aria-label={t("seats.zoomOut")} data-testid="seat-zoom-out">
              −
            </button>
            <button type="button" className="sm-zoom-btn" onClick={() => mapRef.current?.fit()} aria-label={t("seats.zoomFit")} data-testid="seat-zoom-fit">
              ⤢
            </button>
          </div>
          {pointingStage && (
            <div className="sm-banner" role="status" data-testid="seat-stage-banner">
              <span>{t("seats.stage.banner")}</span>
              <button type="button" className="btn" onClick={() => { setTool("seat"); setCollapsed(false); }}>
                {t("common.cancel")}
              </button>
            </div>
          )}
          <ul className="sm-legend" aria-label={t("seats.legend")}>
            <li><span className="sm-key sm-key-sold" aria-hidden="true" />{t("seats.legend.sold")}</li>
            <li><span className="sm-key sm-key-held" aria-hidden="true" />{t("seats.legend.held")}</li>
            <li><span className="sm-key sm-key-closed" aria-hidden="true" />{t("seats.legend.closed")}</li>
            <li><span className="sm-key sm-key-changed" aria-hidden="true" />{t("seats.legend.changed")}</li>
          </ul>
        </div>

        <aside className={collapsed ? "sm-panel sm-panel-collapsed" : "sm-panel"} aria-label={t("seats.panel")} data-testid="seat-panel">
          <div className="sm-tabs">
            <div className="sm-tablist" role="tablist" aria-label={t("seats.tabs")}>
              {tabs.map((x) => (
                <button
                  key={x.id}
                  type="button"
                  role="tab"
                  className="sm-tab"
                  aria-selected={tab === x.id}
                  onClick={() => {
                    setTab(x.id);
                    setCollapsed(false);
                    if (x.id !== "layout") setPreview(null);
                  }}
                  data-testid={`seat-tab-${x.id}`}
                >
                  {x.label}
                  {x.badge !== undefined && x.badge > 0 && <span className="sm-badge">{formatNumber(x.badge, locale)}</span>}
                </button>
              ))}
            </div>
            <button type="button" className="sm-tab sm-collapse" onClick={() => setCollapsed((v) => !v)} aria-expanded={!collapsed} data-testid="seat-collapse">
              {collapsed ? t("seats.panel.show") : t("seats.panel.hide")}
            </button>
          </div>

          <div className="sm-panel-body" role="tabpanel">
            {tab === "brush" && (
              <>
                <ModeSwitch mode={mode} onMode={switchMode} />
                <BrushPalette mode={mode} tiers={tierViews} brush={brush} onBrush={setBrush} />
                <ToolBar tool={tool} onTool={setTool} />
                {seatTiers.length === 0 && mode === "categories" && <p className="notice notice-warn">{t("seats.noTiers")}</p>}
              </>
            )}
            {tab === "changes" && <ChangesPanel changes={changes} totals={totals} base={base} tiers={tierViews} />}
            {tab === "rows" && (
              <RowsPanel
                index={index}
                base={base}
                onRow={(id) => paint(seatsOfRow(index, id))}
                onSection={(key) => paint(seatsOfSection(index, key))}
              />
            )}
            {tab === "history" && (
              <>
                {batchesQ.error !== null && batchesQ.error !== undefined && <ErrorNotice error={batchesQ.error} />}
                <HistoryPanel
                  items={batchesQ.data?.items ?? []}
                  busyId={revert.isPending ? (revert.variables ?? null) : null}
                  canRevert
                  onRevert={(id) => revert.mutate(id)}
                />
              </>
            )}
            {tab === "layout" && (
              <SeatLayoutPanel
                orgId={orgId}
                sessionId={sessionId}
                tiers={tierViews}
                base={base}
                merged={merged}
                nextStrokeId={tray.nextId}
                stage={stage}
                onPointStage={() => {
                  setTool("stage");
                  setCollapsed(true);
                }}
                onPreview={setPreview}
                onTransfer={onTransfer}
              />
            )}
          </div>

          <div className="sm-notes" aria-live="polite">
            {note !== null && <NoteLine note={note} />}
            {tooMany && <p className="notice notice-bad">{t("seats.tooMany", { max: formatNumber(MAX_BATCH_CHANGES, locale) })}</p>}
            {apply.error !== null && <ErrorNotice error={apply.error} testId="seat-apply-error" />}
            {revert.error !== null && <ErrorNotice error={revert.error} testId="seat-revert-error" />}
          </div>
          <ApplyBar
            totals={totals}
            strokes={tray.strokes.length}
            busy={apply.isPending || revert.isPending}
            onApply={() => {
              if (tooMany) return;
              sentStrokes.current = new Set(tray.strokes.map((x) => x.id));
              apply.mutate();
            }}
            onUndo={() => setTray(undoStroke(tray))}
            onReset={() => setTray(clearTray(tray))}
          />
        </aside>
      </div>
    </section>
  );
}

function NoteLine({ note }: { note: Note }) {
  const { t, locale } = useI18n();
  const n = (v: number) => formatNumber(v, locale);
  switch (note.kind) {
    case "noBrush":
      return <p className="notice notice-warn" data-testid="seat-note">{t("seats.note.noBrush")}</p>;
    case "picked":
      if (note.changed === 0 && note.skipped === 0) return <p className="muted" data-testid="seat-note">{t("seats.note.nothing")}</p>;
      return (
        <p className="muted" data-testid="seat-note">
          {t("seats.note.picked", { changed: n(note.changed) })}
          {note.skipped > 0 && ` ${t("seats.note.frozen", { count: n(note.skipped) })}`}
        </p>
      );
    case "transferred":
      return (
        <p className="muted" data-testid="seat-note">
          {t("seats.note.picked", { changed: n(note.changed) })}
        </p>
      );
    case "applied":
      return (
        <p className="notice notice-ok" data-testid="seat-note" data-kind="applied">
          {t("seats.note.applied", { changed: n(note.changed) })}
          {note.skipped > 0 && ` ${t("seats.note.skipped", { count: n(note.skipped) })}`}
        </p>
      );
    case "reverted":
      return (
        <p className="notice notice-ok" data-testid="seat-note" data-kind="reverted">
          {t("seats.note.reverted", { changed: n(note.changed) })}
          {note.skipped > 0 && ` ${t("seats.note.skipped", { count: n(note.skipped) })}`}
        </p>
      );
  }
}
