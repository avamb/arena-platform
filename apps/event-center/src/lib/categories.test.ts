import { describe, expect, it } from "vitest";
import type { TicketTierItem, TierPriceWindow } from "@/lib/api/types";
import {
  activeWindowIndex,
  categoryCaps,
  categoryPriceNow,
  chainTargetName,
  dayOfInstant,
  edgeLabel,
  edgeView,
  inChain,
  isCategoryOpen,
  parseCategoryPrice,
  parseCategoryQuantity,
  planPriceWrite,
  priceInputValue,
  quantityFloor,
  windowBounds,
  windowEdgeInstant,
  withWindowPrice,
  zonedMidnight,
} from "@/lib/categories";

function tier(over: Partial<TicketTierItem> = {}): TicketTierItem {
  return {
    id: "t-1",
    session_id: "s-1",
    name: "Standing",
    pricing_mode: "fixed",
    price_amount: 2500,
    currency: "EUR",
    sort_order: 0,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    kind: "ga",
    is_open: true,
    ...over,
  } as TicketTierItem;
}

function win(from: string, to: string | null, price: number): TierPriceWindow {
  return { id: `w-${from}`, tier_id: "t-1", valid_from: from, valid_to: to, price_amount: price } as TierPriceWindow;
}

describe("categoryCaps (port of categoryCapsFor)", () => {
  it("a plain GA category with a fixed price allows everything", () => {
    expect(categoryCaps(tier(), [tier()])).toEqual({
      toggle: true,
      price: true,
      window: true,
      qty: true,
      chain: false,
      seated: false,
      notFixed: false,
    });
  });

  it("a chain head keeps only its price", () => {
    const head = tier({ id: "a", next_tier_id: "b" });
    const next = tier({ id: "b", name: "Late" });
    const caps = categoryCaps(head, [head, next]);
    expect(caps).toMatchObject({ toggle: false, window: false, qty: false, price: true, chain: true });
    expect(chainTargetName(head, [head, next])).toBe("Late");
  });

  it("a category that another one points at is in the chain too", () => {
    const head = tier({ id: "a", next_tier_id: "b" });
    const next = tier({ id: "b", name: "Late" });
    expect(inChain(next, [head, next])).toBe(true);
    expect(categoryCaps(next, [head, next])).toMatchObject({ toggle: false, window: false, qty: false, price: true });
    expect(chainTargetName(next, [head, next])).toBe("");
  });

  it("a step limit alone puts a category in the chain", () => {
    expect(inChain(tier({ sell_limit: 5 }), [])).toBe(true);
  });

  it("a seated category has no quantity but keeps open/close, price and window", () => {
    const caps = categoryCaps(tier({ kind: "seated" }), []);
    expect(caps).toMatchObject({ toggle: true, price: true, window: true, qty: false, seated: true });
  });

  it("a free or pay-what-you-want category has no price", () => {
    expect(categoryCaps(tier({ pricing_mode: "free", price_amount: 0 }), [])).toMatchObject({ price: false, notFixed: true });
    expect(categoryCaps(tier({ pricing_mode: "pwyw" }), [])).toMatchObject({ price: false, notFixed: true });
  });
});

describe("state helpers", () => {
  it("an absent flag is open, false is closed", () => {
    expect(isCategoryOpen(tier({ is_open: undefined }))).toBe(true);
    expect(isCategoryOpen(tier({ is_open: false }))).toBe(false);
  });
  it("the price now is the scheduled one when there is one", () => {
    expect(categoryPriceNow(tier({ current_price: 4000, price_amount: 3500 }))).toBe(4000);
    expect(categoryPriceNow(tier({ current_price: null, price_amount: 3500 }))).toBe(3500);
  });
  it("the quantity floor is sold plus on hold", () => {
    expect(quantityFloor(tier({ sold: 3, held: 2 }))).toBe(5);
    expect(quantityFloor(tier({ sold: null, held: undefined }))).toBe(0);
  });
});

describe("price schedule (port of activeWindow and withWindowPrice)", () => {
  const now = new Date("2026-10-10T12:00:00Z");
  const past = win("2026-10-01T00:00:00Z", "2026-10-10T12:00:00Z", 3000); // valid_to is exclusive: over at 12:00 sharp
  const current = win("2026-10-10T12:00:00Z", "2026-10-20T00:00:00Z", 4000); // valid_from is inclusive
  const later = win("2026-10-20T00:00:00Z", null, 5000);

  it("finds the window in force, edges included/excluded as the API reads them", () => {
    expect(activeWindowIndex([past, current, later], now)).toBe(1);
    expect(activeWindowIndex([later], now)).toBe(-1);
    expect(activeWindowIndex([], now)).toBe(-1);
    expect(activeWindowIndex([later], new Date("2030-01-01T00:00:00Z"))).toBe(0);
  });

  it("puts the new price into that window only and sends the whole schedule back", () => {
    const out = withWindowPrice([past, current, later], 1, 4500);
    expect(out).toEqual([
      { valid_from: past.valid_from, valid_to: past.valid_to, price_amount: 3000 },
      { valid_from: current.valid_from, valid_to: current.valid_to, price_amount: 4500 },
      { valid_from: later.valid_from, valid_to: null, price_amount: 5000 },
    ]);
  });

  it("writes the window in force now, else the base price", () => {
    const w = planPriceWrite([current, later], now, 4500);
    expect(w.kind).toBe("window");
    if (w.kind === "window") {
      expect(w.windows[0].price_amount).toBe(4500);
      expect(w.from).toBe(current.valid_from);
    }
    expect(planPriceWrite([later], now, 4500)).toEqual({ kind: "base" });
    expect(planPriceWrite([], now, 4500)).toEqual({ kind: "base" });
  });
});

describe("typed price", () => {
  it("reads major units into minor units", () => {
    expect(parseCategoryPrice("25", "EUR")).toEqual({ ok: true, value: 2500 });
    expect(parseCategoryPrice("25,5", "EUR")).toEqual({ ok: true, value: 2550 });
    expect(parseCategoryPrice(" 25.50 ", "EUR")).toEqual({ ok: true, value: 2550 });
    expect(parseCategoryPrice("500", "JPY")).toEqual({ ok: true, value: 500 });
  });
  it("refuses what is not a positive price in words that tell what to type", () => {
    for (const bad of ["", "0", "abc", "-5", "1,000", "25.505", "1e3"]) {
      expect(parseCategoryPrice(bad, "EUR"), bad).toEqual({ ok: false, error: "categories.price.error" });
    }
    expect(parseCategoryPrice("5.5", "JPY").ok).toBe(false);
  });
  it("writes a price back as the person would type it", () => {
    expect(priceInputValue(2500, "EUR")).toBe("25");
    expect(priceInputValue(2550, "EUR")).toBe("25.5");
    expect(priceInputValue(500, "JPY")).toBe("500");
    expect(priceInputValue(2505, "EUR")).toBe("25.05");
  });
});

describe("typed quantity", () => {
  it("takes a whole number from 1 to 1 000 000", () => {
    expect(parseCategoryQuantity("30")).toEqual({ ok: true, value: 30 });
    expect(parseCategoryQuantity(" 1 000 ")).toEqual({ ok: true, value: 1000 });
    expect(parseCategoryQuantity("1000000")).toEqual({ ok: true, value: 1_000_000 });
  });
  it("refuses everything else", () => {
    for (const bad of ["", "0", "-1", "1.5", "abc", "1000001", "12345678901"]) {
      expect(parseCategoryQuantity(bad), bad).toEqual({ ok: false, error: "categories.qty.error" });
    }
  });
});

describe("sale window edges in the venue's zone", () => {
  it("the first day starts at the venue's midnight (winter and summer)", () => {
    expect(zonedMidnight("2026-01-15", "Europe/Madrid").toISOString()).toBe("2026-01-14T23:00:00.000Z");
    expect(zonedMidnight("2026-07-15", "Europe/Madrid").toISOString()).toBe("2026-07-14T22:00:00.000Z");
    expect(zonedMidnight("2026-11-20", "Europe/Prague").toISOString()).toBe("2026-11-19T23:00:00.000Z");
    expect(zonedMidnight("2026-11-20", "America/New_York").toISOString()).toBe("2026-11-20T05:00:00.000Z");
  });

  it("is right on the days the clock changes", () => {
    // Madrid: clocks go back on 2026-10-25 (+02 to +01) and forward on 2026-03-29.
    expect(zonedMidnight("2026-10-25", "Europe/Madrid").toISOString()).toBe("2026-10-24T22:00:00.000Z");
    expect(zonedMidnight("2026-10-26", "Europe/Madrid").toISOString()).toBe("2026-10-25T23:00:00.000Z");
    expect(zonedMidnight("2026-03-29", "Europe/Madrid").toISOString()).toBe("2026-03-28T23:00:00.000Z");
    expect(zonedMidnight("2026-03-30", "Europe/Madrid").toISOString()).toBe("2026-03-29T22:00:00.000Z");
  });

  it("an unknown or missing zone counts as UTC", () => {
    expect(zonedMidnight("2026-11-20", "Nowhere/Land").toISOString()).toBe("2026-11-20T00:00:00.000Z");
    expect(zonedMidnight("2026-11-20", null).toISOString()).toBe("2026-11-20T00:00:00.000Z");
  });

  it("the last day is inclusive: it ends at the midnight after it", () => {
    expect(windowEdgeInstant("2026-11-20", "Europe/Prague", false)).toBe("2026-11-19T23:00:00.000Z");
    expect(windowEdgeInstant("2026-11-20", "Europe/Prague", true)).toBe("2026-11-20T23:00:00.000Z");
    // Across the clock change: the last day 2026-10-25 ends at the midnight of the 26th, +01.
    expect(windowEdgeInstant("2026-10-25", "Europe/Madrid", true)).toBe("2026-10-25T23:00:00.000Z");
  });

  it("reads a stored edge back as the day the organizer chose", () => {
    const start = windowEdgeInstant("2026-11-20", "Europe/Prague", false);
    const end = windowEdgeInstant("2026-11-25", "Europe/Prague", true);
    expect(edgeView(start, "Europe/Prague", false)).toEqual({ day: "2026-11-20" });
    expect(edgeView(end, "Europe/Prague", true)).toEqual({ day: "2026-11-25" });
    expect(edgeLabel(end, "Europe/Prague", true)).toBe("25.11.2026");
    // An edge that is not a midnight (set by an import) has no bare day.
    expect(edgeView("2026-11-25T18:30:00Z", "Europe/Prague", true)).toEqual({ day: null });
    expect(edgeLabel("2026-11-25T18:30:00Z", "Europe/Prague", true)).toBeNull();
  });

  it("finds the calendar day of an instant in the zone", () => {
    expect(dayOfInstant("2026-11-20T23:30:00Z", "Europe/Prague")).toBe("2026-11-21");
    expect(dayOfInstant("2026-11-20T23:30:00Z", "UTC")).toBe("2026-11-20");
  });

  it("limits the calendar to the date's sales end, and the last day to after the first", () => {
    const t = tier({ sale_window_start: windowEdgeInstant("2026-11-20", "Europe/Prague", false) });
    expect(windowBounds("start", t, "Europe/Prague", "2026-12-01T19:00:00Z")).toEqual({ max: "2026-12-01" });
    expect(windowBounds("end", t, "Europe/Prague", "2026-12-01T19:00:00Z")).toEqual({ min: "2026-11-20", max: "2026-12-01" });
    expect(windowBounds("end", tier(), "Europe/Prague", null)).toEqual({});
  });
});
