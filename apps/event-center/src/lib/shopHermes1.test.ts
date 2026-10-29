/**
 * The rules added after the UX run of 29 October (findings E-03, E-04, E-06, E-07, E-08, E-11): the time
 * zone of a shop, the courier text, the currency that stays closed while the shop has products, the
 * search and the sort of the product list, and the names of the wrong fields.
 */
import { describe, expect, it } from "vitest";
import { isIsoCurrency } from "@/lib/currencies";
import { currencyLabel, formatMoneySymbol, regionName } from "@/lib/format";
import {
  SHOP_COUNTRY_ZONES,
  checkSettings,
  checkVariants,
  courierText,
  currencyState,
  emptyRow,
  filterAndSortProducts,
  formatShopTime,
  invalidFields,
  isDefaultTitle,
  isSingleVersion,
  itemLabel,
  rowFromVariant,
  settingsDraft,
  shopZoneFor,
  type VariantRow,
} from "@/lib/shop";

describe("time of the shop (E-03)", () => {
  it("knows the zone of a one-zone country and nothing for the others", () => {
    expect(shopZoneFor("IL")).toBe("Asia/Jerusalem");
    expect(shopZoneFor("il")).toBe("Asia/Jerusalem");
    expect(shopZoneFor("AR")).toBe("America/Argentina/Buenos_Aires");
    expect(shopZoneFor("US")).toBeUndefined();
    expect(shopZoneFor(null)).toBeUndefined();
    expect(shopZoneFor("")).toBeUndefined();
  });

  it("maps only real zones", () => {
    for (const [country, zone] of Object.entries(SHOP_COUNTRY_ZONES)) {
      expect(() => new Intl.DateTimeFormat("en", { timeZone: zone }), country).not.toThrow();
    }
    expect(Object.keys(SHOP_COUNTRY_ZONES)).toHaveLength(40);
  });

  it("reads the moment in the zone of the shop and names the country, not a city", () => {
    // 19:36 UTC on 28 Oct 2026 is 21:36 in Israel (winter time since 25 Oct) and 20:36 in Paris.
    const iso = "2026-10-28T19:36:00Z";
    const il = formatShopTime(iso, "IL", "ru", "Europe/Paris");
    expect(il).toContain("21:36");
    expect(il).toContain("(Израиль)");
    expect(il).not.toContain("Paris");
    const es = formatShopTime(iso, "ES", "en", "Asia/Tokyo");
    expect(es).toContain("20:36");
    expect(es).toContain("(Spain)");
  });

  it("falls back to the browser zone when the shop has no country or several zones", () => {
    const iso = "2026-10-28T19:36:00Z";
    const none = formatShopTime(iso, null, "en", "Europe/Paris");
    expect(none).toContain("20:36");
    expect(none).toContain("(Paris)");
    expect(formatShopTime(iso, "US", "en", "Europe/Paris")).toContain("(Paris)");
  });
});

describe("country and currency in words (E-04, E-06)", () => {
  it("names a country in the language of the screen", () => {
    expect(regionName("IL", "ru")).toBe("Израиль");
    expect(regionName("il", "en")).toBe("Israel");
    expect(regionName("ES", "es")).toBe("España");
    expect(regionName("", "ru")).toBe("");
    expect(regionName(null, "ru")).toBe("");
  });

  it("shows a currency with its symbol and name", () => {
    expect(currencyLabel("ILS", "en")).toMatch(/^₪ \(.*Shekel.*\)$/);
    expect(currencyLabel("ils", "ru")).toMatch(/^₪ \(.*шекел.*\)$/);
    expect(currencyLabel("EUR", "es")).toContain("€");
    expect(currencyLabel("NOPE!", "en")).toBe("NOPE!");
  });

  it("prints money with the short symbol", () => {
    expect(formatMoneySymbol(74000, "ILS", "ru").replace(/\s/g, " ")).toBe("740,00 ₪");
    expect(formatMoneySymbol(1250, "EUR", "es").replace(/\s/g, " ")).toBe("12,50 €");
  });

  it("checks a currency against ISO 4217", () => {
    for (const ok of ["ILS", "eur", " usd ", "CZK", "ARS"]) expect(isIsoCurrency(ok), ok).toBe(true);
    for (const bad of ["ILX", "EU", "", "HRK", "ZZZ"]) expect(isIsoCurrency(bad), bad).toBe(false);
  });
});

describe("the currency stays closed while the shop has products (E-04)", () => {
  it("is editable for an empty shop, locked with products, and closed until the count is known", () => {
    expect(currencyState(0)).toBe("editable");
    expect(currencyState(undefined)).toBe("editable");
    expect(currencyState(1)).toBe("locked");
    expect(currencyState(39)).toBe("locked");
    expect(currencyState(null)).toBe("checking");
  });

  const draft = settingsDraft({
    org_id: "o",
    configured: true,
    currency: "ILS",
    shipping_fee_minor: 0,
    shipping_label: "",
    country_iso2: "IL",
    age_gate_min_age: null,
    announcement_text: "",
    contact_email: null,
    logistics_note: "",
    sales_channel_id: null,
    shop_domain: null,
    max_line_qty: 24,
    enabled: false,
    updated_at: null,
  });

  it("tells a real code from a slip once the field is open", () => {
    expect(checkSettings({ ...draft, currency: "ILX" })).toEqual({ currencyUnknown: true });
    expect(checkSettings({ ...draft, currency: "IL" })).toEqual({ currency: true });
    expect(checkSettings({ ...draft, currency: "ils" })).toEqual({});
  });

  it("does not judge a currency nobody typed", () => {
    expect(checkSettings({ ...draft, currency: "XXX" }, { currencyLocked: true })).toEqual({});
  });

  it("lets a long number reach the range check instead of cutting it (E-15)", () => {
    expect(checkSettings({ ...draft, maxLineQty: "150" })).toEqual({ maxLineQty: true });
    expect(checkSettings({ ...draft, ageGate: "150" })).toEqual({ ageGate: true });
    expect(checkSettings({ ...draft, maxLineQty: "99", ageGate: "18" })).toEqual({});
  });
});

describe("the only version (E-07)", () => {
  const variant = (over: Record<string, unknown> = {}) =>
    ({ id: "v1", title: "Default Title", sku: null, price_minor: 20000, compare_at_minor: null, stock: 82, track_stock: true, held: 0, available: 82, position: 0, option1: "Default Title", option2: null, option3: null, ...over }) as never;

  it("never shows the technical name as a value", () => {
    expect(isDefaultTitle("Default Title")).toBe(true);
    expect(isDefaultTitle(" default title ")).toBe(true);
    expect(isDefaultTitle("")).toBe(true);
    expect(isDefaultTitle(null)).toBe(true);
    expect(isDefaultTitle("Red")).toBe(false);
    const row = rowFromVariant(variant(), "ILS");
    expect(row.title).toBe("");
    expect(row.options).toEqual(["", "", ""]);
  });

  it("calls a nameless single row the only version, with or without Shopify's option name", () => {
    const one = [rowFromVariant(variant(), "ILS")];
    expect(isSingleVersion(one, [])).toBe(true);
    expect(isSingleVersion(one, ["Title"])).toBe(true);
    // The person named an option: the field must come back.
    expect(isSingleVersion(one, ["Size"])).toBe(false);
    // A second variant: both rows need their names.
    expect(isSingleVersion([...one, emptyRow()], [])).toBe(false);
    // A row that has a name is not nameless.
    expect(isSingleVersion([{ ...emptyRow(), title: "Big" }], [])).toBe(false);
  });

  it("labels an order line without the technical variant", () => {
    expect(itemLabel({ product_title: "Red wine", variant_title: "Default Title" })).toBe("Red wine");
    expect(itemLabel({ product_title: "Red wine", variant_title: "" })).toBe("Red wine");
    expect(itemLabel({ product_title: "Red wine", variant_title: "0.75 l" })).toBe("Red wine (0.75 l)");
  });
});

describe("the courier text (E-06, E-11)", () => {
  const order = {
    buyer: { name: "Dana Levi", email: "dana@example.com", phone: "+972 50 123 4567" },
    ship: { name: "", address1: "Herzl 1", address2: "apt 4", city: "Haifa", postal: "3300000", country: "IL" },
    items: [
      { product_title: "Red wine", variant_title: "Default Title", qty: 2 },
      { product_title: "Gift box", variant_title: "Large", qty: 1 },
    ],
    total: 74000,
    currency: "ILS",
    delivery_note: "Leave with the neighbour",
    delivery_kind: "shipping",
  } as never;
  const labels = { order: "Заказ №1000001786", comment: (n: string) => `Комментарий покупателя: ${n}` };

  it("starts with the order number and ends with the buyer's comment", () => {
    const lines = courierText(order, { locale: "ru", shopCountry: "IL", labels }).split("\n");
    expect(lines[0]).toBe("Заказ №1000001786");
    expect(lines.at(-1)).toBe("Комментарий покупателя: Leave with the neighbour");
    expect(lines).toContain("Dana Levi");
    expect(lines).toContain("+972 50 123 4567");
    expect(lines).toContain("Haifa");
    expect(lines).toContain("Herzl 1, apt 4");
    expect(lines).toContain("3300000");
    expect(lines).toContain("Red wine × 2");
    expect(lines).toContain("Gift box (Large) × 1");
  });

  it("prints the total as an amount with the symbol", () => {
    const lines = courierText(order, { locale: "ru", shopCountry: "IL", labels }).split("\n");
    expect(lines.map((l) => l.replace(/\s/g, " "))).toContain("740,00 ₪");
    expect(courierText(order, { locale: "ru", shopCountry: "IL", labels })).not.toContain("ILS");
  });

  it("names a foreign country in words and leaves the shop's own out", () => {
    const abroad = { ...(order as object), ship: { address1: "Calle 1", city: "Madrid", country: "ES" } } as never;
    expect(courierText(abroad, { locale: "ru", shopCountry: "IL", labels })).toContain("Испания");
    expect(courierText(order, { locale: "ru", shopCountry: "IL", labels })).not.toContain("Израиль");
  });

  it("leaves out what is empty: no comment, no address for a pickup", () => {
    const pickup = { ...(order as object), delivery_note: "", delivery_kind: "pickup" } as never;
    const text = courierText(pickup, { locale: "en", shopCountry: "IL", labels });
    expect(text.split("\n").at(-1)).not.toContain("Комментарий");
    expect(text).not.toContain("Herzl");
    expect(text).not.toContain("Haifa");
  });
});

describe("search and sort of the product list (E-08)", () => {
  const v = (stock: number, track = true) => [{ stock, track_stock: track }];
  const products = [
    { title: "Rosé 2 Cats", status: "draft", variants: v(5) },
    { title: "Red dry \"2 Cats\"", status: "active", variants: v(82) },
    { title: "3 Cats Chardonnay", status: "active", variants: v(11) },
    { title: "Gift card", status: "archived", variants: v(0, false) },
    { title: "Éclair set", status: "active", variants: v(3) },
  ];

  it("finds a product by any word of its title, in any case, ignoring accents", () => {
    expect(filterAndSortProducts(products, "3 cats", "title", "en").map((p) => p.title)).toEqual(["3 Cats Chardonnay"]);
    expect(filterAndSortProducts(products, "CATS red", "title", "en").map((p) => p.title)).toEqual(['Red dry "2 Cats"']);
    expect(filterAndSortProducts(products, "eclair", "title", "en").map((p) => p.title)).toEqual(["Éclair set"]);
    expect(filterAndSortProducts(products, "rose", "title", "en")).toHaveLength(1);
    expect(filterAndSortProducts(products, "nothing here", "title", "en")).toEqual([]);
    expect(filterAndSortProducts(products, "   ", "title", "en")).toHaveLength(5);
  });

  it("sorts by name, with numbers in their natural order", () => {
    const names = filterAndSortProducts(products, "", "title", "en").map((p) => p.title);
    expect(names).toEqual(['3 Cats Chardonnay', 'Éclair set', 'Gift card', 'Red dry "2 Cats"', "Rosé 2 Cats"]);
  });

  it("sorts by status: on sale, then draft, then archived, each by name", () => {
    expect(filterAndSortProducts(products, "", "status", "en").map((p) => p.status)).toEqual(["active", "active", "active", "draft", "archived"]);
    expect(filterAndSortProducts(products, "", "status", "en")[0].title).toBe("3 Cats Chardonnay");
  });

  it("sorts by stock: the least first, products that do not count stock last", () => {
    expect(filterAndSortProducts(products, "", "stock", "en").map((p) => p.title)).toEqual([
      "Éclair set",
      "Rosé 2 Cats",
      "3 Cats Chardonnay",
      'Red dry "2 Cats"',
      "Gift card",
    ]);
  });

  it("does not change the list it is given", () => {
    const copy = [...products];
    filterAndSortProducts(products, "", "stock", "en");
    expect(products).toEqual(copy);
  });
});

describe("the names of the wrong fields (E-05)", () => {
  const row = (over: Partial<VariantRow>): VariantRow => ({ ...emptyRow(), price: "5", ...over });

  it("lists them in page order, once each", () => {
    const rows = [row({ title: "A", price: "", stock: "x" }), row({ title: "B", price: "", compareAt: "?" })];
    const check = checkVariants(rows, "ILS", 0);
    expect(invalidFields({ title: true, body: false }, check)).toEqual(["title", "price", "compareAt", "stock"]);
  });

  it("names the description and an empty list of variants", () => {
    expect(invalidFields({ title: false, body: true }, null)).toEqual(["body"]);
    expect(invalidFields({ title: false, body: false }, checkVariants([], "ILS", 0))).toEqual(["variants"]);
  });

  it("is empty when everything is fine", () => {
    expect(invalidFields({ title: false, body: false }, checkVariants([row({})], "ILS", 0))).toEqual([]);
    expect(invalidFields({ title: false, body: false }, null)).toEqual([]);
  });

  it("names the variant name and the SKU", () => {
    const rows = [row({ title: "A", sku: "x".repeat(101) }), row({ title: "a" })];
    expect(invalidFields({ title: false, body: false }, checkVariants(rows, "ILS", 0))).toEqual(["variantName", "sku"]);
  });
});
