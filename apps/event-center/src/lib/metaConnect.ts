/**
 * The Meta connect flow without React (spec 66, sections 4.1 and 4.6).
 *
 * The server starts a connect and answers with a `login_url`. The browser goes there, Meta asks the person and
 * sends the code to the API callback. The callback does NOT exchange it: it sends the person back with
 * `?meta=finish#code=<code>&state=<state>` (the code travels in the fragment, never the query), and the page posts
 * code and state once to `.../integrations/meta/finish` in the signed-in session. No token ever reaches the browser.
 * An error comes back as `?meta=error&reason=<code>`.
 */
import { ApiError } from "@/lib/api/http";
import type { MessageKey } from "@/lib/i18n/en";

export interface MetaStart {
  app_id: string;
  config_id: string;
  graph_version: string;
  login_url: string;
}

/**
 * Connect is the server redirect flow only (Meta's dialog/oauth with config_id): the browser goes to `login_url`,
 * which carries its own signed state. No SDK script, no popup. An empty `login_url` would mean a broken server answer,
 * so nothing starts.
 */
export function startRedirect(start: MetaStart, navigate: (url: string) => void): "redirected" | "notConfigured" {
  if (start.login_url === "") return "notConfigured";
  navigate(start.login_url);
  return "redirected";
}

/** The reasons the callback puts in `?reason=` and the finish route answers as `meta.<reason>`. */
export const META_REASONS = [
  "denied",
  "no_code",
  "forbidden",
  "exchange_failed",
  "graph_failed",
  "unavailable",
  "state_invalid",
  "state_mismatch",
] as const;
export type MetaReason = (typeof META_REASONS)[number];

export const REASON_KEY: Record<MetaReason | "unknown", MessageKey> = {
  denied: "meta.reason.denied",
  no_code: "meta.reason.no_code",
  forbidden: "meta.reason.forbidden",
  exchange_failed: "meta.reason.exchange_failed",
  graph_failed: "meta.reason.graph_failed",
  unavailable: "meta.reason.unavailable",
  state_invalid: "meta.reason.state_invalid",
  state_mismatch: "meta.reason.state_mismatch",
  unknown: "meta.reason.unknown",
};

function knownReason(reason: unknown): MetaReason | "unknown" {
  return (META_REASONS as readonly string[]).includes(String(reason)) ? (reason as MetaReason) : "unknown";
}

/**
 * What the address says after the callback: nothing, a login to finish, a success (set by the page after finish)
 * or an error with its reason.
 */
export type MetaReturn =
  | { kind: "finish" }
  | { kind: "connected" }
  | { kind: "error"; reason: MetaReason | "unknown" }
  | null;

export function metaReturn(meta: unknown, reason: unknown): MetaReturn {
  if (meta === "finish") return { kind: "finish" };
  if (meta === "error") return { kind: "error", reason: knownReason(reason) };
  return null;
}

/** The search of the route: only the two keys the callback sets. */
export function parseMetaSearch(search: Record<string, unknown>): { meta?: string; reason?: string } {
  const out: { meta?: string; reason?: string } = {};
  if (typeof search.meta === "string") out.meta = search.meta;
  if (typeof search.reason === "string") out.reason = search.reason;
  return out;
}

/** Reads code and state from the fragment the callback set (`#code=…&state=…`); null when either is missing. */
export function readFinishHash(hash: string): { code: string; state: string } | null {
  const params = new URLSearchParams(hash.startsWith("#") ? hash.slice(1) : hash);
  const code = params.get("code") ?? "";
  const state = params.get("state") ?? "";
  if (code === "" || state === "") return null;
  return { code, state };
}

/** The sessionStorage key that carries a finish from the first load to the Meta screen. */
export const META_FINISH_KEY = "arena.metaFinish";

export interface MetaFinish {
  code: string;
  state: string;
  org: string;
}

interface FinishAddress {
  pathname: string;
  search: string;
  hash: string;
}
interface FinishHistory {
  state: unknown;
  replaceState(data: unknown, unused: string, url?: string | URL | null): void;
}
interface FinishStorage {
  setItem(key: string, value: string): void;
}

const META_PATH = /^\/o\/([^/]+)\/ads\/meta\/?$/;

/**
 * Called once at boot, before the router exists: takes code and state out of the address of the Meta return
 * (`/o/<org>/ads/meta?meta=finish#code=..&state=..`) into sessionStorage and removes the fragment from the address
 * and the history. Any other fragment with a code on that path is just removed. Other paths are not touched.
 */
export function captureMetaFinish(
  loc: FinishAddress = window.location,
  hist: FinishHistory = window.history,
  getStorage: () => FinishStorage = () => window.sessionStorage,
): void {
  const m = META_PATH.exec(loc.pathname);
  if (m === null || loc.hash === "") return;
  const hash = loc.hash;
  const params = new URLSearchParams(hash.startsWith("#") ? hash.slice(1) : hash);
  if (!params.has("code") && !params.has("state")) return;
  const isFinish = new URLSearchParams(loc.search).get("meta") === "finish";
  const login = isFinish ? readFinishHash(hash) : null;
  if (login !== null) {
    try {
      const entry: MetaFinish = { code: login.code, state: login.state, org: decodeURIComponent(m[1]) };
      getStorage().setItem(META_FINISH_KEY, JSON.stringify(entry));
    } catch {
      // No storage: the screen will say no_code, the code is never kept in the address.
    }
  }
  hist.replaceState(hist.state, "", loc.pathname + (isFinish ? "?meta=finish" : loc.search));
}

/** Reads the stored finish and deletes it, once. Null when nothing valid is stored. */
export function takeMetaFinish(getStorage: () => Pick<Storage, "getItem" | "removeItem"> = () => window.sessionStorage): MetaFinish | null {
  try {
    const st = getStorage();
    const raw = st.getItem(META_FINISH_KEY);
    st.removeItem(META_FINISH_KEY);
    if (raw === null) return null;
    const v = JSON.parse(raw) as Partial<MetaFinish>;
    if (typeof v.code !== "string" || typeof v.state !== "string" || typeof v.org !== "string") return null;
    if (v.code === "" || v.state === "") return null;
    return { code: v.code, state: v.state, org: v.org };
  } catch {
    return null;
  }
}

/** The reason of a failed finish: `meta.<reason>` of the error envelope, else the general message. */
export function finishReason(error: unknown): MetaReason | "unknown" {
  if (error instanceof ApiError && error.code.startsWith("meta.")) return knownReason(error.code.slice("meta.".length));
  return "unknown";
}
