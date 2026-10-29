/** The rules behind the Shopify-parity work (A-02, A-07, A-08, A-10, A-11): quick lists, stock edited in the list, the order note, the repeat letter. */
import { describe, expect, it } from "vitest";
import {
  EMPTY_FILTERS,
  ORDER_TABS,
  STAFF_NOTE_MAX,
  activeOrderTab,
  isOrderView,
  ordersQuery,
  parseShopOrdersSearch,
  parseStockInput,
  resendLetters,
  staffNoteChanged,
  stockEditMode,
  stockUpdateRequest,
  tabCount,
  trackedVariants,
  variantLabel,
  type ShopProductView,
} from "@/lib/shop";

function variant(id: string, stock: number, over: Record<string, unknown> = {}) {
  return { id, title: "", sku: null, price_minor: 2000, compare_at_minor: null, stock, track_stock: true, held: 0, available: stock, position: 0, option1: null, option2: null, option3: null, ...over };
}

function product(variants: ReturnType<typeof variant>[], over: Partial<ShopProductView> = {}): ShopProductView {
  return { id: "p1", org_id: "o", slug: "riesling", title: "Riesling", body_html: "", status: "active", kind: "physical", option_names: [], images: [], variants, created_at: "2026-10-01T10:00:00Z", updated_at: "2026-10-01T10:00:00Z", ...over } as unknown as ShopProductView;
}

describe("the quick lists of the orders (A-08)", () => {
  it("names the five tabs, the first is the whole list", () => {
    expect(ORDER_TABS).toEqual(["all", "to_ship", "unpaid", "shipped", "refunds"]);
    expect(activeOrderTab(undefined)).toBe("all");
    expect(activeOrderTab("")).toBe("all");
    expect(activeOrderTab("to_ship")).toBe("to_ship");
  });

  it("knows only the API's views (the whole list is not one)", () => {
    expect(isOrderView("to_ship")).toBe(true);
    expect(isOrderView("refunds")).toBe(true);
    expect(isOrderView("all")).toBe(false);
    expect(isOrderView("paid")).toBe(false);
    expect(isOrderView(undefined)).toBe(false);
  });

  it("shows a number only on the tabs that carry one, and only when the counts are known", () => {
    const counts = { all: 9, to_ship: 3, unpaid: 2, shipped: 4, refunds: 0 };
    expect(tabCount("to_ship", counts)).toBe(3);
    expect(tabCount("unpaid", counts)).toBe(2);
    expect(tabCount("all", counts)).toBeNull();
    expect(tabCount("shipped", counts)).toBeNull();
    expect(tabCount("refunds", counts)).toBeNull();
    expect(tabCount("to_ship", undefined)).toBeNull();
  });

  it("sends the view with the list request and keeps the status filter working beside it", () => {
    expect(ordersQuery({ ...EMPTY_FILTERS, view: "to_ship" }, 50, 0)).toMatchObject({ view: "to_ship", status: undefined });
    expect(ordersQuery({ ...EMPTY_FILTERS, status: "paid" }, 50, 0)).toMatchObject({ status: "paid", view: undefined });
    expect(ordersQuery({ ...EMPTY_FILTERS, status: "paid", view: "unpaid" }, 50, 0)).toMatchObject({ status: "paid", view: "unpaid" });
  });

  it("keeps the view in the address, and nothing a buyer typed", () => {
    expect(parseShopOrdersSearch({ view: "to_ship", q: "anna@example.com" })).toEqual({ view: "to_ship" });
    expect(parseShopOrdersSearch({ view: "nonsense" })).toEqual({});
    expect(parseShopOrdersSearch({ status: "paid", view: "shipped" })).toEqual({ status: "paid", view: "shipped" });
  });
});

describe("the repeat e-mail (A-11)", () => {
  const paid = { status: "paid", buyer: { name: "A", email: "a@example.com" }, delivery_kind: "shipping", shipped_at: null };

  it("offers the confirmation for a paid order, and the shipping letter too once it shipped", () => {
    expect(resendLetters(paid, true)).toEqual(["received"]);
    expect(resendLetters({ ...paid, status: "shipped", shipped_at: "2026-10-06T09:00:00Z" }, true)).toEqual(["received", "shipped"]);
  });

  it("offers nothing without the right, without an address, or before payment", () => {
    expect(resendLetters(paid, false)).toEqual([]);
    expect(resendLetters({ ...paid, buyer: { name: "A", email: " " } }, true)).toEqual([]);
    for (const status of ["pending_payment", "cancelled", "expired", "manual_review"]) expect(resendLetters({ ...paid, status }, true), status).toEqual([]);
  });

  it("offers the refund letter once any money went back, by status or by amount", () => {
    expect(resendLetters({ ...paid, status: "partially_refunded", refunded_minor: 500 }, true)).toEqual(["received", "refunded"]);
    expect(resendLetters({ ...paid, status: "refunded", refunded_minor: 4490 }, true)).toEqual(["received", "refunded"]);
    expect(resendLetters({ ...paid, refunded_minor: 100 }, true)).toEqual(["received", "refunded"]);
    expect(resendLetters({ ...paid, status: "partially_refunded", refunded_minor: 500, shipped_at: "2026-10-06T09:00:00Z" }, true)).toEqual(["received", "shipped", "refunded"]);
    expect(resendLetters({ ...paid, refunded_minor: 0 }, true)).toEqual(["received"]);
    expect(resendLetters({ ...paid, status: "refunded", refunded_minor: 4490 }, false)).toEqual([]);
  });

  it("never offers a shipping letter for goods that do not ship", () => {
    expect(resendLetters({ ...paid, status: "shipped", delivery_kind: "none", shipped_at: "2026-10-06T09:00:00Z" }, true)).toEqual(["received"]);
  });
});

describe("the internal note (A-10)", () => {
  it("counts a change only when the words differ, not the spaces around them", () => {
    expect(staffNoteChanged("  call after 18:00 ", "call after 18:00")).toBe(false);
    expect(staffNoteChanged("call after 19:00", "call after 18:00")).toBe(true);
    expect(staffNoteChanged("", "something")).toBe(true);
    expect(STAFF_NOTE_MAX).toBe(2000);
  });
});

describe("stock edited in the products list (A-07, A-15)", () => {
  it("takes whole numbers, 0 or more, and nothing else", () => {
    expect(parseStockInput("12")).toBe(12);
    expect(parseStockInput(" 0 ")).toBe(0);
    for (const bad of ["", "-1", "1.5", "1,5", "twelve", "12 bottles", "99999999999"]) expect(parseStockInput(bad), bad).toBeNull();
  });

  it("edits a single variant in the row, several under a disclosure, and nothing when stock is not counted", () => {
    expect(stockEditMode(product([variant("v1", 5)]))).toBe("single");
    expect(stockEditMode(product([variant("v1", 5), variant("v2", 0)]))).toBe("several");
    expect(stockEditMode(product([variant("v1", 5, { track_stock: false })]))).toBe("none");
    expect(stockEditMode(product([variant("v1", 5)], { kind: "digital" }))).toBe("none");
    // One counted variant next to one that is not: still a list, the total is of the counted one.
    expect(stockEditMode(product([variant("v1", 5), variant("v2", 0, { track_stock: false })]))).toBe("several");
    expect(trackedVariants(product([variant("v1", 5), variant("v2", 0, { track_stock: false })])).map((v) => v.id)).toEqual(["v1"]);
  });

  it("sends the WHOLE list back, each variant with the stock the person saw, and only the chosen one changed", () => {
    const p = product([variant("v1", 12, { title: "A", option1: "2021" }), variant("v2", 3, { title: "B", option1: "2022", position: 1 })], { option_names: ["Vintage"] });
    const body = stockUpdateRequest(p, "v1", 20, "EUR");
    expect(body).not.toBeNull();
    expect(body).toHaveLength(2);
    expect(body?.[0]).toMatchObject({ id: "v1", stock: 20, expected_stock: 12, track_stock: true, price_minor: 2000 });
    expect(body?.[1]).toMatchObject({ id: "v2", stock: 3, expected_stock: 3, price_minor: 2000 });
  });

  it("refuses a variant that is not there or does not count stock", () => {
    const p = product([variant("v1", 12, { track_stock: false })]);
    expect(stockUpdateRequest(p, "v1", 5, "EUR")).toBeNull();
    expect(stockUpdateRequest(p, "nope", 5, "EUR")).toBeNull();
  });

  it("keeps a single-version product's empty name empty (never sends the technical Default Title)", () => {
    const p = product([variant("v1", 7, { title: "Default Title" })], { option_names: ["Title"] });
    const body = stockUpdateRequest(p, "v1", 9, "EUR");
    expect(body?.[0]).toMatchObject({ id: "v1", stock: 9, expected_stock: 7 });
    expect(body?.[0].title).toBe("");
  });

  it("names a variant by its options, its title, or as the only version", () => {
    expect(variantLabel({ option1: "2021", option2: "0.75 l" }, "Only")).toBe("2021 / 0.75 l");
    expect(variantLabel({ title: "Magnum" }, "Only")).toBe("Magnum");
    expect(variantLabel({ title: "Default Title" }, "Only")).toBe("Only");
    expect(variantLabel({ title: "" }, "Only")).toBe("Only");
  });
});
