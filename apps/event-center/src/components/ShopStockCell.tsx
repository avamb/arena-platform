/**
 * The stock of a product in the products list, edited in place (A-07 and A-15): the point is a phone, where
 * "a delivery came, fix the number" must not mean opening the card and scrolling four screens.
 *
 * - One variant that counts its stock: a number field in the row. It saves when the person leaves the field or
 *   presses Enter, and says so.
 * - Several variants: the total, and an "Stock" disclosure that lists every variant with the same field.
 * - Nothing counts stock (digital, or counting switched off): a plain word, nothing to edit.
 *
 * A save is the card's own variants route (the whole list goes back, each variant with the stock the person
 * saw as `expected_stock`). When the shelf moved meanwhile (409 `shop.stock_changed`) the field shows the new
 * number and a line says what the person typed, exactly like the card does. Saves of one product are queued,
 * so two fields edited one after another never fight over the numbers.
 */
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import { ErrorNotice } from "@/components/ErrorNotice";
import { fetchShopProduct, replaceShopVariants } from "@/lib/api/shopApi";
import { formatNumber } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import {
  isStockChanged,
  isVariantsChanged,
  parseStockInput,
  stockChangedDetails,
  stockEditMode,
  stockSum,
  stockUpdateRequest,
  variantIdsOf,
  trackedVariants,
  variantLabel,
  type ShopProductView,
} from "@/lib/shop";

/** One queue per product: an edit waits for the one before it, then reads the product as it is by then. */
const queues = new Map<string, Promise<unknown>>();

/** The newest copy of a product that any cached list or card holds, else the one the row was drawn with. */
function latestProduct(client: QueryClient, orgId: string, fallback: ShopProductView): ShopProductView {
  const card = client.getQueryData<ShopProductView>(["shopProduct", orgId, fallback.id]);
  let best: ShopProductView = fallback;
  const newer = (p: ShopProductView) => Date.parse(p.updated_at) >= Date.parse(best.updated_at);
  if (card !== undefined && newer(card)) best = card;
  for (const [, data] of client.getQueriesData<{ products: ShopProductView[] }>({ queryKey: ["shopProducts", orgId] })) {
    const p = data?.products.find((x) => x.id === fallback.id);
    if (p !== undefined && newer(p)) best = p;
  }
  return best;
}

/** Puts a fresh copy of the product into the list and the card, so every field shows what the server has. */
function storeProduct(client: QueryClient, orgId: string, p: ShopProductView) {
  client.setQueryData(["shopProduct", orgId, p.id], p);
  client.setQueriesData<{ products: ShopProductView[] }>({ queryKey: ["shopProducts", orgId] }, (old) =>
    old === undefined ? old : { ...old, products: old.products.map((x) => (x.id === p.id ? p : x)) },
  );
}

export interface ShopStockCellProps {
  orgId: string;
  product: ShopProductView;
  currency: string;
}

export function ShopStockCell({ orgId, product, currency }: ShopStockCellProps) {
  const { t, locale } = useI18n();
  const mode = stockEditMode(product);
  const total = stockSum(product);
  if (mode === "none") return <>{t("shop.products.stockNone")}</>;
  if (mode === "single") {
    const v = trackedVariants(product)[0];
    return (
      <StockField
        orgId={orgId}
        product={product}
        variantId={v.id}
        stock={v.stock}
        currency={currency}
        label={t("shop.stock.fieldOf", { title: product.title })}
        title={product.title}
      />
    );
  }
  return (
    <div className="shop-stock-several">
      <span data-testid="shop-stock-total">{t("shop.stock.total", { count: formatNumber(total ?? 0, locale) })}</span>
      <details className="shop-stock-details" data-testid="shop-stock-details">
        <summary className="shop-stock-summary">{t("shop.stock.disclosure")}</summary>
        <ul className="shop-stock-list">
          {trackedVariants(product).map((v) => {
            const name = variantLabel(v, t("shop.variants.single"));
            return (
              <li key={v.id}>
                <span className="shop-stock-name">{name}</span>
                <StockField orgId={orgId} product={product} variantId={v.id} stock={v.stock} currency={currency} label={t("shop.stock.fieldOf", { title: `${product.title}, ${name}` })} title={name} />
              </li>
            );
          })}
        </ul>
      </details>
    </div>
  );
}

type Status = { kind: "idle" } | { kind: "saving" } | { kind: "saved" } | { kind: "bad"; why: "number" } | { kind: "moved"; was: number | null; now: number; typed: string } | { kind: "failed"; error: unknown };

function StockField({
  orgId,
  product,
  variantId,
  stock,
  currency,
  label,
  title,
}: {
  orgId: string;
  product: ShopProductView;
  variantId: string;
  stock: number;
  currency: string;
  label: string;
  title: string;
}) {
  const { t } = useI18n();
  const client = useQueryClient();
  const id = useId();
  // What the person is typing; null while the field simply shows the saved number.
  const [draft, setDraft] = useState<string | null>(null);
  const [status, setStatus] = useState<Status>({ kind: "idle" });
  const busy = useRef(false);
  const text = draft ?? String(stock);

  // After a save the field keeps showing the number until the list hands the saved product down: no flash of the old one.
  useEffect(() => {
    if (draft !== null && (status.kind === "saved" || status.kind === "moved") && parseStockInput(draft) === stock) setDraft(null);
  }, [draft, status.kind, stock]);

  const commit = () => {
    if (draft === null || busy.current) return;
    const typed = draft;
    if (typed.trim() === String(stock)) {
      setDraft(null);
      setStatus({ kind: "idle" });
      return;
    }
    const n = parseStockInput(typed);
    if (n === null) {
      setStatus({ kind: "bad", why: "number" });
      return;
    }
    busy.current = true;
    setStatus({ kind: "saving" });
    const run = async () => {
      const current = latestProduct(client, orgId, product);
      const body = stockUpdateRequest(current, variantId, n, currency);
      if (body === null) throw new Error("variant is gone");
      try {
        storeProduct(client, orgId, await replaceShopVariants(orgId, product.id, body, variantIdsOf(current)));
        setDraft(String(n));
        setStatus({ kind: "saved" });
      } catch (error) {
        if (isVariantsChanged(error)) {
          // A colleague added or removed a version: bring the list up to date, then say so.
          storeProduct(client, orgId, await fetchShopProduct(orgId, product.id));
          throw error;
        }
        if (!isStockChanged(error)) throw error;
        // Somebody else moved the shelf: show their number, keep what was typed in the line below, change nothing.
        const d = stockChangedDetails(error).find((x) => x.id === variantId);
        storeProduct(client, orgId, await fetchShopProduct(orgId, product.id));
        setDraft(String(d?.stock ?? stock));
        setStatus({ kind: "moved", was: d?.expected ?? stock, now: d?.stock ?? stock, typed });
      }
    };
    const chain = (queues.get(product.id) ?? Promise.resolve()).then(run, run);
    queues.set(
      product.id,
      chain.catch(() => undefined),
    );
    chain
      .catch((error: unknown) => setStatus({ kind: "failed", error }))
      .finally(() => {
        busy.current = false;
      });
  };

  return (
    <div className="shop-stock-field" data-testid="shop-stock-field">
      <input
        id={id}
        className="input shop-stock-input"
        type="text"
        inputMode="numeric"
        pattern="[0-9]*"
        autoComplete="off"
        aria-label={label}
        aria-invalid={status.kind === "bad"}
        aria-describedby={`${id}-status`}
        value={text}
        readOnly={status.kind === "saving"}
        onChange={(e) => {
          setDraft(e.target.value);
          setStatus({ kind: "idle" });
        }}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            commit();
          }
        }}
        data-testid="shop-stock-input"
      />
      <span id={`${id}-status`} className="shop-stock-status" role="status" aria-live="polite" data-testid="shop-stock-status">
        {status.kind === "saving" && t("shop.stock.saving")}
        {status.kind === "saved" && t("shop.stock.saved")}
        {status.kind === "bad" && <span className="field-error">{t("shop.variants.err.stock")}</span>}
        {status.kind === "moved" && (
          <span className="field-error" data-testid="shop-stock-moved-line">
            {t("shop.stock.moved")} {t("shop.variants.stockMoved", { title, was: status.was ?? "", now: status.now, typed: status.typed })}
          </span>
        )}
      </span>
      {status.kind === "failed" && <ErrorNotice error={status.error} testId="shop-stock-error" />}
    </div>
  );
}
