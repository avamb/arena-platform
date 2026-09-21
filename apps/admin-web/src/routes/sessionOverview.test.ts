/**
 * Unit tests for the pure helpers behind the session overview screen: the
 * arithmetic that separates a seat sold here from one sold upstream, the
 * sold share, and the venue-local start time.
 */
import { describe, expect, it } from "vitest";
import {
  addPlaces,
  formatSessionStart,
  sellable,
  soldHere,
  soldPercent,
  ticketsLink,
  type PlaceCounts,
} from "@/routes/sessionOverview";

const hall: PlaceCounts = {
  total: 90,
  available: 51,
  held: 0,
  sold: 22,
  sold_upstream: 21,
  unavailable: 17,
};

describe("soldHere", () => {
  it("takes the upstream sales out of sold", () => {
    expect(soldHere(hall)).toBe(1);
  });
  it("never goes negative on inconsistent input", () => {
    expect(soldHere({ ...hall, sold: 3, sold_upstream: 5 })).toBe(0);
  });
});

describe("sellable and soldPercent", () => {
  it("leaves withheld places out of the base", () => {
    expect(sellable(hall)).toBe(73);
    expect(soldPercent(hall)).toBe(30);
  });
  it("answers 0 for a hall with nothing to sell", () => {
    expect(soldPercent({ total: 5, available: 0, held: 0, sold: 0, sold_upstream: 0, unavailable: 5 })).toBe(0);
    expect(soldPercent({ total: 0, available: 0, held: 0, sold: 0, sold_upstream: 0, unavailable: 0 })).toBe(0);
  });
});

describe("addPlaces", () => {
  it("sums seats and general admission field by field", () => {
    const ga: PlaceCounts = { total: 476, available: 475, held: 0, sold: 1, sold_upstream: 0, unavailable: 0 };
    expect(addPlaces(hall, ga)).toEqual({
      total: 566,
      available: 526,
      held: 0,
      sold: 23,
      sold_upstream: 21,
      unavailable: 17,
    });
  });
});

describe("formatSessionStart", () => {
  it("shows hall-local time with the timezone named", () => {
    const text = formatSessionStart("2026-10-13T17:00:00Z", "Europe/Prague");
    expect(text).toContain("19:00");
    expect(text).toContain("(Europe/Prague)");
  });
  it("falls back to UTC without a timezone or with an unknown one", () => {
    expect(formatSessionStart("2026-10-13T17:00:00Z", null)).toBe("2026-10-13 17:00Z");
    expect(formatSessionStart("2026-10-13T17:00:00Z", "Not/AZone")).toBe("2026-10-13 17:00Z");
  });
  it("returns unparseable input unchanged", () => {
    expect(formatSessionStart("soon", "Europe/Prague")).toBe("soon");
  });
});

describe("ticketsLink", () => {
  it("pre-filters the Tickets console to the session", () => {
    expect(ticketsLink("o1", "e1", "s1")).toBe("/tickets?org_id=o1&event_id=e1&session_id=s1");
  });
});
