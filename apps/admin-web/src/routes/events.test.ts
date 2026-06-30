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
  allowedTransitions,
  filterEventsByDateRange,
  filterEventsByOrg,
  filterEventsByStatus,
  formatDateOnly,
  formatDateTime,
  isEventStatus,
  isEventVisibility,
  paginate,
  posterInitial,
  type EventItem,
} from "@/routes/events";

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
