/**
 * The Telegram mini app entry /tg (the seat manager and other screens opened
 * from the bot's web_app button).
 *
 * The bot opens   <event center>/tg?next=<URL-encoded path>
 * where the path is an event-center page `/o/<organization uuid>/...` (for
 * example the seat manager of one date, with `?embed=1`). The page:
 *
 *   1. loads the Telegram script (a copy in public/vendor, script-src stays
 *      'self') and reads `Telegram.WebApp.initData`;
 *   2. exchanges it at POST /v1/auth/telegram-miniapp, sending `next`;
 *   3. opens the `next` the server answers (a path it has checked itself;
 *      the signed `start_param` of the launch data wins over ours), or the
 *      organizations when it is empty.
 *
 * `next` is data from the address bar, so it is never trusted: the same shape
 * rule as the server's (SafeMiniappNext) runs here before the value is sent
 * and again before the page navigates, and nothing but a `/o/<uuid>/...` path
 * can ever be opened.
 */
import { ApiError } from "@/lib/api/http";
import type { MessageKey } from "@/lib/i18n";

export const TG_ENTER_PATH = "/tg";

const MAX_NEXT = 300;
// The mirror of hauth.miniappNextPattern.
const NEXT_PATTERN =
  /^\/o\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}(\/[A-Za-z0-9._~-]+)*\/?(\?[A-Za-z0-9._~=&-]*)?$/i;

/** `raw` when it is an event-center path the mini app may open, otherwise "" (never an error). */
export function safeNext(raw: unknown): string {
  if (typeof raw !== "string" || raw === "" || raw.length > MAX_NEXT || !NEXT_PATTERN.test(raw)) return "";
  const path = raw.split("?")[0] ?? "";
  if (path.split("/").some((seg) => seg === "." || seg === "..")) return "";
  return raw;
}

export interface TgEnterSearch {
  next?: string;
}

/** The route's search: `next` only when it is a safe path (a bad one is dropped, the page still signs in). */
export function parseTgEnterSearch(raw: Record<string, unknown>): TgEnterSearch {
  const next = safeNext(raw.next);
  return next === "" ? {} : { next };
}

/**
 * The Telegram error codes of POST /v1/auth/telegram-miniapp, each with its
 * own text that says what to do (text standard 2.1). Nothing technical.
 */
export function tgEnterErrorKey(err: unknown): MessageKey {
  if (err instanceof ApiError) {
    if (err.status === 0) return "common.error.network";
    switch (err.code) {
      case "auth.telegram_init_data_invalid":
        return "tgEnter.error.invalid";
      case "auth.telegram_init_data_expired":
        return "tgEnter.error.expired";
      case "auth.telegram_not_linked":
        return "tgEnter.error.notLinked";
      case "auth.user_deactivated":
        return "login.error.deactivated";
      case "auth.web_link_superadmin":
        return "linkLogin.error.superadmin";
      case "auth.rate_limited":
        return "login.error.rateLimited";
      case "auth.telegram_miniapp_not_configured":
        return "tgEnter.error.unavailable";
    }
    if (err.status === 429) return "login.error.rateLimited";
    if (err.status === 401) return "tgEnter.error.invalid";
    if (err.status === 403) return "tgEnter.error.notLinked";
    if (err.status === 503) return "tgEnter.error.unavailable";
  }
  return "common.error.generic";
}

export type TgEnterOutcome = { ok: true; to: string } | { ok: false; key: MessageKey };

export interface TgEnterDeps {
  /** Loads the Telegram script; resolves true when Telegram.WebApp exists afterwards. */
  loadScript: () => Promise<boolean>;
  /** The launch data of the page ("" outside Telegram). */
  initData: () => string;
  /** Signs in and answers the server's `next` ("" for none). */
  exchange: (initData: string, next: string | null) => Promise<string>;
}

/** Where the page goes when nothing better is known. */
export const TG_HOME = "/orgs";

/**
 * Opens the mini app. Never throws: a refusal comes back as the text to show.
 * The path to open is the server's, checked again here; our own `next` is the
 * fallback only if the server sent none.
 */
export async function enterTelegram(next: string | undefined, deps: TgEnterDeps): Promise<TgEnterOutcome> {
  const wanted = safeNext(next);
  await deps.loadScript();
  const initData = deps.initData();
  // No launch data: the page was opened in an ordinary browser, not by the bot.
  if (initData === "") return { ok: false, key: "tgEnter.error.outside" };
  try {
    const fromServer = safeNext(await deps.exchange(initData, wanted === "" ? null : wanted));
    return { ok: true, to: fromServer !== "" ? fromServer : TG_HOME };
  } catch (err) {
    return { ok: false, key: tgEnterErrorKey(err) };
  }
}

/** Set once the person entered through /tg: from then on the Telegram back button walks the history. */
let enteredThroughTelegram = false;
export function markEnteredThroughTelegram(): void {
  enteredThroughTelegram = true;
}
export function wasEnteredThroughTelegram(): boolean {
  return enteredThroughTelegram;
}
/** For tests. */
export function resetTelegramEntry(): void {
  enteredThroughTelegram = false;
}
