/**
 * The rules of the shop screens: money, the variants editor (the stock the person saw
 * goes back as `expected_stock`), the pictures' order, refunds, the orders query and the
 * settings form.
 */
import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/http";
import { CATALOGS, LOCALES, type MessageKey } from "@/lib/i18n";
import type { ShopImage, ShopProduct, ShopSettings, ShopVariant } from "@/lib/api/types";
import {
  EMPTY_FILTERS,
  canCancelShopOrder,
  canEditTracking,
  isShipped,
  orderStatusLabelKey,
  paymentRefundKind,
  canRefundShopOrder,
  canShip,
  checkRefund,
  checkSettings,
  checkVariants,
  emptyRow,
  kindTracksStock,
  shelfEmpty,
  isStockChanged,
  moneyInput,
  moveImagePlan,
  nextDay,
  oversold,
  ordersQuery,
  orderTimeline,
  parseMoney,
  parseProductsSearch,
  parseShopOrdersSearch,
  priceRange,
  refundBody,
  refundOutcome,
  refundable,
  rowFromVariant,
  rowsFromProduct,
  settingsDraft,
  settingsRequest,
  stockChangedDetails,
  stockSum,
  variantsChanged,
  variantsRequest,
  type VariantRow,
} from "@/lib/shop";

function variant(over: Partial<ShopVariant> = {}): ShopVariant {
  return { id: "v1", title: "Red / L", sku: "RED-L", price_minor: 1250, compare_at_minor: null, stock: 7, track_stock: true, held: 2, available: 5, position: 0, option1: "Red", option2: "L", option3: null, ...over };
}

describe("money", () => {
  it("reads a major amount into minor units by the currency's own digits", () => {
    expect(parseMoney("12.50", "EUR")).toBe(1250);
    expect(parseMoney("12,5", "EUR")).toBe(1250);
    expect(parseMoney("12", "EUR")).toBe(1200);
    expect(parseMoney("500", "JPY")).toBe(500);
  });
  it("refuses what is not an amount, or has too many decimals", () => {
    expect(parseMoney("", "EUR")).toBeNull();
    expect(parseMoney("-1", "EUR")).toBeNull();
    expect(parseMoney("12.345", "EUR")).toBeNull();
    expect(parseMoney("1.5", "JPY")).toBeNull();
    expect(parseMoney("abc", "EUR")).toBeNull();
  });
  it("shows minor units as a major amount", () => {
    expect(moneyInput(1250, "EUR")).toBe("12.50");
    expect(moneyInput(5, "EUR")).toBe("0.05");
    expect(moneyInput(500, "JPY")).toBe("500");
  });
});

describe("the product list", () => {
  const p = { variants: [variant({ price_minor: 900, stock: 3 }), variant({ id: "v2", price_minor: 1500, stock: 4 }), variant({ id: "v3", track_stock: false, stock: 99 })] };
  it("gives the price range and the stock of the variants that count it", () => {
    expect(priceRange(p)).toEqual({ min: 900, max: 1500 });
    expect(stockSum(p)).toBe(7);
    expect(priceRange({ variants: [] })).toBeNull();
    expect(stockSum({ variants: [variant({ track_stock: false })] })).toBeNull();
  });
  it("marks an oversell", () => {
    expect(oversold({ variants: [variant({ stock: -1 })] })).toBe(true);
    expect(oversold(p)).toBe(false);
  });
});

describe("the variants editor", () => {
  const product = { variants: [variant(), variant({ id: "v2", title: "Blue / L", option1: "Blue", price_minor: 1300, stock: 0 })] } as ShopProduct;

  it("sends back the stock the person saw as expected_stock", () => {
    const rows = rowsFromProduct(product, "EUR");
    rows[0] = { ...rows[0], stock: "10" }; // the person changed 7 to 10
    const body = variantsRequest(rows, "EUR", 2);
    expect(body[0]).toMatchObject({ id: "v1", stock: 10, expected_stock: 7, price_minor: 1250, option1: "Red", option2: "L", position: 0 });
    expect(body[1]).toMatchObject({ id: "v2", stock: 0, expected_stock: 0, position: 1 });
  });

  it("sends no id and no expected_stock for a variant the person added", () => {
    const added: VariantRow = { ...emptyRow(), options: ["Green", "M", ""], price: "9.90", stock: "4" };
    const body = variantsRequest([...rowsFromProduct(product, "EUR"), added], "EUR", 2);
    expect(body[2].id).toBeUndefined();
    expect(body[2].expected_stock).toBeUndefined();
    expect(body[2]).toMatchObject({ title: "Green / M", price_minor: 990, stock: 4, track_stock: true });
  });

  it("keeps the saved stock of a variant that does not count it", () => {
    const rows = [{ ...rowFromVariant(variant({ stock: 12 }), "EUR"), trackStock: false }];
    expect(variantsRequest(rows, "EUR", 2)[0]).toMatchObject({ track_stock: false, stock: 12, expected_stock: 12 });
  });

  it("names a one-version product with an empty title and no options", () => {
    const row = { ...emptyRow(), price: "5" };
    expect(variantsRequest([row], "EUR", 0)[0]).toMatchObject({ title: "", option1: null, option2: null, option3: null });
    expect(rowFromVariant(variant({ title: "Default Title", option1: null, option2: null }), "EUR").title).toBe("");
  });

  it("finds what is wrong with each row", () => {
    const a: VariantRow = { ...emptyRow(), title: "A", price: "", stock: "x" };
    const b: VariantRow = { ...emptyRow(), title: "a", price: "10", compareAt: "5", stock: "1" };
    const c = checkVariants([a, b], "EUR", 0);
    expect(c.ok).toBe(false);
    expect(c.rows[a.key]).toMatchObject({ price: true, stock: true, duplicate: true });
    expect(c.rows[b.key]).toMatchObject({ compareBelowPrice: true, duplicate: true });
    expect(checkVariants([], "EUR", 0).list).toBe("empty");
    const ok: VariantRow = { ...emptyRow(), price: "10", stock: "0" };
    expect(checkVariants([ok], "EUR", 0).ok).toBe(true);
  });

  it("asks for a name when there are several variants and no options", () => {
    const x: VariantRow = { ...emptyRow(), price: "1" };
    const y: VariantRow = { ...emptyRow(), price: "2" };
    expect(checkVariants([x, y], "EUR", 0).rows[x.key]).toMatchObject({ title: true });
  });

  it("knows when nothing changed", () => {
    const rows = rowsFromProduct(product, "EUR");
    expect(variantsChanged(rows, product.variants, "EUR", 2)).toBe(false);
    expect(variantsChanged([{ ...rows[0], price: "13.00" }, rows[1]], product.variants, "EUR", 2)).toBe(true);
    expect(variantsChanged([rows[1], rows[0]], product.variants, "EUR", 2)).toBe(true);
    expect(variantsChanged([rows[0]], product.variants, "EUR", 2)).toBe(true);
  });

  it("sends a digital product's variant with no stock and no expected_stock", () => {
    const rows = rowsFromProduct(product, "EUR");
    const body = variantsRequest(rows, "EUR", 2, { digital: true });
    expect(body[0]).toMatchObject({ id: "v1", stock: 0, track_stock: false });
    expect(body[0].expected_stock).toBeUndefined();
    const bad = { ...rows[0], stock: "x" };
    expect(checkVariants([bad], "EUR", 2, { digital: true }).ok).toBe(true);
    expect(checkVariants([bad], "EUR", 2).ok).toBe(false);
  });

  it("reads the current stock out of the details of a refused write", () => {
    const err = new ApiError(409, "shop.stock_changed", "moved", undefined, { variants: [{ id: "v1", expected_stock: 7, stock: 4 }, { bad: true }] });
    expect(stockChangedDetails(err)).toEqual([{ id: "v1", expected: 7, stock: 4 }]);
    expect(stockChangedDetails(new ApiError(409, "other", "x"))).toEqual([]);
    expect(stockChangedDetails(new ApiError(409, "shop.stock_changed", "x"))).toEqual([]);
  });

  it("recognizes the stock conflict of the API", () => {
    expect(isStockChanged(new ApiError(409, "shop.stock_changed", "x"))).toBe(true);
    expect(isStockChanged(new ApiError(409, "shop.duplicate_slug", "x"))).toBe(false);
    expect(isStockChanged(new Error("x"))).toBe(false);
  });
});

describe("the pictures", () => {
  const im = (id: string, position: number): ShopImage => ({ media_id: id, url: "", position, alt: `alt ${id}` });
  it("re-attaches every picture with its new place and its alt kept", () => {
    const plan = moveImagePlan([im("a", 0), im("b", 1), im("c", 2)], 2, -1);
    expect(plan).toEqual([
      { media_id: "a", position: 0, alt: "alt a" },
      { media_id: "c", position: 1, alt: "alt c" },
      { media_id: "b", position: 2, alt: "alt b" },
    ]);
  });
  it("does not move past the ends", () => {
    expect(moveImagePlan([im("a", 0), im("b", 1)], 0, -1)).toBeNull();
    expect(moveImagePlan([im("a", 0), im("b", 1)], 1, 1)).toBeNull();
  });
});

describe("the order actions", () => {
  it("offers each action only where it works", () => {
    expect(canShip("paid", true)).toBe(true);
    expect(canShip("manual_review", true)).toBe(true);
    expect(canShip("pending_payment", true)).toBe(false);
    expect(canShip("paid", false)).toBe(false);
    expect(canCancelShopOrder("pending_payment", true)).toBe(true);
    expect(canCancelShopOrder("paid", true)).toBe(false);
  });
  const order = { status: "paid", total: 5000, refunded_minor: 1000, currency: "EUR", payment: { id: "p", provider: "stripe", state: "succeeded", amount: 5000, currency: "EUR" } } as const;
  it("refunds only paid money that is left", () => {
    expect(refundable(order)).toBe(4000);
    expect(canRefundShopOrder(order, true)).toBe(true);
    expect(canRefundShopOrder({ ...order, refunded_minor: 5000 }, true)).toBe(false);
    expect(canRefundShopOrder({ ...order, payment: null }, true)).toBe(false);
    expect(canRefundShopOrder({ ...order, status: "pending_payment" }, true)).toBe(false);
    expect(canRefundShopOrder(order, false)).toBe(false);
  });
  it("refunds everything that is left by default", () => {
    const c = checkRefund(order, "", "");
    expect(c).toEqual({ ok: true, amountMinor: null, full: true });
    expect(refundBody(c as Extract<typeof c, { ok: true }>, "")).toEqual({});
  });
  it("takes a part, and refuses an amount that is not between 1 and what is left", () => {
    const part = checkRefund(order, "12.50", " broken ");
    expect(part).toEqual({ ok: true, amountMinor: 1250, full: false });
    expect(refundBody(part as Extract<typeof part, { ok: true }>, " broken ")).toEqual({ amount_minor: 1250, reason: "broken" });
    expect(checkRefund(order, "40", "")).toEqual({ ok: true, amountMinor: 4000, full: true });
    expect(checkRefund(order, "40.01", "")).toEqual({ ok: false, problem: "tooMuch" });
    expect(checkRefund(order, "0", "")).toEqual({ ok: false, problem: "amount" });
    expect(checkRefund(order, "abc", "")).toEqual({ ok: false, problem: "amount" });
    expect(checkRefund(order, "", "x".repeat(2001))).toEqual({ ok: false, problem: "reasonTooLong" });
  });
  it("tells what became of the refund", () => {
    expect(refundOutcome("succeeded")).toBe("done");
    expect(refundOutcome("provider_pending")).toBe("pending");
    expect(refundOutcome("manual_review")).toBe("review");
    expect(refundOutcome("failed")).toBe("failed");
  });
  it("lists the moments of an order oldest first, with the refund last", () => {
    const tl = orderTimeline({
      created_at: "2026-10-01T10:00:00Z",
      paid_at: "2026-10-01T10:05:00Z",
      shipped_at: "2026-10-02T09:00:00Z",
      cancelled_at: null,
      expires_at: null,
      refunded_minor: 500,
      status: "partially_refunded",
      updated_at: "2026-10-03T09:00:00Z",
    });
    expect(tl.map((e) => e.kind)).toEqual(["created", "paid", "shipped", "refunded"]);
  });
});

describe("the orders query", () => {
  it("sends only what is filled, and the day after the last day", () => {
    expect(ordersQuery({ ...EMPTY_FILTERS, status: "paid", q: " anna ", from: "2026-10-01", to: "2026-10-31" }, 50, 0)).toEqual({
      status: "paid",
      q: "anna",
      from: "2026-10-01",
      to: "2026-11-01",
      limit: 50,
      offset: undefined,
    });
    expect(ordersQuery(EMPTY_FILTERS, 50, 100)).toMatchObject({ status: undefined, q: undefined, from: undefined, to: undefined, offset: 100 });
  });
  it("leaves out a day that is not a day", () => {
    expect(ordersQuery({ ...EMPTY_FILTERS, from: "yesterday", to: "2026-13" }, 50, 0)).toMatchObject({ from: undefined, to: undefined });
  });
  it("counts the day after across a month and a year", () => {
    expect(nextDay("2026-02-28")).toBe("2026-03-01");
    expect(nextDay("2026-12-31")).toBe("2027-01-01");
  });
  it("keeps only the status in the address, never the search text", () => {
    expect(parseShopOrdersSearch({ status: "manual_review", q: "anna@example.com" })).toEqual({ status: "manual_review" });
    expect(parseShopOrdersSearch({ status: "nonsense" })).toEqual({});
    expect(parseProductsSearch({ status: "draft", deleted: "Tote bag" })).toEqual({ status: "draft", deleted: "Tote bag" });
    expect(parseProductsSearch({ status: "x" })).toEqual({});
  });
});

describe("the settings form", () => {
  const settings: ShopSettings = {
    org_id: "o",
    configured: true,
    currency: "EUR",
    shipping_fee_minor: 490,
    shipping_label: "Courier",
    country_iso2: "ES",
    age_gate_min_age: 18,
    announcement_text: "Free delivery over 50",
    contact_email: "shop@example.com",
    logistics_note: "Call first",
    sales_channel_id: "c1",
    shop_domain: "shop.example.com",
    max_line_qty: 24,
    enabled: true,
    updated_at: "2026-10-01T10:00:00Z",
  };

  it("fills the form from the stored row, money as a major amount", () => {
    expect(settingsDraft(settings)).toMatchObject({ currency: "EUR", shippingFee: "4.90", country: "ES", ageGate: "18", salesChannelId: "c1", contactEmail: "shop@example.com" });
  });

  it("sends the whole row, and never the domain or the open flag", () => {
    const body = settingsRequest({ ...settingsDraft(settings), currency: "ils", shippingFee: "12,5", ageGate: "", country: "", contactEmail: "", salesChannelId: "" });
    expect(body).toEqual({
      currency: "ILS",
      shipping_fee_minor: 1250,
      shipping_label: "Courier",
      country_iso2: null,
      age_gate_min_age: null,
      announcement_text: "Free delivery over 50",
      contact_email: null,
      logistics_note: "Call first",
      sales_channel_id: null,
      max_line_qty: 24,
    });
    expect(body).not.toHaveProperty("shop_domain");
    expect(body).not.toHaveProperty("enabled");
  });

  it("names what does not fit", () => {
    const d = { ...settingsDraft(settings), currency: "EU", ageGate: "120", contactEmail: "no-at-sign", country: "Spain", announcement: "x".repeat(501) };
    expect(Object.keys(checkSettings(d)).sort()).toEqual(["ageGate", "announcement", "contactEmail", "country", "currency"]);
    expect(checkSettings({ ...settingsDraft(settings), maxLineQty: "100" })).toEqual({ maxLineQty: true });
    expect(checkSettings({ ...settingsDraft(settings), shippingFee: "free" })).toEqual({ shippingFee: true });
    expect(checkSettings(settingsDraft(settings))).toEqual({});
  });
});

describe("the money-cycle run: stock, delivery and history (M-01, M-03, M-04)", () => {
  const v = (stock: number, track_stock = true) => ({ stock, track_stock });
  it("a new row counts stock only when asked: a file or a service has no shelf", () => {
    expect(emptyRow().trackStock).toBe(true);
    expect(emptyRow(false).trackStock).toBe(false);
    expect(kindTracksStock("physical")).toBe(true);
    expect(kindTracksStock("digital")).toBe(false);
    expect(kindTracksStock("service")).toBe(false);
  });
  it("sends the tracking flag explicitly, off for a service row", () => {
    const row = { ...emptyRow(false), price: "20" };
    const [sent] = variantsRequest([row], "EUR", 0);
    expect(sent.track_stock).toBe(false);
  });
  it("knows the shelf is empty: every variant counts and none has a unit", () => {
    expect(shelfEmpty({ kind: "physical", variants: [v(0)] })).toBe(true);
    expect(shelfEmpty({ kind: "service", variants: [v(0), v(-1)] })).toBe(true);
    expect(shelfEmpty({ kind: "physical", variants: [v(0), v(3)] })).toBe(false);
    expect(shelfEmpty({ kind: "physical", variants: [v(0), v(0, false)] })).toBe(false);
    expect(shelfEmpty({ kind: "physical", variants: [v(5, false)] })).toBe(false);
    expect(shelfEmpty({ kind: "digital", variants: [v(0)] })).toBe(false);
    expect(shelfEmpty({ kind: "physical", variants: [] })).toBe(false);
  });
  it("offers no shipping for an order without delivery", () => {
    expect(canShip("paid", true, "none")).toBe(false);
    expect(canShip("paid", true, "shipping")).toBe(true);
    expect(canShip("paid", true, "pickup")).toBe(true);
    expect(canShip("paid", true)).toBe(true);
  });
  it("keeps the payment deadline line after it passed, as 'not paid in time'", () => {
    const base = { created_at: "2026-10-29T10:00:00Z", paid_at: null, shipped_at: null, cancelled_at: null, expires_at: "2026-10-29T16:16:00Z", refunded_minor: 0, updated_at: "2026-10-29T16:20:00Z" };
    expect(orderTimeline({ ...base, status: "pending_payment" }).map((e) => e.kind)).toEqual(["created", "expires"]);
    const expired = orderTimeline({ ...base, status: "expired" });
    expect(expired.map((e) => e.kind)).toEqual(["created", "expired"]);
    expect(expired[1].at).toBe("2026-10-29T16:16:00Z");
    expect(orderTimeline({ ...base, status: "expired", expires_at: null }).map((e) => e.kind)).toEqual(["created"]);
  });
});

describe("shipped-ness survives a refund (M-10)", () => {
  const SHIPPED = "2026-10-29T10:00:00Z";
  const statuses = ["paid", "shipped", "partially_refunded", "refunded", "manual_review", "pending_payment", "cancelled", "expired"];

  it("never offers 'mark shipped' again once shipped_at is set, whatever the status", () => {
    for (const status of statuses) expect(canShip(status, true, "shipping", SHIPPED), status).toBe(false);
  });

  it("still offers it to a paid, a partly refunded and a parked order that was not shipped", () => {
    for (const status of ["paid", "partially_refunded", "manual_review"]) {
      expect(canShip(status, true, "shipping", null), status).toBe(true);
      expect(canShip(status, true, "shipping", undefined), status).toBe(true);
    }
    for (const status of ["pending_payment", "cancelled", "expired", "refunded", "shipped"]) expect(canShip(status, true, "shipping", null), status).toBe(false);
  });

  it("lets a shipped order keep its tracking note, also after a partial refund, but not after a full one", () => {
    expect(canEditTracking("shipped", true, SHIPPED)).toBe(true);
    expect(canEditTracking("partially_refunded", true, SHIPPED)).toBe(true);
    expect(canEditTracking("partially_refunded", true, null)).toBe(false);
    expect(canEditTracking("refunded", true, SHIPPED)).toBe(false);
    expect(canEditTracking("shipped", false, SHIPPED)).toBe(false);
  });

  it("knows an order is shipped from the status or from shipped_at", () => {
    expect(isShipped({ status: "shipped" })).toBe(true);
    expect(isShipped({ status: "partially_refunded", shipped_at: SHIPPED })).toBe(true);
    expect(isShipped({ status: "refunded", shipped_at: SHIPPED })).toBe(true);
    expect(isShipped({ status: "partially_refunded", shipped_at: null })).toBe(false);
    expect(isShipped({ status: "paid" })).toBe(false);
  });

  it("names both facts on the badge of a refunded order that had been shipped", () => {
    expect(orderStatusLabelKey("partially_refunded", SHIPPED)).toBe("shop.order.status.shipped_partially_refunded");
    expect(orderStatusLabelKey("refunded", SHIPPED)).toBe("shop.order.status.shipped_refunded");
    expect(orderStatusLabelKey("partially_refunded", null)).toBe("shop.order.status.partially_refunded");
    expect(orderStatusLabelKey("refunded")).toBe("shop.order.status.refunded");
    expect(orderStatusLabelKey("shipped", SHIPPED)).toBe("shop.order.status.shipped");
    expect(orderStatusLabelKey("paid", null)).toBe("shop.order.status.paid");
    expect(orderStatusLabelKey("mystery", null)).toBeNull();
  });

  it("has a badge text for every combination in all three locales", () => {
    for (const locale of LOCALES) {
      for (const status of statuses) {
        for (const at of [null, SHIPPED]) {
          const key = orderStatusLabelKey(status, at);
          expect(key, `${status} ${at}`).not.toBeNull();
          expect(CATALOGS[locale][key as MessageKey], `${locale} ${key}`).toBeTruthy();
        }
      }
    }
  });
});

describe("the payment block after a refund (M-12)", () => {
  const base = { status: "paid", total: 25000, refunded_minor: 0 };
  it("says nothing special for an order with no refund or whose payment did not succeed", () => {
    expect(paymentRefundKind(base, "succeeded")).toBeNull();
    expect(paymentRefundKind({ ...base, refunded_minor: 100 }, "failed")).toBeNull();
    expect(paymentRefundKind({ ...base, refunded_minor: 100 }, null)).toBeNull();
  });
  it("says 'partly' for a part of the money and 'refunded' for all of it", () => {
    expect(paymentRefundKind({ status: "partially_refunded", total: 74000, refunded_minor: 5000 }, "succeeded")).toBe("partly");
    expect(paymentRefundKind({ status: "refunded", total: 25000, refunded_minor: 25000 }, "succeeded")).toBe("refunded");
    expect(paymentRefundKind({ status: "paid", total: 25000, refunded_minor: 25000 }, "succeeded")).toBe("refunded");
  });
});
