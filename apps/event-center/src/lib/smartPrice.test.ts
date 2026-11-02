import { describe, expect, it } from "vitest";
import type { SmartPriceRule, TicketTierItem } from "@/lib/api/types";
import { activeRuleOf, allowedKinds, defaultSmartForm, endedRuleOf, parseSmartForm, smartEligibility } from "@/lib/smartPrice";

const tier = (extra: Partial<TicketTierItem>): TicketTierItem =>
  ({ id: "t1", name: "Parter", pricing_mode: "fixed", price_amount: 2500, currency: "EUR", ...extra }) as TicketTierItem;

const rule = (extra: Partial<SmartPriceRule>): SmartPriceRule =>
  ({
    id: "r1",
    tier_id: "t1",
    tier_name: "Parter",
    step_tier_ids: [],
    kind: "by_sales",
    params: {},
    base_price: 2500,
    currency: "EUR",
    status: "active",
    created_at: "2026-11-02T10:00:00Z",
    ...extra,
  }) as SmartPriceRule;

describe("parseSmartForm", () => {
  it("builds the body of a step by tickets sold", () => {
    const r = parseSmartForm({ kind: "by_sales", step: "10", every: " 30 ", cap: "50", start: null });
    expect(r).toEqual({ ok: true, body: { kind: "by_sales", step_percent: 10, every: 30, cap_percent: 50 } });
  });

  it("sends the start day of a rise by days only when one is chosen", () => {
    const r = parseSmartForm({ ...defaultSmartForm("by_days"), start: "2026-11-10" });
    expect(r.ok && r.body.start_date).toBe("2026-11-10");
    const plain = parseSmartForm(defaultSmartForm("by_days"));
    expect(plain.ok && "start_date" in plain.body).toBe(false);
  });

  it("asks no cap for the last days", () => {
    const r = parseSmartForm({ kind: "last_days", step: "20", every: "3", cap: "x", start: null });
    expect(r).toEqual({ ok: true, body: { kind: "last_days", step_percent: 20, every: 3 } });
  });

  it("refuses what is not a whole number, out of range, or a cap below one step", () => {
    const bad = parseSmartForm({ kind: "by_sales", step: "2.5", every: "0", cap: "50", start: null });
    expect(bad.ok).toBe(false);
    if (!bad.ok) {
      expect(bad.errors.step?.key).toBe("smartPrice.err.number");
      expect(bad.errors.every).toEqual({ key: "smartPrice.err.range", params: { min: 1, max: 100000 } });
    }
    const days = parseSmartForm({ kind: "by_days", step: "10", every: "400", cap: "50", start: null });
    expect(!days.ok && days.errors.every?.params).toEqual({ min: 1, max: 365 });
    const cap = parseSmartForm({ kind: "by_days", step: "30", every: "7", cap: "20", start: null });
    expect(!cap.ok && cap.errors.cap?.key).toBe("smartPrice.err.cap");
  });
});

describe("rules of a category", () => {
  it("finds the active preset and the last ended one", () => {
    const rules = [rule({ id: "old", status: "overridden" }), rule({ id: "now" }), rule({ id: "other", tier_id: "t2" })];
    expect(activeRuleOf(rules, "t1")?.id).toBe("now");
    expect(endedRuleOf(rules, "t1")?.id).toBe("old");
    expect(activeRuleOf(rules, "t3")).toBeUndefined();
  });

  it("offers a preset only to a fixed paid category outside a chain", () => {
    expect(smartEligibility(tier({}), false)).toBe("ok");
    expect(smartEligibility(tier({}), true)).toBe("chain");
    expect(smartEligibility(tier({ price_amount: 0 }), false)).toBe("free");
    expect(smartEligibility(tier({ pricing_mode: "pwyw" }), false)).toBe("free");
  });

  it("lets a seated category rise by days only", () => {
    expect(allowedKinds(true)).toEqual(["by_days", "last_days"]);
    expect(allowedKinds(false)).toEqual(["by_sales", "by_days", "last_days"]);
  });
});
