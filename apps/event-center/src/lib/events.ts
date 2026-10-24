/**
 * Pure helpers of the events screens. The SALES STATE comes from the server
 * (`sales_state`, computed once for bot and web): never recompute it here.
 */
import type { EventItem, SessionPlaceCounts, SummaryMoneyItem } from "@/lib/api/types";

export type EventFilter = "run" | "arc";

function time(iso: string | null | undefined): number | null {
  if (iso === null || iso === undefined) return null;
  const t = Date.parse(iso);
  return Number.isNaN(t) ? null : t;
}

/**
 * "run" = every event whose state is not archived, nearest session first
 * (no date last). "arc" = archived events, the latest one first. The same
 * split as the bot's «Мои мероприятия».
 */
export function filterEvents(events: readonly EventItem[], filter: EventFilter): EventItem[] {
  if (filter === "arc") {
    return events
      .filter((e) => e.sales_state === "archived")
      .sort((a, b) => (time(b.last_session_at) ?? 0) - (time(a.last_session_at) ?? 0));
  }
  return events
    .filter((e) => e.sales_state !== "archived")
    .sort((a, b) => {
      const ta = time(a.next_session_at) ?? time(a.first_session_at);
      const tb = time(b.next_session_at) ?? time(b.first_session_at);
      if (ta === null && tb === null) return a.name.localeCompare(b.name);
      if (ta === null) return 1;
      if (tb === null) return -1;
      return ta - tb;
    });
}

function fold(text: string): string {
  return text.toLocaleLowerCase().replace(/ё/g, "е");
}

/** Every typed word must occur in the name (case-insensitive, ё = е). */
export function searchEvents(events: readonly EventItem[], query: string): EventItem[] {
  const words = fold(query).split(/\s+/).filter((w) => w.length > 0);
  if (words.length === 0) return [...events];
  return events.filter((e) => {
    const name = fold(e.name);
    return words.every((w) => name.includes(w));
  });
}

export interface PlaceFigures {
  sold: number;
  total: number;
  available: number;
  held: number;
}

/** Seats and general admission places together, as the bot shows them. */
export function placeFigures(places: { seats: SessionPlaceCounts; ga: SessionPlaceCounts }): PlaceFigures {
  const sum = (k: keyof SessionPlaceCounts) => places.seats[k] + places.ga[k];
  return {
    sold: sum("sold"),
    total: sum("total"),
    available: sum("available"),
    held: sum("held"),
  };
}

/** Money lines worth showing: something was paid, is pending or was refunded. */
export function moneyLines(money: readonly SummaryMoneyItem[]): SummaryMoneyItem[] {
  return money.filter((m) => m.paid_orders > 0 || m.paid > 0 || m.refunded > 0 || m.pending > 0);
}

/** The server's sales states, in the bot's order. Unknown values stay as they are. */
export const SALES_STATES = ["on_sale", "upcoming", "sold_out", "archived"] as const;
export type SalesState = (typeof SALES_STATES)[number];

export function isKnownSalesState(s: string): s is SalesState {
  return (SALES_STATES as readonly string[]).includes(s);
}

/**
 * The sales chip to show next to an event, or null for none. A draft that
 * has not finished is never "on sale" (nobody can buy an unpublished event),
 * so it shows only its "Draft" badge; the server says `draft` for it since
 * UXH-01, and an older server's `on_sale` / `upcoming` / `sold_out` for a
 * draft is hidden here too. A finished draft still shows "Finished".
 */
export function visibleSalesState(event: Pick<EventItem, "status" | "sales_state">): SalesState | null {
  const state = event.sales_state;
  if (!isKnownSalesState(state)) return null;
  if (event.status === "draft" && state !== "archived") return null;
  return state;
}
