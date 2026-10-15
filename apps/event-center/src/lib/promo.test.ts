/**
 * The pure rules of the promo-code screens (lib/promo.ts). They are the
 * rules of the bot's promo dialog (eventbot/promo.go), so each case here is a
 * rule the bot's e2e also pins.
 */
import { describe, expect, it } from "vitest";
import type { PromoCodeItem } from "@/lib/api/types";
import {
  buildCreateRequest,
  isClubCode,
  isNameTaken,
  mergeSessions,
  normalizePromoCode,
  parseAmountMinor,
  parseLimit,
  parsePercent,
  promoCurrencyOf,
  promoState,
  draftProblem,
  dropOtherCurrencySessions,
  readPromoForm,
  sessionTakesCode,
  untilInstant,
  type PromoFormRaw,
} from "@/lib/promo";

const item = (over: Partial<PromoCodeItem>): PromoCodeItem =>
  ({
    id: "1",
    org_id: "o",
    code: "X",
    discount_type: "percent",
    discount_value: 10,
    applies_to_tier_ids: [],
    applies_to_session_ids: [],
    currency: null,
    uses: 0,
    discount_total: 0,
    discount_currency: null,
    last_used_at: null,
    max_uses: null,
    max_uses_per_customer: null,
    valid_from: null,
    valid_until: null,
    min_order_amount: 0,
    status: "active",
    ...over,
  }) as PromoCodeItem;

describe("the code name", () => {
  it("is turned into capitals", () => {
    expect(normalizePromoCode(" club-15 ")).toEqual({ ok: true, value: "CLUB-15" });
  });
  it("refuses empty, spaces, exotic signs and over 64 characters", () => {
    expect(normalizePromoCode("   ")).toMatchObject({ ok: false, error: "promo.err.codeEmpty" });
    expect(normalizePromoCode("two words")).toMatchObject({ ok: false, error: "promo.err.codeChars" });
    expect(normalizePromoCode("a$b")).toMatchObject({ ok: false, error: "promo.err.codeChars" });
    // Only decimal digits count, like the bot: a fraction or a Roman numeral is no digit.
    expect(normalizePromoCode("a½")).toMatchObject({ ok: false, error: "promo.err.codeChars" });
    expect(normalizePromoCode("aⅧ")).toMatchObject({ ok: false, error: "promo.err.codeChars" });
    expect(normalizePromoCode("A".repeat(65))).toMatchObject({ ok: false, error: "promo.err.codeLong" });
    expect(normalizePromoCode("A".repeat(64)).ok).toBe(true);
  });
  it("accepts letters of any alphabet", () => {
    expect(normalizePromoCode("весна_2026")).toEqual({ ok: true, value: "ВЕСНА_2026" });
  });
  it("is compared without case", () => {
    expect(isNameTaken("spring", ["SPRING"])).toBe(true);
    expect(isNameTaken("summer", ["SPRING"])).toBe(false);
  });
});

describe("the numbers", () => {
  it("a percent is a whole number from 1 to 100, with or without the sign", () => {
    expect(parsePercent("15 %")).toEqual({ ok: true, value: 15 });
    expect(parsePercent("100").ok).toBe(true);
    for (const bad of ["0", "101", "1.5", "-5", "", "abc"]) expect(parsePercent(bad).ok, bad).toBe(false);
  });
  it("an amount becomes minor units of its currency", () => {
    expect(parseAmountMinor("5", "EUR")).toEqual({ ok: true, value: 500 });
    expect(parseAmountMinor("5,5", "EUR")).toEqual({ ok: true, value: 550 });
    expect(parseAmountMinor("5.50", "EUR")).toEqual({ ok: true, value: 550 });
    expect(parseAmountMinor("500", "JPY")).toEqual({ ok: true, value: 500 });
  });
  it("an amount refuses a thousands separator, extra decimals, zero and junk", () => {
    for (const bad of ["1,000.50", "5.555", "0", "0.00", "", "abc", "-1"]) expect(parseAmountMinor(bad, "EUR").ok, bad).toBe(false);
    expect(parseAmountMinor("5.5", "JPY").ok).toBe(false);
  });
  it("a limit is a whole number from 1 to a million", () => {
    expect(parseLimit("100")).toEqual({ ok: true, value: 100 });
    for (const bad of ["0", "1000001", "1.5", ""]) expect(parseLimit(bad).ok, bad).toBe(false);
  });
});

describe("the last day", () => {
  it("ends at 23:59:59 UTC of the chosen day", () => {
    expect(untilInstant("2026-12-31")).toBe("2026-12-31T23:59:59Z");
  });
  it("refuses a day that does not exist", () => {
    expect(untilInstant("2026-02-30")).toBeNull();
    expect(untilInstant("tomorrow")).toBeNull();
  });
});

describe("the state of a code", () => {
  const now = new Date("2026-10-15T12:00:00Z");
  it("is paused, expired, scheduled, exhausted or active", () => {
    expect(promoState(item({ status: "paused" }), now)).toBe("paused");
    expect(promoState(item({ valid_until: "2026-10-14T23:59:59Z" }), now)).toBe("expired");
    expect(promoState(item({ valid_from: "2026-11-01T00:00:00Z" }), now)).toBe("scheduled");
    expect(promoState(item({ max_uses: 3, uses: 3 }), now)).toBe("exhausted");
    expect(promoState(item({ max_uses: 3, uses: 2 }), now)).toBe("active");
    expect(promoState(item({}), now)).toBe("active");
  });
  it("a club code has no session list", () => {
    expect(isClubCode(item({}))).toBe(true);
    expect(isClubCode(item({ applies_to_session_ids: ["s1"] }))).toBe(false);
  });
  it("the currency of its money is its own, else the report's, else none", () => {
    expect(promoCurrencyOf(item({ discount_type: "fixed_amount", currency: "EUR" }))).toBe("EUR");
    expect(promoCurrencyOf(item({ discount_currency: "CZK" }))).toBe("CZK");
    // Several currencies: the server sends none, so no sum is printed.
    expect(promoCurrencyOf(item({}))).toBe("");
  });
});

describe("the sessions of a code", () => {
  const eventA = new Set(["a1", "a2"]);
  it("re-choosing one event keeps the sessions of the others", () => {
    expect(mergeSessions(["a1", "b1"], eventA, ["a2"])).toEqual(["b1", "a2"]);
  });
  it("taking one event's dates away keeps the other event's", () => {
    expect(mergeSessions(["a1", "a2", "b1"], eventA, [])).toEqual(["b1"]);
  });
  it("can come out empty, which the screen then refuses to save", () => {
    expect(mergeSessions(["a1"], eventA, [])).toEqual([]);
  });
  it("never repeats an id", () => {
    expect(mergeSessions(["b1", "b1"], eventA, ["a1", "a1"])).toEqual(["b1", "a1"]);
  });
  it("a fixed code takes only sessions sold in its currency", () => {
    expect(sessionTakesCode("EUR", "eur")).toBe(true);
    expect(sessionTakesCode("EUR", "CZK")).toBe(false);
    expect(sessionTakesCode("", "CZK")).toBe(true);
  });
});

describe("dropping sessions of another currency", () => {
  it("keeps the ones that fit or are unknown, drops the rest", () => {
    expect(dropOtherCurrencySessions(["a", "b", "c"], { a: "EUR", b: "CZK" }, "EUR")).toEqual({ kept: ["a", "c"], dropped: ["b"] });
  });
  it("drops nothing for a percent code", () => {
    expect(dropOtherCurrencySessions(["a", "b"], { a: "EUR", b: "CZK" }, "")).toEqual({ kept: ["a", "b"], dropped: [] });
  });
});

describe("the form", () => {
  const raw = (over: Partial<PromoFormRaw>): PromoFormRaw => ({
    code: "club15",
    type: "percent",
    value: "15",
    currency: "",
    all: true,
    total: "",
    per: "",
    until: null,
    status: "active",
    ...over,
  });

  it("a club code is sent with an EMPTY session list, named explicitly", () => {
    const read = readPromoForm(raw({}), [], []);
    expect(read.errors).toEqual({});
    expect(buildCreateRequest(read.draft!)).toMatchObject({ code: "CLUB15", discount_type: "percent", discount_value: 15, applies_to_session_ids: [] });
  });
  it("chosen sessions need at least one: an empty list would be a club code", () => {
    expect(readPromoForm(raw({ all: false }), [], []).errors.scope).toBe("promo.err.pickNone");
    const ok = readPromoForm(raw({ all: false }), ["s1"], []);
    expect(buildCreateRequest(ok.draft!)?.applies_to_session_ids).toEqual(["s1"]);
  });
  it("a name that is taken is refused whatever its case", () => {
    expect(readPromoForm(raw({ code: "Spring" }), [], ["SPRING"]).errors.code).toBe("promo.err.codeDup");
  });
  it("a fixed discount names its currency and sends minor units", () => {
    const read = readPromoForm(raw({ type: "fixed_amount", value: "5,50", currency: "eur", until: "2026-12-31" }), [], []);
    expect(read.errors).toEqual({});
    const body = buildCreateRequest(read.draft!);
    expect(body).toMatchObject({ discount_type: "fixed_amount", discount_value: 550, currency: "EUR", valid_until: "2026-12-31T23:59:59Z" });
    expect(readPromoForm(raw({ type: "fixed_amount", value: "5", currency: "" }), [], []).errors.currency).toBe("promo.err.currency");
  });
  it("the limit per buyer cannot be above the total", () => {
    expect(readPromoForm(raw({ total: "5", per: "6" }), [], []).errors.per).toBe("promo.err.perGtTotal");
    expect(readPromoForm(raw({ total: "5", per: "5" }), [], []).errors).toEqual({});
  });
  it("a fixed code is refused when a ticked session is sold in another currency", () => {
    const cur = { s1: "EUR", s2: "CZK" };
    const mixed = readPromoForm(raw({ type: "fixed_amount", value: "5", currency: "eur", all: false }), ["s1", "s2"], [], cur);
    expect(mixed.draft).toBeNull();
    expect(mixed.errors.scope).toBe("promo.err.otherCurrency");
    const ok = readPromoForm(raw({ type: "fixed_amount", value: "5", currency: "eur", all: false }), ["s1"], [], cur);
    expect(ok.errors).toEqual({});
    // A percent code takes any currency, and a session never seen cannot be judged.
    expect(readPromoForm(raw({ all: false }), ["s1", "s2"], [], cur).errors).toEqual({});
    expect(readPromoForm(raw({ type: "fixed_amount", value: "5", currency: "eur", all: false }), ["s9"], [], cur).errors).toEqual({});
  });
  it("draftProblem judges the same mix", () => {
    const d = readPromoForm(raw({ type: "fixed_amount", value: "5", currency: "eur", all: false }), ["s1"], [], { s1: "EUR" }).draft!;
    expect(draftProblem(d, { s1: "CZK" })).toBe("promo.err.otherCurrency");
    expect(draftProblem(d, { s1: "EUR" })).toBeNull();
  });
  it("every field is checked in one go", () => {
    const read = readPromoForm(raw({ code: "", value: "x", total: "0" }), [], []);
    expect(read.draft).toBeNull();
    expect(Object.keys(read.errors).sort()).toEqual(["code", "total", "value"]);
  });
});
