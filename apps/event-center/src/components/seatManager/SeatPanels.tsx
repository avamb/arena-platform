/**
 * The parts of the seat manager's panel: brushes, tools, the apply bar, the
 * list of changes, the rows and the history. Every control is a real button
 * of at least 44 px (a finger), none depends on hover, and a state is never
 * colour only: a category has a name and a count next to its colour.
 */
import { useState } from "react";
import { formatMoney, formatNumber, intlLocale } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import type { SeatBatchSummary } from "@/lib/api/types";
import type { HallIndex } from "@/lib/seatManager/model";
import type { BatchChange, Brush, SeatState, TierCounts, TierDelta, TrayTotals } from "@/lib/seatManager/pending";
import { tierDeltas } from "@/lib/seatManager/pending";
import { readableOn } from "@/lib/seatManager/colors";
import { checkDraft, type DraftErrors, type NewTierDraft, type NewTierValue } from "@/lib/seatManager/layout";
import type { MapTool } from "@/components/seatManager/SeatMapCanvas";

export interface TierView {
  readonly id: string;
  readonly name: string;
  readonly price: number;
  readonly currency: string;
  readonly color: string;
  readonly open: boolean;
  readonly counts: TierCounts | undefined;
  /** A category of the tray: the server does not know it until "Apply". */
  readonly pending?: boolean;
}

// ─── a new category: its fields ──────────────────────────────────────────

const ERR_KEYS: Record<string, MessageKey> = {
  empty: "seats.newTier.err.empty",
  long: "seats.newTier.err.long",
  taken: "seats.newTier.err.taken",
  bad: "seats.newTier.err.price",
  max: "seats.newTier.err.max",
};

/** Name, price and colour of a category that does not exist yet. Used by the slot mapping and by "+ category". */
export function NewTierFields({
  draft,
  errors,
  currency,
  testPrefix,
  onChange,
}: {
  draft: NewTierDraft;
  errors: DraftErrors | null;
  currency: string;
  testPrefix: string;
  onChange: (next: NewTierDraft) => void;
}) {
  const { t } = useI18n();
  return (
    <div className="sm-newtier-fields">
      <label className="field">
        <span>{t("seats.newTier.name")}</span>
        <input
          type="text"
          autoComplete="off"
          value={draft.name}
          aria-invalid={errors?.name !== undefined}
          onChange={(e) => onChange({ ...draft, name: e.target.value })}
          data-testid={`${testPrefix}-name`}
        />
        {errors?.name !== undefined && (
          <small className="field-error" role="alert">
            {t(ERR_KEYS[errors.name]!)}
          </small>
        )}
      </label>
      <label className="field">
        <span>{t("seats.newTier.price", { currency })}</span>
        <input
          type="text"
          inputMode="decimal"
          autoComplete="off"
          value={draft.price}
          aria-invalid={errors?.price !== undefined}
          onChange={(e) => onChange({ ...draft, price: e.target.value })}
          data-testid={`${testPrefix}-price`}
        />
        {errors?.price !== undefined && (
          <small className="field-error" role="alert">
            {t(ERR_KEYS[errors.price]!)}
          </small>
        )}
      </label>
      <label className="field">
        <span>{t("seats.newTier.color")}</span>
        <input
          type="color"
          value={/^#[0-9a-fA-F]{6}$/.test(draft.color) ? draft.color : "#4f46e5"}
          onChange={(e) => onChange({ ...draft, color: e.target.value })}
          data-testid={`${testPrefix}-color`}
        />
        {errors?.color !== undefined && (
          <small className="field-error" role="alert">
            {t("seats.newTier.err.color")}
          </small>
        )}
      </label>
    </div>
  );
}

/** The small form behind "+ category" of the brush panel. */
function AddTierForm({
  currency,
  takenNames,
  defaultColor,
  onAdd,
  onCancel,
}: {
  currency: string;
  takenNames: readonly string[];
  defaultColor: string;
  onAdd: (v: NewTierValue) => void;
  onCancel: () => void;
}) {
  const { t } = useI18n();
  const [draft, setDraft] = useState<NewTierDraft>({ name: "", price: "", color: defaultColor });
  const [shown, setShown] = useState(false);
  const check = checkDraft(draft, currency, new Set(takenNames.map((x) => x.trim().toLowerCase())));
  return (
    <form
      className="sm-addtier"
      aria-label={t("seats.brush.add.title")}
      data-testid="seat-add-tier-form"
      onSubmit={(e) => {
        e.preventDefault();
        setShown(true);
        if (check.ok) onAdd(check.value);
      }}
    >
      <h3>{t("seats.brush.add.title")}</h3>
      <NewTierFields draft={draft} errors={shown && !check.ok ? check.errors : null} currency={currency} testPrefix="seat-add-tier" onChange={setDraft} />
      <p className="muted">{t("seats.brush.add.hint")}</p>
      <div className="sm-apply-buttons">
        <button type="button" className="btn" onClick={onCancel}>
          {t("common.cancel")}
        </button>
        <button type="submit" className="btn btn-primary" data-testid="seat-add-tier-submit">
          {t("seats.brush.add.confirm")}
        </button>
      </div>
    </form>
  );
}

/** What "+ category" needs; absent when the person may not create categories. */
export interface AddTierOptions {
  readonly currency: string;
  readonly takenNames: readonly string[];
  readonly defaultColor: string;
  /** The date already has the most categories (15). */
  readonly full: boolean;
  readonly onAdd: (v: NewTierValue) => void;
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
  add,
  onRemovePending,
}: {
  mode: PanelMode;
  tiers: readonly TierView[];
  brush: Brush | null;
  onBrush: (b: Brush) => void;
  add?: AddTierOptions;
  onRemovePending?: (tierId: string) => void;
}) {
  const { t, locale } = useI18n();
  const [adding, setAdding] = useState(false);
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
    <>
      <div className="sm-brushes" role="group" aria-label={t("seats.brushes")} data-testid="seat-brushes">
        {tiers.map((tier) => {
          const button = (
            <button
              key={tier.id}
              type="button"
              className="sm-brush"
              aria-pressed={brush?.kind === "tier" && brush.tierId === tier.id}
              onClick={() => onBrush({ kind: "tier", tierId: tier.id })}
              data-testid="seat-brush"
              data-tier-name={tier.name}
              data-pending={tier.pending === true ? "1" : undefined}
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
                  {tier.pending === true
                    ? ` · ${t("seats.brush.pending")}`
                    : tier.counts !== undefined &&
                      ` · ${t("seats.brush.free", { free: formatNumber(tier.counts.free, locale), total: formatNumber(tier.counts.total, locale) })}`}
                  {!tier.open && ` · ${t("tier.closed")}`}
                </span>
              </span>
            </button>
          );
          if (tier.pending !== true || onRemovePending === undefined) return button;
          return (
            <div key={tier.id} className="sm-brush-pending">
              {button}
              <button
                type="button"
                className="sm-brush-remove"
                aria-label={t("seats.brush.remove", { name: tier.name })}
                onClick={() => onRemovePending(tier.id)}
                data-testid="seat-pending-remove"
              >
                ×
              </button>
            </div>
          );
        })}
      </div>
      {add !== undefined && !adding && (
        <>
          <button type="button" className="btn" onClick={() => setAdding(true)} disabled={add.full} data-testid="seat-add-tier">
            {t("seats.brush.add")}
          </button>
          {add.full && <p className="notice notice-warn">{t("seats.newTier.tooMany")}</p>}
        </>
      )}
      {add !== undefined && adding && (
        <AddTierForm
          currency={add.currency}
          takenNames={add.takenNames}
          defaultColor={add.defaultColor}
          onCancel={() => setAdding(false)}
          onAdd={(v) => {
            add.onAdd(v);
            setAdding(false);
          }}
        />
      )}
    </>
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
  canReset,
  onApply,
  onUndo,
  onReset,
}: {
  totals: TrayTotals;
  strokes: number;
  busy: boolean;
  /** The tray holds something to clear: strokes, or a category that is not created yet. */
  canReset?: boolean;
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
        <button type="button" className="btn" onClick={onReset} disabled={(canReset ?? strokes > 0) === false || busy} data-testid="seat-reset">
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
  onRemovePending,
}: {
  changes: readonly BatchChange[];
  totals: TrayTotals;
  base: ReadonlyMap<string, SeatState>;
  /** The categories of the date and the ones of the tray (`pending`). */
  tiers: readonly TierView[];
  onRemovePending?: (tierId: string) => void;
}) {
  const { t, locale } = useI18n();
  const pending = tiers.filter((x) => x.pending === true);
  if (totals.seats === 0 && pending.length === 0) return <p className="muted">{t("seats.changes.empty")}</p>;
  const deltas: TierDelta[] = tierDeltas(base, changes);
  const name = (id: string) => tiers.find((x) => x.id === id)?.name ?? t("seats.tier.unknown");
  const seatsOf = (id: string) => changes.filter((c) => c.tier_id === id).length;
  return (
    <div data-testid="seat-changes">
      {pending.length > 0 && (
        <section aria-label={t("seats.changes.newTiers")} data-testid="seat-pending-tiers">
          <h3>{t("seats.changes.newTiers")}</h3>
          <ul className="sm-deltas">
            {pending.map((p) => {
              const n = seatsOf(p.id);
              return (
                <li key={p.id} data-testid="seat-pending-tier" data-tier-name={p.name}>
                  <div className="sm-pending-line">
                    <span>
                      <strong>{p.name}</strong>{" "}
                      <span className="muted">
                        {formatMoney(p.price, p.currency, locale)} · {t("seats.brush.pending")}
                      </span>
                    </span>
                    {onRemovePending !== undefined && (
                      <button type="button" className="btn" onClick={() => onRemovePending(p.id)} data-testid="seat-pending-remove-change">
                        {t("seats.brush.remove", { name: p.name })}
                      </button>
                    )}
                  </div>
                  {n > 0 ? (
                    <span className="muted">{t("seats.changes.newTier.seats", { count: formatNumber(n, locale) })}</span>
                  ) : (
                    <span className="notice notice-warn" data-testid="seat-pending-unused">
                      {t("seats.changes.newTier.empty")}
                    </span>
                  )}
                </li>
              );
            })}
          </ul>
        </section>
      )}
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
