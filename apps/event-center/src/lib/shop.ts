/**
 * Pure rules of the shop screens (spec 47, section 6): products and their
 * variants, orders and their actions, the settings form. No React and no
 * `fetch`, so every rule is tested alone.
 *
 * The SERVER stays the judge (prices, stock, slugs, who may do what). These
 * checks only spare a person a round trip and give a clear message.
 *
 * Money is MINOR units on the wire and in the order data. A person types the
 * major amount ("12.50"); `parseMoney` and `moneyInput` are the only place
 * where the two meet, with the number of digits taken from the currency.
 */
import { ApiError } from "@/lib/api/http";
import type {
  ShopImage,
  ShopOrder,
  ShopOrderCounts,
  ShopProduct,
  ShopSettings,
  ShopSettingsUpdateRequest,
  ShopVariant,
  ShopVariantInput,
} from "@/lib/api/types";
import { isIsoCurrency } from "@/lib/currencies";
import { currencyFractionDigits, formatDateTime, formatMoneySymbol, regionName } from "@/lib/format";
import type { Tone } from "@/lib/orders";

// ─── What the API will add (lane "shop-core") ───────────────────────────────
// These fields are optional here, so the screens work with an API that does
// not send them yet. When the generated client has them, the intersections
// collapse into the generated types.

export const SHOP_KINDS = ["physical", "digital", "service"] as const;
export type ShopKind = (typeof SHOP_KINDS)[number];

/** A product as the screens read it: `kind`, `digital_media_id` and `option_names` may be missing. */
export type ShopProductView = ShopProduct & {
  kind?: string;
  digital_media_id?: string | null;
  option_names?: string[] | null;
};
/** A variant row to send: `expected_stock` is the stock the person saw. */
export type ShopVariantSend = ShopVariantInput & { expected_stock?: number };
/** The settings as the screens read them. */
export type ShopSettingsView = ShopSettings;

export function productKind(p: { kind?: string }): ShopKind {
  return (SHOP_KINDS as readonly string[]).includes(p.kind ?? "") ? (p.kind as ShopKind) : "physical";
}

// ─── Limits (the server's, shop_catalog.go and the OpenAPI text) ────────────

export const SHOP_LIMITS = {
  title: 300,
  sku: 100,
  option: 100,
  optionName: 50,
  maxOptions: 3,
  maxVariants: 100,
  announcement: 500,
  shippingLabel: 200,
  logisticsNote: 2000,
  trackingNote: 2000,
  refundReason: 2000,
  alt: 500,
  /** The largest digital file the web sends (the media API's own cap is 32 MiB). */
  digitalBytes: 32 * 1024 * 1024,
} as const;

/** Media owner types. `shop_product` is a picture of a product. */
export const SHOP_IMAGE_OWNER = "shop_product";
/** The file a buyer of a digital product receives (assumed name, see the report of lane L4). */
export const SHOP_DIGITAL_OWNER = "shop_digital";

export const PRODUCT_STATUSES = ["draft", "active", "archived"] as const;
export type ProductStatus = (typeof PRODUCT_STATUSES)[number];

export function isProductStatus(value: string): value is ProductStatus {
  return (PRODUCT_STATUSES as readonly string[]).includes(value);
}

export function productTone(status: string): Tone {
  if (status === "active") return "ok";
  if (status === "draft") return "wait";
  return "neutral";
}

// ─── Money ───────────────────────────────────────────────────────────────────

/** "12.50" or "12,5" in the currency's major units to minor units, or null when it is not an amount. */
export function parseMoney(text: string, currency: string): number | null {
  const digits = currencyFractionDigits(currency);
  const clean = text.trim().replace(",", ".");
  if (!/^\d+(\.\d+)?$/.test(clean)) return null;
  const [whole, frac = ""] = clean.split(".");
  if (frac.length > digits) return null;
  const minor = Number(whole + frac.padEnd(digits, "0"));
  return Number.isSafeInteger(minor) ? minor : null;
}

/** Minor units as a major amount for an input: 1250 EUR is "12.50", 500 JPY is "500". */
export function moneyInput(minor: number, currency: string): string {
  const digits = currencyFractionDigits(currency);
  if (digits === 0) return String(minor);
  const sign = minor < 0 ? "-" : "";
  const abs = String(Math.abs(minor)).padStart(digits + 1, "0");
  return `${sign}${abs.slice(0, -digits)}.${abs.slice(-digits)}`;
}

// ─── Product list ────────────────────────────────────────────────────────────

/** Cheapest and dearest variant price in minor units, or null when there is no variant. */
export function priceRange(p: { variants: readonly { price_minor: number }[] }): { min: number; max: number } | null {
  if (p.variants.length === 0) return null;
  const prices = p.variants.map((v) => v.price_minor);
  return { min: Math.min(...prices), max: Math.max(...prices) };
}

/** Units on the shelf over the variants that track stock, or null when none of them does. */
export function stockSum(p: { variants: readonly { stock: number; track_stock: boolean }[] }): number | null {
  const tracked = p.variants.filter((v) => v.track_stock);
  if (tracked.length === 0) return null;
  return tracked.reduce((sum, v) => sum + v.stock, 0);
}

/** True when a tracked variant is below zero (an oversell): the list marks it. */
export function oversold(p: { variants: readonly { stock: number; track_stock: boolean }[] }): boolean {
  return p.variants.some((v) => v.track_stock && v.stock < 0);
}

// ─── Variants editor ─────────────────────────────────────────────────────────

export interface VariantRow {
  /** A key for React only. */
  key: string;
  /** The variant's id; absent for a row the person added. */
  id?: string;
  title: string;
  options: [string, string, string];
  sku: string;
  price: string;
  compareAt: string;
  stock: string;
  trackStock: boolean;
  /** The stock the person saw when the screen loaded: sent back as `expected_stock`, but only when they edited it. */
  loadedStock?: number;
}

/**
 * True when the person changed the stock of a row they loaded. Only such a row sends `stock` and
 * `expected_stock` (SA-04): a row whose stock was not touched leaves the shelf to the server, so a sale
 * made while the card was open can neither refuse the save nor be overwritten by it.
 */
export function stockEdited(r: VariantRow, digital = false): boolean {
  if (digital || r.id === undefined || !r.trackStock || r.loadedStock === undefined) return false;
  return r.stock.trim() !== String(r.loadedStock);
}

let rowSeq = 0;
function nextKey(): string {
  rowSeq += 1;
  return `v${rowSeq}`;
}

/** `trackStock` is off for a file or a service (M-01): they have no shelf, and a counted 0 would sell out at birth. */
export function emptyRow(trackStock = true): VariantRow {
  return { key: nextKey(), title: "", options: ["", "", ""], sku: "", price: "", compareAt: "", stock: "0", trackStock };
}

/** True when a new variant of this kind counts its stock by default: only a physical product has a shelf. */
export function kindTracksStock(kind: ShopKind): boolean {
  return kind === "physical";
}

/**
 * M-01: the storefront shows "Sold out" for this product and the person has no hint of it: every variant counts
 * stock and none has a unit left. A digital product never counts, so it is never empty.
 */
export function shelfEmpty(p: { kind?: string; variants: readonly { stock: number; track_stock: boolean }[] }): boolean {
  if (productKind(p) === "digital" || p.variants.length === 0) return false;
  return p.variants.every((v) => v.track_stock && v.stock <= 0);
}

const DEFAULT_TITLE = "Default Title";

/** Shopify's own name of a product with one version: a technical word, never something a person typed. */
export function isDefaultTitle(value: string | null | undefined): boolean {
  const v = (value ?? "").trim();
  return v === "" || v.toLowerCase() === DEFAULT_TITLE.toLowerCase();
}

/** A text for a field: the technical "Default Title" is shown as an empty field. */
function visibleName(value: string | null | undefined): string {
  return isDefaultTitle(value) ? "" : (value ?? "");
}

export function rowFromVariant(v: ShopVariant, currency: string): VariantRow {
  return {
    key: nextKey(),
    id: v.id,
    title: visibleName(v.title),
    options: [visibleName(v.option1), visibleName(v.option2), visibleName(v.option3)],
    sku: v.sku ?? "",
    price: moneyInput(v.price_minor, currency),
    compareAt: v.compare_at_minor === null || v.compare_at_minor === undefined ? "" : moneyInput(v.compare_at_minor, currency),
    stock: String(v.stock),
    trackStock: v.track_stock,
    loadedStock: v.stock,
  };
}

export function rowsFromProduct(p: ShopProduct, currency: string): VariantRow[] {
  return p.variants.map((v) => rowFromVariant(v, currency));
}

/** The option names a product carries, trimmed, without holes, at most three. */
export function optionNamesOf(p: { option_names?: string[] | null }): string[] {
  return (p.option_names ?? []).map((n) => n.trim()).filter((n) => n !== "").slice(0, SHOP_LIMITS.maxOptions);
}

/** How many option columns the editor shows: the named options, or the ones a loaded variant already uses. */
export function optionCountOf(names: readonly string[], rows: readonly VariantRow[]): number {
  let used = 0;
  for (const r of rows) {
    for (let i = 0; i < 3; i++) if (r.options[i].trim() !== "") used = Math.max(used, i + 1);
  }
  return Math.min(SHOP_LIMITS.maxOptions, Math.max(names.length, used));
}

/** True when the row carries no name and no option value at all. */
export function rowIsNameless(row: VariantRow): boolean {
  return row.title.trim() === "" && row.options.every((o) => o.trim() === "");
}

/** The option names Shopify gives a product that has no options. */
function onlyDefaultOptionNames(names: readonly string[]): boolean {
  return names.length === 0 || (names.length === 1 && names[0].trim().toLowerCase() === "title");
}

/**
 * A product with ONE version and nothing to tell versions apart: the editor shows "The only
 * version" instead of an empty name field (or a field named after a technical option). As soon
 * as the person names an option or adds a second variant, the fields come back.
 */
export function isSingleVersion(rows: readonly VariantRow[], optionNames: readonly string[]): boolean {
  return rows.length === 1 && rowIsNameless(rows[0]) && onlyDefaultOptionNames(optionNames);
}

/** The title of a row: its option values joined, else the typed title. */
export function rowTitle(row: VariantRow, optionCount: number): string {
  const values = row.options.slice(0, optionCount).map((o) => o.trim()).filter((o) => o !== "");
  return values.length > 0 ? values.join(" / ") : row.title.trim();
}

export type VariantProblem =
  | "price"
  | "compareAt"
  | "compareBelowPrice"
  | "stock"
  | "sku"
  | "title"
  | "optionTooLong"
  | "duplicate";

export type RowErrors = Partial<Record<VariantProblem, true>>;

export interface VariantsCheck {
  ok: boolean;
  /** Errors by row key. */
  rows: Record<string, RowErrors>;
  /** The list itself is not valid: empty or too long. */
  list: "empty" | "tooMany" | null;
}

/** Checks the whole list. A simple product (one row, no options) may leave the title empty. */
/** A digital product never tracks stock: its rows have no stock to check or send. */
export interface VariantsMode {
  digital?: boolean;
}

export function checkVariants(rows: readonly VariantRow[], currency: string, optionCount: number, mode: VariantsMode = {}): VariantsCheck {
  const out: Record<string, RowErrors> = {};
  let ok = true;
  const mark = (key: string, p: VariantProblem) => {
    (out[key] ??= {})[p] = true;
    ok = false;
  };
  const seen = new Map<string, string>();
  for (const r of rows) {
    const price = parseMoney(r.price, currency);
    if (price === null) mark(r.key, "price");
    if (r.compareAt.trim() !== "") {
      const cmp = parseMoney(r.compareAt, currency);
      if (cmp === null) mark(r.key, "compareAt");
      else if (price !== null && cmp <= price) mark(r.key, "compareBelowPrice");
    }
    if (mode.digital !== true && r.trackStock && !/^\d+$/.test(r.stock.trim())) mark(r.key, "stock");
    if ([...r.sku.trim()].length > SHOP_LIMITS.sku) mark(r.key, "sku");
    if (r.options.some((o) => [...o.trim()].length > SHOP_LIMITS.option)) mark(r.key, "optionTooLong");
    const title = rowTitle(r, optionCount);
    if (rows.length > 1 && title === "") mark(r.key, "title");
    if (title !== "") {
      const k = title.toLowerCase();
      if (seen.has(k)) {
        mark(r.key, "duplicate");
        mark(seen.get(k) as string, "duplicate");
      } else seen.set(k, r.key);
    }
  }
  const list = rows.length === 0 ? "empty" : rows.length > SHOP_LIMITS.maxVariants ? "tooMany" : null;
  return { ok: ok && list === null, rows: out, list };
}

/** The body of PUT .../variants for rows that passed `checkVariants`. */
export function variantsRequest(rows: readonly VariantRow[], currency: string, optionCount: number, mode: VariantsMode = {}): ShopVariantSend[] {
  const digital = mode.digital === true;
  return rows.map((r, i) => {
    const out: ShopVariantSend = {
      title: rowTitle(r, optionCount),
      sku: r.sku.trim() === "" ? null : r.sku.trim(),
      price_minor: parseMoney(r.price, currency) ?? 0,
      compare_at_minor: r.compareAt.trim() === "" ? null : parseMoney(r.compareAt, currency),
      track_stock: digital ? false : r.trackStock,
      position: i,
      option1: optionCount >= 1 && r.options[0].trim() !== "" ? r.options[0].trim() : null,
      option2: optionCount >= 2 && r.options[1].trim() !== "" ? r.options[1].trim() : null,
      option3: optionCount >= 3 && r.options[2].trim() !== "" ? r.options[2].trim() : null,
    };
    if (r.id === undefined) {
      // A new row: its first stock (a file or a row that does not count stock starts at 0).
      out.stock = digital || !r.trackStock ? 0 : Number(r.stock.trim());
    } else {
      out.id = r.id;
      // Only an edited stock goes out, with the number the person saw: the server refuses the save when the
      // shelf moved meanwhile. A row without it keeps the server's stock.
      if (stockEdited(r, digital)) {
        out.stock = Number(r.stock.trim());
        out.expected_stock = r.loadedStock;
      }
    }
    return out;
  });
}

/** The ids of the variants a product has, as the editor saw them (`expected_variant_ids` of PUT .../variants). */
export function variantIdsOf(p: { variants: readonly { id: string }[] }): string[] {
  return p.variants.map((v) => v.id);
}

/**
 * The rows after the API refused a save with 409 `shop.stock_changed` (SA-04): ONLY the stock the person saw in
 * the affected rows is brought up to date. Prices, SKUs, names, new rows and every typed number stay as they are,
 * so pressing Save again sends the same edits against the fresh shelf.
 */
export function rowsAfterStockMoved(rows: readonly VariantRow[], moved: readonly { id: string; stock: number }[]): VariantRow[] {
  return rows.map((r) => {
    const m = r.id === undefined ? undefined : moved.find((x) => x.id === r.id);
    return m === undefined ? r : { ...r, loadedStock: m.stock };
  });
}

/** True when the API refused a variants save because the set of variants is no longer the one the editor loaded. */
export function isVariantsChanged(err: unknown): boolean {
  return err instanceof ApiError && err.code === "shop.variants_changed";
}

/**
 * The rows after 409 `shop.variants_changed` (SA-13): a colleague added or removed a variant. The person's edits
 * stay, rows of variants that are gone are dropped, variants added elsewhere are appended as saved. `loadedIds`
 * is the set the editor had; a variant the person deleted from the screen stays deleted.
 */
export function mergeVariantSet(
  rows: readonly VariantRow[],
  fresh: { variants: readonly ShopVariant[] },
  loadedIds: readonly string[],
  currency: string,
): { rows: VariantRow[]; removed: number; added: number } {
  const live = new Set(fresh.variants.map((v) => v.id));
  const known = new Set(loadedIds);
  const kept = rows.filter((r) => r.id === undefined || live.has(r.id));
  const onScreen = new Set(rows.map((r) => r.id));
  const added = fresh.variants.filter((v) => !known.has(v.id) && !onScreen.has(v.id)).map((v) => rowFromVariant(v, currency));
  return { rows: [...kept, ...added], removed: rows.length - kept.length, added: added.length };
}

/** What the details form holds, for `productPatch`. */
export interface ProductDetailsState {
  title: string;
  status: string;
  kind: ShopKind;
  optionNames: readonly string[];
  digitalId: string | null;
  body: string;
  bodyTouched: boolean;
}

/** The body of PATCH .../products/{id} (SA-13): ONLY the fields that differ from the saved product. */
export function productPatch(now: ProductDetailsState, saved: ShopProductView): {
  title?: string;
  status?: string;
  kind?: string;
  option_names?: string[];
  digital_media_id?: string | null;
  body_html?: string;
} {
  const out: ReturnType<typeof productPatch> = {};
  if (now.title.trim() !== saved.title) out.title = now.title.trim();
  if (now.status !== saved.status) out.status = now.status;
  if (now.kind !== productKind(saved)) out.kind = now.kind;
  if (JSON.stringify(now.optionNames) !== JSON.stringify(optionNamesOf(saved))) out.option_names = [...now.optionNames];
  // Absent keeps the file, null removes it. Only a digital product has one.
  if (now.kind === "digital") {
    if (now.digitalId !== (saved.digital_media_id ?? null)) out.digital_media_id = now.digitalId;
  } else if ((saved.digital_media_id ?? null) !== null) out.digital_media_id = null;
  if (now.bodyTouched && now.body !== (saved.body_html ?? "")) out.body_html = now.body;
  return out;
}

/** True when the API refused a variants save because the stock moved since the screen loaded. */
export function isStockChanged(err: unknown): boolean {
  return err instanceof ApiError && err.code === "shop.stock_changed";
}

/** What the API said about a refused stock write: each variant's stock now, by variant id. */
export function stockChangedDetails(err: unknown): { id: string; expected: number | null; stock: number }[] {
  if (!isStockChanged(err)) return [];
  const list = (err as ApiError).details?.variants;
  if (!Array.isArray(list)) return [];
  const out: { id: string; expected: number | null; stock: number }[] = [];
  for (const v of list) {
    if (v === null || typeof v !== "object") continue;
    const o = v as { id?: unknown; expected_stock?: unknown; stock?: unknown };
    if (typeof o.id === "string" && typeof o.stock === "number") out.push({ id: o.id, expected: typeof o.expected_stock === "number" ? o.expected_stock : null, stock: o.stock });
  }
  return out;
}

function variantsSignature(rows: readonly VariantRow[], currency: string, optionCount: number, mode: VariantsMode): string {
  return JSON.stringify(variantsRequest(rows, currency, optionCount, mode).map((v) => ({ ...v, position: undefined, expected_stock: undefined })));
}

/** True when the rows differ from the saved variants (so «Save» is worth pressing). */
export function variantsChanged(rows: readonly VariantRow[], saved: readonly ShopVariant[], currency: string, optionCount: number, mode: VariantsMode = {}): boolean {
  const was = saved.map((v) => rowFromVariant(v, currency));
  return variantsSignature(rows, currency, optionCount, mode) !== variantsSignature(was, currency, optionCount, mode);
}

// ─── Pictures ────────────────────────────────────────────────────────────────

export interface ImageAttach {
  media_id: string;
  position: number;
  alt: string;
}

/**
 * Moving a picture is re-attaching: "attaching a picture again moves and relabels it". The plan
 * lists EVERY picture with its new place (0, 1, 2…) and its alt kept, in the new order, so the
 * result never depends on how the server shifts the others.
 */
export function moveImagePlan(images: readonly ShopImage[], from: number, delta: -1 | 1): ImageAttach[] | null {
  const to = from + delta;
  if (from < 0 || from >= images.length || to < 0 || to >= images.length) return null;
  const next = [...images];
  const [item] = next.splice(from, 1);
  next.splice(to, 0, item);
  return next.map((im, i) => ({ media_id: im.media_id, position: i, alt: im.alt }));
}

// ─── Orders ──────────────────────────────────────────────────────────────────

export const SHOP_ORDER_STATUSES = [
  "pending_payment",
  "paid",
  "shipped",
  "cancelled",
  "expired",
  "refunded",
  "partially_refunded",
  "manual_review",
] as const;
export type ShopOrderStatus = (typeof SHOP_ORDER_STATUSES)[number];

export function isShopOrderStatus(value: string): value is ShopOrderStatus {
  return (SHOP_ORDER_STATUSES as readonly string[]).includes(value);
}

export function shopOrderTone(status: string): Tone {
  switch (status) {
    case "paid":
    case "shipped":
      return "ok";
    case "pending_payment":
    case "manual_review":
      return "wait";
    case "cancelled":
    case "expired":
      return "bad";
    default:
      return "neutral";
  }
}

export function shopOrderNumber(n: number): string {
  return `№${n}`;
}

/**
 * Whether the order has been handed over. `status` says so only until a refund
 * overwrites it (shipped -> partially_refunded / refunded), so the fact is
 * read from `shipped_at` too (M-10).
 */
export function isShipped(o: { status: string; shipped_at?: string | null }): boolean {
  return o.status === "shipped" || (o.shipped_at !== null && o.shipped_at !== undefined && o.shipped_at !== "");
}

/**
 * "Mark shipped" exists for a paid order (also partly refunded, or parked for a check) that has not
 * been shipped yet. Once `shippedAt` is set it never comes back, whatever a refund did to the status.
 */
export function canShip(status: string, canManage: boolean, deliveryKind?: string | null, shippedAt?: string | null): boolean {
  // M-03: an order with no delivery (a file, a service) has nothing to hand over.
  if (deliveryKind === "none") return false;
  if (isShipped({ status, shipped_at: shippedAt })) return false;
  return canManage && (status === "paid" || status === "partially_refunded" || status === "manual_review");
}

/** A shipped order can still get its tracking note changed (also after a partial refund; a fully refunded one is closed). */
export function canEditTracking(status: string, canManage: boolean, shippedAt?: string | null): boolean {
  if (!canManage) return false;
  return status === "shipped" || (status === "partially_refunded" && isShipped({ status, shipped_at: shippedAt }));
}

/**
 * The badge text of an order: the status, and next to a refund status the fact that the order had
 * been shipped ("Shipped · Partly refunded"). The keys are in the three locales.
 */
export function orderStatusLabelKey(status: string, shippedAt?: string | null): string | null {
  if (!isShopOrderStatus(status)) return null;
  if ((status === "refunded" || status === "partially_refunded") && isShipped({ status, shipped_at: shippedAt })) {
    return `shop.order.status.shipped_${status}`;
  }
  return `shop.order.status.${status}`;
}

/** What the payment block says about a paid order: refunded as a whole, refunded in part, or just the state. */
export function paymentRefundKind(order: Pick<ShopOrder, "status" | "total" | "refunded_minor">, payState: string | null): "refunded" | "partly" | null {
  if (payState !== "succeeded") return null;
  if (order.refunded_minor <= 0) return null;
  return order.status === "refunded" || order.refunded_minor >= order.total ? "refunded" : "partly";
}

/** Cancel exists for an order that still waits for its payment, and only that. */
export function canCancelShopOrder(status: string, canManage: boolean): boolean {
  return canManage && status === "pending_payment";
}

/** What can still go back to the buyer, minor units. */
export function refundable(order: Pick<ShopOrder, "total" | "refunded_minor">): number {
  return Math.max(0, order.total - order.refunded_minor);
}

/**
 * What can go back to the buyer, minor units, as the card knows it. The card sees only SUCCEEDED refunds
 * (`refunded_minor`), the server also subtracts the ones still on their way. When the server has said what is
 * left (`serverLeft`, from `details.refundable_minor` of a refused refund), the smaller of the two wins.
 */
export function refundableNow(order: Pick<ShopOrder, "total" | "refunded_minor">, serverLeft: number | null = null): number {
  const own = refundable(order);
  return serverLeft === null ? own : Math.max(0, Math.min(own, serverLeft));
}

/**
 * What the server said is left to refund (SA-19): `details.refundable_minor` of 422 `shop.invalid_refund_amount`,
 * 0 for 409 `shop.nothing_to_refund`. Null for any other failure.
 */
export function refundableFromError(err: unknown): number | null {
  if (!(err instanceof ApiError)) return null;
  if (err.code === "shop.nothing_to_refund") return 0;
  const left = err.details?.refundable_minor;
  return typeof left === "number" && Number.isFinite(left) && left >= 0 ? left : null;
}

/** Refund exists when money was taken through arena and some of it is left. */
/** Refunding needs shop.manage AND the refund rights of the money engine (refund.create and refund.approve). */
export function canRefundShopOrder(order: Pick<ShopOrder, "status" | "total" | "refunded_minor" | "payment">, canManage: boolean): boolean {
  if (!canManage || order.payment === null || order.payment === undefined) return false;
  if (!["paid", "shipped", "partially_refunded", "manual_review"].includes(order.status)) return false;
  return refundable(order) > 0;
}

/**
 * The lines that 409 `shop.out_of_stock` names in `details.items` (SA-19): the variant, how many the order holds and
 * how many are free on the shelf. Empty for any other failure.
 */
export function shortItems(err: unknown): { variant_id: string; ordered: number; available: number }[] {
  if (!(err instanceof ApiError) || err.code !== "shop.out_of_stock") return [];
  const list = err.details?.items;
  if (!Array.isArray(list)) return [];
  const out: { variant_id: string; ordered: number; available: number }[] = [];
  for (const v of list) {
    if (v === null || typeof v !== "object") continue;
    const o = v as { variant_id?: unknown; ordered?: unknown; available?: unknown };
    if (typeof o.variant_id === "string" && typeof o.ordered === "number" && typeof o.available === "number") {
      out.push({ variant_id: o.variant_id, ordered: o.ordered, available: o.available });
    }
  }
  return out;
}

export type RefundCheck =
  | { ok: true; amountMinor: number | null; full: boolean }
  | { ok: false; problem: "amount" | "tooMuch" | "reasonTooLong" };

/** A blank amount refunds what is left. Otherwise 1 to what is left. */
export function checkRefund(
  order: Pick<ShopOrder, "total" | "refunded_minor" | "currency">,
  amountText: string,
  reason: string,
  serverLeft: number | null = null,
): RefundCheck {
  if ([...reason.trim()].length > SHOP_LIMITS.refundReason) return { ok: false, problem: "reasonTooLong" };
  const left = refundableNow(order, serverLeft);
  if (amountText.trim() === "") return { ok: true, amountMinor: null, full: true };
  const minor = parseMoney(amountText, order.currency);
  if (minor === null || minor < 1) return { ok: false, problem: "amount" };
  if (minor > left) return { ok: false, problem: "tooMuch" };
  return { ok: true, amountMinor: minor, full: minor === left };
}

export function refundBody(check: Extract<RefundCheck, { ok: true }>, reason: string): { amount_minor?: number; reason?: string } {
  const body: { amount_minor?: number; reason?: string } = {};
  if (check.amountMinor !== null) body.amount_minor = check.amountMinor;
  if (reason.trim() !== "") body.reason = reason.trim();
  return body;
}

/** The refund answered by the API: did the money go back, or is it on its way. */
export function refundOutcome(state: string): "done" | "pending" | "failed" | "review" {
  if (state === "succeeded") return "done";
  if (state === "provider_pending") return "pending";
  if (state === "manual_review") return "review";
  return "failed";
}

export interface TimelineEntry {
  kind: "created" | "paid" | "shipped" | "cancelled" | "refunded" | "expires" | "expired";
  at: string | null;
}

/** The moments of an order, oldest first. A refund has no time of its own: it is listed last, with its sum. */
export function orderTimeline(o: Pick<ShopOrder, "created_at" | "paid_at" | "shipped_at" | "cancelled_at" | "expires_at" | "refunded_minor" | "status" | "updated_at">): TimelineEntry[] {
  const list: TimelineEntry[] = [{ kind: "created", at: o.created_at }];
  if (o.paid_at) list.push({ kind: "paid", at: o.paid_at });
  if (o.shipped_at) list.push({ kind: "shipped", at: o.shipped_at });
  if (o.cancelled_at) list.push({ kind: "cancelled", at: o.cancelled_at });
  // M-04: "pay by" while the order waits; after the time ran out the line stays, as "not paid in time".
  if (o.status === "pending_payment" && o.expires_at) list.push({ kind: "expires", at: o.expires_at });
  if (o.status === "expired" && o.expires_at) list.push({ kind: "expired", at: o.expires_at });
  list.sort((a, b) => Date.parse(a.at ?? "") - Date.parse(b.at ?? ""));
  if (o.refunded_minor > 0) list.push({ kind: "refunded", at: o.updated_at });
  return list;
}

/**
 * The quick lists over the orders (A-08): the first one is the whole list, the others are the API's
 * `view` values. "to_ship" is what a manager asks first thing in the morning: paid, something to hand over,
 * not shipped yet.
 */
export const ORDER_TABS = ["all", "to_ship", "unpaid", "shipped", "refunds"] as const;
export type OrderTab = (typeof ORDER_TABS)[number];
/** The values the list route understands for `view`. */
export type OrderView = Exclude<OrderTab, "all">;

export function isOrderView(value: unknown): value is OrderView {
  return typeof value === "string" && value !== "all" && (ORDER_TABS as readonly string[]).includes(value);
}

/** The tab that is open: the view when there is one, else the whole list. */
export function activeOrderTab(view: OrderView | "" | undefined): OrderTab {
  return view === undefined || view === "" ? "all" : view;
}

/** The number a tab shows, or null when it shows none (the whole list and the closed ones carry no number). */
export function tabCount(tab: OrderTab, counts: ShopOrderCounts | undefined): number | null {
  if (counts === undefined) return null;
  if (tab === "to_ship") return counts.to_ship;
  if (tab === "unpaid") return counts.unpaid;
  return null;
}

export interface ShopOrdersFilters {
  status: ShopOrderStatus | "";
  /** A quick list; "" is none. It narrows the list together with the status. */
  view: OrderView | "";
  q: string;
  /** YYYY-MM-DD, inclusive. */
  from: string;
  /** YYYY-MM-DD, inclusive. */
  to: string;
}

export const EMPTY_FILTERS: ShopOrdersFilters = { status: "", view: "", q: "", from: "", to: "" };

const DAY = /^\d{4}-\d{2}-\d{2}$/;

/** The day after a YYYY-MM-DD, as YYYY-MM-DD: the API's `to` is "before this instant". */
export function nextDay(day: string): string {
  const d = new Date(`${day}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() + 1);
  return d.toISOString().slice(0, 10);
}

/** The offset of a zone from UTC at an instant, in milliseconds. */
function zoneOffsetMs(zone: string, at: number): number {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: zone,
    hourCycle: "h23",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).formatToParts(new Date(at));
  const n = (type: string) => Number(parts.find((p) => p.type === type)?.value ?? "0");
  return Date.UTC(n("year"), n("month") - 1, n("day"), n("hour"), n("minute"), n("second")) - Math.floor(at / 1000) * 1000;
}

/**
 * The instant a calendar day starts in a time zone, as RFC 3339 in UTC (SA-25): the day of an order is the day the
 * SHOP reads on its screen, not the UTC day. An unknown zone falls back to UTC.
 */
export function dayStartIso(day: string, zone: string | undefined): string {
  const [y, m, d] = day.split("-").map(Number);
  const midnightUtc = Date.UTC(y, m - 1, d);
  let at = midnightUtc;
  if (zone !== undefined) {
    try {
      // Twice, so a day on which the clocks change still lands on its own midnight.
      at = midnightUtc - zoneOffsetMs(zone, midnightUtc);
      at = midnightUtc - zoneOffsetMs(zone, at);
    } catch {
      at = midnightUtc;
    }
  }
  return new Date(at).toISOString();
}

/** The zone the shop reads its times in: its country's zone, else the browser's, else UTC. */
export function shopTimeZone(country: string | null | undefined): string {
  return shopZoneFor(country) ?? browserZone() ?? "UTC";
}

/**
 * The body of POST .../shop/orders/search (SA-17, SA-25). The same filters as the old query, but in the body: a
 * buyer's e-mail or name never goes into an address that a proxy logs. `from` is the start of the first day and
 * `to` the start of the day after the last one, both RFC 3339 in the shop's zone. A malformed day is left out.
 */
export function ordersSearchBody(f: ShopOrdersFilters, limit: number, offset: number, zone?: string): Record<string, string | number> {
  const body: Record<string, string | number> = { limit };
  if (f.status !== "") body.status = f.status;
  if (f.view !== "") body.view = f.view;
  if (f.q.trim() !== "") body.q = f.q.trim();
  if (DAY.test(f.from)) body.from = dayStartIso(f.from, zone);
  if (DAY.test(f.to)) body.to = dayStartIso(nextDay(f.to), zone);
  if (offset > 0) body.offset = offset;
  return body;
}

export interface ShopOrdersSearch {
  status?: ShopOrderStatus;
  view?: OrderView;
}

/** Only the status and the quick list go into the address: a buyer's e-mail or name never does. */
export function parseShopOrdersSearch(raw: Record<string, unknown>): ShopOrdersSearch {
  const out: ShopOrdersSearch = {};
  if (typeof raw.status === "string" && isShopOrderStatus(raw.status)) out.status = raw.status;
  if (isOrderView(raw.view)) out.view = raw.view;
  return out;
}

/**
 * The letters of an order the buyer can be sent again (A-11): the confirmation, the shipping one when it shipped,
 * and the refund one when any money went back (a refunded or partly refunded status, or a refunded amount).
 */
export type OrderLetter = "received" | "shipped" | "refunded";

export function resendLetters(
  order: Pick<ShopOrder, "status" | "buyer" | "delivery_kind" | "shipped_at"> & { refunded_minor?: number },
  canManage: boolean,
): OrderLetter[] {
  if (!canManage || order.buyer.email.trim() === "") return [];
  if (!["paid", "shipped", "partially_refunded", "refunded"].includes(order.status)) return [];
  const out: OrderLetter[] = ["received"];
  if (order.delivery_kind !== "none" && order.shipped_at !== null && order.shipped_at !== undefined) out.push("shipped");
  if (order.status === "partially_refunded" || order.status === "refunded" || (order.refunded_minor ?? 0) > 0) out.push("refunded");
  return out;
}

/** The longest internal note of an order, in characters (the server's limit). */
export const STAFF_NOTE_MAX = 2000;

/** True when the typed note differs from the saved one (spaces around it do not count). */
export function staffNoteChanged(typed: string, saved: string): boolean {
  return typed.trim() !== saved.trim();
}

// ─── Settings ────────────────────────────────────────────────────────────────

export interface SettingsDraft {
  currency: string;
  shippingFee: string;
  shippingLabel: string;
  country: string;
  ageGate: string;
  announcement: string;
  contactEmail: string;
  logisticsNote: string;
  salesChannelId: string;
  maxLineQty: string;
}

export function settingsDraft(s: ShopSettings): SettingsDraft {
  return {
    currency: s.currency,
    shippingFee: moneyInput(s.shipping_fee_minor, s.currency),
    shippingLabel: s.shipping_label,
    country: s.country_iso2 ?? "",
    ageGate: s.age_gate_min_age === null || s.age_gate_min_age === undefined ? "" : String(s.age_gate_min_age),
    announcement: s.announcement_text,
    contactEmail: s.contact_email ?? "",
    logisticsNote: s.logistics_note,
    salesChannelId: s.sales_channel_id ?? "",
    maxLineQty: s.max_line_qty === undefined ? "" : String(s.max_line_qty),
  };
}

export type SettingsProblem =
  | "currency"
  | "currencyUnknown"
  | "shippingFee"
  | "shippingLabel"
  | "country"
  | "ageGate"
  | "announcement"
  | "contactEmail"
  | "logisticsNote"
  | "maxLineQty";

/**
 * The rule of the form. `currencyLocked`: the shop has products, the currency is shown and not typed,
 * so it is not judged here (the server keeps the stored one).
 */
export function checkSettings(d: SettingsDraft, opts: { currencyLocked?: boolean } = {}): Partial<Record<SettingsProblem, true>> {
  const bad: Partial<Record<SettingsProblem, true>> = {};
  const cur = d.currency.trim().toUpperCase();
  if (opts.currencyLocked !== true && !/^[A-Z]{3}$/.test(cur)) bad.currency = true;
  else if (opts.currencyLocked !== true && !isIsoCurrency(cur)) bad.currencyUnknown = true;
  else if (parseMoney(d.shippingFee.trim() === "" ? "0" : d.shippingFee, cur) === null) bad.shippingFee = true;
  if ([...d.shippingLabel.trim()].length > SHOP_LIMITS.shippingLabel) bad.shippingLabel = true;
  if (d.country.trim() !== "" && !/^[A-Za-z]{2}$/.test(d.country.trim())) bad.country = true;
  if (d.ageGate.trim() !== "") {
    const n = Number(d.ageGate.trim());
    if (!/^\d+$/.test(d.ageGate.trim()) || n < 1 || n > 99) bad.ageGate = true;
  }
  if ([...d.announcement].length > SHOP_LIMITS.announcement) bad.announcement = true;
  if (d.contactEmail.trim() !== "" && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(d.contactEmail.trim())) bad.contactEmail = true;
  if ([...d.logisticsNote].length > SHOP_LIMITS.logisticsNote) bad.logisticsNote = true;
  const qty = d.maxLineQty.trim();
  if (qty !== "" && (!/^[0-9]+$/.test(qty) || Number(qty) < 1 || Number(qty) > 99)) bad.maxLineQty = true;
  return bad;
}

/**
 * The body of PUT .../shop/settings: the WHOLE row. The domain and the enabled flag belong to the platform
 * operator and are not sent from here (the API refuses them for anybody else).
 */
export function settingsRequest(d: SettingsDraft, loadedUpdatedAt?: string | null): ShopSettingsUpdateRequest {
  const currency = d.currency.trim().toUpperCase();
  const body: ShopSettingsUpdateRequest = {
    currency,
    shipping_fee_minor: parseMoney(d.shippingFee.trim() === "" ? "0" : d.shippingFee, currency) ?? 0,
    shipping_label: d.shippingLabel.trim(),
    country_iso2: d.country.trim() === "" ? null : d.country.trim().toUpperCase(),
    age_gate_min_age: d.ageGate.trim() === "" ? null : Number(d.ageGate.trim()),
    announcement_text: d.announcement.trim(),
    contact_email: d.contactEmail.trim() === "" ? null : d.contactEmail.trim(),
    logistics_note: d.logisticsNote.trim(),
    sales_channel_id: d.salesChannelId === "" ? null : d.salesChannelId,
  };
  // Absent keeps the stored value.
  if (d.maxLineQty.trim() !== "") body.max_line_qty = Number(d.maxLineQty.trim());
  // The version the person loaded, sent back as the server printed it: a newer save is refused (SA-12).
  if (loadedUpdatedAt !== null && loadedUpdatedAt !== undefined && loadedUpdatedAt !== "") body.expected_updated_at = loadedUpdatedAt;
  return body;
}

/** True when the API refused a settings save because somebody saved them after they were loaded (SA-12). */
export function isSettingsChanged(err: unknown): boolean {
  return err instanceof ApiError && err.code === "shop.settings_changed";
}

/** True when a draft differs from the saved settings (the form has something to save, or to lose). */
export function settingsDirty(d: SettingsDraft, saved: ShopSettings): boolean {
  return JSON.stringify(d) !== JSON.stringify(settingsDraft(saved));
}

/**
 * Whether the shop's money goes where the organizer decided (SA-23). Without a chosen channel the checkout takes
 * the first channel of the organization whose provider is set up, which may not be the one meant for the shop.
 */
export function channelWarning(d: Pick<SettingsDraft, "salesChannelId">, channelCount: number | null): "none" | "unchosen" | "noChannels" {
  if (d.salesChannelId !== "") return "none";
  if (channelCount === 0) return "noChannels";
  return "unchosen";
}

/** The courier text goes to the clipboard: true when the browser allowed it. */
export async function copyText(text: string, clip: Pick<Clipboard, "writeText"> | undefined = typeof navigator !== "undefined" ? navigator.clipboard : undefined): Promise<boolean> {
  if (clip === undefined) return false;
  try {
    await clip.writeText(text);
    return true;
  } catch {
    return false;
  }
}

// ─── Currency of the shop ────────────────────────────────────────────────────

/**
 * The currency may be typed only while the shop has NO product: every price is stored in it, and
 * a typo would silently turn 200 ILS into 200 EUR. `productCount` null means "not known yet":
 * the field stays closed until the answer comes.
 */
export function currencyState(productCount: number | null | undefined): "editable" | "locked" | "checking" {
  if (productCount === null) return "checking";
  return productCount === undefined || productCount === 0 ? "editable" : "locked";
}

// ─── Time of the shop ────────────────────────────────────────────────────────

/** Countries with ONE time zone: the shop's times are read in its zone, not in the zone of the reader. */
export const SHOP_COUNTRY_ZONES: Readonly<Record<string, string>> = {
  IL: "Asia/Jerusalem",
  CZ: "Europe/Prague",
  ES: "Europe/Madrid",
  FR: "Europe/Paris",
  DE: "Europe/Berlin",
  IT: "Europe/Rome",
  PT: "Europe/Lisbon",
  GE: "Asia/Tbilisi",
  PL: "Europe/Warsaw",
  AT: "Europe/Vienna",
  NL: "Europe/Amsterdam",
  BE: "Europe/Brussels",
  CH: "Europe/Zurich",
  GB: "Europe/London",
  IE: "Europe/Dublin",
  GR: "Europe/Athens",
  RS: "Europe/Belgrade",
  ME: "Europe/Podgorica",
  BA: "Europe/Sarajevo",
  HR: "Europe/Zagreb",
  SI: "Europe/Ljubljana",
  SK: "Europe/Bratislava",
  HU: "Europe/Budapest",
  RO: "Europe/Bucharest",
  BG: "Europe/Sofia",
  EE: "Europe/Tallinn",
  LV: "Europe/Riga",
  LT: "Europe/Vilnius",
  FI: "Europe/Helsinki",
  SE: "Europe/Stockholm",
  NO: "Europe/Oslo",
  DK: "Europe/Copenhagen",
  CY: "Asia/Nicosia",
  AE: "Asia/Dubai",
  TR: "Europe/Istanbul",
  AR: "America/Argentina/Buenos_Aires",
  CL: "America/Santiago",
  CO: "America/Bogota",
  PE: "America/Lima",
  UY: "America/Montevideo",
};

/** The zone of the shop's country, or undefined when the country is unknown or has several zones. */
export function shopZoneFor(country: string | null | undefined): string | undefined {
  if (country === null || country === undefined) return undefined;
  return SHOP_COUNTRY_ZONES[country.trim().toUpperCase()];
}

/**
 * A moment as the SHOP reads it: in the zone of the shop's country, with the country named in brackets
 * ("(Израиль)"). A shop with no country (or one with several zones) falls back to the browser's zone.
 */
export function formatShopTime(iso: string | null | undefined, country: string | null | undefined, locale: string, browser: string | undefined = browserZone()): string {
  const zone = shopZoneFor(country);
  if (zone === undefined) return formatDateTime(iso, browser, locale);
  return formatDateTime(iso, zone, locale, regionName(country, locale));
}

// ─── Courier text ────────────────────────────────────────────────────────────

/** The label of a line: the product, the variant in brackets unless it is the single technical one. */
export function itemLabel(it: { product_title: string; variant_title?: string | null }): string {
  return isDefaultTitle(it.variant_title) ? it.product_title : `${it.product_title} (${(it.variant_title ?? "").trim()})`;
}

export interface CourierLabels {
  /** "Order №1042" */
  order: string;
  /** "Buyer's comment: ..." for a note */
  comment: (note: string) => string;
}

/** A map search for an address (street, city, postal code, country; entrance and floor stay out of it). */
export function mapLinks(query: string): { google: string; waze: string } {
  const q = encodeURIComponent(query);
  // TODO geocode: with coordinates on the order, link to "...&query=<lat>,<lng>" and "ul?ll=<lat>,<lng>&navigate=yes".
  return { google: `https://www.google.com/maps/search/?api=1&query=${q}`, waze: `https://waze.com/ul?q=${q}&navigate=yes` };
}

/**
 * The delivery details for a courier form, one per line: the order number, then name, phone, street
 * (with the entrance and floor), city, postal code and the country, the Google Maps and Waze links of
 * that address, the goods, the total, and the buyer's comment last. The street comes before the city, as on
 * an envelope. Empty parts are left out; the total carries the currency symbol ("740,00 ₪"). The country
 * is always named; an old order without one gets the shop's own (`shopCountry`).
 */
export function courierText(
  order: Pick<ShopOrder, "buyer" | "ship" | "items" | "total" | "currency" | "delivery_note" | "delivery_kind">,
  opts: { locale: string; shopCountry?: string | null; labels: CourierLabels },
): string {
  const lines: string[] = [opts.labels.order];
  const add = (v: string | null | undefined) => {
    const t = (v ?? "").trim();
    if (t !== "") lines.push(t);
  };
  const a = order.ship;
  const hasAddress = order.delivery_kind !== "none" && order.delivery_kind !== "pickup";
  add(a.name !== undefined && a.name.trim() !== "" ? a.name : order.buyer.name);
  add(order.buyer.phone);
  if (hasAddress) {
    const clean = (v: string | null | undefined) => (v ?? "").trim();
    const street = [a.address1, a.address2].map(clean).filter((part) => part !== "").join(", ");
    add(street);
    add(a.city);
    add(a.postal);
    const code = clean(a.country).toUpperCase() || clean(opts.shopCountry).toUpperCase();
    add(regionName(code, opts.locale));
    if (clean(a.address1) !== "" || clean(a.city) !== "") {
      // The map query leaves the entrance and floor out: they only confuse a geocoder.
      const query = [a.address1, a.city, a.postal, regionName(code, "en")].map(clean).filter((part) => part !== "").join(", ");
      const links = mapLinks(query);
      lines.push(`🗺 Google Maps: ${links.google}`, `🚗 Waze: ${links.waze}`);
    }
  }
  for (const it of order.items) add(`${itemLabel(it)} × ${it.qty}`);
  add(formatMoneySymbol(order.total, order.currency, opts.locale));
  if (order.delivery_note.trim() !== "") add(opts.labels.comment(order.delivery_note.trim()));
  return lines.join("\n");
}

// ─── Product list: search and sort ───────────────────────────────────────────

export const PRODUCT_SORTS = ["title", "status", "stock"] as const;
export type ProductSort = (typeof PRODUCT_SORTS)[number];

export function isProductSort(value: string): value is ProductSort {
  return (PRODUCT_SORTS as readonly string[]).includes(value);
}

const STATUS_ORDER: Record<string, number> = { active: 0, draft: 1, archived: 2 };

/**
 * Products whose title holds the typed words (any case, any order, accents ignored), sorted by name,
 * by status (on sale, draft, archived) or by stock (least first, products that do not count stock last).
 * Ties keep the name order.
 */
export function filterAndSortProducts<P extends { title: string; status: string; variants: readonly { stock: number; track_stock: boolean }[] }>(
  products: readonly P[],
  query: string,
  sort: ProductSort,
  locale: string,
): P[] {
  const fold = (v: string) => v.normalize("NFD").replace(/[\u0300-\u036f]/g, "").toLocaleLowerCase(locale);
  const words = fold(query).split(/\s+/).filter((w) => w !== "");
  const kept = products.filter((p) => {
    const title = fold(p.title);
    return words.every((w) => title.includes(w));
  });
  const byName = (a: P, b: P) => a.title.localeCompare(b.title, locale, { sensitivity: "base", numeric: true });
  const cmp = (a: P, b: P): number => {
    if (sort === "status") return (STATUS_ORDER[a.status] ?? 9) - (STATUS_ORDER[b.status] ?? 9);
    if (sort === "stock") {
      const sa = stockSum(a);
      const sb = stockSum(b);
      if (sa === sb) return 0;
      if (sa === null) return 1;
      if (sb === null) return -1;
      return sa - sb;
    }
    return 0;
  };
  return [...kept].sort((a, b) => cmp(a, b) || byName(a, b));
}

// ─── Product form: which fields are wrong ────────────────────────────────────

export type InvalidField = "title" | "body" | "price" | "compareAt" | "stock" | "sku" | "variantName" | "variants";

/**
 * The fields that failed, once, in the order they stand on the page: the product's name and description,
 * then what the variants check found. The form names them next to its button.
 */
export function invalidFields(details: { title: boolean; body: boolean }, check: VariantsCheck | null): InvalidField[] {
  const found = new Set<InvalidField>();
  const add = (f: InvalidField) => found.add(f);
  if (details.title) add("title");
  if (details.body) add("body");
  if (check !== null) {
    if (check.list !== null) add("variants");
    for (const errors of Object.values(check.rows)) {
      if (errors.title || errors.optionTooLong || errors.duplicate) add("variantName");
      if (errors.sku) add("sku");
      if (errors.price) add("price");
      if (errors.compareAt || errors.compareBelowPrice) add("compareAt");
      if (errors.stock) add("stock");
    }
  }
  return PAGE_ORDER.filter((f) => found.has(f));
}

/** Where the fields stand on the page, top to bottom. */
const PAGE_ORDER: readonly InvalidField[] = ["title", "body", "variants", "variantName", "sku", "price", "compareAt", "stock"];

// ─── Addresses ───────────────────────────────────────────────────────────────

export interface ShopProductsSearch {
  status?: ProductStatus;
  /** The title of the product that was just deleted, for one line of confirmation. */
  deleted?: string;
}

export function parseProductsSearch(raw: Record<string, unknown>): ShopProductsSearch {
  const out: ShopProductsSearch = {};
  if (typeof raw.status === "string" && isProductStatus(raw.status)) out.status = raw.status;
  if (typeof raw.deleted === "string" && raw.deleted.trim() !== "") out.deleted = raw.deleted.slice(0, SHOP_LIMITS.title);
  return out;
}

export interface ShopProductCardSearch {
  created?: true;
}

export function parseProductCardSearch(raw: Record<string, unknown>): ShopProductCardSearch {
  return raw.created === true || raw.created === "1" || raw.created === 1 || raw.created === "true" ? { created: true } : {};
}

/** The time zone of this browser: a shop has no venue, so its times are shown in the zone of the person reading. */
export function browserZone(): string | undefined {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone;
  } catch {
    return undefined;
  }
}

// ─── Stock edited from the products list (A-07, A-15) ────────────────────────

const MAX_STOCK = 2_147_483_647;

/** The stock a person typed: a whole number, 0 or more. Null for anything else. */
export function parseStockInput(text: string): number | null {
  const v = text.trim();
  if (!/^\d+$/.test(v)) return null;
  const n = Number(v);
  return Number.isSafeInteger(n) && n <= MAX_STOCK ? n : null;
}

/** The variants of a product that count their stock: only these have a number to edit. */
export function trackedVariants<V extends { track_stock: boolean }>(p: { variants: readonly V[] }): V[] {
  return p.variants.filter((v) => v.track_stock);
}

/**
 * How the list edits the stock of a product (the whole point is a phone, with no card to open):
 * "single" is one variant that counts stock, edited in the row. "several" is a total with the variants
 * under a disclosure. "none" counts nothing, so there is nothing to edit.
 */
export function stockEditMode(p: { kind?: string; variants: readonly { track_stock: boolean }[] }): "single" | "several" | "none" {
  if (productKind(p) === "digital") return "none";
  const tracked = trackedVariants(p);
  if (tracked.length === 0) return "none";
  return p.variants.length === 1 ? "single" : "several";
}

/**
 * The body of PUT .../variants that changes ONE variant's stock and leaves the rest as the product has it.
 * The route replaces the whole list (a variant left out is deleted), so every variant goes back, each with
 * the stock the person saw (`expected_stock`): the server refuses the write when the shelf moved meanwhile.
 * Null when the product has no such variant or it does not count stock.
 */
export function stockUpdateRequest(product: ShopProduct, variantId: string, stock: number, currency: string): ShopVariantSend[] | null {
  const target = product.variants.find((v) => v.id === variantId);
  if (target === undefined || !target.track_stock) return null;
  const rows = rowsFromProduct(product, currency).map((r) => (r.id === variantId ? { ...r, stock: String(stock) } : r));
  return variantsRequest(rows, currency, optionCountOf(optionNamesOf(product), rows));
}

/** What a variant is called in the stock list: its name, its options, or "the only version". */
export function variantLabel(v: { title?: string | null; option1?: string | null; option2?: string | null; option3?: string | null }, only: string): string {
  const options = [v.option1, v.option2, v.option3].filter((o): o is string => !isDefaultTitle(o)).join(" / ");
  if (options !== "") return options;
  return isDefaultTitle(v.title) ? only : (v.title as string);
}
