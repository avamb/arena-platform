/**
 * The Telegram mini app page /tg/invitations (spec 46, 4.2, INV-05m).
 *
 * The bot's «Invitations» button on the session card (a Telegram web_app
 * button, the same trick as «Править в редакторе») opens
 *
 *   <event center>/tg/invitations?session=<session id>&event=<event id>#code=<code>
 *
 *   code     a one-time sign-in code of purpose "miniapp" (5 minutes), exchanged
 *            at POST /v1/auth/web/link for a 30-minute session, exactly as the
 *            description editor does (lib/tgEdit.ts). It sits after "#".
 *   session  the session (date) whose invitations the page opens.
 *   event    the event that session belongs to.
 *
 * session and event are accepted in the query AND in the fragment, so a bot
 * that puts everything after "#" works too. The code is only ever read from
 * the fragment: a code in a query string would reach a server log.
 *
 * main.tsx calls captureTgInvitations BEFORE the first render and before any
 * request: the three values leave the address bar and the history and are kept
 * in memory only. What Telegram put into the fragment (tgWebAppData and the
 * rest) stays, its script reads it after this capture.
 *
 * The page never believes the ids: the event is looked up through the
 * organization-scoped events list of every organization of the person, and the
 * session among that event's sessions. Without a code the page falls back to
 * Telegram's signed launch data (the way /tg signs in), and to a session that
 * is already open, so a reopened window keeps working.
 */
import { ApiError } from "@/lib/api/http";
import type { EventItem, SessionItem } from "@/lib/api/types";
import type { MessageKey } from "@/lib/i18n";
import { tgEditErrorKey } from "@/lib/tgEdit";
import { failureKey, findEvent } from "@/lib/tgEditEvent";
import { tgEnterErrorKey } from "@/lib/tgEnter";

export const TG_INVITATIONS_PATH = "/tg/invitations";

const MAX_CODE = 128;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export interface TgInvitationsTarget {
  code: string | null;
  sessionId: string | null;
  eventId: string | null;
}

export interface TgInvitationsAddress extends TgInvitationsTarget {
  /** The fragment without our parameters, without "#"; "" when nothing is left. */
  rest: string;
}

function uuidOrNull(raw: string | null | undefined): string | null {
  const v = raw?.trim() ?? "";
  return UUID.test(v) ? v.toLowerCase() : null;
}

/** What the address says: the code from the fragment, the ids from the fragment first and then the query. */
export function parseTgInvitationsAddress(search: string, hash: string): TgInvitationsAddress {
  // Telegram may put its own part after a second "#": past it nothing is ours, and a value must never swallow it.
  const body = hash.replace(/^#/, "");
  const cut = body.indexOf("#");
  const own = new URLSearchParams(cut < 0 ? body : body.slice(0, cut));
  const foreign = cut < 0 ? "" : body.slice(cut + 1);
  const query = new URLSearchParams(search);
  const code = own.get("code")?.trim() ?? "";
  const target: TgInvitationsTarget = {
    code: code !== "" && code.length <= MAX_CODE ? code : null,
    sessionId: uuidOrNull(own.get("session") ?? query.get("session")),
    eventId: uuidOrNull(own.get("event") ?? query.get("event")),
  };
  for (const key of ["code", "session", "event"]) own.delete(key);
  return { ...target, rest: [own.toString(), foreign].filter((s) => s !== "").join("&") };
}

interface AddressLike {
  pathname: string;
  search: string;
  hash: string;
}

interface HistoryLike {
  state: unknown;
  replaceState(data: unknown, unused: string, url?: string | URL | null): void;
}

let captured: TgInvitationsTarget | null = null;

/** Takes the parameters out of the address of the mini app page. */
export function captureTgInvitations(loc: AddressLike = window.location, hist: HistoryLike = window.history): void {
  if (loc.pathname.replace(/\/+$/, "") !== TG_INVITATIONS_PATH) return;
  const { rest, ...target } = parseTgInvitationsAddress(loc.search, loc.hash);
  captured = target;
  hist.replaceState(hist.state, "", rest === "" ? TG_INVITATIONS_PATH : `${TG_INVITATIONS_PATH}#${rest}`);
}

/** The captured parameters, or null when the page was not opened at /tg/invitations. */
export function tgInvitationsTarget(): TgInvitationsTarget | null {
  return captured;
}

/** Forgets the code once it was exchanged or refused. */
export function forgetTgInvitationsTarget(): void {
  captured = null;
}

/** The error text of a refused or failed sign-in with the one-time code. */
export function tgInvitationsErrorKey(err: unknown): MessageKey {
  const key = tgEditErrorKey(err);
  return key === "tgEdit.error.invalid" ? "tgInv.error.invalid" : key;
}

/** What the page shows once the person is signed in and the session looked up. */
export type TgInvitationsOpened =
  | { phase: "ready"; orgId: string; eventId: string; sessionId: string; event: EventItem; session: SessionItem }
  | { phase: "noAccess" }
  | { phase: "error"; key: MessageKey };

export interface OpenTgInvitationsDeps {
  /** Loads the Telegram script; resolves true when Telegram.WebApp exists afterwards. */
  loadScript: () => Promise<boolean>;
  /** The signed launch data of the page ("" outside Telegram). */
  initData: () => string;
  /** True when a session of the app is already open. */
  hasSession: () => boolean;
  /** Exchanges the one-time code for a session. */
  exchangeCode: (code: string) => Promise<void>;
  /** Signs in with Telegram's launch data. */
  exchangeTelegram: (initData: string, next: string | null) => Promise<string>;
  /** The organizations the person belongs to (platform roles left out). */
  organizations: () => Promise<readonly string[]>;
  /** The events list of one organization. */
  list: (orgId: string) => Promise<{ events: readonly EventItem[] }>;
  /** The sessions of one event. */
  sessions: (orgId: string, eventId: string) => Promise<{ sessions: readonly SessionItem[] }>;
}

type SignedIn = { ok: true } | { ok: false; key: MessageKey };

async function signIn(code: string | null, deps: OpenTgInvitationsDeps): Promise<SignedIn> {
  if (code !== null) {
    void deps.loadScript();
    try {
      await deps.exchangeCode(code);
      return { ok: true };
    } catch (err) {
      return { ok: false, key: tgInvitationsErrorKey(err) };
    }
  }
  await deps.loadScript();
  const initData = deps.initData();
  if (initData !== "") {
    try {
      await deps.exchangeTelegram(initData, null);
      return { ok: true };
    } catch (err) {
      return { ok: false, key: tgEnterErrorKey(err) };
    }
  }
  return deps.hasSession() ? { ok: true } : { ok: false, key: "tgInv.error.missing" };
}

/**
 * Opens the page: the Telegram script, the sign-in, then the event and the
 * session of the link among the person's own organizations. A link without a
 * session or an event, or one the person cannot see, ends in a calm message,
 * never in a half-open screen. Never throws.
 */
export async function openTgInvitations(target: TgInvitationsTarget | null, deps: OpenTgInvitationsDeps): Promise<TgInvitationsOpened> {
  forgetTgInvitationsTarget();
  const entered = await signIn(target?.code ?? null, deps);
  if (!entered.ok) return { phase: "error", key: entered.key };
  const sessionId = target?.sessionId ?? null;
  const eventId = target?.eventId ?? null;
  if (sessionId === null || eventId === null) return { phase: "error", key: "tgInv.error.missing" };
  try {
    const found = await findEvent(eventId, await deps.organizations(), deps.list);
    if (found === null) return { phase: "noAccess" };
    const list = await deps.sessions(found.orgId, eventId);
    const session = list.sessions.find((s) => s.id === sessionId);
    if (session === undefined) return { phase: "noAccess" };
    return { phase: "ready", orgId: found.orgId, eventId, sessionId, event: found.event, session };
  } catch (err) {
    // A refusal of the sessions list is "not yours"; anything else (network, 500) is not mistaken for it.
    if (err instanceof ApiError && (err.status === 403 || err.status === 404)) return { phase: "noAccess" };
    return { phase: "error", key: failureKey(err) };
  }
}

/** The page of the same invitations in the web event center, for what the phone cannot do (CSV, a big screen). */
export function webInvitationsPath(orgId: string, eventId: string, sessionId: string): string {
  return `/o/${orgId}/events/${eventId}/sessions/${sessionId}/invitations`;
}
