import { describe, expect, it } from "vitest";
import type { OrgBillingModulePeriod, OrgBillingModulesResponse } from "@/lib/api/types";
import { currentShopPeriod, shopModuleLine, shopModulePrice, shopPeriods } from "@/lib/billingModules";

function period(over: Partial<OrgBillingModulePeriod>): OrgBillingModulePeriod {
  return {
    id: "p",
    org_id: "o",
    module: "shop",
    starts_on: "2026-11-01",
    ends_on: null,
    monthly_price_minor: null,
    currency: null,
    status: "in_force",
    created_at: "2026-11-01T00:00:00Z",
    ended_at: null,
    effective_monthly_minor: 0,
    effective_currency: "EUR",
    ...over,
  } as OrgBillingModulePeriod;
}

function res(items: OrgBillingModulePeriod[], over: Partial<OrgBillingModulesResponse> = {}): OrgBillingModulesResponse {
  return { items, price_list_monthly_minor: 0, effective_monthly_minor: 0, currency: "EUR", price_source: "price_list", ...over } as OrgBillingModulesResponse;
}

describe("shopModuleLine", () => {
  it("says free for a price of 0", () => {
    const line = shopModuleLine(res([period({})]));
    expect(line).toEqual({ state: "active", since: "2026-11-01", until: null, price: { kind: "free" } });
  });

  it("shows the monthly price that applies today", () => {
    const line = shopModuleLine(res([period({ monthly_price_minor: 1900, currency: "EUR" })], { effective_monthly_minor: 1900, price_source: "individual" }));
    expect(line?.price).toEqual({ kind: "amount", minor: 1900, currency: "EUR" });
  });

  it("does not know a price without a currency", () => {
    expect(shopModulePrice({ effective_monthly_minor: 500, currency: null })).toEqual({ kind: "unknown" });
    expect(shopModulePrice({ effective_monthly_minor: null, currency: "EUR" })).toEqual({ kind: "unknown" });
  });

  it("speaks about the period in force, the next one, or the last one that ended", () => {
    const ended = period({ id: "a", starts_on: "2026-01-01", ends_on: "2026-03-31", status: "ended" });
    const now = period({ id: "b", starts_on: "2026-05-01", status: "in_force" });
    const later = period({ id: "c", starts_on: "2027-01-01", status: "scheduled" });
    expect(currentShopPeriod(shopPeriods(res([later, now, ended])))?.id).toBe("b");
    expect(currentShopPeriod(shopPeriods(res([later, ended])))?.id).toBe("c");
    expect(shopModuleLine(res([ended], { effective_monthly_minor: null }))).toEqual({ state: "ended", since: "2026-01-01", until: "2026-03-31", price: { kind: "unknown" } });
  });

  it("is null when the organization never had the module", () => {
    expect(shopModuleLine(res([]))).toBeNull();
    expect(shopModuleLine(undefined)).toBeNull();
    expect(shopModuleLine(res([period({ module: "other" })]))).toBeNull();
  });
});
