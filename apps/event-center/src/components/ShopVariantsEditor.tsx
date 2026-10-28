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
import { MoveButtons } from "@/components/materials/shared";
import { TextField } from "@/components/FormField";
import { useI18n, type MessageKey } from "@/lib/i18n";
import {
  SHOP_LIMITS,
  emptyRow,
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

export function ShopVariantsEditor({ rows, onChange, currency, optionNames, optionCount, check, disabled = false, stats }: ShopVariantsEditorProps) {
  const { t } = useI18n();

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
    const next = [...rows];
    const [item] = next.splice(index, 1);
    next.splice(index + delta, 0, item);
    onChange(next);
  };
  const remove = (key: string) => onChange(rows.filter((r) => r.key !== key));
  const errorOf = (key: string, p: VariantProblem): string | undefined => {
    const e: RowErrors | undefined = check?.rows[key];
    if (e?.[p] !== true) return undefined;
    return t(PROBLEM_KEY[p], { max: p === "sku" ? SHOP_LIMITS.sku : SHOP_LIMITS.option });
  };

  return (
    <div data-testid="shop-variants">
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
                {optionCount === 0 ? (
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
                      error={i === 0 ? (errorOf(r.key, "title") ?? errorOf(r.key, "optionTooLong") ?? errorOf(r.key, "duplicate")) : undefined}
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
                {!r.trackStock && <p className="field-hint">{t("shop.variants.f.trackHint")}</p>}
                {r.trackStock && (
                  <TextField
                    label={t("shop.variants.f.stock")}
                    error={errorOf(r.key, "stock")}
                    value={r.stock}
                    onChange={(v) => patch(r.key, { stock: v })}
                    testId={`shop-variant-stock-${index}`}
                  />
                )}
                {stat !== undefined && (
                  <p className="muted" data-testid={`shop-variant-stat-${index}`}>
                    {t("shop.variants.held", { count: stat.held })}
                    {stat.available !== null && <> · {t("shop.variants.available", { count: stat.available })}</>}
                  </p>
                )}
              </fieldset>
              {!disabled && (
                <div className="actions">
                  <MoveButtons index={index} count={rows.length} disabled={false} onMove={(d) => move(index, d)} testId={`shop-variant-${index}`} />
                  <button
                    type="button"
                    className="btn"
                    disabled={rows.length === 1}
                    onClick={() => remove(r.key)}
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
            onClick={() => onChange([...rows, emptyRow()])}
            data-testid="shop-variant-add"
          >
            {t("shop.variants.add")}
          </button>
        </p>
      )}
    </div>
  );
}
