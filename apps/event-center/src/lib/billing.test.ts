import { describe, expect, it } from "vitest";
import type { BillingChargeItem } from "@/lib/api/types";
import { formatMonth, groupByEvent, isMonth, kindKey, monthOf, monthRange, reasonKey, ruleKey, shiftMonth } from "@/lib/billing";

function charge(over: Partial<BillingChargeItem>): BillingChargeItem {
  return {
    id: "c1",
    billable_event_id: "b1",
    kind: "ticket_sold",
    occurred_at: "2026-10-10T12:00:00Z",
    rated_at: "2026-10-10T12:00:30Z",
    event_id: "e1",
    session_id: null,
    order_id: null,
    currency: "EUR",
    amount_minor: 59,
    rule_type: "fixed_per_ticket",
    reason: null,
    reversal_of: null,
    price_list_version: 1,
    pricing_class: "standard",
    seated: false,
    sale_currency: "EUR",
    unit_price_minor: 1500,
    ...over,
  };
}

describe("months", () => {
  it("shifts across year ends in both directions", () => {
    expect(shiftMonth("2026-12", 1)).toBe("2027-01");
    expect(shiftMonth("2026-01", -1)).toBe("2025-12");
    expect(shiftMonth("2026-10", 0)).toBe("2026-10");
    expect(shiftMonth("2026-03", -15)).toBe("2024-12");
  });
  it("builds the half-open UTC range of a month", () => {
    expect(monthRange("2026-10")).toEqual({ from: "2026-10-01T00:00:00Z", to: "2026-11-01T00:00:00Z" });
    expect(monthRange("2026-12").to).toBe("2027-01-01T00:00:00Z");
  });
  it("reads the month of a moment in UTC", () => {
    expect(monthOf(new Date("2026-10-31T23:59:59Z"))).toBe("2026-10");
    expect(monthOf(new Date("2026-11-01T00:00:00Z"))).toBe("2026-11");
  });
  it("validates and names a month", () => {
    expect(isMonth("2026-10")).toBe(true);
    expect(isMonth("2026-13")).toBe(false);
    expect(isMonth("2026-1")).toBe(false);
    expect(formatMonth("2026-10", "en")).toContain("2026");
  });
});

describe("labels", () => {
  it("falls back to other for an unknown kind or rule", () => {
    expect(kindKey("ticket_sold")).toBe("billing.kind.ticket_sold");
    expect(kindKey("something_new")).toBe("billing.kind.other");
    expect(ruleKey("fixed_per_ticket")).toBe("billing.rule.fixed_per_ticket");
    expect(ruleKey("future_rule")).toBe("billing.rule.other");
  });
  it("labels only the stated reasons", () => {
    expect(reasonKey("journal_only")).toBe("billing.reason.journal_only");
    expect(reasonKey("")).toBeNull();
    expect(reasonKey(null)).toBeNull();
    expect(reasonKey("weird")).toBeNull();
  });
});

describe("groupByEvent", () => {
  it("nets a reversal into its event and counts only direct charges", () => {
    const rows = [
      charge({ id: "a", event_id: "e1", amount_minor: 59 }),
      charge({ id: "b", event_id: "e1", amount_minor: 59 }),
      charge({ id: "c", event_id: "e1", amount_minor: -59, reversal_of: "a" }),
      charge({ id: "d", event_id: "e2", amount_minor: 59 }),
    ];
    expect(groupByEvent(rows)).toEqual([
      { eventId: "e1", charges: 2, net: [{ currency: "EUR", netMinor: 59 }] },
      { eventId: "e2", charges: 1, net: [{ currency: "EUR", netMinor: 59 }] },
    ]);
  });
  it("never adds two currencies together and keeps no-event charges apart", () => {
    const lines = groupByEvent([
      charge({ id: "a", event_id: "e1", currency: "EUR", amount_minor: 59 }),
      charge({ id: "b", event_id: "e1", currency: "CZK", amount_minor: 1500 }),
      charge({ id: "c", event_id: null, currency: "EUR", amount_minor: 0 }),
    ]);
    expect(lines.find((l) => l.eventId === "e1")?.net).toEqual([
      { currency: "CZK", netMinor: 1500 },
      { currency: "EUR", netMinor: 59 },
    ]);
    expect(lines.some((l) => l.eventId === null)).toBe(true);
  });
});
