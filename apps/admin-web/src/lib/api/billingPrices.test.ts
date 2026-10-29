import { describe, expect, it } from "vitest";
import {
  buildCreateRequest,
  buildRateRequest,
  buildTermsRequest,
  canCloseRate,
  canRemoveRate,
  canRevokeVersion,
  emptyPack,
  emptyRateForm,
  emptyRule,
  emptyVersionForm,
  formFromDetail,
  formatBasisPoints,
  formatMinor,
  groupSeries,
  localInputToIso,
  minorPreview,
  parseMinor,
  priceListCodes,
  termsFormFrom,
  type PriceListDetail,
  type PriceListVersion,
  type VersionForm,
} from "@/lib/api/billingPrices";
import { requiresAdminReason } from "@/lib/api/reason";

// Test numbers, not the policy's: the screen never supplies a number itself.
const NOW = new Date("2026-10-17T10:00:00Z");
const FUTURE = "2026-11-01T00:00";

function validForm(over: Partial<VersionForm> = {}): VersionForm {
  const rule = emptyRule("fixed_per_ticket", "EUR");
  return {
    ...emptyVersionForm("b15-test", "EUR"),
    effectiveFromLocal: FUTURE,
    bonusShareCapBp: "3000",
    rules: [{ ...rule, params: { unseated_minor: "71", seated_minor: "133" } }],
    ...over,
  };
}

describe("money and time helpers", () => {
  it("formats minor units with two decimals", () => {
    expect(formatMinor(59, "EUR")).toBe("0.59 EUR");
    expect(formatMinor(5, "EUR")).toBe("0.05 EUR");
    expect(formatMinor(100000, "USD")).toBe("1000.00 USD");
    expect(formatMinor(0, "EUR")).toBe("0.00 EUR");
  });

  it("parses only whole non-negative numbers", () => {
    expect(parseMinor("59")).toBe(59);
    expect(parseMinor(" 7 ")).toBe(7);
    for (const bad of ["", "-1", "0.5", "1e3", "abc", "12 3", "9999999999999999"]) {
      expect(parseMinor(bad), bad).toBeNull();
    }
    expect(minorPreview("59", "EUR")).toBe("= 0.59 EUR");
    expect(minorPreview("x", "EUR")).toBe("");
  });

  it("formats basis points as percent", () => {
    expect(formatBasisPoints(300)).toBe("3%");
    expect(formatBasisPoints(150)).toBe("1.5%");
    expect(formatBasisPoints(5)).toBe("0.05%");
    expect(formatBasisPoints(0)).toBe("0%");
  });

  it("reads a datetime-local value as UTC", () => {
    expect(localInputToIso("2026-11-01T00:00")).toBe("2026-11-01T00:00:00.000Z");
    expect(localInputToIso("2026-11-01T12:30:15")).toBe("2026-11-01T12:30:15.000Z");
    expect(localInputToIso("")).toBeNull();
    expect(localInputToIso("tomorrow")).toBeNull();
  });
});

describe("buildCreateRequest", () => {
  it("builds the request of a valid form", () => {
    const r = buildCreateRequest(validForm({ locked: true, freeLimit: "12", floorMinor: "9" }), NOW);
    expect(r.ok).toBe(true);
    if (!r.ok) return;
    expect(r.request).toMatchObject({
      code: "b15-test",
      currency: "EUR",
      effective_from: "2026-11-01T00:00:00.000Z",
      locked: true,
      free_tickets_annual_limit: 12,
      floor_per_ticket_minor: 9,
      bonus_share_cap_bp: 3000,
    });
    expect(r.request.rules).toEqual([
      {
        rule_type: "fixed_per_ticket",
        line: "standard",
        event_kind: "ticket_sold",
        params: { unseated_minor: 71, seated_minor: 133 },
      },
    ]);
  });

  it("leaves an empty free limit and floor unset (null), never zero", () => {
    const r = buildCreateRequest(validForm(), NOW);
    expect(r.ok && r.request.free_tickets_annual_limit).toBeNull();
    expect(r.ok && r.request.floor_per_ticket_minor).toBeNull();
  });

  it("refuses a start in the past or missing, and numbers it cannot read", () => {
    const past = buildCreateRequest(validForm({ effectiveFromLocal: "2026-10-01T00:00" }), NOW);
    expect(past.ok).toBe(false);
    const none = buildCreateRequest(validForm({ effectiveFromLocal: "" }), NOW);
    expect(none.ok).toBe(false);
    const bad = buildCreateRequest(validForm({ bonusShareCapBp: "3005" }), NOW);
    expect(!bad.ok && bad.errors.join(" ")).toContain("Bonus share cap");
    const noCap = buildCreateRequest(validForm({ bonusShareCapBp: "" }), NOW);
    expect(noCap.ok).toBe(false);
  });

  it("refuses an empty rule list, an unfilled number and two rules for one slot", () => {
    expect(buildCreateRequest(validForm({ rules: [] }), NOW).ok).toBe(false);
    const blank = buildCreateRequest(validForm({ rules: [emptyRule("fixed_per_ticket", "EUR")] }), NOW);
    expect(!blank.ok && blank.errors.join(" ")).toContain("Ticket without a seat");
    const twice = buildCreateRequest(
      validForm({
        rules: [
          { ...emptyRule("fixed_per_ticket"), params: { unseated_minor: "1", seated_minor: "2" } },
          { ...emptyRule("fixed_per_ticket"), params: { unseated_minor: "3", seated_minor: "4" } },
        ],
      }),
      NOW,
    );
    expect(!twice.ok && twice.errors.join(" ")).toContain("same type");
  });

  it("builds a low-price rule with its sale currency and checks min against max", () => {
    const low = {
      ...emptyRule("low_price_formula", "EUR"),
      params: {
        threshold_minor: "500",
        strict_below: "true",
        pct_bp: "300",
        min_minor: "5",
        max_minor: "30",
        seated_surcharge_minor: "7",
      },
    };
    const ok = buildCreateRequest(validForm({ rules: [low] }), NOW);
    expect(ok.ok && ok.request.rules[0]).toMatchObject({ sale_currency: "EUR", params: { strict_below: true, pct_bp: 300 } });
    const swapped = buildCreateRequest(validForm({ rules: [{ ...low, params: { ...low.params, min_minor: "40" } }] }), NOW);
    expect(!swapped.ok && swapped.errors.join(" ")).toContain("smallest charge");
    const noCur = buildCreateRequest(validForm({ rules: [{ ...low, saleCurrency: "" }] }), NOW);
    expect(noCur.ok).toBe(false);
  });

  it("checks packs: tier, face value, duplicates", () => {
    const pack = { ...emptyPack(), tier: "1", faceMinor: "1000", unseatedRateMinor: "50", seatedSurchargeMinor: "30" };
    const ok = buildCreateRequest(validForm({ packs: [pack] }), NOW);
    expect(ok.ok && ok.request.packs).toEqual([
      { tier: 1, face_minor: 1000, unseated_rate_minor: 50, seated_surcharge_minor: 30, active: true },
    ]);
    expect(buildCreateRequest(validForm({ packs: [pack, { ...pack }] }), NOW).ok).toBe(false);
    expect(buildCreateRequest(validForm({ packs: [{ ...pack, faceMinor: "0" }] }), NOW).ok).toBe(false);
    expect(buildCreateRequest(validForm({ packs: [emptyPack()] }), NOW).ok).toBe(false);
  });
});

describe("formFromDetail", () => {
  const detail = {
    id: "id-1",
    code: "list-a",
    version: 2,
    currency: "EUR",
    effective_from: "2026-01-01T00:00:00Z",
    revoked_at: null,
    locked: true,
    free_tickets_annual_limit: null,
    floor_per_ticket_minor: 15,
    bonus_share_cap_bp: 2500,
    bonus_covers_surcharge: true,
    created_by: null,
    reason: null,
    created_at: "2026-01-01T00:00:00Z",
    status: "in_force",
    rules: [
      {
        id: "r1",
        rule_type: "fixed_per_ticket",
        line: "standard",
        event_kind: "ticket_sold",
        sale_currency: null,
        params: { unseated_minor: 71, seated_minor: 133 },
        sort_order: 10,
      },
    ],
    packs: [
      {
        id: "p1",
        tier: 3,
        currency: "EUR",
        face_minor: 900,
        unseated_rate_minor: 40,
        seated_surcharge_minor: 20,
        active: false,
        sort_order: 3,
      },
    ],
  } as PriceListDetail;

  it("copies numbers into the form and leaves the start empty", () => {
    const f = formFromDetail(detail);
    expect(f.effectiveFromLocal).toBe("");
    expect(f.bonusShareCapBp).toBe("2500");
    expect(f.floorMinor).toBe("15");
    expect(f.freeLimit).toBe("");
    expect(f.locked).toBe(true);
    expect(f.rules[0]?.params).toEqual({ unseated_minor: "71", seated_minor: "133" });
    expect(f.packs[0]).toMatchObject({ tier: "3", faceMinor: "900", active: false });
  });

  it("round-trips into a request the server accepts", () => {
    const f = { ...formFromDetail(detail), effectiveFromLocal: FUTURE };
    const r = buildCreateRequest(f, NOW);
    expect(r.ok).toBe(true);
  });
});

describe("rates and terms forms", () => {
  it("builds a rate request, refuses a past start and a missing organization", () => {
    const ok = buildRateRequest(
      { ...emptyRateForm(), orgId: "org-1", currency: "EUR", unseatedMinor: "50", seatedMinor: "100", effectiveFromLocal: FUTURE, note: " contract " },
      NOW,
    );
    expect(ok.ok && ok.request).toMatchObject({ org_id: "org-1", unseated_minor: 50, seated_minor: 100, note: "contract" });
    expect(ok.ok && "effective_to" in ok.request).toBe(false);
    expect(buildRateRequest({ ...emptyRateForm(), currency: "EUR", unseatedMinor: "1", seatedMinor: "2", effectiveFromLocal: FUTURE }, NOW).ok).toBe(false);
    const past = buildRateRequest(
      { ...emptyRateForm(), orgId: "o", currency: "EUR", unseatedMinor: "1", seatedMinor: "2", effectiveFromLocal: "2026-10-01T00:00" },
      NOW,
    );
    expect(past.ok).toBe(false);
    const endBeforeStart = buildRateRequest(
      { ...emptyRateForm(), orgId: "o", currency: "EUR", unseatedMinor: "1", seatedMinor: "2", effectiveFromLocal: FUTURE, effectiveToLocal: "2026-10-30T00:00" },
      NOW,
    );
    expect(endBeforeStart.ok).toBe(false);
  });

  it("builds a terms request and keeps a tools account prepaid", () => {
    const f = { ...termsFormFrom(undefined), effectiveFromLocal: FUTURE, priceListCode: "b15-test" };
    const ok = buildTermsRequest(f, "organizer", NOW);
    expect(ok.ok && ok.request).toMatchObject({ price_list_code: "b15-test", plan_kind: "standard", billing_cycle: "monthly" });
    expect(buildTermsRequest(f, "tools", NOW).ok).toBe(false);
    expect(buildTermsRequest({ ...f, billingCycle: "prepaid" }, "tools", NOW).ok).toBe(true);
    expect(buildTermsRequest({ ...f, priceListCode: "" }, "organizer", NOW).ok).toBe(false);
    expect(buildTermsRequest({ ...f, planKind: "venue" }, "organizer", NOW).ok && true).toBe(true);
  });
});

describe("status helpers and grouping", () => {
  it("offers revoke only for a version that has not started", () => {
    expect(canRevokeVersion({ status: "scheduled" })).toBe(true);
    for (const s of ["in_force", "superseded", "revoked"]) expect(canRevokeVersion({ status: s }), s).toBe(false);
  });

  it("offers close until a rate ended and remove only before it started", () => {
    expect(canCloseRate({ status: "in_force" })).toBe(true);
    expect(canCloseRate({ status: "scheduled" })).toBe(true);
    expect(canCloseRate({ status: "ended" })).toBe(false);
    expect(canRemoveRate({ status: "scheduled" })).toBe(true);
    expect(canRemoveRate({ status: "in_force" })).toBe(false);
    expect(canRemoveRate({ status: "ended" })).toBe(false);
  });

  const v = (code: string, currency: string, version: number, status: string): PriceListVersion =>
    ({ id: `${code}-${currency}-${version}`, code, currency, version, status }) as PriceListVersion;

  it("groups by code and currency, newest version first, and lists usable codes", () => {
    const items = [v("b", "EUR", 1, "in_force"), v("a", "EUR", 1, "superseded"), v("a", "EUR", 2, "scheduled"), v("a", "USD", 1, "in_force"), v("c", "EUR", 1, "revoked")];
    const g = groupSeries(items);
    expect(g.map((s) => s.key)).toEqual(["a|EUR", "a|USD", "b|EUR", "c|EUR"]);
    expect(g[0]?.versions.map((x) => x.version)).toEqual([2, 1]);
    expect(priceListCodes(items, "EUR")).toEqual(["a", "b"]);
  });
});

describe("the reason gate", () => {
  it("asks for X-Admin-Reason on writes of /v1/admin/billing, not on reads", () => {
    for (const m of ["POST", "DELETE"]) {
      expect(requiresAdminReason("/v1/admin/billing/price-lists", m), m).toBe(true);
      expect(requiresAdminReason("/v1/admin/billing/rate-overrides/abc", m), m).toBe(true);
      expect(requiresAdminReason("/v1/admin/billing/accounts/abc/terms", m), m).toBe(true);
    }
    expect(requiresAdminReason("/v1/admin/billing/price-lists", "GET")).toBe(false);
    expect(requiresAdminReason("/v1/admin/billing/accounts", "GET")).toBe(false);
  });
});

describe("complimentary billing of the account terms", () => {
  const f = { ...termsFormFrom(undefined), effectiveFromLocal: FUTURE, priceListCode: "b15-test" };
  const current = { price_list_code: "b15-test", plan_kind: "venue", billing_cycle: "prepaid", shortfall_policy: "block_publishing", fee_mode: "buyer_pays", invoice_delay_days: 3, due_days: 14 };

  it("defaults to free and sends the choice with the request", () => {
    expect(f.complimentaryBilling).toBe("free");
    const free = buildTermsRequest(f, "organizer", NOW);
    expect(free.ok && free.request.complimentary_billing).toBe("free");
    const std = buildTermsRequest({ ...f, complimentaryBilling: "standard" }, "organizer", NOW);
    expect(std.ok && std.request.complimentary_billing).toBe("standard");
  });

  it("pre-fills the current value and keeps the other terms fields unchanged", () => {
    const form = { ...termsFormFrom({ ...current, complimentary_billing: "standard" } as never), effectiveFromLocal: FUTURE };
    expect(form.complimentaryBilling).toBe("standard");
    expect(termsFormFrom({ ...current, complimentary_billing: "free" } as never).complimentaryBilling).toBe("free");
    expect(termsFormFrom({ ...current, complimentary_billing: undefined } as never).complimentaryBilling).toBe("free");
    const before = buildTermsRequest(form, "organizer", NOW);
    const after = buildTermsRequest({ ...form, complimentaryBilling: "free" }, "organizer", NOW);
    expect(before.ok && after.ok).toBe(true);
    if (before.ok && after.ok) {
      const { complimentary_billing: a, ...restBefore } = before.request;
      const { complimentary_billing: b, ...restAfter } = after.request;
      expect([a, b]).toEqual(["standard", "free"]);
      expect(restAfter).toEqual(restBefore);
      expect(restBefore).toMatchObject({ plan_kind: "venue", billing_cycle: "prepaid", shortfall_policy: "block_publishing", fee_mode: "buyer_pays", invoice_delay_days: 3, due_days: 14 });
    }
  });
});

describe("fee mode of the account terms", () => {
  const f = { ...termsFormFrom(undefined), effectiveFromLocal: FUTURE, priceListCode: "b15-test" };

  it("defaults to organizer pays and sends the choice", () => {
    expect(f.feeMode).toBe("organizer_pays");
    const ok = buildTermsRequest(f, "organizer", NOW);
    expect(ok.ok && ok.request.fee_mode).toBe("organizer_pays");
    const buyer = buildTermsRequest({ ...f, feeMode: "buyer_pays" }, "organizer", NOW);
    expect(buyer.ok && buyer.request.fee_mode).toBe("buyer_pays");
  });

  it("pre-fills the form from the terms in force", () => {
    const current = { price_list_code: "b15-test", plan_kind: "standard", billing_cycle: "monthly", shortfall_policy: "invoice_postpaid", fee_mode: "buyer_pays" };
    expect(termsFormFrom(current as never).feeMode).toBe("buyer_pays");
    expect(termsFormFrom({ ...current, fee_mode: "organizer_pays" } as never).feeMode).toBe("organizer_pays");
    expect(termsFormFrom({ ...current, fee_mode: undefined } as never).feeMode).toBe("organizer_pays");
  });
});
