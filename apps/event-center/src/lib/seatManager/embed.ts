/**
 * The seat manager also opens inside the Telegram mini app (spec 40, S3b). There
 * the page has no header: Telegram draws its own bar with the back button. The
 * flag is the address parameter ?embed=1, so the same screen serves both.
 */
import { useRouterState } from "@tanstack/react-router";

/** The tabs of the seat manager's panel. */
export const SEAT_TABS = ["brush", "changes", "rows", "history", "layout"] as const;
export type SeatTab = (typeof SEAT_TABS)[number];

export interface SeatsSearch {
  embed?: boolean;
  /** ?tab=layout opens the panel on "Suggest a layout"; without it the panel opens on the brush. */
  tab?: SeatTab;
}

/** ?embed=1 (or true) switches the page chrome off; ?tab=<tab> picks the first tab. Anything else is the normal page. */
export function parseSeatsSearch(raw: Record<string, unknown>): SeatsSearch {
  const out: SeatsSearch = {};
  if (raw.embed === true || raw.embed === 1 || raw.embed === "1" || raw.embed === "true") out.embed = true;
  const tab = raw.tab;
  if (typeof tab === "string" && (SEAT_TABS as readonly string[]).includes(tab)) out.tab = tab as SeatTab;
  return out;
}

/**
 * The address of the seat manager of a date, opened on the "Suggest a layout" tab. This is the entry point of the
 * step "Split seats by price" (spec 64, section 2.4): that step sends the organizer here with the hall already
 * drawn. It builds no screen itself.
 */
export function splitSeatsByPricePath(orgId: string, eventId: string, sessionId: string): string {
  return `/o/${encodeURIComponent(orgId)}/events/${encodeURIComponent(eventId)}/sessions/${encodeURIComponent(sessionId)}/seats?tab=layout`;
}

/** True when the current address asks for the page without its chrome. */
export function useEmbedded(): boolean {
  const search = useRouterState({ select: (s) => s.location.search as Record<string, unknown> });
  return parseSeatsSearch(search).embed === true;
}

/** The tab the current address asks for, or null. */
export function useSeatsTab(): SeatTab | null {
  const search = useRouterState({ select: (s) => s.location.search as Record<string, unknown> });
  return parseSeatsSearch(search).tab ?? null;
}
