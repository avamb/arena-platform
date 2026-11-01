import { describe, expect, it } from "vitest";
import type { SalesFunnelResponse, SalesFunnelSource, SalesFunnelStep } from "@/lib/api/types";
import { FUNNEL_STEPS, formatShare, funnelRows, isEmptyFunnel, lastDays, lostPercent, sourceRows, stepCount } from "@/lib/salesFunnel";

const step = (name: string, count: number | null, share: number | null = null, suspect = 0): SalesFunnelStep => ({
  step: name,
  count,
  suspect_count: suspect,
  share_of_previous: share,
});

const SIX: SalesFunnelStep[] = [
  step("view", 200, null, 12),
  step("session_open", 100, 0.5),
  step("seats_chosen", 40, 0.4),
  step("checkout_start", 20, 0.5),
  step("payment_open", 15, 0.75),
  step("paid", 10, 0.6667),
];

describe("lostPercent", () => {
  it("is the rounded complement of the share", () => {
    expect(lostPercent(0.5)).toBe(50);
    expect(lostPercent(0.6667)).toBe(33);
    expect(lostPercent(1)).toBe(0);
    expect(lostPercent(0)).toBe(100);
  });
  it("has no loss for an unknown share or one above 1 (the counters are a guide)", () => {
    expect(lostPercent(null)).toBeNull();
    expect(lostPercent(1.2)).toBeNull();
  });
});

describe("funnelRows", () => {
  it("keeps the six steps in order with losses and bars relative to the biggest", () => {
    const rows = funnelRows(SIX);
    expect(rows.map((r) => r.step)).toEqual([...FUNNEL_STEPS]);
    expect(rows.map((r) => r.bar)).toEqual([100, 50, 20, 10, 8, 5]);
    expect(rows.map((r) => r.lost)).toEqual([null, 50, 60, 50, 25, 33]);
    expect(rows[0].suspect).toBe(12);
  });
  it("gives a step without a count no bar and does not crash on an all-zero list", () => {
    const rows = funnelRows([step("view", null), step("session_open", 0, null)]);
    expect(rows[0]).toMatchObject({ count: null, bar: 0 });
    expect(rows[1]).toMatchObject({ count: 0, bar: 0 });
  });
});

describe("isEmptyFunnel", () => {
  const base = { event_id: "e", session_id: null, from: "2026-10-01", to: "2026-10-30", timezone: null, revenue: [], sources: [], sales_links: [] };
  it("is empty only when nothing was counted anywhere", () => {
    const zeros = FUNNEL_STEPS.map((n) => step(n, 0));
    expect(isEmptyFunnel({ ...base, steps: zeros, gateway: null } as SalesFunnelResponse)).toBe(true);
    expect(isEmptyFunnel({ ...base, steps: SIX, gateway: null } as SalesFunnelResponse)).toBe(false);
    // Only bots counted: still something happened.
    expect(isEmptyFunnel({ ...base, steps: [step("view", 0, null, 3)], gateway: null } as SalesFunnelResponse)).toBe(false);
    // A site on the plugin with carts but no browser steps is not an empty event.
    expect(isEmptyFunnel({ ...base, steps: zeros, gateway: { steps: [step("seats_chosen", 2)], revenue: [] } } as SalesFunnelResponse)).toBe(false);
  });
});

describe("sourceRows", () => {
  const src = (utm_source: string, utm_campaign: string, view: number): SalesFunnelSource => ({
    utm_source,
    utm_campaign,
    steps: [step("view", view), step("paid", Math.floor(view / 10))],
    revenue: [],
  });
  it("puts the visits without a campaign last and reads the counts by step", () => {
    const rows = sourceRows([src("", "", 90), src("ig", "spring", 50), src("mail", "", 20)]);
    expect(rows.map((r) => r.key)).toEqual(["ig|spring", "mail|", "|"]);
    expect(rows[0].counts.view).toBe(50);
    expect(rows[0].counts.paid).toBe(5);
    expect(rows[0].counts.checkout_start).toBe(0);
    expect(rows[2].direct).toBe(true);
  });
});

describe("stepCount", () => {
  it("is 0 for a step the list does not hold or cannot read", () => {
    expect(stepCount(SIX, "paid")).toBe(10);
    expect(stepCount([step("view", null)], "view")).toBe(0);
    expect(stepCount(SIX, "nope")).toBe(0);
  });
});

describe("formatShare", () => {
  it("formats a share as a percent", () => {
    expect(formatShare(0.5, "en")).toBe("50%");
    expect(formatShare(0.375, "en")).toBe("38%");
  });
});

describe("lastDays", () => {
  it("counts today as the last day of the window", () => {
    expect(lastDays(7, new Date(2026, 10, 2))).toEqual({ from: "2026-10-27", to: "2026-11-02" });
    expect(lastDays(1, new Date(2026, 0, 1))).toEqual({ from: "2026-01-01", to: "2026-01-01" });
  });
});
