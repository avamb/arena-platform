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
  SESSION_ADMISSION_MODES,
  SESSION_STATUSES,
  allowedTransitions,
  buildSessionRequestBody,
  emptyEventForm,
  emptySessionForm,
  eventToForm,
  filterEventsByDateRange,
  filterEventsByOrg,
  filterEventsByStatus,
  findOverlappingSessions,
  formatDateOnly,
  formatDateTime,
  isEventStatus,
  isEventVisibility,
  isSessionAdmissionMode,
  isSessionStatus,
  mapSessionError,
  paginate,
  parseLocalDatetime,
  posterInitial,
  sessionToForm,
  toLocalDatetimeValue,
  toRFC3339,
  validateEventForm,
  validateSessionForm,
  TIER_PRICING_MODES,
  buildTierRequestBody,
  centsToDecimal,
  decimalToCents,
  emptyTierForm,
  isTierPricingMode,
  mapTierError,
  tierToForm,
  validateTierForm,
  buildPublicationRequestBody,
  deriveDefaultCityID,
  emptyPublicationForm,
  isUUID,
  mapPublicationError,
  validatePublicationForm,
  type EventItem,
  type SessionFormValues,
  type TicketTierItem,
  type TierFormValues,
} from "@/routes/events";
import { ApiError } from "@/lib/api/client";

function ev(overrides: Partial<EventItem>): EventItem {
  return {
    id: "01929d0e-0e47-7000-8000-000000000301",
    display_number: 301,
    org_id: "01929d0e-0e47-7000-8000-000000000001",
    name: "Test Event",
    description: null,
    status: "draft",
    first_session_at: "2026-08-15T18:00:00Z",
    last_session_at: "2026-08-15T23:00:00Z",
    venue_names: ["Palac Akropolis"],
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
    ev({ id: "early", first_session_at: "2026-07-01T10:00:00Z" }),
    ev({ id: "mid", first_session_at: "2026-08-15T18:00:00Z" }),
    ev({ id: "late", first_session_at: "2026-09-30T20:00:00Z" }),
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

  it("keeps sessionless events (null first_session_at) when unbounded", () => {
    const withNull = [...events, ev({ id: "none", first_session_at: null })];
    expect(filterEventsByDateRange(withNull, "", "")).toBe(withNull);
  });

  it("excludes sessionless events as soon as either bound is set", () => {
    const withNull = [
      ev({ id: "none", first_session_at: null, last_session_at: null, venue_names: [] }),
      ev({ id: "mid", first_session_at: "2026-08-15T18:00:00Z" }),
    ];
    expect(
      filterEventsByDateRange(withNull, "2026-01-01", "").map((e) => e.id),
    ).toEqual(["mid"]);
    expect(
      filterEventsByDateRange(withNull, "", "2026-12-31").map((e) => e.id),
    ).toEqual(["mid"]);
  });
});

describe("emptyEventForm / eventToForm / validateEventForm", () => {
  it("emptyEventForm has no venue / date fields (AB-36/AB-37)", () => {
    expect(emptyEventForm()).toEqual({
      name: "",
      description: "",
      org_id: "",
      visibility: "",
    });
  });

  it("eventToForm hydrates only name / description / org / visibility", () => {
    const f = eventToForm(
      ev({ name: "Gala", description: "desc", visibility: "unlisted" }),
    );
    expect(f).toEqual({
      name: "Gala",
      description: "desc",
      org_id: "01929d0e-0e47-7000-8000-000000000001",
      visibility: "unlisted",
    });
  });

  it("eventToForm renders a null description as an empty string", () => {
    expect(eventToForm(ev({ description: null })).description).toBe("");
  });

  it("validateEventForm requires only name (and org on create)", () => {
    expect(
      validateEventForm(
        { name: "X", description: "", org_id: "", visibility: "" },
        false,
      ),
    ).toEqual({});
    expect(
      validateEventForm(
        { name: "", description: "", org_id: "o", visibility: "" },
        false,
      ).name,
    ).toBeDefined();
    expect(
      validateEventForm(
        { name: "X", description: "", org_id: "", visibility: "" },
        true,
      ).org_id,
    ).toBeDefined();
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

describe("SESSION_ADMISSION_MODES / isSessionAdmissionMode", () => {
  it("enumerates the three admission modes, GA first (the default)", () => {
    expect(SESSION_ADMISSION_MODES).toEqual([
      "general_admission",
      "assigned_seats",
      "hybrid",
    ]);
  });
  it("isSessionAdmissionMode accepts canonical values only", () => {
    expect(isSessionAdmissionMode("general_admission")).toBe(true);
    expect(isSessionAdmissionMode("assigned_seats")).toBe(true);
    expect(isSessionAdmissionMode("hybrid")).toBe(true);
    expect(isSessionAdmissionMode("GA")).toBe(false);
    expect(isSessionAdmissionMode("")).toBe(false);
  });
});

describe("emptySessionForm / sessionToForm", () => {
  it("emptySessionForm starts blank with GA admission and draft status", () => {
    const f = emptySessionForm();
    expect(f.venue_id).toBe("");
    expect(f.start_at).toBe("");
    expect(f.end_at).toBe("");
    expect(f.capacity_override).toBe("");
    expect(f.status).toBe("draft");
    expect(f.admission_mode).toBe("general_admission");
    expect(f.seating_plan_version_id).toBe("");
    expect(f.currency).toBe("");
  });
  it("sessionToForm hydrates fields from an existing session row", () => {
    const f = sessionToForm({
      venue_id: "01929d0e-0e47-7000-8000-000000000201",
      start_at: "2026-08-15T18:00:00Z",
      end_at: "2026-08-15T23:00:00Z",
      capacity_override: 250,
      status: "scheduled",
      admission_mode: "assigned_seats",
      seating_plan_version_id: "01929d0e-0e47-7000-8000-000000000901",
    });
    expect(f).toEqual({
      venue_id: "01929d0e-0e47-7000-8000-000000000201",
      start_at: "2026-08-15T18:00",
      end_at: "2026-08-15T23:00",
      capacity_override: "250",
      status: "scheduled",
      admission_mode: "assigned_seats",
      seating_plan_version_id: "01929d0e-0e47-7000-8000-000000000901",
      currency: "",
    });
  });
  it("sessionToForm leaves a null capacity_override blank (derived)", () => {
    const f = sessionToForm({
      venue_id: "v1",
      start_at: "2026-08-15T18:00:00Z",
      end_at: "2026-08-15T23:00:00Z",
      capacity_override: null,
      status: "scheduled",
      admission_mode: "general_admission",
      seating_plan_version_id: null,
    });
    expect(f.capacity_override).toBe("");
    expect(f.seating_plan_version_id).toBe("");
  });
  it("sessionToForm falls back to draft / GA on unknown enum values", () => {
    const f = sessionToForm({
      venue_id: "v1",
      start_at: "2026-08-15T18:00:00Z",
      end_at: "2026-08-15T23:00:00Z",
      capacity_override: null,
      status: "garbage",
      admission_mode: "garbage",
      seating_plan_version_id: null,
    });
    expect(f.status).toBe("draft");
    expect(f.admission_mode).toBe("general_admission");
  });
});

describe("validateSessionForm", () => {
  function form(o: Partial<SessionFormValues>): SessionFormValues {
    return {
      venue_id: "01929d0e-0e47-7000-8000-000000000201",
      start_at: "2026-08-15T18:00",
      end_at: "2026-08-15T23:00",
      capacity_override: "",
      status: "draft",
      admission_mode: "general_admission",
      seating_plan_version_id: "",
      currency: "",
      ...o,
    };
  }

  it("accepts a fully valid form (no capacity override — derived)", () => {
    expect(validateSessionForm(form({}))).toEqual({});
  });
  it("requires a venue (AB-36)", () => {
    expect(validateSessionForm(form({ venue_id: "" })).venue_id).toBeDefined();
    expect(validateSessionForm(form({ venue_id: "  " })).venue_id).toBeDefined();
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
  it("capacity_override is optional but must be a positive integer when set", () => {
    expect(validateSessionForm(form({ capacity_override: "" }))).toEqual({});
    expect(validateSessionForm(form({ capacity_override: "100" }))).toEqual({});
    expect(
      validateSessionForm(form({ capacity_override: "0" })).capacity_override,
    ).toBeDefined();
    expect(
      validateSessionForm(form({ capacity_override: "-5" })).capacity_override,
    ).toBeDefined();
    expect(
      validateSessionForm(form({ capacity_override: "1.5" })).capacity_override,
    ).toBeDefined();
    expect(
      validateSessionForm(form({ capacity_override: "abc" })).capacity_override,
    ).toBeDefined();
  });
  it("rejects a capacity_override that would overflow int32", () => {
    expect(
      validateSessionForm(form({ capacity_override: "9999999999" }))
        .capacity_override,
    ).toBeDefined();
  });
  it("rejects an invalid status value", () => {
    expect(
      validateSessionForm(form({ status: "archived" as never })).status,
    ).toBeDefined();
  });
  it("requires a seating plan version for seated admission modes", () => {
    expect(
      validateSessionForm(form({ admission_mode: "assigned_seats" }))
        .seating_plan_version_id,
    ).toBeDefined();
    expect(
      validateSessionForm(form({ admission_mode: "hybrid" }))
        .seating_plan_version_id,
    ).toBeDefined();
    expect(
      validateSessionForm(
        form({
          admission_mode: "assigned_seats",
          seating_plan_version_id: "01929d0e-0e47-7000-8000-000000000901",
        }),
      ),
    ).toEqual({});
  });
  it("currency is optional but must be 3 uppercase letters when set", () => {
    expect(validateSessionForm(form({ currency: "" }))).toEqual({});
    expect(validateSessionForm(form({ currency: "CZK" }))).toEqual({});
    expect(validateSessionForm(form({ currency: "czk" })).currency).toBeDefined();
    expect(validateSessionForm(form({ currency: "EURO" })).currency).toBeDefined();
    expect(validateSessionForm(form({ currency: "E1" })).currency).toBeDefined();
  });
});

describe("buildSessionRequestBody", () => {
  function form(o: Partial<SessionFormValues>): SessionFormValues {
    return {
      venue_id: "01929d0e-0e47-7000-8000-000000000201",
      start_at: "2026-08-15T18:00",
      end_at: "2026-08-15T23:00",
      capacity_override: "",
      status: "draft",
      admission_mode: "general_admission",
      seating_plan_version_id: "",
      currency: "",
      ...o,
    };
  }

  it("never sends capacity_total; omits blank optionals", () => {
    const body = buildSessionRequestBody(form({}), "create");
    expect(body).toEqual({
      venue_id: "01929d0e-0e47-7000-8000-000000000201",
      start_at: "2026-08-15T18:00:00Z",
      end_at: "2026-08-15T23:00:00Z",
      status: "draft",
      admission_mode: "general_admission",
    });
    expect("capacity_total" in body).toBe(false);
    expect("capacity_override" in body).toBe(false);
    expect("currency" in body).toBe(false);
    expect("seating_plan_version_id" in body).toBe(false);
  });
  it("sends capacity_override only when supplied", () => {
    expect(
      buildSessionRequestBody(form({ capacity_override: "250" }), "create")
        .capacity_override,
    ).toBe(250);
  });
  it("uppercases and sends the currency only when typed", () => {
    expect(
      buildSessionRequestBody(form({ currency: "eur" }), "edit").currency,
    ).toBe("EUR");
    expect(
      "currency" in buildSessionRequestBody(form({}), "edit"),
    ).toBe(false);
  });
  it("sends admission_mode + seating_plan_version_id on seated create", () => {
    const body = buildSessionRequestBody(
      form({
        admission_mode: "assigned_seats",
        seating_plan_version_id: "01929d0e-0e47-7000-8000-000000000901",
      }),
      "create",
    );
    expect(body.admission_mode).toBe("assigned_seats");
    expect(body.seating_plan_version_id).toBe(
      "01929d0e-0e47-7000-8000-000000000901",
    );
  });
  it("never sends admission_mode / seating_plan_version_id on edit", () => {
    const body = buildSessionRequestBody(
      form({
        admission_mode: "assigned_seats",
        seating_plan_version_id: "01929d0e-0e47-7000-8000-000000000901",
      }),
      "edit",
    );
    expect("admission_mode" in body).toBe(false);
    expect("seating_plan_version_id" in body).toBe(false);
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
  it("maps the AB-36/AB-38 wave-4 error codes", () => {
    const cases: ReadonlyArray<[string, RegExp]> = [
      ["session.missing_venue_id", /venue is required/i],
      ["session.invalid_venue_id", /valid uuid/i],
      ["session.venue_not_found", /no longer exists/i],
      ["session.venue_org_mismatch", /different organization/i],
      ["session.invalid_admission_mode", /admission mode/i],
      ["session.missing_seating_plan_version", /seating plan version/i],
      ["session.invalid_seating_plan_version", /seating plan version/i],
      ["session.seating_plan_not_applicable", /general-admission/i],
      ["session.invalid_currency", /iso 4217/i],
      ["session.currency_unresolvable", /explicit currency/i],
      ["session.invalid_capacity_override", /greater than zero/i],
      ["session.capacity_unresolvable", /capacity/i],
      ["session.capacity_override_not_applicable", /seating plan/i],
    ];
    for (const [code, pattern] of cases) {
      expect(mapSessionError(new ApiError(422, { code, message: "x" }))).toMatch(
        pattern,
      );
    }
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
  it("emptyTierForm defaults to fixed pricing and carries no currency (AB-38)", () => {
    const f = emptyTierForm();
    expect(f.pricing_mode).toBe("fixed");
    expect("currency" in f).toBe(false);
    expect(f.sort_order).toBe("0");
    expect(f.price_amount).toBe("");
  });
  it("tierToForm hydrates a fixed-price tier without a currency field", () => {
    const f = tierToForm(
      tier({ pricing_mode: "fixed", price_amount: 1599, currency: "GBP" }),
    );
    expect(f.pricing_mode).toBe("fixed");
    expect(f.price_amount).toBe("15.99");
    expect("currency" in f).toBe(false);
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
  function form(overrides: Partial<TierFormValues> = {}): TierFormValues {
    return {
      ...emptyTierForm(),
      name: "General Admission",
      price_amount: "10.00",
      ...overrides,
    };
  }
  it("accepts a minimal fixed-price tier", () => {
    expect(validateTierForm(form())).toEqual({});
  });
  it("requires name", () => {
    expect(validateTierForm(form({ name: "" })).name).toBeDefined();
  });
  it("rejects fixed price <= 0", () => {
    expect(
      validateTierForm(form({ name: "GA", price_amount: "0" })).price_amount,
    ).toBeDefined();
  });
  it("rejects malformed prices", () => {
    expect(
      validateTierForm(form({ name: "GA", price_amount: "abc" })).price_amount,
    ).toBeDefined();
  });
  it("accepts a pwyw tier with empty bounds", () => {
    expect(
      validateTierForm(
        form({ name: "Pay what you want", pricing_mode: "pwyw", price_amount: "" }),
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
    );
    expect(e.pwyw_max).toBeDefined();
  });
  it("accepts a free tier with no price", () => {
    expect(
      validateTierForm(
        form({ name: "Free", pricing_mode: "free", price_amount: "" }),
      ),
    ).toEqual({});
  });
  it("rejects capacity <= 0", () => {
    expect(
      validateTierForm(form({ name: "GA", capacity: "0" })).capacity,
    ).toBeDefined();
  });
  it("rejects sale_window_end <= sale_window_start", () => {
    const e = validateTierForm(
      form({
        name: "GA",
        sale_window_start: "2026-08-01T10:00",
        sale_window_end: "2026-08-01T10:00",
      }),
    );
    expect(e.sale_window_end).toBeDefined();
  });
  it("rejects non-integer sort_order", () => {
    expect(
      validateTierForm(form({ name: "GA", sort_order: "1.5" })).sort_order,
    ).toBeDefined();
  });
});

describe("buildTierRequestBody", () => {
  it("emits zero price + null pwyw bounds for free tiers", () => {
    const body = buildTierRequestBody({
      ...emptyTierForm(),
      name: "Free",
      pricing_mode: "free",
      price_amount: "",
    });
    expect(body.price_amount).toBe(0);
    expect(body.pwyw_min).toBeNull();
    expect(body.pwyw_max).toBeNull();
  });
  it("converts fixed-price decimals to integer cents", () => {
    const body = buildTierRequestBody({
      ...emptyTierForm(),
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
      ...emptyTierForm(),
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
      buildTierRequestBody({ ...emptyTierForm(), name: "GA", price_amount: "10.00" })
        .capacity,
    ).toBeNull();
    expect(
      buildTierRequestBody({
        ...emptyTierForm(),
        name: "GA",
        price_amount: "10.00",
        capacity: "250",
      }).capacity,
    ).toBe(250);
  });
  it("normalises sale-window timestamps to RFC3339 UTC and blank to null", () => {
    const body = buildTierRequestBody({
      ...emptyTierForm(),
      name: "GA",
      price_amount: "10.00",
      sale_window_start: "2026-08-01T10:00",
      sale_window_end: "",
    });
    expect(body.sale_window_start).toBe("2026-08-01T10:00:00Z");
    expect(body.sale_window_end).toBeNull();
  });
  it("never sends a currency — the server stamps the session currency (AB-38)", () => {
    const body = buildTierRequestBody({
      ...emptyTierForm(),
      name: "GA",
      price_amount: "10.00",
    });
    expect("currency" in body).toBe(false);
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

// ---------------------------------------------------------------------------
// Publications tab helpers (feature #284 / E-6)
// ---------------------------------------------------------------------------

const FEED_TOKEN_ID = "01929d0e-0e47-7000-8000-000000000901";
const CITY_ID = "01929d0e-0e47-7000-8000-000000000c01";

describe("isUUID", () => {
  it("accepts canonical UUID strings (any version)", () => {
    expect(isUUID(FEED_TOKEN_ID)).toBe(true);
    expect(isUUID("550e8400-e29b-41d4-a716-446655440000")).toBe(true);
  });
  it("rejects empty strings and garbage", () => {
    expect(isUUID("")).toBe(false);
    expect(isUUID("not-a-uuid")).toBe(false);
    expect(isUUID("12345")).toBe(false);
  });
  it("trims whitespace", () => {
    expect(isUUID(`  ${FEED_TOKEN_ID}  `)).toBe(true);
  });
});

// AB-43: PublicationFormValues now carries a sales_channel_id as the
// primary input; feed_token_id is only used in Advanced mode to pin a
// specific token. A helper avoids repeating the boilerplate in tests.
const CHANNEL_ID = "0192a5fb-000f-7000-8000-000000000010";
function pubForm(
  overrides: Partial<{
    sales_channel_id: string;
    feed_token_id: string;
    city_id: string;
    advanced_open: boolean;
  }> = {},
) {
  return {
    sales_channel_id: CHANNEL_ID,
    feed_token_id: "",
    city_id: "",
    advanced_open: false,
    ...overrides,
  };
}

describe("emptyPublicationForm", () => {
  it("returns blank values with advanced closed (AB-43)", () => {
    expect(emptyPublicationForm()).toEqual({
      sales_channel_id: "",
      feed_token_id: "",
      city_id: "",
      advanced_open: false,
    });
  });
  it("returns a fresh object each call (state-safe)", () => {
    const a = emptyPublicationForm();
    const b = emptyPublicationForm();
    expect(a).not.toBe(b);
  });
});

describe("validatePublicationForm", () => {
  it("requires sales_channel_id (AB-43 primary input)", () => {
    const errs = validatePublicationForm(pubForm({ sales_channel_id: "" }));
    expect(errs.sales_channel_id).toBeDefined();
    expect(errs.city_id).toBeUndefined();
  });
  it("requires sales_channel_id to be a UUID", () => {
    const errs = validatePublicationForm(
      pubForm({ sales_channel_id: "not-a-uuid" }),
    );
    expect(errs.sales_channel_id).toMatch(/UUID/);
  });
  it("accepts a channel-only form (feed token auto-resolved, global city)", () => {
    const errs = validatePublicationForm(pubForm());
    expect(errs).toEqual({});
  });
  it("accepts a pinned feed_token_id when set (advanced mode)", () => {
    const errs = validatePublicationForm(
      pubForm({ feed_token_id: FEED_TOKEN_ID, city_id: CITY_ID }),
    );
    expect(errs).toEqual({});
  });
  it("rejects a non-UUID feed_token_id when set", () => {
    const errs = validatePublicationForm(
      pubForm({ feed_token_id: "not-a-uuid" }),
    );
    expect(errs.feed_token_id).toMatch(/UUID/);
  });
  it("rejects a non-UUID city_id even when channel is valid", () => {
    const errs = validatePublicationForm(pubForm({ city_id: "not-a-uuid" }));
    expect(errs.city_id).toMatch(/UUID/);
    expect(errs.sales_channel_id).toBeUndefined();
  });
  it("trims whitespace before validating", () => {
    const errs = validatePublicationForm(
      pubForm({
        sales_channel_id: `   ${CHANNEL_ID}   `,
        feed_token_id: `   ${FEED_TOKEN_ID}   `,
        city_id: `   ${CITY_ID}   `,
      }),
    );
    expect(errs).toEqual({});
  });
});

describe("buildPublicationRequestBody", () => {
  it("omits city_id when blank (global scope)", () => {
    const body = buildPublicationRequestBody(pubForm(), FEED_TOKEN_ID);
    expect(body).toEqual({ feed_token_id: FEED_TOKEN_ID });
    expect("city_id" in body).toBe(false);
  });
  it("includes city_id when set (scoped publication)", () => {
    const body = buildPublicationRequestBody(
      pubForm({ city_id: CITY_ID }),
      FEED_TOKEN_ID,
    );
    expect(body).toEqual({ feed_token_id: FEED_TOKEN_ID, city_id: CITY_ID });
  });
  it("trims the resolved feed token id", () => {
    const body = buildPublicationRequestBody(
      pubForm({ city_id: `  ${CITY_ID}  ` }),
      `  ${FEED_TOKEN_ID}  `,
    );
    expect(body).toEqual({ feed_token_id: FEED_TOKEN_ID, city_id: CITY_ID });
  });
  it("treats whitespace-only city_id as global", () => {
    const body = buildPublicationRequestBody(
      pubForm({ city_id: "   " }),
      FEED_TOKEN_ID,
    );
    expect("city_id" in body).toBe(false);
  });
});

describe("deriveDefaultCityID (AB-43)", () => {
  const V1 = "01929d0e-0e47-7000-8000-000000000701";
  const V2 = "01929d0e-0e47-7000-8000-000000000702";
  const C1 = "01929d0e-0e47-7000-8000-000000000801";
  const C2 = "01929d0e-0e47-7000-8000-000000000802";
  it("returns '' when there are no sessions", () => {
    expect(
      deriveDefaultCityID([], [{ id: V1, city_id: C1 }]),
    ).toBe("");
  });
  it("returns '' when there are no venues", () => {
    expect(
      deriveDefaultCityID(
        [{ venue_id: V1, start_at: "2026-01-01T00:00:00Z" }],
        [],
      ),
    ).toBe("");
  });
  it("returns the venue city of the first-in-time session", () => {
    expect(
      deriveDefaultCityID(
        [
          { venue_id: V2, start_at: "2026-02-01T00:00:00Z" },
          { venue_id: V1, start_at: "2026-01-01T00:00:00Z" },
        ],
        [
          { id: V1, city_id: C1 },
          { id: V2, city_id: C2 },
        ],
      ),
    ).toBe(C1);
  });
  it("skips sessions whose venues have no city_id and tries the next", () => {
    expect(
      deriveDefaultCityID(
        [
          { venue_id: V1, start_at: "2026-01-01T00:00:00Z" },
          { venue_id: V2, start_at: "2026-02-01T00:00:00Z" },
        ],
        [
          { id: V1, city_id: null },
          { id: V2, city_id: C2 },
        ],
      ),
    ).toBe(C2);
  });
  it("returns '' when no matching venue is found", () => {
    expect(
      deriveDefaultCityID(
        [{ venue_id: "00000000-0000-0000-0000-000000000999", start_at: "2026-01-01T00:00:00Z" }],
        [{ id: V1, city_id: C1 }],
      ),
    ).toBe("");
  });
});

describe("mapPublicationError", () => {
  it.each([
    ["publication.invalid_event_id", /event id/i],
    ["publication.invalid_feed_token_id", /feed token id/i],
    ["publication.invalid_city_id", /city id/i],
    ["publication.feed_token_id_required", /required/i],
    ["publication.body_required", /body/i],
    ["publication.content_type_required", /json/i],
    ["publication.invalid_json", /json/i],
    ["publication.internal", /server/i],
    // AB-43: FK-violation 404s are surfaced with actionable messages.
    ["publication.feed_token_not_found", /feed token/i],
    ["publication.city_not_found", /city/i],
    ["publication.event_not_found", /event/i],
  ])("maps %s to an operator-readable message", (code, pattern) => {
    expect(
      mapPublicationError(
        new ApiError(400, { code, message: "raw backend message" }),
      ),
    ).toMatch(pattern);
  });
  it("maps permissions.denied", () => {
    expect(
      mapPublicationError(
        new ApiError(403, { code: "permissions.denied", message: "" }),
      ),
    ).toMatch(/permission/i);
  });
  it("handles 401 with a sign-in hint", () => {
    expect(
      mapPublicationError(new ApiError(401, { code: "auth.expired", message: "" })),
    ).toMatch(/sign in again/i);
  });
  it("handles 403 fallback", () => {
    expect(
      mapPublicationError(new ApiError(403, { code: "x.y", message: "" })),
    ).toMatch(/forbidden/i);
  });
  it("falls back to message + code on unknown codes", () => {
    expect(
      mapPublicationError(
        new ApiError(500, { code: "boom.something", message: "kaboom" }),
      ),
    ).toBe("kaboom (boom.something)");
  });
});
