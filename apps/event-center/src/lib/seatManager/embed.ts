/**
 * The seat manager also opens inside the Telegram mini app (spec 40, S3b). There
 * the page has no header: Telegram draws its own bar with the back button. The
 * flag is the address parameter ?embed=1, so the same screen serves both.
 */
import { useRouterState } from "@tanstack/react-router";

export interface SeatsSearch {
  embed?: boolean;
}

/** ?embed=1 (or true) switches the page chrome off. Anything else is the normal page. */
export function parseSeatsSearch(raw: Record<string, unknown>): SeatsSearch {
  return raw.embed === true || raw.embed === 1 || raw.embed === "1" || raw.embed === "true" ? { embed: true } : {};
}

/** True when the current address asks for the page without its chrome. */
export function useEmbedded(): boolean {
  const search = useRouterState({ select: (s) => s.location.search as Record<string, unknown> });
  return parseSeatsSearch(search).embed === true;
}
