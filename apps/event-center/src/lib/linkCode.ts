/**
 * The one-time code of a link from the bot's "Open in browser" button
 * (spec 37 section 4.3): <event center>/login/link#code=<code>.
 *
 * The code sits after "#", so the browser never sends it to a server. The
 * app takes it out of the address BEFORE the first render and before any
 * request (main.tsx calls captureLinkCode first), so it stays neither in
 * the address bar nor in the browser history. It is kept in memory only.
 *
 * Spec 61, section 4.5: the link may also carry the screen to open after the
 * sign-in, <event center>/login/link#code=<code>&next=<url-encoded path>.
 * `next` comes from the address bar, so it is data, never trusted: only a
 * SHOP path of the event center passes (`safeLinkNext`), never an absolute
 * address, a protocol-relative one, a dotted segment, a query or a fragment.
 * Anything else is dropped without an error and the person lands on the
 * organizations as before.
 */

export const LINK_PATH = "/login/link";

/** The longest code accepted; the bot's codes are 43 characters. */
const MAX_CODE = 128;

let captured: string | null = null;
let capturedNext: string | null = null;

/** The longest `next` accepted: /o/<uuid>/shop/orders/<uuid> is 60 characters. */
const MAX_NEXT = 200;

/** Exactly the shop paths of one organization, segments of letters, digits, "_" and "-" only. */
const NEXT_PATTERN = /^\/o\/[0-9a-fA-F-]{36}\/shop(\/[A-Za-z0-9_-]+)*$/;

/** `raw` when it is a shop path of the event center the link may open, otherwise null (never an error). */
export function safeLinkNext(raw: unknown): string | null {
  if (typeof raw !== "string" || raw === "" || raw.length > MAX_NEXT || !NEXT_PATTERN.test(raw)) return null;
  return raw;
}

/** The screen a "#...&next=<path>" fragment names, when it is a safe shop path. */
export function parseLinkNext(hash: string): string | null {
  return safeLinkNext(new URLSearchParams(hash.replace(/^#/, "")).get("next"));
}

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
  capturedNext = captured === null ? null : parseLinkNext(loc.hash);
  hist.replaceState(hist.state, "", LINK_PATH);
}

/** The captured code, or null when the page was opened without one. */
export function linkCode(): string | null {
  return captured;
}

/** The screen to open after the sign-in, or null (the organizations). */
export function linkNext(): string | null {
  return capturedNext;
}

/** Forgets the code (and the screen with it) once it was exchanged or refused. */
export function forgetLinkCode(): void {
  captured = null;
  capturedNext = null;
}
