/**
 * Unit tests for the Events admin route (feature #281 / E-3).
 *
 * Pure-function coverage only; the React surface (list table, drawer,
 * tab switching) is exercised by the route tree smoke build, not by
 * this suite. We pin the helpers exposed by events.tsx so a regression
 * in the lifecycle transition table, the client-side filter combinators,
 * or the pagination math surfaces before the DOM does.
 */
import { describe, expect, it } from "vitest";
import {
  EVENT_STATUSES,
  EVENT_VISIBILITIES,
  PAGE_SIZE,
  SESSION_STATUSES,
  allowedTransitions,
  emptySessionForm,
  filterEventsByDateRange,
  filterEventsByOrg,
  filterEventsByStatus,
  findOverlappingSessions,
  formatDateOnly,
  formatDateTime,
  isEventStatus,
  isEventVisibility,
  isSessionStatus,
  mapSessionError,
  paginate,
  parseLocalDatetime,
  posterInitial,
  sessionToForm,
  toLocalDatetimeValue,
  toRFC3339,
  validateSessionForm,
  TIER_PRICING_MODES,
  PROVIDER_CURRENCIES,
  allowedCurrenciesForProviders,
  buildTierRequestBody,
  centsToDecimal,
  decimalToCents,
  emptyTierForm,
  isTierPricingMode,
  mapTierError,
  tierToForm,
  validateTierForm,
  type EventItem,
  type SessionFormValues,
  type TicketTierItem,
  type TierFormValues,
} from "@/routes/events";
import { ApiError } from "@/lib/api/client";

function ev(overrides: Partial<EventItem>): EventItem {
  return {
    id: "01929d0e-0e47-7000-8000-000000000301",
    org_id: "01929d0e-0e47-7000-8000-000000000001",
    venue_id: null,
    name: "Test Event",
    description: null,
    status: "draft",
    start_at: "2026-08-15T18:00:00Z",
    end_at: "2026-08-15T23:00:00Z",
    visibility: "public",
    image_url: null,
    created_at: "2026-06-01T00:00:00Z",
    updated_at: "2026-06-02T12:34:56Z",
    ...overrides,
  };
}

describe("EVENT_STATUSES / EVENT_VISIBILITIES", () => {
  it("enumerates the four OpenAPI lifecycle statuses in canonical order", () => {
    expect(EVENT_STATUSES).toEqual(["draft", "published", "cancelled", "archived"]);
  });

  it("enumerates the three OpenAPI visibility values in canonical order", () => {
    expect(EVENT_VISIBILITIES).toEqual(["public", "private", "unlisted"]);
  });

  it("isEventStatus rejects unknown values", () => {
    expect(isEventStatus("draft")).toBe(true);
    expect(isEventStatus("published")).toBe(true);
    expect(isEventStatus("DRAFT")).toBe(false);
    expect(isEventStatus("")).toBe(false);
    expect(isEventStatus("deleted")).toBe(false);
  });

  it("isEventVisibility rejects unknown values", () => {
    expect(isEventVisibility("public")).toBe(true);
    expect(isEventVisibility("unlisted")).toBe(true);
    expect(isEventVisibility("all")).toBe(false);
    expect(isEventVisibility("")).toBe(false);
  });
});

describe("allowedTransitions", () => {
  it("mirrors the backend state machine exactly", () => {
    expect(allowedTransitions("draft")).toEqual(["published", "cancelled"]);
    expect(allowedTransitions("published")).toEqual(["cancelled", "archived"]);
    expect(allowedTransitions("cancelled")).toEqual(["archived"]);
    expect(allowedTransitions("archived")).toEqual([]);
  });

  it("never returns the current status (re-applying is a no-op, not a UI option)", () => {
    for (const s of EVENT_STATUSES) {
      expect(allowedTransitions(s)).not.toContain(s);
    }
  });

  it("only returns valid EventStatus values", () => {
    for (const s of EVENT_STATUSES) {
      for (const t of allowedTransitions(s)) {
        expect(isEventStatus(t)).toBe(true);
      }
    }
  });
});

describe("filterEventsByOrg", () => {
  const events = [
    ev({ id: "a", org_id: "01929d0e-0e47-7000-8000-000000000001" }),
    ev({ id: "b", org_id: "01929d0e-0e47-7000-8000-000000000002" }),
    ev({ id: "c", org_id: "01929d0e-0e47-7000-8000-000000000001" }),
  ];

  it("returns the input untouched when the filter is empty", () => {
    expect(filterEventsByOrg(events, "")).toBe(events);
    expect(filterEventsByOrg(events, "   ")).toBe(events);
  });

  it("filters by exact org_id match", () => {
    const out = filterEventsByOrg(
      events,
      "01929d0e-0e47-7000-8000-000000000001",
    );
    expect(out.map((e) => e.id)).toEqual(["a", "c"]);
  });
});

describe("filterEventsByStatus", () => {
  const events = [
    ev({ id: "a", status: "draft" }),
    ev({ id: "b", status: "published" }),
    ev({ id: "c", status: "cancelled" }),
  ];

  it("returns the input untouched on empty filter", () => {
    expect(filterEventsByStatus(events, "")).toBe(events);
  });

  it("filters by exact status", () => {
    expect(filterEventsByStatus(events, "published").map((e) => e.id)).toEqual(["b"]);
  });
});

describe("filterEventsByDateRange", () => {
  const events = [
    ev({ id: "early", start_at: "2026-07-01T10:00:00Z" }),
    ev({ id: "mid", start_at: "2026-08-15T18:00:00Z" }),
    ev({ id: "late", start_at: "2026-09-30T20:00:00Z" }),
  ];

  it("returns the input untouched when both bounds are empty", () => {
    expect(filterEventsByDateRange(events, "", "")).toBe(events);
    expect(filterEventsByDateRange(events, "  ", "  ")).toBe(events);
  });

  it("filters with only a lower bound", () => {
    const out = filterEventsByDateRange(events, "2026-08-01", "");
    expect(out.map((e) => e.id)).toEqual(["mid", "late"]);
  });

  it("filters with only an upper bound", () => {
    const out = filterEventsByDateRange(events, "", "2026-08-15");
    expect(out.map((e) => e.id)).toEqual(["early", "mid"]);
  });

  it("filters with both bounds (inclusive)", () => {
    const out = filterEventsByDateRange(events, "2026-08-15", "2026-08-15");
    expect(out.map((e) => e.id)).toEqual(["mid"]);
  });

  it("filters out everything when range excludes all events", () => {
    expect(filterEventsByDateRange(events, "2027-01-01", "")).toEqual([]);
    expect(filterEventsByDateRange(events, "", "2026-01-01")).toEqual([]);
  });
});

describe("paginate", () => {
  const items = Array.from({ length: 57 }, (_, i) => i);

  it("returns first page when page=1", () => {
    const out = paginate(items, 1, 25);
    expect(out.rows.length).toBe(25);
    expect(out.rows[0]).toBe(0);
    expect(out.page).toBe(1);
    expect(out.totalPages).toBe(3);
  });

  it("returns last partial page", () => {
    const out = paginate(items, 3, 25);
    expect(out.rows.length).toBe(7);
    expect(out.rows[0]).toBe(50);
    expect(out.page).toBe(3);
  });

  it("clamps overflow page to the last page", () => {
    const out = paginate(items, 999, 25);
    expect(out.page).toBe(3);
  });

  it("clamps underflow page to 1", () => {
    const out = paginate(items, 0, 25);
    expect(out.page).toBe(1);
    const neg = paginate(items, -5, 25);
    expect(neg.page).toBe(1);
  });

  it("totalPages is always >= 1 even when the input is empty", () => {
    const out = paginate([], 1, 25);
    expect(out.rows).toEqual([]);
    expect(out.totalPages).toBe(1);
    expect(out.page).toBe(1);
  });

  it("PAGE_SIZE matches the documented client-side default of 25", () => {
    expect(PAGE_SIZE).toBe(25);
  });
});

describe("formatDateTime / formatDateOnly", () => {
  it("formats ISO timestamps as UTC YYYY-MM-DD HH:MM UTC", () => {
    expect(formatDateTime("2026-08-15T18:00:00Z")).toBe("2026-08-15 18:00 UTC");
    expect(formatDateTime("2026-01-02T03:04:05Z")).toBe("2026-01-02 03:04 UTC");
  });

  it("returns the input on unparseable timestamps", () => {
    expect(formatDateTime("not-a-date")).toBe("not-a-date");
  });

  it("formatDateOnly extracts the YYYY-MM-DD prefix", () => {
    expect(formatDateOnly("2026-08-15T18:00:00Z")).toBe("2026-08-15");
    expect(formatDateOnly("")).toBe("");
  });
});

describe("SESSION_STATUSES / isSessionStatus", () => {
  it("enumerates the four backend lifecycle statuses in canonical order", () => {
    expect(SESSION_STATUSES).toEqual([
      "draft",
      "scheduled",
      "cancelled",
      "completed",
    ]);
  });
  it("isSessionStatus accepts canonical values only", () => {
    expect(isSessionStatus("draft")).toBe(true);
    expect(isSessionStatus("scheduled")).toBe(true);
    expect(isSessionStatus("Completed")).toBe(false);
    expect(isSessionStatus("")).toBe(false);
    expect(isSessionStatus("archived")).toBe(false);
  });
});

describe("parseLocalDatetime / toLocalDatetimeValue / toRFC3339", () => {
  it("parseLocalDatetime returns null on blank or unparseable input", () => {
    expect(parseLocalDatetime("")).toBeNull();
    expect(parseLocalDatetime("   ")).toBeNull();
    expect(parseLocalDatetime("not-a-date")).toBeNull();
  });
  it("parseLocalDatetime parses a datetime-local string into a Date", () => {
    const d = parseLocalDatetime("2026-08-15T18:00");
    expect(d).not.toBeNull();
    expect(d!.getTime()).toBeGreaterThan(0);
  });
  it("toLocalDatetimeValue formats an ISO timestamp as UTC datetime-local", () => {
    expect(toLocalDatetimeValue("2026-08-15T18:00:00Z")).toBe("2026-08-15T18:00");
    expect(toLocalDatetimeValue("2026-01-02T03:04:05Z")).toBe("2026-01-02T03:04");
  });
  it("toLocalDatetimeValue returns empty string on invalid input", () => {
    expect(toLocalDatetimeValue("nope")).toBe("");
  });
  it("toRFC3339 round-trips through toLocalDatetimeValue losslessly to the minute", () => {
    const original = "2026-08-15T18:00:00Z";
    const local = toLocalDatetimeValue(original);
    expect(toRFC3339(local)).toBe(original);
  });
});

describe("emptySessionForm / sessionToForm", () => {
  it("emptySessionForm starts with blank times and capacity, draft status", () => {
    const f = emptySessionForm();
    expect(f.start_at).toBe("");
    expect(f.end_at).toBe("");
    expect(f.capacity_total).toBe("");
    expect(f.status).toBe("draft");
  });
  it("sessionToForm hydrates fields from an existing session row", () => {
    const f = sessionToForm({
      start_at: "2026-08-15T18:00:00Z",
      end_at: "2026-08-15T23:00:00Z",
      capacity_total: 250,
      status: "scheduled",
    });
    expect(f).toEqual({
      start_at: "2026-08-15T18:00",
      end_at: "2026-08-15T23:00",
      capacity_total: "250",
      status: "scheduled",
    });
  });
  it("sessionToForm falls back to draft when the status is unknown", () => {
    const f = sessionToForm({
      start_at: "2026-08-15T18:00:00Z",
      end_at: "2026-08-15T23:00:00Z",
      capacity_total: 1,
      status: "garbage",
    });
    expect(f.status).toBe("draft");
  });
});

describe("validateSessionForm", () => {
  function form(o: Partial<SessionFormValues>): SessionFormValues {
    return {
      start_at: "2026-08-15T18:00",
      end_at: "2026-08-15T23:00",
      capacity_total: "100",
      status: "draft",
      ...o,
    };
  }

  it("accepts a fully valid form", () => {
    expect(validateSessionForm(form({}))).toEqual({});
  });
  it("requires both start and end", () => {
    expect(validateSessionForm(form({ start_at: "" })).start_at).toBeDefined();
    expect(validateSessionForm(form({ end_at: "" })).end_at).toBeDefined();
  });
  it("rejects end_at <= start_at (mirroring server CHECK)", () => {
    expect(
      validateSessionForm(form({ end_at: "2026-08-15T18:00" })).end_at,
    ).toBeDefined();
    expect(
      validateSessionForm(form({ end_at: "2026-08-15T17:00" })).end_at,
    ).toBeDefined();
  });
  it("requires capacity_total to be a positive integer", () => {
    expect(validateSessionForm(form({ capacity_total: "" })).capacity_total).toBeDefined();
    expect(validateSessionForm(form({ capacity_total: "0" })).capacity_total).toBeDefined();
    expect(validateSessionForm(form({ capacity_total: "-5" })).capacity_total).toBeDefined();
    expect(validateSessionForm(form({ capacity_total: "1.5" })).capacity_total).toBeDefined();
    expect(validateSessionForm(form({ capacity_total: "abc" })).capacity_total).toBeDefined();
  });
  it("rejects capacity_total that would overflow int32", () => {
    expect(
      validateSessionForm(form({ capacity_total: "9999999999" })).capacity_total,
    ).toBeDefined();
  });
  it("rejects an invalid status value", () => {
    expect(
      validateSessionForm(form({ status: "archived" as never })).status,
    ).toBeDefined();
  });
});

describe("findOverlappingSessions", () => {
  const siblings = [
    {
      id: "s1",
      start_at: "2026-08-15T18:00:00Z",
      end_at: "2026-08-15T20:00:00Z",
    },
    {
      id: "s2",
      start_at: "2026-08-15T22:00:00Z",
      end_at: "2026-08-16T00:00:00Z",
    },
  ];

  it("returns empty when the range is invalid", () => {
    expect(findOverlappingSessions(siblings, "", "", null)).toEqual([]);
    expect(
      findOverlappingSessions(
        siblings,
        "2026-08-15T20:00",
        "2026-08-15T19:00",
        null,
      ),
    ).toEqual([]);
  });
  it("detects an exact-overlap range", () => {
    const out = findOverlappingSessions(
      siblings,
      "2026-08-15T19:00",
      "2026-08-15T21:00",
      null,
    );
    expect(out.map((s) => s.id)).toEqual(["s1"]);
  });
  it("treats abutting ranges (end == start) as non-overlapping", () => {
    const out = findOverlappingSessions(
      siblings,
      "2026-08-15T20:00",
      "2026-08-15T22:00",
      null,
    );
    expect(out).toEqual([]);
  });
  it("excludes the session being edited", () => {
    const out = findOverlappingSessions(
      siblings,
      "2026-08-15T18:00",
      "2026-08-15T20:00",
      "s1",
    );
    expect(out).toEqual([]);
  });
  it("returns all conflicting siblings", () => {
    const out = findOverlappingSessions(
      siblings,
      "2026-08-15T19:00",
      "2026-08-15T23:00",
      null,
    );
    expect(out.map((s) => s.id)).toEqual(["s1", "s2"]);
  });
});

describe("mapSessionError", () => {
  it("maps known session error codes to human-readable strings", () => {
    expect(
      mapSessionError(
        new ApiError(400, { code: "session.invalid_date_range", message: "x" }),
      ),
    ).toMatch(/end must be after start/i);
    expect(
      mapSessionError(
        new ApiError(400, { code: "session.invalid_capacity", message: "x" }),
      ),
    ).toMatch(/greater than zero/i);
    expect(
      mapSessionError(
        new ApiError(404, { code: "session.not_found", message: "x" }),
      ),
    ).toMatch(/no longer exists/i);
  });
  it("falls back to a status-aware message for 401/403", () => {
    expect(
      mapSessionError(new ApiError(401, { code: "auth.expired", message: "x" })),
    ).toMatch(/sign in again/i);
    expect(
      mapSessionError(
        new ApiError(403, { code: "permissions.denied", message: "x" }),
      ),
    ).toMatch(/missing the permission/i);
  });
  it("uses the message + code on unrecognised codes", () => {
    expect(
      mapSessionError(
        new ApiError(500, { code: "boom.weird", message: "bang" }),
      ),
    ).toBe("bang (boom.weird)");
  });
});

describe("posterInitial", () => {
  it("returns the uppercased first character of the name", () => {
    expect(posterInitial("summer")).toBe("S");
    expect(posterInitial("  party  ")).toBe("P");
  });
  it("falls back to a question mark on blank input", () => {
    expect(posterInitial("")).toBe("?");
    expect(posterInitial("   ")).toBe("?");
  });
});

// ---------------------------------------------------------------------------
// Ticket-tier helpers (feature #283 / E-5)
// ---------------------------------------------------------------------------

function tier(overrides: Partial<TicketTierItem> = {}): TicketTierItem {
  return {
    id: "01929d0e-0e47-7000-8000-000000000401",
    session_id: "01929d0e-0e47-7000-8000-000000000201",
    name: "GA",
    pricing_mode: "fixed",
    price_amount: 2500,
    currency: "USD",
    pwyw_min: null,
    pwyw_max: null,
    capacity: 100,
    sale_window_start: null,
    sale_window_end: null,
    sort_order: 1,
    ...overrides,
  };
}

describe("TIER_PRICING_MODES / isTierPricingMode", () => {
  it("enumerates the three modes in the order expected by the API", () => {
    expect(TIER_PRICING_MODES).toEqual(["fixed", "free", "pwyw"]);
  });
  it("isTierPricingMode rejects unknown values", () => {
    expect(isTierPricingMode("fixed")).toBe(true);
    expect(isTierPricingMode("free")).toBe(true);
    expect(isTierPricingMode("pwyw")).toBe(true);
    expect(isTierPricingMode("FIXED")).toBe(false);
    expect(isTierPricingMode("donation")).toBe(false);
    expect(isTierPricingMode("")).toBe(false);
  });
});

describe("PROVIDER_CURRENCIES / allowedCurrenciesForProviders", () => {
  it("declares stripe and allpay capability sets", () => {
    expect(PROVIDER_CURRENCIES.stripe).toContain("USD");
    expect(PROVIDER_CURRENCIES.stripe).toContain("ILS");
    expect(PROVIDER_CURRENCIES.allpay).toContain("ILS");
    expect(PROVIDER_CURRENCIES.allpay).not.toContain("RUB");
  });
  it("returns the default set when no providers are supplied", () => {
    expect(allowedCurrenciesForProviders([])).toEqual(["USD", "EUR", "ILS"]);
  });
  it("returns the stripe set for a stripe-only org", () => {
    const got = allowedCurrenciesForProviders(["stripe"]);
    expect(got).toContain("USD");
    expect(got).toContain("RUB");
  });
  it("returns the union of stripe + allpay for a multi-provider org", () => {
    const got = allowedCurrenciesForProviders(["stripe", "allpay"]);
    // Union must include every currency from both sets.
    for (const c of [...PROVIDER_CURRENCIES.stripe!, ...PROVIDER_CURRENCIES.allpay!]) {
      expect(got).toContain(c);
    }
  });
  it("ignores unknown providers", () => {
    expect(allowedCurrenciesForProviders(["mystery"])).toEqual([
      "USD",
      "EUR",
      "ILS",
    ]);
  });
  it("returns results sorted (stable presentation in the dropdown)", () => {
    const got = allowedCurrenciesForProviders(["stripe", "allpay"]);
    const sorted = [...got].sort();
    expect(got).toEqual(sorted);
  });
});

describe("centsToDecimal / decimalToCents", () => {
  it("centsToDecimal renders cents as two-decimal major-unit strings", () => {
    expect(centsToDecimal(0)).toBe("0.00");
    expect(centsToDecimal(5)).toBe("0.05");
    expect(centsToDecimal(50)).toBe("0.50");
    expect(centsToDecimal(1234)).toBe("12.34");
    expect(centsToDecimal(100000)).toBe("1000.00");
  });
  it("decimalToCents parses common operator inputs", () => {
    expect(decimalToCents("0")).toBe(0);
    expect(decimalToCents("0.00")).toBe(0);
    expect(decimalToCents("12")).toBe(1200);
    expect(decimalToCents("12.5")).toBe(1250);
    expect(decimalToCents("12.50")).toBe(1250);
    expect(decimalToCents("1000.00")).toBe(100000);
  });
  it("decimalToCents rejects malformed strings", () => {
    expect(decimalToCents("")).toBeNull();
    expect(decimalToCents("abc")).toBeNull();
    expect(decimalToCents("-1")).toBeNull();
    expect(decimalToCents("1.234")).toBeNull(); // > 2 fractional digits
    expect(decimalToCents("1,50")).toBeNull();
  });
  it("round-trips integer cents losslessly", () => {
    for (const c of [0, 1, 99, 100, 199, 250, 99999]) {
      expect(decimalToCents(centsToDecimal(c))).toBe(c);
    }
  });
});

describe("emptyTierForm / tierToForm", () => {
  it("emptyTierForm defaults to fixed pricing with the first allowed currency", () => {
    const f = emptyTierForm("EUR");
    expect(f.pricing_mode).toBe("fixed");
    expect(f.currency).toBe("EUR");
    expect(f.sort_order).toBe("0");
    expect(f.price_amount).toBe("");
  });
  it("tierToForm hydrates a fixed-price tier", () => {
    const f = tierToForm(
      tier({ pricing_mode: "fixed", price_amount: 1599, currency: "GBP" }),
    );
    expect(f.pricing_mode).toBe("fixed");
    expect(f.price_amount).toBe("15.99");
    expect(f.currency).toBe("GBP");
    expect(f.capacity).toBe("100");
  });
  it("tierToForm hydrates a pwyw tier with both bounds", () => {
    const f = tierToForm(
      tier({
        pricing_mode: "pwyw",
        price_amount: 0,
        pwyw_min: 500,
        pwyw_max: 5000,
      }),
    );
    expect(f.pricing_mode).toBe("pwyw");
    expect(f.pwyw_min).toBe("5.00");
    expect(f.pwyw_max).toBe("50.00");
  });
  it("tierToForm falls back to 'fixed' on an unknown pricing_mode", () => {
    const f = tierToForm(tier({ pricing_mode: "donation" }));
    expect(f.pricing_mode).toBe("fixed");
  });
  it("tierToForm leaves optional fields blank when null", () => {
    const f = tierToForm(
      tier({ capacity: null, sale_window_start: null, sale_window_end: null }),
    );
    expect(f.capacity).toBe("");
    expect(f.sale_window_start).toBe("");
    expect(f.sale_window_end).toBe("");
  });
});

describe("validateTierForm", () => {
  const ALLOWED = ["USD", "EUR", "ILS"] as const;
  function form(overrides: Partial<TierFormValues> = {}): TierFormValues {
    return {
      ...emptyTierForm("USD"),
      name: "General Admission",
      price_amount: "10.00",
      ...overrides,
    };
  }
  it("accepts a minimal fixed-price tier", () => {
    expect(validateTierForm(form(), ALLOWED)).toEqual({});
  });
  it("requires name", () => {
    expect(validateTierForm(form({ name: "" }), ALLOWED).name).toBeDefined();
  });
  it("rejects fixed price <= 0", () => {
    expect(
      validateTierForm(form({ name: "GA", price_amount: "0" }), ALLOWED)
        .price_amount,
    ).toBeDefined();
  });
  it("rejects malformed prices", () => {
    expect(
      validateTierForm(form({ name: "GA", price_amount: "abc" }), ALLOWED)
        .price_amount,
    ).toBeDefined();
  });
  it("rejects currencies outside the allowed set", () => {
    expect(
      validateTierForm(form({ name: "GA", currency: "RUB" }), ALLOWED).currency,
    ).toBeDefined();
  });
  it("accepts a pwyw tier with empty bounds", () => {
    expect(
      validateTierForm(
        form({ name: "Pay what you want", pricing_mode: "pwyw", price_amount: "" }),
        ALLOWED,
      ),
    ).toEqual({});
  });
  it("rejects pwyw with min > max", () => {
    const e = validateTierForm(
      form({
        name: "PWYW",
        pricing_mode: "pwyw",
        price_amount: "",
        pwyw_min: "10.00",
        pwyw_max: "5.00",
      }),
      ALLOWED,
    );
    expect(e.pwyw_max).toBeDefined();
  });
  it("accepts a free tier with no price", () => {
    expect(
      validateTierForm(
        form({ name: "Free", pricing_mode: "free", price_amount: "" }),
        ALLOWED,
      ),
    ).toEqual({});
  });
  it("rejects capacity <= 0", () => {
    expect(
      validateTierForm(form({ name: "GA", capacity: "0" }), ALLOWED).capacity,
    ).toBeDefined();
  });
  it("rejects sale_window_end <= sale_window_start", () => {
    const e = validateTierForm(
      form({
        name: "GA",
        sale_window_start: "2026-08-01T10:00",
        sale_window_end: "2026-08-01T10:00",
      }),
      ALLOWED,
    );
    expect(e.sale_window_end).toBeDefined();
  });
  it("rejects non-integer sort_order", () => {
    expect(
      validateTierForm(form({ name: "GA", sort_order: "1.5" }), ALLOWED)
        .sort_order,
    ).toBeDefined();
  });
});

describe("buildTierRequestBody", () => {
  it("emits zero price + null pwyw bounds for free tiers", () => {
    const body = buildTierRequestBody({
      ...emptyTierForm("USD"),
      name: "Free",
      pricing_mode: "free",
      price_amount: "",
    });
    expect(body.price_amount).toBe(0);
    expect(body.pwyw_min).toBeNull();
    expect(body.pwyw_max).toBeNull();
    expect(body.currency).toBe("USD");
  });
  it("converts fixed-price decimals to integer cents", () => {
    const body = buildTierRequestBody({
      ...emptyTierForm("EUR"),
      name: "VIP",
      pricing_mode: "fixed",
      price_amount: "199.99",
    });
    expect(body.price_amount).toBe(19999);
    expect(body.pwyw_min).toBeNull();
    expect(body.pwyw_max).toBeNull();
  });
  it("emits pwyw bounds as cents when supplied, null when blank", () => {
    const body = buildTierRequestBody({
      ...emptyTierForm("USD"),
      name: "PWYW",
      pricing_mode: "pwyw",
      price_amount: "",
      pwyw_min: "5.00",
      pwyw_max: "",
    });
    expect(body.pwyw_min).toBe(500);
    expect(body.pwyw_max).toBeNull();
  });
  it("normalises blank capacity to null and number when supplied", () => {
    expect(
      buildTierRequestBody({ ...emptyTierForm("USD"), name: "GA", price_amount: "10.00" })
        .capacity,
    ).toBeNull();
    expect(
      buildTierRequestBody({
        ...emptyTierForm("USD"),
        name: "GA",
        price_amount: "10.00",
        capacity: "250",
      }).capacity,
    ).toBe(250);
  });
  it("normalises sale-window timestamps to RFC3339 UTC and blank to null", () => {
    const body = buildTierRequestBody({
      ...emptyTierForm("USD"),
      name: "GA",
      price_amount: "10.00",
      sale_window_start: "2026-08-01T10:00",
      sale_window_end: "",
    });
    expect(body.sale_window_start).toBe("2026-08-01T10:00:00Z");
    expect(body.sale_window_end).toBeNull();
  });
  it("uppercases the currency code", () => {
    const body = buildTierRequestBody({
      ...emptyTierForm("usd"),
      name: "GA",
      price_amount: "10.00",
      currency: "eur",
    });
    expect(body.currency).toBe("EUR");
  });
});

describe("mapTierError", () => {
  it("translates the known tier.* codes", () => {
    expect(
      mapTierError(new ApiError(400, { code: "tier.missing_name", message: "" })),
    ).toMatch(/name is required/i);
    expect(
      mapTierError(
        new ApiError(400, { code: "tier.invalid_pricing_mode", message: "" }),
      ),
    ).toMatch(/fixed.*free.*pwyw/i);
    expect(
      mapTierError(
        new ApiError(400, { code: "tier.invalid_capacity", message: "" }),
      ),
    ).toMatch(/capacity/i);
  });
  it("translates the pricing.* domain codes", () => {
    expect(
      mapTierError(
        new ApiError(400, {
          code: "pricing.pwyw_min_greater_than_max",
          message: "",
        }),
      ),
    ).toMatch(/pwyw_min/);
  });
  it("falls back to message + code on unknown codes", () => {
    expect(
      mapTierError(
        new ApiError(500, { code: "boom.something", message: "kaboom" }),
      ),
    ).toBe("kaboom (boom.something)");
  });
  it("handles 401/403 specially", () => {
    expect(
      mapTierError(new ApiError(401, { code: "auth.expired", message: "" })),
    ).toMatch(/sign in again/i);
    expect(
      mapTierError(new ApiError(403, { code: "x.y", message: "" })),
    ).toMatch(/forbidden/i);
  });
});
