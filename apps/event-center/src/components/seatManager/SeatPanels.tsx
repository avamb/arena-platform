/**
 * The parts of the seat manager's panel: brushes, tools, the apply bar, the
 * list of changes, the rows and the history. Every control is a real button
 * of at least 44 px (a finger), none depends on hover, and a state is never
 * colour only: a category has a name and a count next to its colour.
 */
import { formatMoney, formatNumber, intlLocale } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import type { SeatBatchSummary } from "@/lib/api/types";
import type { HallIndex } from "@/lib/seatManager/model";
import type { BatchChange, Brush, SeatState, TierCounts, TierDelta, TrayTotals } from "@/lib/seatManager/pending";
import { tierDeltas } from "@/lib/seatManager/pending";
import { readableOn } from "@/lib/seatManager/colors";
import type { MapTool } from "@/components/seatManager/SeatMapCanvas";

export interface TierView {
  readonly id: string;
  readonly name: string;
  readonly price: number;
  readonly currency: string;
  readonly color: string;
  readonly open: boolean;
  readonly counts: TierCounts | undefined;
}

export type PanelMode = "categories" | "sale";

// ─── brushes ─────────────────────────────────────────────────────────────

export function ModeSwitch({ mode, onMode }: { mode: PanelMode; onMode: (m: PanelMode) => void }) {
  const { t } = useI18n();
  return (
    <div className="sm-seg" role="group" aria-label={t("seats.mode")}>
      {(["categories", "sale"] as const).map((m) => (
        <button
          key={m}
          type="button"
          className="sm-seg-btn"
          aria-pressed={mode === m}
          onClick={() => onMode(m)}
          data-testid={`seat-mode-${m}`}
        >
          {t(m === "categories" ? "seats.mode.categories" : "seats.mode.sale")}
        </button>
      ))}
    </div>
  );
}

export function BrushPalette({
  mode,
  tiers,
  brush,
  onBrush,
}: {
  mode: PanelMode;
  tiers: readonly TierView[];
  brush: Brush | null;
  onBrush: (b: Brush) => void;
}) {
  const { t, locale } = useI18n();
  if (mode === "sale") {
    return (
      <div className="sm-brushes" role="group" aria-label={t("seats.brushes")}>
        {(["close", "open"] as const).map((k) => (
          <button
            key={k}
            type="button"
            className="sm-brush"
            aria-pressed={brush?.kind === k}
            onClick={() => onBrush({ kind: k })}
            data-testid={`seat-brush-${k}`}
          >
            <span className={k === "close" ? "sm-swatch sm-swatch-closed" : "sm-swatch sm-swatch-open"} aria-hidden="true" />
            <span className="sm-brush-text">
              <strong>{t(k === "close" ? "seats.brush.close" : "seats.brush.open")}</strong>
              <span className="muted">{t(k === "close" ? "seats.brush.close.hint" : "seats.brush.open.hint")}</span>
            </span>
          </button>
        ))}
      </div>
    );
  }
  return (
    <div className="sm-brushes" role="group" aria-label={t("seats.brushes")} data-testid="seat-brushes">
      {tiers.map((tier) => (
        <button
          key={tier.id}
          type="button"
          className="sm-brush"
          aria-pressed={brush?.kind === "tier" && brush.tierId === tier.id}
          onClick={() => onBrush({ kind: "tier", tierId: tier.id })}
          data-testid="seat-brush"
          data-tier-name={tier.name}
        >
          <span
            className="sm-swatch"
            data-color={tier.color}
            ref={(el) => {
              if (el !== null) {
                el.style.backgroundColor = tier.color;
                el.style.color = readableOn(tier.color);
              }
            }}
            aria-hidden="true"
          />
          <span className="sm-brush-text">
            <strong>{tier.name}</strong>
            <span className="muted">
              {formatMoney(tier.price, tier.currency, locale)}
              {tier.counts !== undefined && ` · ${t("seats.brush.free", { free: formatNumber(tier.counts.free, locale), total: formatNumber(tier.counts.total, locale) })}`}
              {!tier.open && ` · ${t("tier.closed")}`}
            </span>
          </span>
        </button>
      ))}
    </div>
  );
}

// ─── tools ───────────────────────────────────────────────────────────────

const TOOL_KEYS: Record<MapTool, "seats.tool.pan" | "seats.tool.seat" | "seats.tool.row" | "seats.tool.section" | "seats.tool.rect" | "seats.tool.lasso" | "seats.tool.stage"> = {
  pan: "seats.tool.pan",
  seat: "seats.tool.seat",
  row: "seats.tool.row",
  section: "seats.tool.section",
  rect: "seats.tool.rect",
  lasso: "seats.tool.lasso",
  stage: "seats.tool.stage",
};

export function ToolBar({ tool, onTool }: { tool: MapTool; onTool: (t: MapTool) => void }) {
  const { t } = useI18n();
  const tools: MapTool[] = ["seat", "row", "section", "rect", "lasso", "pan"];
  return (
    <div className="sm-tools" role="group" aria-label={t("seats.tools")}>
      {tools.map((k) => (
        <button
          key={k}
          type="button"
          className="sm-tool"
          aria-pressed={tool === k}
          onClick={() => onTool(k)}
          data-testid={`seat-tool-${k}`}
        >
          {t(TOOL_KEYS[k])}
        </button>
      ))}
    </div>
  );
}

// ─── apply bar ───────────────────────────────────────────────────────────

export function ApplyBar({
  totals,
  strokes,
  busy,
  onApply,
  onUndo,
  onReset,
}: {
  totals: TrayTotals;
  strokes: number;
  busy: boolean;
  onApply: () => void;
  onUndo: () => void;
  onReset: () => void;
}) {
  const { t, locale } = useI18n();
  const empty = totals.seats === 0;
  return (
    <div className="sm-apply" data-testid="seat-apply-bar">
      <p className="sm-status" role="status" aria-live="polite" data-testid="seat-tray-count">
        {empty ? t("seats.tray.empty") : t("seats.tray.count", { count: formatNumber(totals.seats, locale) })}
      </p>
      <div className="sm-apply-buttons">
        <button type="button" className="btn" onClick={onUndo} disabled={strokes === 0 || busy} data-testid="seat-undo">
          {t("seats.undo")}
        </button>
        <button type="button" className="btn" onClick={onReset} disabled={strokes === 0 || busy} data-testid="seat-reset">
          {t("seats.reset")}
        </button>
        <button type="button" className="btn btn-primary" onClick={onApply} disabled={empty || busy} data-testid="seat-apply">
          {busy ? t("seats.applying") : t("seats.apply")}
        </button>
      </div>
    </div>
  );
}

// ─── changes ─────────────────────────────────────────────────────────────

export function ChangesPanel({
  changes,
  totals,
  base,
  tiers,
}: {
  changes: readonly BatchChange[];
  totals: TrayTotals;
  base: ReadonlyMap<string, SeatState>;
  tiers: readonly TierView[];
}) {
  const { t, locale } = useI18n();
  if (totals.seats === 0) return <p className="muted">{t("seats.changes.empty")}</p>;
  const deltas: TierDelta[] = tierDeltas(base, changes);
  const name = (id: string) => tiers.find((x) => x.id === id)?.name ?? t("seats.tier.unknown");
  return (
    <div data-testid="seat-changes">
      <ul className="facts">
        {totals.painted > 0 && <li>{t("seats.changes.painted", { count: formatNumber(totals.painted, locale) })}</li>}
        {totals.closed > 0 && <li>{t("seats.changes.closed", { count: formatNumber(totals.closed, locale) })}</li>}
        {totals.opened > 0 && <li>{t("seats.changes.opened", { count: formatNumber(totals.opened, locale) })}</li>}
      </ul>
      {deltas.length > 0 && (
        <ul className="sm-deltas">
          {deltas.map((d) => (
            <li key={d.tierId}>
              <strong>{name(d.tierId)}</strong>
              {d.gained > 0 && <span className="sm-plus"> +{formatNumber(d.gained, locale)}</span>}
              {d.lost > 0 && <span className="sm-minus"> −{formatNumber(d.lost, locale)}</span>}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// ─── rows ────────────────────────────────────────────────────────────────

export function RowsPanel({
  index,
  base,
  onRow,
  onSection,
}: {
  index: HallIndex;
  base: ReadonlyMap<string, SeatState>;
  onRow: (rowId: string) => void;
  onSection: (sectionKey: string) => void;
}) {
  const { t, locale } = useI18n();
  const sold = (keys: readonly string[]) =>
    keys.reduce((n, k) => n + (base.get(k)?.status === "sold" || base.get(k)?.status === "held" ? 1 : 0), 0);
  return (
    <div data-testid="seat-rows">
      <p className="muted">{t("seats.rows.hint")}</p>
      {index.sections.map((sec) => (
        <section key={sec.key} aria-label={sec.name}>
          <div className="sm-section-head">
            <h3>{sec.name}</h3>
            <button type="button" className="btn" onClick={() => onSection(sec.key)} data-testid="seat-section-apply">
              {t("seats.rows.section", { name: sec.name })}
            </button>
          </div>
          <ul className="sm-rowlist">
            {sec.rowIds.map((id) => {
              const row = index.rowById.get(id)!;
              const frozen = sold(row.seatKeys);
              return (
                <li key={id}>
                  <button type="button" className="sm-rowbtn" onClick={() => onRow(id)} data-testid="seat-row-apply">
                    <strong>{t("seats.rows.row", { name: row.name })}</strong>
                    <span className="muted">
                      {t("seats.rows.seats", { count: formatNumber(row.seatKeys.length, locale) })}
                      {frozen > 0 && ` · ${t("seats.rows.frozen", { count: formatNumber(frozen, locale) })}`}
                    </span>
                  </button>
                </li>
              );
            })}
          </ul>
        </section>
      ))}
    </div>
  );
}

// ─── history ─────────────────────────────────────────────────────────────

export function HistoryPanel({
  items,
  busyId,
  canRevert,
  onRevert,
}: {
  items: readonly SeatBatchSummary[];
  busyId: string | null;
  canRevert: boolean;
  onRevert: (id: string) => void;
}) {
  const { t, locale } = useI18n();
  if (items.length === 0) return <p className="muted">{t("seats.history.empty")}</p>;
  const when = (iso: string) =>
    new Intl.DateTimeFormat(intlLocale(locale), { dateStyle: "short", timeStyle: "short", hourCycle: "h23" }).format(new Date(iso));
  return (
    <ul className="sm-history" data-testid="seat-history">
      {items.map((b) => {
        const isRevert = b.kind === "revert";
        const reverted = b.reverted_at !== null;
        return (
          <li key={b.id} data-testid="seat-history-item" data-kind={b.kind} data-reverted={reverted ? "1" : "0"}>
            <div>
              <strong>{t(`seats.kind.${b.kind}` as never)}</strong> <span className="muted">{when(b.applied_at)}</span>
              <br />
              <span className="muted">
                {t("seats.history.counts", {
                  changed: formatNumber(b.changed_count, locale),
                  skipped: formatNumber(b.skipped_count, locale),
                })}
                {reverted && ` · ${t("seats.history.reverted")}`}
              </span>
            </div>
            {canRevert && !isRevert && !reverted && (
              <button
                type="button"
                className="btn"
                disabled={busyId !== null}
                onClick={() => onRevert(b.id)}
                data-testid="seat-revert"
              >
                {busyId === b.id ? t("seats.applying") : t("seats.history.revert")}
              </button>
            )}
          </li>
        );
      })}
    </ul>
  );
}
