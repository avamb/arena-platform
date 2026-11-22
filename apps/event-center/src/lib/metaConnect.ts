/**
 * The Meta connect flow without React (spec 66, sections 4.1 and 4.6).
 *
 * The server starts a connect and answers with a `login_url`. The browser goes there, Meta asks the person and
 * sends the code to the API callback, which exchanges it: no token ever reaches the browser.
 * The callback sends the person back with `?meta=connected` or `?meta=error&reason=<code>`.
 */
import type { MessageKey } from "@/lib/i18n/en";

export interface MetaStart {
  app_id: string;
  config_id: string;
  graph_version: string;
  state: string;
  login_url: string;
}

/**
 * Connect is the server redirect flow only (Meta's dialog/oauth with config_id): the browser goes to `login_url`,
 * which carries its own signed state. No SDK script, no popup. An empty `login_url` means the server has no
 * redirect address set, so nothing starts.
 */
export function startRedirect(start: MetaStart, navigate: (url: string) => void): "redirected" | "notConfigured" {
  if (start.login_url === "") return "notConfigured";
  navigate(start.login_url);
  return "redirected";
}

/** The reasons the callback puts in `?reason=`. */
export const META_REASONS = ["denied", "no_code", "forbidden", "exchange_failed", "graph_failed", "unavailable"] as const;
export type MetaReason = (typeof META_REASONS)[number];

export const REASON_KEY: Record<MetaReason | "unknown", MessageKey> = {
  denied: "meta.reason.denied",
  no_code: "meta.reason.no_code",
  forbidden: "meta.reason.forbidden",
  exchange_failed: "meta.reason.exchange_failed",
  graph_failed: "meta.reason.graph_failed",
  unavailable: "meta.reason.unavailable",
  unknown: "meta.reason.unknown",
};

/** What the address says after the callback: nothing, a success or an error with its reason. */
export type MetaReturn = { kind: "connected" } | { kind: "error"; reason: MetaReason | "unknown" } | null;

export function metaReturn(meta: unknown, reason: unknown): MetaReturn {
  if (meta === "connected") return { kind: "connected" };
  if (meta === "error") {
    const known = (META_REASONS as readonly string[]).includes(String(reason));
    return { kind: "error", reason: known ? (reason as MetaReason) : "unknown" };
  }
  return null;
}

/** The search of the route: only the two keys the callback sets. */
export function parseMetaSearch(search: Record<string, unknown>): { meta?: string; reason?: string } {
  const out: { meta?: string; reason?: string } = {};
  if (typeof search.meta === "string") out.meta = search.meta;
  if (typeof search.reason === "string") out.reason = search.reason;
  return out;
}
