/**
 * The one-time code of a link from the bot's "Open in browser" button
 * (spec 37 section 4.3): <event center>/login/link#code=<code>.
 *
 * The code sits after "#", so the browser never sends it to a server. The
 * app takes it out of the address BEFORE the first render and before any
 * request (main.tsx calls captureLinkCode first), so it stays neither in
 * the address bar nor in the browser history. It is kept in memory only.
 */

export const LINK_PATH = "/login/link";

/** The longest code accepted; the bot's codes are 43 characters. */
const MAX_CODE = 128;

let captured: string | null = null;

/** The code of a "#code=..." fragment, or null. */
export function parseLinkCode(hash: string): string | null {
  const code = new URLSearchParams(hash.replace(/^#/, "")).get("code")?.trim() ?? "";
  return code !== "" && code.length <= MAX_CODE ? code : null;
}

interface AddressLike {
  pathname: string;
  hash: string;
}

interface HistoryLike {
  state: unknown;
  replaceState(data: unknown, unused: string, url?: string | URL | null): void;
}

/** Takes the code out of the address of the link page. */
export function captureLinkCode(loc: AddressLike = window.location, hist: HistoryLike = window.history): void {
  if (loc.pathname !== LINK_PATH || loc.hash === "") return;
  captured = parseLinkCode(loc.hash);
  hist.replaceState(hist.state, "", LINK_PATH);
}

/** The captured code, or null when the page was opened without one. */
export function linkCode(): string | null {
  return captured;
}

/** Forgets the code once it was exchanged or refused. */
export function forgetLinkCode(): void {
  captured = null;
}
