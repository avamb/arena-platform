/**
 * Unit tests for the pure helpers behind the Promo codes admin screen:
 * discount/validity/limit labels, the RFC3339 <-> datetime-local
 * conversions, form validation, request-body building, the CSV filename,
 * and the server-error -> message mapping.
 */
import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/client";
import {
  buildCreatePromoCodeBody,
  buildSessionOptions,
  buildStatusPatchBody,
  discountLabel,
  emptyPromoForm,
  limitLabel,
  localDatetimeToRFC3339,
  mapPromoError,
  promoCodesLink,
  redemptionsCsvFilename,
  rfc3339ToLocalDatetime,
  sessionsCellLabel,
  sessionsCellTitle,
  validatePromoForm,
  validityLabel,
  type PromoCode,
  type PromoFormValues,
} from "@/routes/promoCodes";

function promoCode(overrides: Partial<PromoCode> = {}): PromoCode {
  return {
    id: "code-1",
    org_id: "org-1",
    code: "SUMMER25",
    discount_type: "percent",
    discount_value: 25,
    currency: null,
    applies_to_tier_ids: [],
    applies_to_session_ids: [],
    uses: 0,
    discount_total: 0,
    last_used_at: null,
    max_uses: null,
    max_uses_per_customer: null,
    valid_from: null,
    valid_until: null,
    min_order_amount: 0,
    status: "active",
    created_at: "2026-06-01T00:00:00Z",
    updated_at: "2026-06-01T00:00:00Z",
    ...overrides,
  };
}

describe("promoCodesLink", () => {
  it("builds the org-scoped URL", () => {
    expect(promoCodesLink("org-1")).toBe("/organizations/org-1/promo-codes");
  });
  it("encodes the org id", () => {
    expect(promoCodesLink("a b")).toBe("/organizations/a%20b/promo-codes");
  });
});

describe("discountLabel", () => {
  it("renders a percent code as a percentage", () => {
    expect(discountLabel({ discount_type: "percent", discount_value: 25, currency: null })).toBe(
      "25 %",
    );
  });
  it("renders a fixed-amount code as money", () => {
    expect(
      discountLabel({ discount_type: "fixed_amount", discount_value: 5000, currency: "CZK" }),
    ).toBe("50.00 CZK");
  });
});

describe("validityLabel", () => {
  it("says 'always' when both bounds are null", () => {
    expect(validityLabel(null, null)).toBe("always");
  });
  it("renders both bounds when set", () => {
    expect(validityLabel("2026-06-01T00:00:00Z", "2026-08-31T23:59:59Z")).toBe(
      "2026-06-01 00:00Z – 2026-08-31 23:59Z",
    );
  });
  it("renders one-sided ranges", () => {
    expect(validityLabel(null, "2026-08-31T23:59:59Z")).toBe("any time – 2026-08-31 23:59Z");
    expect(validityLabel("2026-06-01T00:00:00Z", null)).toBe("2026-06-01 00:00Z – no end");
  });
});

describe("limitLabel", () => {
  it("renders the infinity symbol for null", () => {
    expect(limitLabel(null)).toBe("∞");
  });
  it("renders the number otherwise", () => {
    expect(limitLabel(5)).toBe("5");
    expect(limitLabel(0)).toBe("0");
  });
});

describe("sessionsCellLabel / sessionsCellTitle", () => {
  it("says 'any' for an empty session list", () => {
    expect(sessionsCellLabel([])).toBe("any");
    expect(sessionsCellTitle([], new Map())).toMatch(/any session/);
  });
  it("counts sessions, singular vs plural", () => {
    expect(sessionsCellLabel(["s1"])).toBe("1 session");
    expect(sessionsCellLabel(["s1", "s2"])).toBe("2 sessions");
  });
  it("lists session labels in the hover title, falling back to the id", () => {
    const byId = new Map([["s1", "Gala — 2026-10-13 17:00Z (Main Hall)"]]);
    expect(sessionsCellTitle(["s1", "s2"], byId)).toBe(
      "Gala — 2026-10-13 17:00Z (Main Hall)\ns2",
    );
  });
});

describe("buildSessionOptions", () => {
  it("composes 'Event — start (venue)' labels and sorts them", () => {
    const events = [
      { id: "e1", name: "Zeta Fest" },
      { id: "e2", name: "Alpha Gala" },
    ];
    const sessions = [
      { id: "s1", event_id: "e1", venue_id: "v1", start_at: "2026-10-13T17:00:00Z" },
      { id: "s2", event_id: "e2", venue_id: "v2", start_at: "2026-11-01T18:00:00Z" },
    ];
    const venues = [
      { id: "v1", name: "Main Hall" },
      { id: "v2", name: "Small Room" },
    ];
    const options = buildSessionOptions(events, sessions, venues);
    expect(options).toEqual([
      { id: "s2", label: "Alpha Gala — 2026-11-01 18:00Z (Small Room)" },
      { id: "s1", label: "Zeta Fest — 2026-10-13 17:00Z (Main Hall)" },
    ]);
  });
  it("falls back gracefully for unknown event/venue ids", () => {
    const options = buildSessionOptions(
      [],
      [{ id: "s1", event_id: "missing", venue_id: "missing", start_at: "2026-10-13T17:00:00Z" }],
      [],
    );
    expect(options[0]?.label).toBe("Unknown event — 2026-10-13 17:00Z (Unknown venue)");
  });
});

describe("localDatetimeToRFC3339 / rfc3339ToLocalDatetime", () => {
  it("converts a datetime-local value to RFC3339 UTC", () => {
    expect(localDatetimeToRFC3339("2026-06-01T00:00")).toBe("2026-06-01T00:00:00Z");
  });
  it("returns null for a blank value", () => {
    expect(localDatetimeToRFC3339("  ")).toBeNull();
  });
  it("round-trips an RFC3339 timestamp back to datetime-local", () => {
    expect(rfc3339ToLocalDatetime("2026-06-01T00:00:00Z")).toBe("2026-06-01T00:00");
  });
  it("returns '' for null or an unparseable timestamp", () => {
    expect(rfc3339ToLocalDatetime(null)).toBe("");
    expect(rfc3339ToLocalDatetime("not-a-date")).toBe("");
  });
});

describe("validatePromoForm", () => {
  function form(overrides: Partial<PromoFormValues> = {}): PromoFormValues {
    return { ...emptyPromoForm(), code: "SUMMER25", value: "25", ...overrides };
  }

  it("accepts a valid percent form", () => {
    expect(validatePromoForm(form())).toEqual({});
  });

  it("requires a code", () => {
    expect(validatePromoForm(form({ code: "  " })).code).toMatch(/required/);
  });

  it("requires a percent value in [1, 100]", () => {
    expect(validatePromoForm(form({ value: "0" })).value).toBeDefined();
    expect(validatePromoForm(form({ value: "101" })).value).toBeDefined();
    expect(validatePromoForm(form({ value: "50" })).value).toBeUndefined();
  });

  it("requires a positive decimal and a currency for fixed_amount", () => {
    const errs = validatePromoForm(
      form({ discount_type: "fixed_amount", value: "", currency: "" }),
    );
    expect(errs.value).toBeDefined();
    expect(errs.currency).toBeDefined();
  });

  it("accepts a valid fixed_amount form", () => {
    const errs = validatePromoForm(
      form({ discount_type: "fixed_amount", value: "12.50", currency: "EUR" }),
    );
    expect(errs).toEqual({});
  });

  it("rejects a non-3-letter currency", () => {
    const errs = validatePromoForm(
      form({ discount_type: "fixed_amount", value: "12.50", currency: "EURO" }),
    );
    expect(errs.currency).toBeDefined();
  });

  it("rejects a non-positive max_uses / max_uses_per_customer", () => {
    expect(validatePromoForm(form({ max_uses: "0" })).max_uses).toBeDefined();
    expect(validatePromoForm(form({ max_uses: "-1" })).max_uses).toBeDefined();
    expect(
      validatePromoForm(form({ max_uses_per_customer: "abc" })).max_uses_per_customer,
    ).toBeDefined();
  });

  it("allows blank max_uses / max_uses_per_customer (unlimited)", () => {
    expect(validatePromoForm(form({ max_uses: "", max_uses_per_customer: "" }))).toEqual({});
  });

  it("requires valid_until after valid_from when both are set", () => {
    const errs = validatePromoForm(
      form({ valid_from: "2026-08-01T00:00", valid_until: "2026-06-01T00:00" }),
    );
    expect(errs.valid_until).toMatch(/after/);
  });

  it("accepts a valid_from/valid_until window in order", () => {
    const errs = validatePromoForm(
      form({ valid_from: "2026-06-01T00:00", valid_until: "2026-08-31T23:59" }),
    );
    expect(errs).toEqual({});
  });
});

describe("buildCreatePromoCodeBody", () => {
  it("sends an integer percent value and always [] tier ids / 0 min order", () => {
    const body = buildCreatePromoCodeBody({
      ...emptyPromoForm(),
      code: " SUMMER25 ",
      value: "25",
      session_ids: ["s1", "s2"],
    });
    expect(body).toMatchObject({
      code: "SUMMER25",
      discount_type: "percent",
      discount_value: 25,
      applies_to_tier_ids: [],
      applies_to_session_ids: ["s1", "s2"],
      min_order_amount: 0,
      status: "active",
    });
    expect(body.currency).toBeUndefined();
  });

  it("converts a fixed_amount decimal to minor units and includes currency", () => {
    const body = buildCreatePromoCodeBody({
      ...emptyPromoForm(),
      code: "WELCOME10",
      discount_type: "fixed_amount",
      value: "12.50",
      currency: "eur",
    });
    expect(body.discount_value).toBe(1250);
    expect(body.currency).toBe("EUR");
  });

  it("sends null for blank usage caps and validity bounds", () => {
    const body = buildCreatePromoCodeBody({ ...emptyPromoForm(), code: "X", value: "10" });
    expect(body.max_uses).toBeNull();
    expect(body.max_uses_per_customer).toBeNull();
    expect(body.valid_from).toBeNull();
    expect(body.valid_until).toBeNull();
  });

  it("converts usage caps and validity bounds when set", () => {
    const body = buildCreatePromoCodeBody({
      ...emptyPromoForm(),
      code: "X",
      value: "10",
      max_uses: "100",
      max_uses_per_customer: "1",
      valid_from: "2026-06-01T00:00",
      valid_until: "2026-08-31T23:59",
    });
    expect(body.max_uses).toBe(100);
    expect(body.max_uses_per_customer).toBe(1);
    expect(body.valid_from).toBe("2026-06-01T00:00:00Z");
    expect(body.valid_until).toBe("2026-08-31T23:59:00Z");
  });
});

describe("buildStatusPatchBody", () => {
  it("pauses an active code", () => {
    expect(buildStatusPatchBody(promoCode({ status: "active" }))).toEqual({ status: "paused" });
  });
  it("activates a paused code", () => {
    expect(buildStatusPatchBody(promoCode({ status: "paused" }))).toEqual({ status: "active" });
  });
});

describe("redemptionsCsvFilename", () => {
  it("names an org-wide file when no code is given", () => {
    expect(redemptionsCsvFilename()).toMatch(/^promo-codes-redemptions-\d{4}-\d{2}-\d{2}\.csv$/);
  });
  it("names a per-code file, sanitizing the code for the filename", () => {
    expect(redemptionsCsvFilename("SUMMER 25!")).toMatch(
      /^promo-code-SUMMER-25-redemptions-\d{4}-\d{2}-\d{2}\.csv$/,
    );
  });
});

describe("mapPromoError", () => {
  it.each([
    ["promo.invalid_code", /required/],
    ["promo.invalid_discount_type", /percent or fixed/],
    ["promo.invalid_discount_value", /invalid/],
    ["promo.invalid_currency", /3-letter/],
    ["promo.currency_required", /Currency is required/],
    ["promo.invalid_session_id", /sessions is invalid/],
    ["promo.invalid_session", /does not belong/],
    ["promo.invalid_valid_from", /Valid-from/],
    ["promo.invalid_valid_until", /Valid-until/],
    ["promo.duplicate", /already exists/],
    ["permissions.denied", /missing the required permission/],
  ] as const)("maps %s to a readable message", (code, expected) => {
    const err = new ApiError(400, { code, message: "raw" });
    expect(mapPromoError(err)).toMatch(expected);
  });

  it("falls back to a generic 401/403/unknown message", () => {
    expect(mapPromoError(new ApiError(401, { code: "x", message: "y" }))).toMatch(
      /Session expired/,
    );
    expect(mapPromoError(new ApiError(403, { code: "x", message: "y" }))).toMatch(/Forbidden/);
    expect(mapPromoError(new ApiError(500, { code: "boom", message: "kaboom" }))).toBe(
      "kaboom (boom)",
    );
  });
});
