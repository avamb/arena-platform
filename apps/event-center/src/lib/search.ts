/**
 * Query-string parsers of the screens. A bad or unknown value falls back
 * to the default: a hand-edited address never breaks a screen.
 */
import type { EventFilter } from "@/lib/events";
import type { OrderTab } from "@/lib/api/endpoints";

export interface LoginSearch {
  redirect?: string;
  expired?: boolean;
}

/** Only a path inside this app is a safe place to return to. */
export function safeRedirect(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  if (!value.startsWith("/") || value.startsWith("//") || value.includes("\\")) return undefined;
  return value;
}

export function parseLoginSearch(raw: Record<string, unknown>): LoginSearch {
  const out: LoginSearch = {};
  const redirect = safeRedirect(raw.redirect);
  if (redirect !== undefined) out.redirect = redirect;
  if (raw.expired === true || raw.expired === "1" || raw.expired === 1) out.expired = true;
  return out;
}

export interface EventsSearch {
  f?: EventFilter;
  q?: string;
  page?: number;
}

export function parseEventsSearch(raw: Record<string, unknown>): EventsSearch {
  const out: EventsSearch = {};
  if (raw.f === "arc") out.f = "arc";
  if (typeof raw.q === "string" && raw.q.trim().length > 0) out.q = raw.q.slice(0, 100);
  const page = Number(raw.page);
  if (Number.isInteger(page) && page > 1) out.page = page;
  return out;
}

export interface OrdersSearch {
  tab?: OrderTab;
  event?: string;
  session?: string;
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function parseOrdersSearch(raw: Record<string, unknown>): OrdersSearch {
  const out: OrdersSearch = {};
  if (raw.tab === "paid" || raw.tab === "unpaid") out.tab = raw.tab;
  // No `q`: the orders search never goes into the address (review finding L5).
  if (typeof raw.event === "string" && UUID.test(raw.event)) out.event = raw.event;
  if (typeof raw.session === "string" && UUID.test(raw.session)) out.session = raw.session;
  return out;
}
