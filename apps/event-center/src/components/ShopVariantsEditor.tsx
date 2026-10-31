/**
 * The variants of a shop product (spec 47, section 6): up to three options,
 * and for each variant its name or option values, SKU, price, old price and
 * stock. The component is controlled and pure: the screen owns the rows, the
 * save and the 409 `shop.stock_changed` reload.
 *
 * A row that was loaded carries the stock the person saw (`loadedStock`); the
 * screen sends it back as `expected_stock`, so two people editing the same
 * variant never overwrite each other's numbers.
 */
import { useEffect, useRef } from "react";
import { MoveButtons } from "@/components/materials/shared";
import { TextField } from "@/components/FormField";
import { useI18n, type MessageKey } from "@/lib/i18n";
import {
  SHOP_LIMITS,
  emptyRow,
  isSingleVersion,
  type RowErrors,
  type VariantProblem,
  type VariantRow,
  type VariantsCheck,
} from "@/lib/shop";

export interface VariantStat {
  held: number;
  available: number | null;
}

export interface ShopVariantsEditorProps {
  rows: readonly VariantRow[];
  onChange: (rows: VariantRow[]) => void;
  currency: string;
  /** The names of the options in use, leading and without holes. */
  optionNames: readonly string[];
  /** How many option columns the rows show. */
  optionCount: number;
  /** The result of `checkVariants` once the person tried to save, else null. */
  check: VariantsCheck | null;
  disabled?: boolean;
  /** A digital product: no stock controls, the file is not counted. */
  digital?: boolean;
  /** Whether a variant the person adds counts its stock from the start: a service does not (M-01). Default: yes. */
  newRowTracks?: boolean;
  /** Held and available units of the saved variants, by id. */
  stats?: Readonly<Record<string, VariantStat>>;
}

const PROBLEM_KEY: Record<VariantProblem, MessageKey> = {
  price: "shop.variants.err.price",
  compareAt: "shop.variants.err.compareAt",
  compareBelowPrice: "shop.variants.err.compareBelowPrice",
  stock: "shop.variants.err.stock",
  sku: "shop.variants.err.sku",
  title: "shop.variants.err.title",
  optionTooLong: "shop.variants.err.optionTooLong",
  duplicate: "shop.variants.err.duplicate",
};

export function ShopVariantsEditor({ rows, onChange, currency, optionNames, optionCount, check, disabled = false, digital = false, newRowTracks = true, stats }: ShopVariantsEditorProps) {
  const { t } = useI18n();
  // One version with nothing to tell versions apart: no empty name field, a plain word instead.
  const single = isSingleVersion(rows, optionNames);
  const root = useRef<HTMLDivElement>(null);
  // Where the focus goes after the rows changed (it would be lost with the control that was pressed).
  const pending = useRef<{ what: "add" } | { what: "move"; key: string; delta: -1 | 1 } | { what: "remove"; index: number } | null>(null);
  useEffect(() => {
    const p = pending.current;
    const box = root.current;
    if (p === null || box === null) return;
    pending.current = null;
    const items = Array.from(box.querySelectorAll<HTMLElement>('[data-testid="shop-variant-row"]'));
    const live = (el: HTMLElement | null | undefined) => (el !== null && el !== undefined && !(el as HTMLButtonElement).disabled ? el : null);
    let target: HTMLElement | null = null;
    if (p.what === "add") {
      target = live(items[items.length - 1]?.querySelector<HTMLElement>("input:not([disabled]):not([type=checkbox]), button:not([disabled])"));
    } else if (p.what === "move") {
      const at = rows.findIndex((r) => r.key === p.key);
      const pick = (d: "up" | "down") => items[at]?.querySelector<HTMLElement>(`[data-testid="shop-variant-${at}-${d}"]`);
      target = live(pick(p.delta < 0 ? "up" : "down")) ?? live(pick(p.delta < 0 ? "down" : "up"));
    } else {
      const next = items[Math.min(p.index, items.length - 1)];
      target = live(next?.querySelector<HTMLElement>(`[data-testid^="shop-variant-remove-"]`)) ?? box.querySelector<HTMLElement>('[data-testid="shop-variant-add"]');
    }
    target?.focus();
  });

  const patch = (key: string, change: Partial<VariantRow>) => onChange(rows.map((r) => (r.key === key ? { ...r, ...change } : r)));
  const setOption = (key: string, i: number, value: string) =>
    onChange(
      rows.map((r) => {
        if (r.key !== key) return r;
        const options: [string, string, string] = [...r.options];
        options[i] = value;
        return { ...r, options };
      }),
    );
  const move = (index: number, delta: -1 | 1) => {
    pending.current = { what: "move", key: rows[index].key, delta };
    const next = [...rows];
    const [item] = next.splice(index, 1);
    next.splice(index + delta, 0, item);
    onChange(next);
  };
  const remove = (key: string) => {
    pending.current = { what: "remove", index: rows.findIndex((r) => r.key === key) };
    onChange(rows.filter((r) => r.key !== key));
  };
  // The option that is too long gets the message, not always the first one.
  const optionTooLong = (r: VariantRow, i: number) => [...r.options[i].trim()].length > SHOP_LIMITS.option;
  const errorOf = (key: string, p: VariantProblem): string | undefined => {
    const e: RowErrors | undefined = check?.rows[key];
    if (e?.[p] !== true) return undefined;
    return t(PROBLEM_KEY[p], { max: p === "sku" ? SHOP_LIMITS.sku : SHOP_LIMITS.option });
  };

  return (
    <div ref={root} data-testid="shop-variants">
      {check?.list === "empty" && (
        <p className="field-error" role="alert" data-testid="shop-variants-empty">
          {t("shop.variants.err.empty")}
        </p>
      )}
      {check?.list === "tooMany" && (
        <p className="field-error" role="alert">
          {t("shop.variants.err.tooMany", { max: SHOP_LIMITS.maxVariants })}
        </p>
      )}
      <ul className="cards">
        {rows.map((r, index) => {
          const stat = r.id !== undefined ? stats?.[r.id] : undefined;
          return (
            <li key={r.key} className="card" data-testid="shop-variant-row">
              <fieldset disabled={disabled}>
                <legend>{t("shop.variants.row", { n: index + 1 })}</legend>
                {single ? (
                  <div data-testid="shop-variant-single">
                    <p>
                      <strong>{t("shop.variants.single")}</strong>
                    </p>
                    <p className="field-hint">{t("shop.variants.singleHint")}</p>
                  </div>
                ) : optionCount === 0 ? (
                  <TextField
                    label={t("shop.variants.f.title")}
                    hint={rows.length === 1 ? t("shop.variants.f.titleHint") : undefined}
                    error={errorOf(r.key, "title") ?? errorOf(r.key, "duplicate")}
                    value={r.title}
                    maxLength={SHOP_LIMITS.title}
                    onChange={(v) => patch(r.key, { title: v })}
                    testId={`shop-variant-title-${index}`}
                  />
                ) : (
                  Array.from({ length: optionCount }, (_, i) => (
                    <TextField
                      key={i}
                      label={optionNames[i] ?? t("shop.options.name", { n: i + 1 })}
                      error={
                        (i === 0 ? (errorOf(r.key, "title") ?? errorOf(r.key, "duplicate")) : undefined) ??
                        (optionTooLong(r, i) ? errorOf(r.key, "optionTooLong") : undefined)
                      }
                      value={r.options[i]}
                      maxLength={SHOP_LIMITS.option}
                      onChange={(v) => setOption(r.key, i, v)}
                      testId={`shop-variant-option${i + 1}-${index}`}
                    />
                  ))
                )}
                <TextField
                  label={t("shop.variants.f.sku")}
                  error={errorOf(r.key, "sku")}
                  value={r.sku}
                  maxLength={SHOP_LIMITS.sku}
                  onChange={(v) => patch(r.key, { sku: v })}
                  testId={`shop-variant-sku-${index}`}
                />
                <TextField
                  label={t("shop.variants.f.price", { currency })}
                  error={errorOf(r.key, "price")}
                  value={r.price}
                  onChange={(v) => patch(r.key, { price: v })}
                  testId={`shop-variant-price-${index}`}
                />
                <TextField
                  label={t("shop.variants.f.compareAt", { currency })}
                  hint={t("shop.variants.f.compareAtHint")}
                  error={errorOf(r.key, "compareAt") ?? errorOf(r.key, "compareBelowPrice")}
                  value={r.compareAt}
                  onChange={(v) => patch(r.key, { compareAt: v })}
                  testId={`shop-variant-compare-${index}`}
                />
                {!digital && (
                <p>
                  <label>
                    <input
                      type="checkbox"
                      checked={r.trackStock}
                      onChange={(e) => patch(r.key, { trackStock: e.target.checked })}
                      data-testid={`shop-variant-track-${index}`}
                    />{" "}
                    {t("shop.variants.f.track")}
                  </label>
                </p>
                )}
                {digital && <p className="field-hint" data-testid="shop-variants-digital-note">{t("shop.variants.digitalNote")}</p>}
                {!digital && !r.trackStock && <p className="field-hint">{t("shop.variants.f.trackHint")}</p>}
                {!digital && r.trackStock && (
                  <TextField
                    label={t("shop.variants.f.stock")}
                    error={errorOf(r.key, "stock")}
                    value={r.stock}
                    onChange={(v) => patch(r.key, { stock: v })}
                    testId={`shop-variant-stock-${index}`}
                  />
                )}
                {!digital && stat !== undefined && (
                  <p className="muted" data-testid={`shop-variant-stat-${index}`}>
                    {t("shop.variants.held", { count: stat.held })}
                    {stat.available !== null && <> · {t("shop.variants.available", { count: stat.available })}</>}
                  </p>
                )}
              </fieldset>
              {!disabled && (
                <div className="actions">
                  <MoveButtons
                    index={index}
                    count={rows.length}
                    disabled={false}
                    onMove={(d) => move(index, d)}
                    testId={`shop-variant-${index}`}
                    upLabel={t("shop.variants.moveUp", { n: index + 1 })}
                    downLabel={t("shop.variants.moveDown", { n: index + 1 })}
                  />
                  <button
                    type="button"
                    className="btn"
                    disabled={rows.length === 1}
                    onClick={() => remove(r.key)}
                    aria-label={t("shop.variants.removeN", { n: index + 1 })}
                    data-testid={`shop-variant-remove-${index}`}
                  >
                    {t("shop.variants.remove")}
                  </button>
                </div>
              )}
            </li>
          );
        })}
      </ul>
      {!disabled && (
        <p>
          <button
            type="button"
            className="btn"
            disabled={rows.length >= SHOP_LIMITS.maxVariants}
            onClick={() => {
              pending.current = { what: "add" };
              onChange([...rows, emptyRow(newRowTracks)]);
            }}
            aria-describedby={rows.length >= SHOP_LIMITS.maxVariants ? "shop-variant-add-limit" : undefined}
            data-testid="shop-variant-add"
          >
            {t("shop.variants.add")}
          </button>
          {rows.length >= SHOP_LIMITS.maxVariants && (
            <span className="field-hint" id="shop-variant-add-limit">
              {" "}
              {t("shop.variants.err.tooMany", { max: SHOP_LIMITS.maxVariants })}
            </span>
          )}
        </p>
      )}
    </div>
  );
}
