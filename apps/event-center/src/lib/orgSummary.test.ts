import { describe, expect, it } from "vitest";
import { activePreset, groupBreakdown, presetRange, rangeIsBackwards } from "@/lib/orgSummary";

describe("presetRange", () => {
  it("counts this month and last month in whole calendar months", () => {
    expect(presetRange("month", "2026-11-07")).toEqual({ from: "2026-11-01", to: "2026-11-30" });
    expect(presetRange("last", "2026-11-07")).toEqual({ from: "2026-10-01", to: "2026-10-31" });
    expect(presetRange("last", "2026-01-15")).toEqual({ from: "2025-12-01", to: "2025-12-31" });
    expect(presetRange("month", "2028-02-10")).toEqual({ from: "2028-02-01", to: "2028-02-29" });
  });
  it("takes the last 90 days including today, and all time as open bounds", () => {
    expect(presetRange("quarter", "2026-11-07")).toEqual({ from: "2026-08-10", to: "2026-11-07" });
    expect(presetRange("all", "2026-11-07")).toEqual({ from: "", to: "" });
  });
  it("recognises a preset and a hand-typed range", () => {
    expect(activePreset({ from: "2026-11-01", to: "2026-11-30" }, "2026-11-07")).toBe("month");
    expect(activePreset({ from: "2026-11-02", to: "2026-11-30" }, "2026-11-07")).toBeNull();
  });
  it("flags a backwards range", () => {
    expect(rangeIsBackwards({ from: "2026-11-10", to: "2026-11-01" })).toBe(true);
    expect(rangeIsBackwards({ from: "2026-11-10", to: "" })).toBe(false);
  });
});

describe("groupBreakdown", () => {
  const row = (tier: string, price: number, promo: string | null, tickets: number, revenue: number) => ({
    tier_id: tier,
    tier_name: tier.toUpperCase(),
    unit_price: price,
    promo_code: promo,
    tickets,
    discount: 0,
    revenue,
  });
  it("adds the promo rows into their price line and lists them under it", () => {
    const groups = groupBreakdown({
      breakdown: [row("a", 3000, null, 3, 9000), row("a", 3000, "CLUB", 1, 2500), row("a", 3000, "ALPHA", 4, 10000), row("a", 2000, null, 1, 2000), row("b", 3000, null, 2, 6000)],
    });
    expect(groups.map((g) => [g.tierId, g.unitPrice, g.tickets, g.revenue])).toEqual([
      ["a", 3000, 8, 21500],
      ["a", 2000, 1, 2000],
      ["b", 3000, 2, 6000],
    ]);
    expect(groups[0].promos.map((p) => p.promo_code)).toEqual(["ALPHA", "CLUB"]);
    expect(groups[1].promos).toEqual([]);
  });
});
