/**
 * The automatic layout (task S2c). The list of algorithms and the form of
 * their parameters are read from the server (GET .../seat-layout/algorithms)
 * and drawn generically: a new algorithm needs no change here. A proposal is a
 * preview on the map (POST .../seat-layout/suggest writes nothing); only the
 * button "Move as changes" turns it into strokes of the tray, and the usual
 * Apply sends them as one batch. Sold and held seats are frozen: the proposal
 * never touches them and the revenue line counts them at the price they were
 * sold or held for.
 */
import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { listLayoutAlgorithms, suggestSeatLayout } from "@/lib/api/endpoints";
import type { SeatLayoutAlgorithm, SeatLayoutParam, SeatLayoutSuggestResponse } from "@/lib/api/types";
import { formatMoney, formatNumber } from "@/lib/format";
import { CATALOGS, useI18n, type MessageKey } from "@/lib/i18n";
import {
  UNIT_MINOR,
  UNIT_PERCENT,
  buildParams,
  humanizeKey,
  initialValues,
  prefillFromTiers,
  majorText,
  NEW_CATEGORY,
  checkDrafts,
  draftOfSlot,
  mappingComplete,
  mappingWithNew,
  overTierLimit,
  parseIntList,
  parseMajor,
  parseMoneyList,
  previewTable,
  proposalToStrokes,
  resolveNewSlots,
  sliderBounds,
  suggestMapping,
  type DraftCheck,
  type FormValues,
  type NewTierDraft,
  type NewTierValue,
} from "@/lib/seatManager/layout";
import { slotColor } from "@/lib/seatManager/colors";
import type { Merged, PendingTier, SeatState, Stroke } from "@/lib/seatManager/pending";
import type { Point } from "@/lib/seatManager/model";
import { ErrorNotice } from "@/components/ErrorNotice";
import { Loading } from "@/components/ui";
import { NewTierFields, type TierView } from "@/components/seatManager/SeatPanels";

/** What the map paints over the seats while a proposal is shown. */
export interface LayoutPreview {
  /** seat key -> colour of its proposed slot. */
  readonly colors: ReadonlyMap<string, string>;
  /** seat key -> name of its proposed slot, for the seat card. */
  readonly names: ReadonlyMap<string, string>;
}

interface Props {
  orgId: string;
  sessionId: string;
  /** The categories the server has (not the ones of the tray). */
  tiers: readonly TierView[];
  /** Categories of the tray that are not created yet. */
  pending: readonly PendingTier[];
  /** Names a new category may not take: every category of the date and of the tray. */
  takenNames: readonly string[];
  /** How many categories the date has now (all kinds). */
  liveTierCount: number;
  /** May the person create categories (the right `tier.create`). */
  canCreateTiers: boolean;
  base: ReadonlyMap<string, SeatState>;
  merged: Merged;
  nextStrokeId: number;
  stage: Point | null;
  onPointStage: () => void;
  onPreview: (p: LayoutPreview | null) => void;
  onTransfer: (strokes: Stroke[], note: { changed: number; leftOut: number }, created: PendingTier[]) => void;
}

const DEBOUNCE_MS = 450;

function hasKey(key: string): key is MessageKey {
  return Object.prototype.hasOwnProperty.call(CATALOGS.en, key);
}

export function SeatLayoutPanel(props: Props) {
  const { orgId, sessionId } = props;
  const { t, locale } = useI18n();
  const tx = (key: string): string => (hasKey(key) ? t(key) : humanizeKey(key));

  const algorithms = useQuery({
    queryKey: ["seat-algorithms", orgId, sessionId],
    queryFn: () => listLayoutAlgorithms(orgId, sessionId),
    staleTime: 5 * 60_000,
  });

  const [algId, setAlgId] = useState<string | null>(null);
  const alg: SeatLayoutAlgorithm | undefined = useMemo(
    () => algorithms.data?.algorithms.find((a) => a.id === (algId ?? algorithms.data.algorithms[0]?.id)),
    [algorithms.data, algId],
  );
  const currency = algorithms.data?.currency ?? "EUR";

  if (algorithms.isLoading) return <Loading />;
  if (algorithms.error !== null && algorithms.error !== undefined) return <ErrorNotice error={algorithms.error} />;
  if (alg === undefined) return <p className="muted">{t("seats.layout.none")}</p>;

  return (
    <div data-testid="seat-layout">
      {algorithms.data!.algorithms.length > 1 && (
        <label className="field">
          <span>{t("seats.layout.algorithm")}</span>
          <select value={alg.id} onChange={(e) => setAlgId(e.target.value)} data-testid="layout-algorithm">
            {algorithms.data!.algorithms.map((a) => (
              <option key={a.id} value={a.id}>
                {tx(a.label_key)}
              </option>
            ))}
          </select>
        </label>
      )}
      {alg.description_key !== undefined && <p className="muted">{tx(alg.description_key)}</p>}
      {/* The form is keyed by the algorithm: another algorithm is another form with its own defaults. */}
      <LayoutForm key={alg.id} alg={alg} currency={currency} tx={tx} locale={locale} {...props} />
    </div>
  );
}

function LayoutForm({
  alg,
  currency,
  tx,
  locale,
  orgId,
  sessionId,
  tiers,
  pending,
  takenNames,
  liveTierCount,
  canCreateTiers,
  base,
  merged,
  nextStrokeId,
  stage,
  onPointStage,
  onPreview,
  onTransfer,
}: Props & { alg: SeatLayoutAlgorithm; currency: string; tx: (k: string) => string; locale: string }) {
  const { t } = useI18n();
  const [values, setValues] = useState<FormValues>(() => prefillFromTiers(alg, initialValues(alg), tiers.map((x) => x.price), canCreateTiers));
  const [raw, setRaw] = useState<Record<string, string>>({});
  const [bad, setBad] = useState<Record<string, boolean>>({});
  const [result, setResult] = useState<SeatLayoutSuggestResponse | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);
  const [mapping, setMapping] = useState<Map<string, string | null>>(new Map());
  const [drafts, setDrafts] = useState<Map<string, NewTierDraft>>(new Map());
  const [moved, setMoved] = useState<{ changed: number; leftOut: number } | null>(null);
  const ticket = useRef(0);

  const needsStage = alg.required_annotations.includes("focal_point");
  const missingStage = needsStage && stage === null;
  const anyBad = Object.values(bad).some(Boolean);
  const built = useMemo(() => buildParams(alg, values), [alg, values]);

  // The preview follows the form: a pause after the last change, then one request.
  useEffect(() => {
    if (anyBad || !built.ok || missingStage) {
      ticket.current++;
      setLoading(false);
      return;
    }
    const mine = ++ticket.current;
    setLoading(true);
    const timer = setTimeout(() => {
      suggestSeatLayout(orgId, sessionId, {
        algorithm: alg.id,
        params: built.params,
        annotations: stage !== null ? { focal_point: { x: stage.x, y: stage.y } } : undefined,
      })
        .then((res) => {
          if (ticket.current !== mine) return;
          setResult(res);
          setError(null);
          setMoved(null);
          setMapping(canCreateTiers ? mappingWithNew(res.layout.slots, tiers) : suggestMapping(res.layout.slots, tiers));
          // Every slot starts with what the proposal says: its name (or "Category N"), its price, its colour.
          setDrafts(
            new Map(
              res.layout.slots.map((s, i) => [s.key, draftOfSlot(s, currency, t("seats.layout.slot", { n: i + 1 }), slotColor(i))] as const),
            ),
          );
        })
        .catch((err: unknown) => {
          if (ticket.current !== mine) return;
          setError(err);
          setResult(null);
        })
        .finally(() => {
          if (ticket.current === mine) setLoading(false);
        });
    }, DEBOUNCE_MS);
    return () => clearTimeout(timer);
    // tiers is read only to suggest the first mapping of a NEW proposal.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [alg.id, built, anyBad, missingStage, stage, orgId, sessionId]);

  // The map shows the proposal.
  useEffect(() => {
    if (result === null) {
      onPreview(null);
      return;
    }
    const colors = new Map<string, string>();
    const names = new Map<string, string>();
    const slotIndex = new Map(result.layout.slots.map((s, i) => [s.key, i] as const));
    const slotName = new Map(result.layout.slots.map((s, i) => [s.key, s.name ?? t("seats.layout.slot", { n: i + 1 })] as const));
    for (const [seat, slot] of Object.entries(result.layout.assignments)) {
      colors.set(seat, slotColor(slotIndex.get(slot) ?? 0));
      const name = slotName.get(slot);
      if (name !== undefined) names.set(seat, name);
    }
    onPreview({ colors, names });
    // onPreview is stable for the life of the manager.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [result]);
  useEffect(() => () => onPreview(null), []); // eslint-disable-line react-hooks/exhaustive-deps

  const set = (name: string, v: FormValues[string]) => setValues((prev) => ({ ...prev, [name]: v }));
  const setBadFlag = (name: string, flag: boolean) => setBad((prev) => (prev[name] === flag ? prev : { ...prev, [name]: flag }));

  // The slots that become a new category, with what the person typed, judged the way the server will judge it.
  const newSlots = useMemo(() => {
    const m = new Map<string, NewTierDraft>();
    if (result === null) return m;
    for (const s of result.layout.slots) {
      const d = drafts.get(s.key);
      if (mapping.get(s.key) === NEW_CATEGORY && d !== undefined) m.set(s.key, d);
    }
    return m;
  }, [result, mapping, drafts]);
  const checks = useMemo(() => checkDrafts(newSlots, currency, takenNames), [newSlots, currency, takenNames]);
  const draftsBad = [...checks.values()].some((c) => !c.ok);
  const overLimit = overTierLimit(liveTierCount, pending.length, newSlots.size);

  const transfer = () => {
    if (result === null || draftsBad || overLimit) return;
    const values = new Map<string, NewTierValue>();
    for (const [slot, c] of checks) if (c.ok) values.set(slot, c.value);
    const resolved = resolveNewSlots(result.layout, mapping, values, pending);
    const res = proposalToStrokes(result.layout, resolved.mapping, base, merged, nextStrokeId);
    onTransfer(res.strokes, { changed: res.changed, leftOut: res.leftOut }, resolved.created);
    setMoved({ changed: res.changed, leftOut: res.leftOut });
    setResult(null);
  };

  return (
    <div>
      {needsStage && (
        <div className="sm-stage" data-testid="layout-stage">
          <p>{stage === null ? t("seats.layout.stage.missing") : t("seats.layout.stage.set")}</p>
          <button type="button" className="btn" onClick={onPointStage} data-testid="layout-stage-btn">
            {stage === null ? t("seats.layout.stage.point") : t("seats.layout.stage.move")}
          </button>
        </div>
      )}

      {alg.params.map((p) => (
        <ParamField
          key={p.name}
          p={p}
          value={values[p.name]}
          raw={raw[p.name]}
          currency={currency}
          tx={tx}
          bad={bad[p.name] === true}
          onValue={(v) => set(p.name, v)}
          onRaw={(text, parsed) => {
            setRaw((prev) => ({ ...prev, [p.name]: text }));
            setBadFlag(p.name, text.trim() !== "" && parsed === "bad");
            set(p.name, parsed === "bad" || parsed === "empty" ? undefined : parsed);
          }}
        />
      ))}

      {!built.ok && <p className="notice notice-warn">{t("seats.layout.paramsMissing")}</p>}

      <p className="muted" role="status" aria-live="polite" data-testid="layout-state">
        {loading ? t("seats.layout.calculating") : result !== null ? t("seats.layout.ready") : ""}
      </p>
      {error !== null && <ErrorNotice error={error} />}

      {result !== null && (
        <Proposal
          result={result}
          currency={currency}
          locale={locale}
          tiers={tiers}
          mapping={mapping}
          canCreateTiers={canCreateTiers}
          drafts={drafts}
          checks={checks}
          overLimit={overLimit}
          onMap={(slot, tier) => setMapping((prev) => new Map(prev).set(slot, tier))}
          onDraft={(slot, d) => setDrafts((prev) => new Map(prev).set(slot, d))}
          onTransfer={transfer}
        />
      )}
      {moved !== null && (
        <p className="notice notice-ok" role="status" data-testid="layout-moved">
          {t("seats.layout.moved", { count: formatNumber(moved.changed, locale) })}
          {moved.leftOut > 0 && ` ${t("seats.layout.leftOut", { count: formatNumber(moved.leftOut, locale) })}`}
        </p>
      )}
    </div>
  );
}

// ─── the proposal ────────────────────────────────────────────────────────

function Proposal({
  result,
  currency,
  locale,
  tiers,
  mapping,
  canCreateTiers,
  drafts,
  checks,
  overLimit,
  onMap,
  onDraft,
  onTransfer,
}: {
  result: SeatLayoutSuggestResponse;
  currency: string;
  locale: string;
  tiers: readonly TierView[];
  mapping: ReadonlyMap<string, string | null>;
  canCreateTiers: boolean;
  drafts: ReadonlyMap<string, NewTierDraft>;
  checks: ReadonlyMap<string, DraftCheck>;
  overLimit: boolean;
  onMap: (slot: string, tier: string | null) => void;
  onDraft: (slot: string, draft: NewTierDraft) => void;
  onTransfer: () => void;
}) {
  const { t } = useI18n();
  const m = result.metrics;
  const slots = result.layout.slots;
  const complete = mappingComplete(slots, mapping);
  const used = new Set([...mapping.values()].filter((x): x is string => x !== null && x !== NEW_CATEGORY));
  const anyMapped = [...mapping.values()].some((x) => x !== null);
  const draftsBad = [...checks.values()].some((c) => !c.ok);
  const slotName = (s: (typeof slots)[number], i: number) => s.name ?? t("seats.layout.slot", { n: i + 1 });
  const table = previewTable(slots, m.by_slot, slotName);
  return (
    <div data-testid="layout-proposal">
      <h3>{t("seats.layout.revenue")}</h3>
      <ul className="facts" data-testid="layout-revenue">
        <li>{t("seats.layout.revenue.sold", { amount: formatMoney(m.revenue_sold_minor, currency, locale), count: formatNumber(m.sold_seats, locale) })}</li>
        {m.held_seats > 0 && (
          <li>{t("seats.layout.revenue.held", { amount: formatMoney(m.revenue_held_minor, currency, locale), count: formatNumber(m.held_seats, locale) })}</li>
        )}
        <li>{t("seats.layout.revenue.rest", { amount: formatMoney(m.revenue_remaining_minor, currency, locale) })}</li>
        <li>
          <strong>{t("seats.layout.revenue.total", { amount: formatMoney(m.revenue_estimate_minor, currency, locale) })}</strong>
        </li>
      </ul>
      <p className="muted">{t("seats.layout.revenue.note")}</p>

      <div className="sm-table-wrap">
        <table className="sm-table" data-testid="layout-table">
          <caption>{t("seats.layout.table.caption")}</caption>
          <thead>
            <tr>
              <th scope="col">{t("seats.layout.table.category")}</th>
              <th scope="col" className="sm-num">{t("seats.layout.table.seats")}</th>
              <th scope="col" className="sm-num">{t("seats.layout.table.price")}</th>
              <th scope="col" className="sm-num">{t("seats.layout.table.full")}</th>
            </tr>
          </thead>
          <tbody>
            {table.rows.map((r) => (
              <tr key={r.slotKey} data-testid="layout-table-row">
                <th scope="row">{r.name}</th>
                <td className="sm-num">{formatNumber(r.seats, locale)}</td>
                <td className="sm-num">{formatMoney(r.price, currency, locale)}</td>
                <td className="sm-num">{formatMoney(r.revenue, currency, locale)}</td>
              </tr>
            ))}
          </tbody>
          <tfoot>
            <tr data-testid="layout-table-total">
              <th scope="row">{t("seats.layout.table.total")}</th>
              <td className="sm-num">{formatNumber(table.totalSeats, locale)}</td>
              <td className="sm-num" aria-hidden="true" />
              <td className="sm-num">{formatMoney(table.totalRevenue, currency, locale)}</td>
            </tr>
          </tfoot>
        </table>
      </div>
      <p data-testid="layout-realistic">
        <strong>{t("seats.layout.realistic", { amount: formatMoney(m.realistic_revenue_minor, currency, locale) })}</strong>
      </p>
      <p className="muted">{t("seats.layout.realistic.note")}</p>

      <h3>{t("seats.layout.slots")}</h3>
      <ul className="sm-slots">
        {slots.map((s, i) => {
          const sm = m.by_slot.find((x) => x.slot_key === s.key);
          return (
            <li key={s.key} data-testid="layout-slot">
              <span
                className="sm-swatch"
                ref={(el) => {
                  if (el !== null) el.style.backgroundColor = slotColor(i);
                }}
                aria-hidden="true"
              />
              <div className="sm-slot-text">
                <strong>{slotName(s, i)}</strong>
                <span className="muted">
                  {formatMoney(s.price_amount, currency, locale)}
                  {sm !== undefined && ` · ${t("seats.layout.slot.seats", { count: formatNumber(sm.seats, locale), share: Math.round(sm.share * 100) })}`}
                </span>
              </div>
              <label className="sm-slot-map">
                <span className="visually-hidden">{t("seats.layout.mapTo", { n: i + 1 })}</span>
                <select
                  value={mapping.get(s.key) ?? ""}
                  onChange={(e) => onMap(s.key, e.target.value === "" ? null : e.target.value)}
                  data-testid="layout-map"
                >
                  {canCreateTiers && <option value={NEW_CATEGORY}>{t("seats.layout.mapNew")}</option>}
                  {tiers.map((tier) => (
                    <option key={tier.id} value={tier.id} disabled={used.has(tier.id) && mapping.get(s.key) !== tier.id}>
                      {tier.name} · {formatMoney(tier.price, tier.currency, locale)}
                    </option>
                  ))}
                  <option value="">{t("seats.layout.mapNone")}</option>
                </select>
              </label>
              {mapping.get(s.key) === NEW_CATEGORY && drafts.get(s.key) !== undefined && (
                <div className="sm-newtier" role="group" aria-label={t("seats.layout.newTier", { n: i + 1 })} data-testid="layout-new-tier">
                  <NewTierFields
                    draft={drafts.get(s.key)!}
                    errors={(() => {
                      const c = checks.get(s.key);
                      return c !== undefined && !c.ok ? c.errors : null;
                    })()}
                    currency={currency}
                    testPrefix="layout-new"
                    onChange={(d) => onDraft(s.key, d)}
                  />
                </div>
              )}
            </li>
          );
        })}
      </ul>
      {!canCreateTiers && <p className="muted" data-testid="layout-no-create">{t("seats.layout.noCreate")}</p>}
      {!complete && <p className="notice notice-warn">{t("seats.layout.mapIncomplete")}</p>}
      {overLimit && (
        <p className="notice notice-bad" role="alert" data-testid="layout-too-many">
          {t("seats.newTier.tooMany")}
        </p>
      )}
      {[...mapping.values()].some((x) => x === NEW_CATEGORY) && <p className="muted">{t("seats.layout.newTier.hint")}</p>}
      <button type="button" className="btn btn-primary" onClick={onTransfer} disabled={!anyMapped || draftsBad || overLimit} data-testid="layout-transfer">
        {t("seats.layout.transfer")}
      </button>
      <p className="muted">{t("seats.layout.transfer.hint")}</p>
    </div>
  );
}

// ─── one parameter ───────────────────────────────────────────────────────

type Parsed = number | number[] | "bad" | "empty";

function ParamField({
  p,
  value,
  raw,
  currency,
  tx,
  bad,
  onValue,
  onRaw,
}: {
  p: SeatLayoutParam;
  value: FormValues[string];
  raw: string | undefined;
  currency: string;
  tx: (key: string) => string;
  bad: boolean;
  onValue: (v: FormValues[string]) => void;
  onRaw: (text: string, parsed: Parsed) => void;
}) {
  const { t } = useI18n();
  const id = `lp-${p.name}`;
  const hint = p.hint_key !== undefined ? <small className="muted">{tx(p.hint_key)}</small> : null;

  if (p.type === "boolean") {
    return (
      <div className="field">
        <label className="sm-check" htmlFor={id}>
          <input id={id} type="checkbox" checked={value === true} onChange={(e) => onValue(e.target.checked)} data-testid={`param-${p.name}`} />
          <span>{tx(p.label_key)}</span>
        </label>
        {hint}
      </div>
    );
  }

  if (p.type === "enum") {
    return (
      <div className="field" role="group" aria-label={tx(p.label_key)}>
        <span>{tx(p.label_key)}</span>
        <div className="sm-seg">
          {(p.options ?? []).map((o) => (
            <button
              key={o.value}
              type="button"
              className="sm-seg-btn"
              aria-pressed={value === o.value}
              onClick={() => onValue(o.value)}
              data-testid={`param-${p.name}-${o.value}`}
            >
              {tx(o.label_key)}
            </button>
          ))}
        </div>
        {hint}
      </div>
    );
  }

  const slider = sliderBounds(p);
  if (slider !== null) {
    const shown = typeof value === "number" ? value : slider.min;
    return (
      <div className="field">
        <label htmlFor={id}>
          {tx(p.label_key)}: <strong data-testid={`param-${p.name}-value`}>{shown}{p.unit === UNIT_PERCENT ? " %" : ""}</strong>
        </label>
        <input
          id={id}
          type="range"
          min={slider.min}
          max={slider.max}
          step={1}
          value={shown}
          onChange={(e) => onValue(Number(e.target.value))}
          data-testid={`param-${p.name}`}
        />
        {hint}
      </div>
    );
  }

  // Typed: money (major units), a whole number, or a list.
  const isList = p.type === "integer_list";
  const money = p.unit === UNIT_MINOR;
  const text =
    raw !== undefined
      ? raw
      : Array.isArray(value)
        ? value.map((v) => (money ? majorText(v, currency) : String(v))).join(" ")
        : typeof value === "number"
          ? money
            ? majorText(value, currency)
            : String(value)
          : "";
  const parse = (s: string): Parsed => {
    if (s.trim() === "") return "empty";
    if (isList) {
      const list = money ? parseMoneyList(s, currency) : parseIntList(s);
      if (list === null) return "bad";
      if (p.min_items !== undefined && p.min_items > 0 && list.length < p.min_items) return "bad";
      if (p.max_items !== undefined && p.max_items > 0 && list.length > p.max_items) return "bad";
      if (list.some((n) => (typeof p.min === "number" && n < p.min) || (typeof p.max === "number" && n > p.max))) return "bad";
      return list;
    }
    const n = money ? parseMajor(s, currency) : /^\d{1,9}$/.test(s.trim()) ? Number(s.trim()) : null;
    if (n === null) return "bad";
    if ((typeof p.min === "number" && n < p.min) || (typeof p.max === "number" && n > p.max)) return "bad";
    return n;
  };
  return (
    <div className="field">
      <label htmlFor={id}>
        {tx(p.label_key)}
        {money && ` (${currency})`}
        {p.unit === UNIT_PERCENT && " (%)"}
      </label>
      <input
        id={id}
        type="text"
        inputMode={money ? "decimal" : "numeric"}
        autoComplete="off"
        value={text}
        aria-invalid={bad}
        onChange={(e) => onRaw(e.target.value, parse(e.target.value))}
        data-testid={`param-${p.name}`}
      />
      {isList && <small className="muted">{t("seats.layout.listHint")}</small>}
      {bad && (
        <small className="field-error" role="alert">
          {t("seats.layout.paramBad")}
        </small>
      )}
      {hint}
    </div>
  );
}
