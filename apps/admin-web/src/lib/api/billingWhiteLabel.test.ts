import { describe, expect, it } from "vitest";
import {
  buildWhiteLabelRequest,
  canEndWhiteLabel,
  emptyWhiteLabelForm,
  monthlyPriceLabel,
  type WhiteLabelPeriod,
} from "./billingWhiteLabel";

const NOW = new Date("2026-11-02T10:00:00Z");
const ORG = "0199a0b1-0000-7000-8000-000000000001";
const PROMOTER = "0199a0b1-0000-7000-8000-000000000002";

describe("white label request", () => {
  it("builds the request for the organization itself with the price list", () => {
    const r = buildWhiteLabelRequest({ ...emptyWhiteLabelForm(), orgId: ORG, startsOn: "2026-11-02" }, NOW);
    expect(r).toEqual({ ok: true, request: { org_id: ORG, starts_on: "2026-11-02" } });
  });

  it("sends a promoter, a last day and an individual price in minor units", () => {
    const r = buildWhiteLabelRequest(
      { orgId: ORG, promoterId: PROMOTER, startsOn: "2026-12-01", endsOn: "2027-11-30", monthlyPrice: "45,5" },
      NOW,
    );
    expect(r).toEqual({
      ok: true,
      request: { org_id: ORG, promoter_id: PROMOTER, starts_on: "2026-12-01", ends_on: "2027-11-30", monthly_price_minor: 4550 },
    });
  });

  it("sends 0 for a free brand", () => {
    const r = buildWhiteLabelRequest({ ...emptyWhiteLabelForm(), orgId: ORG, startsOn: "2026-11-02", monthlyPrice: "0" }, NOW);
    expect(r.ok && r.request.monthly_price_minor).toBe(0);
  });

  it("refuses a past first day, a last day before it and a bad price", () => {
    const r = buildWhiteLabelRequest(
      { orgId: "x", promoterId: "nope", startsOn: "2026-11-01", endsOn: "2026-10-31", monthlyPrice: "-1" },
      NOW,
    );
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.errors).toHaveLength(5);
  });
});

describe("white label list", () => {
  const base = { ends_on: null, status: "in_force" } as unknown as WhiteLabelPeriod;
  it("offers an end only while the period is not over", () => {
    expect(canEndWhiteLabel(base, NOW)).toBe(true);
    expect(canEndWhiteLabel({ ...base, ends_on: "2026-11-02" }, NOW)).toBe(true);
    expect(canEndWhiteLabel({ ...base, ends_on: "2026-11-01" }, NOW)).toBe(false);
    expect(canEndWhiteLabel({ ...base, status: "ended" }, NOW)).toBe(false);
  });

  it("names the price", () => {
    expect(monthlyPriceLabel(null)).toBe("Price list");
    expect(monthlyPriceLabel(0)).toBe("Free");
    expect(monthlyPriceLabel(4500)).toBe("45.00");
  });
});
