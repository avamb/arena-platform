/**
 * The rules of the "Links and QR" screens (spec 43, task QL-12), a port of
 * the bot's `eventbot/sales_link_dialog.go` so the two clients agree:
 *
 * - a target is an event, one date, the organization page, a promoter page
 *   or any http(s) address; the placement names where the code is put;
 * - the label is where the link is placed (1-120 characters), the address
 *   starts with http(s):// and has no spaces (the API checks the rest);
 * - utm labels are letters, digits and . _ ~ - (64 characters at most);
 *   an empty set means the server's defaults;
 * - the card explains why a code does not lead to its target from the
 *   server's `destination_reason`, and offers a retarget only where another
 *   target changes the address (the bot's `slWhyKey`).
 */
import type { MessageKey } from "@/lib/i18n/en";
import type { SalesLink, SalesLinkDayStats, SalesLinkTarget } from "@/lib/api/types";

export const PLACEMENTS = ["poster", "flyer", "sign", "social", "email", "messenger", "other"] as const;
export type Placement = (typeof PLACEMENTS)[number];

export const TARGET_KINDS = ["event", "session", "org_page", "promoter_page", "url"] as const;
export type TargetKind = (typeof TARGET_KINDS)[number];

export const UTM_KEYS = ["utm_source", "utm_medium", "utm_campaign", "utm_content", "utm_term"] as const;
export type UtmKey = (typeof UTM_KEYS)[number];

export const MAX_LABEL = 120;
export const MAX_URL = 2048;
const UTM_VALUE = /^[A-Za-z0-9._~-]{1,64}$/;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function isPlacement(v: unknown): v is Placement {
  return typeof v === "string" && (PLACEMENTS as readonly string[]).includes(v);
}

export function isUuid(v: unknown): v is string {
  return typeof v === "string" && UUID.test(v);
}

export type Parsed<T> = { ok: true; value: T } | { ok: false; error: MessageKey };

/** The label: spaces folded, 1 to 120 characters. */
export function parseLabel(raw: string): Parsed<string> {
  const value = raw.split(/\s+/).filter((w) => w.length > 0).join(" ");
  if (value.length === 0) return { ok: false, error: "links.err.labelEmpty" };
  if ([...value].length > MAX_LABEL) return { ok: false, error: "links.err.labelLong" };
  return { ok: true, value };
}

/** A typed address: http or https, no spaces, at most 2048 characters. */
export function parseUrl(raw: string): Parsed<string> {
  const value = raw.trim();
  const low = value.toLowerCase();
  const scheme = low.startsWith("http://") || low.startsWith("https://");
  if (!scheme || /\s/.test(value) || value.length < 11) return { ok: false, error: "links.err.url" };
  if ([...value].length > MAX_URL) return { ok: false, error: "links.err.urlLong" };
  return { ok: true, value };
}

/**
 * The utm labels of a form: empty values are dropped, a value outside the
 * server's alphabet is refused with the key it is on. An empty result means
 * "the server's defaults" on a create and "no labels" on a change.
 */
export function parseUtm(raw: Partial<Record<UtmKey, string>>): { ok: true; value: Record<string, string> } | { ok: false; error: MessageKey; key: UtmKey } {
  const value: Record<string, string> = {};
  for (const key of UTM_KEYS) {
    const v = (raw[key] ?? "").trim();
    if (v === "") continue;
    if (!UTM_VALUE.test(v)) return { ok: false, error: "links.err.utm", key };
    value[key] = v;
  }
  return { ok: true, value };
}

/** The utm of a link as the five form fields. */
export function utmFields(utm: Record<string, string> | null | undefined): Record<UtmKey, string> {
  const out = {} as Record<UtmKey, string>;
  for (const key of UTM_KEYS) out[key] = utm?.[key] ?? "";
  return out;
}

/** True when two targets name the same page: the kind and its one id or address. */
export function sameTarget(a: SalesLinkTarget, b: SalesLinkTarget): boolean {
  if (a.kind !== b.kind) return false;
  switch (a.kind) {
    case "event":
      return (a.event_id ?? null) === (b.event_id ?? null);
    case "session":
      return (a.session_id ?? null) === (b.session_id ?? null);
    case "promoter_page":
      return (a.promoter_id ?? null) === (b.promoter_id ?? null);
    case "url":
      return (a.url ?? "") === (b.url ?? "");
    default:
      return true; // org_page
  }
}

/** The body of a target as the API takes it: one id or address of its kind, nothing else. */
export function targetBody(t: SalesLinkTarget): SalesLinkTarget {
  switch (t.kind) {
    case "event":
      return { kind: "event", event_id: t.event_id ?? null };
    case "session":
      return { kind: "session", session_id: t.session_id ?? null };
    case "promoter_page":
      return { kind: "promoter_page", promoter_id: t.promoter_id ?? null };
    case "url":
      return { kind: "url", url: t.url ?? "" };
    default:
      return { kind: "org_page" };
  }
}

/** Is the target complete enough to send? */
export function targetComplete(t: SalesLinkTarget | null): t is SalesLinkTarget {
  if (t === null) return false;
  switch (t.kind) {
    case "event":
      return isUuid(t.event_id);
    case "session":
      return isUuid(t.session_id);
    case "promoter_page":
      return isUuid(t.promoter_id);
    case "url":
      return parseUrl(t.url ?? "").ok;
    case "org_page":
      return true;
    default:
      return false;
  }
}

/**
 * Why the code does not lead to the page its target names, as a text key,
 * or null when it does. The bot's `slWhyKey`: an archived link first, then
 * the server's reason, then the degraded flag alone.
 */
export function reasonKey(link: Pick<SalesLink, "status" | "destination_reason" | "destination_degraded">): MessageKey | null {
  if (link.status === "archived") return "links.why.archived";
  switch (link.destination_reason) {
    case "":
      return link.destination_degraded ? "links.why.degraded" : null;
    case "event_not_published":
      return "links.why.eventUnpublished";
    case "event_no_slug":
      return "links.why.eventNoPage";
    case "session_not_live":
      return "links.why.session";
    case "promoter_unavailable":
      return "links.why.promoter";
    case "no_page_address":
    case "org_not_found":
      return "links.why.noAddress";
    case "archived":
      return "links.why.archived";
    default:
      return "links.why.degraded";
  }
}

/** True when choosing another target would change where the code leads (the reason names the target itself). */
export function retargetHelps(link: Pick<SalesLink, "status" | "destination_reason" | "destination_degraded">): boolean {
  if (link.status === "archived") return false;
  if (link.destination_reason === "session_not_live" || link.destination_reason === "promoter_unavailable") return true;
  return link.destination_reason === "" && link.destination_degraded;
}

/** The key of a target's one-line description on the list and the card. */
export function targetKindKey(kind: string): MessageKey {
  switch (kind) {
    case "event":
      return "links.where.event";
    case "session":
      return "links.where.session";
    case "promoter_page":
      return "links.where.promoter";
    case "url":
      return "links.where.url";
    default:
      return "links.where.org";
  }
}

export function placementKey(kind: string): MessageKey {
  return (isPlacement(kind) ? `links.pl.${kind}` : "links.pl.other") as MessageKey;
}

/** `YYYY-MM-DD` of an instant in UTC: the API's day key. */
export function utcDay(d: Date): string {
  return d.toISOString().slice(0, 10);
}

/** Scans of today and of the last seven days (today included) from the per-day series, in UTC days. */
export function scanSums(byDay: readonly SalesLinkDayStats[], now: Date): { today: number; week: number } {
  const todayKey = utcDay(now);
  const weekFrom = utcDay(new Date(now.getTime() - 6 * 86_400_000));
  let today = 0;
  let week = 0;
  for (const d of byDay) {
    if (d.day === todayKey) today += d.hits;
    if (d.day >= weekFrom && d.day <= todayKey) week += d.hits;
  }
  return { today, week };
}

/** A country code as a name in the person's language, or the code itself, or "" when unknown. */
export function countryName(code: string, locale: string): string {
  if (code === "") return "";
  try {
    return new Intl.DisplayNames([locale], { type: "region" }).of(code.toUpperCase()) ?? code;
  } catch {
    return code;
  }
}

// ─── the addresses of the screens ───────────────────────────────────────────

export type LinksFilter = "active" | "archived" | "all";

export interface LinksSearch {
  f?: "archived" | "all";
  event?: string;
}

/** `/o/$orgId/links?f=archived&event=<uuid>`: a bad value falls back to the default. */
export function parseLinksSearch(raw: Record<string, unknown>): LinksSearch {
  const out: LinksSearch = {};
  if (raw.f === "archived" || raw.f === "all") out.f = raw.f;
  if (isUuid(raw.event)) out.event = raw.event.toLowerCase();
  return out;
}

export function linksFilter(search: LinksSearch): LinksFilter {
  return search.f ?? "active";
}

export interface LinkNewSearch {
  event?: string;
  session?: string;
}

/** `/o/$orgId/links/new?event=<uuid>&session=<uuid>`: the preselected target of an event or session card. */
export function parseLinkNewSearch(raw: Record<string, unknown>): LinkNewSearch {
  const out: LinkNewSearch = {};
  if (isUuid(raw.event)) out.event = raw.event.toLowerCase();
  if (isUuid(raw.session)) out.session = raw.session.toLowerCase();
  return out;
}

export interface LinkCardSearch {
  created?: true;
}

export function parseLinkCardSearch(raw: Record<string, unknown>): LinkCardSearch {
  return raw.created === true || raw.created === "1" || raw.created === 1 ? { created: true } : {};
}

/** The redirect of a code on the API host, for a person who wants to try it (the short host may not be set up locally). */
export function codeFromShortUrl(shortUrl: string): string {
  const i = shortUrl.lastIndexOf("/");
  return i < 0 ? shortUrl : shortUrl.slice(i + 1);
}
