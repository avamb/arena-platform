/**
 * The Telegram mini app page /tg/edit (spec 44, RT-12).
 *
 * The bot's «Править в редакторе» button (a Telegram web_app button) opens
 *
 *   <event center>/tg/edit#code=<code>&kind=<description|email_note>&event=<event id>
 *
 *   code   a one-time sign-in code of purpose "miniapp" (5 minutes). The page
 *          exchanges it at POST /v1/auth/web/link, the same call as the
 *          "Open in browser" link, for a 30-minute session.
 *   kind   which text the editor opens; event the event it belongs to.
 *
 * The code sits after "#", so it never reaches a server log. main.tsx calls
 * captureTgEdit BEFORE the first render and before any request: the three
 * parameters leave the address bar and the history, and are kept in memory
 * only. What else Telegram put into the fragment (tgWebAppData,
 * tgWebAppVersion, tgWebAppPlatform, tgWebAppThemeParams) STAYS: the Telegram
 * script reads it when it loads, which happens after this capture. Our code
 * is gone by then, so the script cannot copy it to sessionStorage.
 *
 * If a Telegram client turns out to drop the fragment of a web_app URL, the
 * fall-back of the spec is the code in the path (/tg/edit/<code>). The nginx
 * access log writes the path, so that change needs its own log rule first.
 */
import { linkLoginErrorKey } from "@/lib/errors";
import type { MessageKey } from "@/lib/i18n";

export const TG_EDIT_PATH = "/tg/edit";

/**
 * The Telegram script, a copy in public/vendor (script-src stays 'self'). The
 * file name carries the first 8 hex digits of its sha256, so a changed copy
 * has a new name and the one-year cache of nginx cannot serve the old one.
 * See README.md, "Telegram web app script".
 */
export const TELEGRAM_SCRIPT_SRC = "/vendor/telegram-web-app.124bdff7.js";

const MAX_CODE = 128;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export type TgEditKind = "description" | "email_note";

export interface TgEditTarget {
  code: string | null;
  kind: TgEditKind | null;
  eventId: string | null;
}

/** What the fragment says and what is left of it once our parameters are out. */
export interface TgEditFragment extends TgEditTarget {
  /** The fragment without code, kind and event, without "#"; "" when nothing is left. */
  rest: string;
}

export function parseTgEditFragment(hash: string): TgEditFragment {
  const params = new URLSearchParams(hash.replace(/^#/, ""));
  const code = params.get("code")?.trim() ?? "";
  const kind = params.get("kind");
  const eventId = params.get("event")?.trim() ?? "";
  for (const key of ["code", "kind", "event"]) params.delete(key);
  return {
    code: code !== "" && code.length <= MAX_CODE ? code : null,
    kind: kind === "description" || kind === "email_note" ? kind : null,
    eventId: UUID.test(eventId) ? eventId.toLowerCase() : null,
    rest: params.toString(),
  };
}

interface AddressLike {
  pathname: string;
  hash: string;
}

interface HistoryLike {
  state: unknown;
  replaceState(data: unknown, unused: string, url?: string | URL | null): void;
}

let captured: TgEditTarget | null = null;

/** Takes the parameters out of the address of the mini app page. */
export function captureTgEdit(loc: AddressLike = window.location, hist: HistoryLike = window.history): void {
  if (loc.pathname.replace(/\/+$/, "") !== TG_EDIT_PATH) return;
  const { rest, ...target } = parseTgEditFragment(loc.hash);
  captured = target;
  hist.replaceState(hist.state, "", rest === "" ? TG_EDIT_PATH : `${TG_EDIT_PATH}#${rest}`);
}

/** The captured parameters, or null when the page was not opened at /tg/edit. */
export function tgEditTarget(): TgEditTarget | null {
  return captured;
}

/** Forgets the code once it was exchanged or refused. */
export function forgetTgEditTarget(): void {
  captured = null;
}

/** The error text for a refused or failed exchange of a mini app code. */
export function tgEditErrorKey(err: unknown): MessageKey {
  const key = linkLoginErrorKey(err);
  return key === "linkLogin.error.invalid" ? "tgEdit.error.invalid" : key;
}

interface TelegramWebApp {
  close?: () => void;
  ready?: () => void;
}

/** A window, or a stand-in with a Telegram global (a window has no declared Telegram property). */
type TelegramWindow = object;

function webApp(win: TelegramWindow): TelegramWebApp | null {
  return (win as { Telegram?: { WebApp?: TelegramWebApp } }).Telegram?.WebApp ?? null;
}

let loading: Promise<boolean> | null = null;

/**
 * Loads the Telegram script. It resolves true when Telegram.WebApp exists
 * afterwards and false when the file is missing or refused: the page works
 * without it (the close button then goes back to the app).
 */
export function loadTelegramScript(doc: Document = document, win: TelegramWindow = window): Promise<boolean> {
  // One load per page: a second call (React StrictMode runs effects twice) waits for the first.
  if (loading !== null) return loading;
  loading = new Promise((resolve) => {
    if (webApp(win) !== null) {
      resolve(true);
      return;
    }
    const el = doc.createElement("script");
    el.src = TELEGRAM_SCRIPT_SRC;
    el.async = true;
    el.onload = () => {
      webApp(win)?.ready?.(); // tells Telegram the page is up
      resolve(webApp(win) !== null);
    };
    el.onerror = () => resolve(false);
    doc.head.appendChild(el);
  });
  return loading;
}

/** Closes the mini app. False when Telegram.WebApp is not there (a plain browser). */
export function closeMiniApp(win: TelegramWindow = window): boolean {
  const app = webApp(win);
  if (app?.close === undefined) return false;
  app.close();
  return true;
}

export type TgEditOutcome = { ok: true } | { ok: false; key: MessageKey };

export interface TgEditDeps {
  loadScript: () => Promise<boolean>;
  exchange: (code: string) => Promise<void>;
}

/**
 * Opens the page: starts the Telegram script (its outcome never blocks the
 * sign-in) and exchanges the code. The code is forgotten either way.
 */
export async function enterMiniApp(target: TgEditTarget | null, deps: TgEditDeps): Promise<TgEditOutcome> {
  const code = target?.code ?? null;
  forgetTgEditTarget();
  void deps.loadScript();
  if (code === null) return { ok: false, key: "tgEdit.error.missing" };
  try {
    await deps.exchange(code);
    return { ok: true };
  } catch (err) {
    return { ok: false, key: tgEditErrorKey(err) };
  }
}
