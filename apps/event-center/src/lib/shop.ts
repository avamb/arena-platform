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
  ShopProduct,
  ShopSettings,
  ShopSettingsUpdateRequest,
  ShopVariant,
  ShopVariantInput,
} from "@/lib/api/types";
import { currencyFractionDigits } from "@/lib/format";
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
  /** The stock the person saw when the screen loaded: sent back as `expected_stock`. */
  loadedStock?: number;
}

let rowSeq = 0;
function nextKey(): string {
  rowSeq += 1;
  return `v${rowSeq}`;
}

export function emptyRow(): VariantRow {
  return { key: nextKey(), title: "", options: ["", "", ""], sku: "", price: "", compareAt: "", stock: "0", trackStock: true };
}

const DEFAULT_TITLE = "Default Title";

export function rowFromVariant(v: ShopVariant, currency: string): VariantRow {
  return {
    key: nextKey(),
    id: v.id,
    title: v.title === DEFAULT_TITLE ? "" : v.title,
    options: [v.option1 ?? "", v.option2 ?? "", v.option3 ?? ""],
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
export function checkVariants(rows: readonly VariantRow[], currency: string, optionCount: number): VariantsCheck {
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
    if (r.trackStock && !/^\d+$/.test(r.stock.trim())) mark(r.key, "stock");
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
export function variantsRequest(rows: readonly VariantRow[], currency: string, optionCount: number): ShopVariantSend[] {
  return rows.map((r, i) => {
    const out: ShopVariantSend = {
      title: rowTitle(r, optionCount),
      sku: r.sku.trim() === "" ? null : r.sku.trim(),
      price_minor: parseMoney(r.price, currency) ?? 0,
      compare_at_minor: r.compareAt.trim() === "" ? null : parseMoney(r.compareAt, currency),
      stock: r.trackStock ? Number(r.stock.trim()) : (r.loadedStock ?? 0),
      track_stock: r.trackStock,
      position: i,
      option1: optionCount >= 1 && r.options[0].trim() !== "" ? r.options[0].trim() : null,
      option2: optionCount >= 2 && r.options[1].trim() !== "" ? r.options[1].trim() : null,
      option3: optionCount >= 3 && r.options[2].trim() !== "" ? r.options[2].trim() : null,
    };
    if (r.id !== undefined) {
      out.id = r.id;
      // The stock the person saw: the server refuses the save when the shelf moved meanwhile.
      if (r.loadedStock !== undefined) out.expected_stock = r.loadedStock;
    }
    return out;
  });
}

/** True when the API refused a variants save because the stock moved since the screen loaded. */
export function isStockChanged(err: unknown): boolean {
  return err instanceof ApiError && err.code === "shop.stock_changed";
}

function variantsSignature(rows: readonly VariantRow[], currency: string, optionCount: number): string {
  return JSON.stringify(variantsRequest(rows, currency, optionCount).map((v) => ({ ...v, position: undefined, expected_stock: undefined })));
}

/** True when the rows differ from the saved variants (so «Save» is worth pressing). */
export function variantsChanged(rows: readonly VariantRow[], saved: readonly ShopVariant[], currency: string, optionCount: number): boolean {
  const was = saved.map((v) => rowFromVariant(v, currency));
  return variantsSignature(rows, currency, optionCount) !== variantsSignature(was, currency, optionCount);
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

/** "Mark shipped" exists for a paid order (also partly refunded, or parked for a check). */
export function canShip(status: string, canManage: boolean): boolean {
  return canManage && (status === "paid" || status === "partially_refunded" || status === "manual_review");
}

/** A shipped order can still get its tracking note changed. */
export function canEditTracking(status: string, canManage: boolean): boolean {
  return canManage && status === "shipped";
}

/** Cancel exists for an order that still waits for its payment, and only that. */
export function canCancelShopOrder(status: string, canManage: boolean): boolean {
  return canManage && status === "pending_payment";
}

/** What can still go back to the buyer, minor units. */
export function refundable(order: Pick<ShopOrder, "total" | "refunded_minor">): number {
  return Math.max(0, order.total - order.refunded_minor);
}

/** Refund exists when money was taken through arena and some of it is left. */
export function canRefundShopOrder(order: Pick<ShopOrder, "status" | "total" | "refunded_minor" | "payment">, canManage: boolean): boolean {
  if (!canManage || order.payment === null || order.payment === undefined) return false;
  if (!["paid", "shipped", "partially_refunded", "manual_review"].includes(order.status)) return false;
  return refundable(order) > 0;
}

export type RefundCheck =
  | { ok: true; amountMinor: number | null; full: boolean }
  | { ok: false; problem: "amount" | "tooMuch" | "reasonTooLong" };

/** A blank amount refunds what is left. Otherwise 1 to what is left. */
export function checkRefund(order: Pick<ShopOrder, "total" | "refunded_minor" | "currency">, amountText: string, reason: string): RefundCheck {
  if ([...reason.trim()].length > SHOP_LIMITS.refundReason) return { ok: false, problem: "reasonTooLong" };
  const left = refundable(order);
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
  kind: "created" | "paid" | "shipped" | "cancelled" | "refunded" | "expires";
  at: string | null;
}

/** The moments of an order, oldest first. A refund has no time of its own: it is listed last, with its sum. */
export function orderTimeline(o: Pick<ShopOrder, "created_at" | "paid_at" | "shipped_at" | "cancelled_at" | "expires_at" | "refunded_minor" | "status" | "updated_at">): TimelineEntry[] {
  const list: TimelineEntry[] = [{ kind: "created", at: o.created_at }];
  if (o.paid_at) list.push({ kind: "paid", at: o.paid_at });
  if (o.shipped_at) list.push({ kind: "shipped", at: o.shipped_at });
  if (o.cancelled_at) list.push({ kind: "cancelled", at: o.cancelled_at });
  if (o.status === "pending_payment" && o.expires_at) list.push({ kind: "expires", at: o.expires_at });
  list.sort((a, b) => Date.parse(a.at ?? "") - Date.parse(b.at ?? ""));
  if (o.refunded_minor > 0) list.push({ kind: "refunded", at: o.updated_at });
  return list;
}

export interface ShopOrdersFilters {
  status: ShopOrderStatus | "";
  q: string;
  /** YYYY-MM-DD, inclusive. */
  from: string;
  /** YYYY-MM-DD, inclusive. */
  to: string;
}

export const EMPTY_FILTERS: ShopOrdersFilters = { status: "", q: "", from: "", to: "" };

const DAY = /^\d{4}-\d{2}-\d{2}$/;

/** The day after a YYYY-MM-DD, as YYYY-MM-DD: the API's `to` is "before this instant". */
export function nextDay(day: string): string {
  const d = new Date(`${day}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() + 1);
  return d.toISOString().slice(0, 10);
}

/** The query of GET .../shop/orders. A malformed day is left out, not sent. */
export function ordersQuery(f: ShopOrdersFilters, limit: number, offset: number): Record<string, string | number | undefined> {
  return {
    status: f.status === "" ? undefined : f.status,
    q: f.q.trim() === "" ? undefined : f.q.trim(),
    from: DAY.test(f.from) ? f.from : undefined,
    to: DAY.test(f.to) ? nextDay(f.to) : undefined,
    limit,
    offset: offset > 0 ? offset : undefined,
  };
}

export interface ShopOrdersSearch {
  status?: ShopOrderStatus;
}

/** Only the status goes into the address: a buyer's e-mail or name never does. */
export function parseShopOrdersSearch(raw: Record<string, unknown>): ShopOrdersSearch {
  return typeof raw.status === "string" && isShopOrderStatus(raw.status) ? { status: raw.status } : {};
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
  | "shippingFee"
  | "shippingLabel"
  | "country"
  | "ageGate"
  | "announcement"
  | "contactEmail"
  | "logisticsNote"
  | "maxLineQty";

export function checkSettings(d: SettingsDraft): Partial<Record<SettingsProblem, true>> {
  const bad: Partial<Record<SettingsProblem, true>> = {};
  const cur = d.currency.trim().toUpperCase();
  if (!/^[A-Z]{3}$/.test(cur)) bad.currency = true;
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
export function settingsRequest(d: SettingsDraft): ShopSettingsUpdateRequest {
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
  return body;
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
