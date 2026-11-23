import { describe, expect, it } from "vitest";
import { currenciesDiffer, formatMer, merToShow, refreshLocked, untaggedPercent } from "./metaAds";

const report = (mer: number | null, adCur: string | null, orderCur: string | null, spend = 1000) => ({
  mer,
  meta: { currency: adCur, spend_minor: spend },
  ours: { currency: orderCur },
});

describe("currenciesDiffer", () => {
  it("is true only when both are known and different", () => {
    expect(currenciesDiffer("EUR", "ILS")).toBe(true);
    expect(currenciesDiffer("EUR", "EUR")).toBe(false);
    expect(currenciesDiffer("EUR", null)).toBe(false);
    expect(currenciesDiffer(null, "EUR")).toBe(false);
  });
});

describe("merToShow", () => {
  it("shows the server's number in one currency", () => {
    expect(merToShow(report(2.5, "EUR", "EUR"))).toBe(2.5);
    expect(merToShow(report(2.5, "EUR", null))).toBe(2.5);
  });
  it("is null across two currencies, without spend, or without a number", () => {
    expect(merToShow(report(2.5, "EUR", "ILS"))).toBeNull();
    expect(merToShow(report(2.5, "EUR", "EUR", 0))).toBeNull();
    expect(merToShow(report(null, "EUR", "EUR"))).toBeNull();
  });
});

describe("formatMer", () => {
  it("has two digits, and a dash for none", () => {
    expect(formatMer(2.5, "en")).toBe("2.50");
    expect(formatMer(null, "en")).toBe("—");
    expect(formatMer(Number.NaN, "en")).toBe("—");
  });
});

describe("untaggedPercent", () => {
  it("rounds the share of untagged paid orders", () => {
    expect(untaggedPercent({ paid_orders: 8, untagged_orders: 2 })).toBe(25);
    expect(untaggedPercent({ paid_orders: 3, untagged_orders: 1 })).toBe(33);
    expect(untaggedPercent({ paid_orders: 4, untagged_orders: 0 })).toBe(0);
  });
  it("is null with no paid orders and never above 100", () => {
    expect(untaggedPercent({ paid_orders: 0, untagged_orders: 0 })).toBeNull();
    expect(untaggedPercent({ paid_orders: 2, untagged_orders: 5 })).toBe(100);
  });
});

describe("refreshLocked", () => {
  it("is locked until not_before", () => {
    const now = Date.parse("2026-11-23T10:00:00Z");
    expect(refreshLocked("2026-11-23T10:05:00Z", now)).toBe(true);
    expect(refreshLocked("2026-11-23T09:59:00Z", now)).toBe(false);
    expect(refreshLocked(null, now)).toBe(false);
  });
});
