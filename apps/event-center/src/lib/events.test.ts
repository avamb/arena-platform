import { describe, expect, it } from "vitest";
import { filterEvents, moneyLines, placeFigures, searchEvents } from "@/lib/events";
import type { EventItem } from "@/lib/api/types";

function ev(partial: Partial<EventItem> & { id: string; name: string }): EventItem {
  return {
    display_number: 1,
    org_id: "o",
    description: null,
    status: "published",
    first_session_at: null,
    last_session_at: null,
    venue_names: [],
    sales_state: "on_sale",
    next_session_at: null,
    session_count: 1,
    visibility: "public",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...partial,
  } as EventItem;
}

const events = [
  ev({ id: "late", name: "Late show", next_session_at: "2026-12-01T19:00:00Z" }),
  ev({ id: "soon", name: "Ёлка", next_session_at: "2026-10-20T19:00:00Z" }),
  ev({ id: "draft", name: "Draft", status: "draft", sales_state: "upcoming" }),
  ev({ id: "old", name: "Old", sales_state: "archived", last_session_at: "2026-01-10T19:00:00Z" }),
  ev({ id: "older", name: "Older", sales_state: "archived", last_session_at: "2025-05-10T19:00:00Z" }),
];

describe("filterEvents", () => {
  it("lists running events nearest first, events without a date last", () => {
    expect(filterEvents(events, "run").map((e) => e.id)).toEqual(["soon", "late", "draft"]);
  });
  it("lists the archive latest first", () => {
    expect(filterEvents(events, "arc").map((e) => e.id)).toEqual(["old", "older"]);
  });
});

describe("searchEvents", () => {
  it("needs every word and treats ё as е", () => {
    expect(searchEvents(events, "елка").map((e) => e.id)).toEqual(["soon"]);
    expect(searchEvents(events, "late SHOW").map((e) => e.id)).toEqual(["late"]);
    expect(searchEvents(events, "late old")).toEqual([]);
    expect(searchEvents(events, "  ")).toHaveLength(events.length);
  });
});

describe("figures", () => {
  it("adds seats and general admission", () => {
    const c = (sold: number, total: number) => ({ total, available: total - sold, held: 1, sold, sold_upstream: 2, unavailable: 0 });
    expect(placeFigures({ seats: c(3, 10), ga: c(5, 20) })).toEqual({ sold: 8, total: 30, available: 22, held: 2 });
  });
  it("hides money lines with nothing in them", () => {
    const zero = { currency: "EUR", paid_orders: 0, paid: 0, service_charge: 0, discount: 0, refunded: 0, net: 0, pending_orders: 0, pending: 0 };
    expect(moneyLines([zero, { ...zero, currency: "CZK", paid: 100, paid_orders: 1 }]).map((m) => m.currency)).toEqual(["CZK"]);
  });
});
