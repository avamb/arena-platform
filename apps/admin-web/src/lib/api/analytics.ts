/**
 * Own product analytics — the superadmin UX dashboards (spec 50, stage A8).
 *
 *   GET /v1/admin/analytics/flows
 *   GET /v1/admin/analytics/errors
 *   GET /v1/admin/analytics/people/{person_id}/timeline
 *
 * All three need `superadmin.read`, are read-only and need no reason. The
 * wire types mirror openapi/clients/ts/index.d.ts (AnalyticsFlowsResponse,
 * AnalyticsErrorsResponse, AnalyticsPersonTimeline). The pure helpers are
 * exported for analytics.test.ts. Mock data: NONE.
 */
import { authedFetch } from "@/lib/api/client";

// ---------------------------------------------------------------------------
// Wire types
// ---------------------------------------------------------------------------

export interface FlowNumbers {
  readonly entered: number;
  readonly left_here: number;
  readonly drop_off_pct: number | null;
  readonly errors: number;
  readonly median_seconds: number | null;
}

export interface FlowStep extends FlowNumbers {
  readonly step: string;
  readonly previous: FlowNumbers;
}

export interface Flow {
  readonly surface: string;
  readonly flow: string;
  readonly steps: readonly FlowStep[];
}

export interface FlowsResponse {
  readonly surface: string;
  readonly from: string;
  readonly to: string;
  readonly previous_from: string;
  readonly previous_to: string;
  readonly flows: readonly Flow[];
}

export interface ErrorItem {
  readonly kind: string;
  readonly surface: string;
  readonly key: string;
  readonly place: string;
  readonly count: number;
  readonly previous_count: number;
  readonly change_pct: number | null;
}

export interface ErrorsResponse {
  readonly surface: string;
  readonly from: string;
  readonly to: string;
  readonly previous_from: string;
  readonly previous_to: string;
  readonly items: readonly ErrorItem[];
}

export interface TimelineProfile {
  readonly stage: string;
  readonly last_seen_at: string | null;
  readonly visits_30d: number;
  readonly engaged_minutes_30d: number;
  readonly articles: ReadonlyArray<Record<string, unknown>>;
  readonly milestones: Record<string, unknown>;
  readonly activity_score: number;
  readonly score_explanation: string;
  readonly changed_at: string;
}

export interface TimelineSession {
  readonly id: string;
  readonly surface: string;
  readonly started_at: string;
  readonly ended_at: string;
  readonly pages: number;
  readonly engaged_seconds: number;
  readonly entry: Record<string, unknown>;
}

export interface TimelineEvent {
  readonly at: string;
  readonly surface: string;
  readonly name: string;
  readonly props: Record<string, unknown>;
}

export interface PersonTimeline {
  readonly person_id: string;
  readonly twenty_person_id: string | null;
  readonly user_id: string | null;
  readonly first_touch: Record<string, unknown>;
  readonly last_touch: Record<string, unknown>;
  readonly created_at: string;
  readonly profile: TimelineProfile | null;
  readonly sessions: readonly TimelineSession[];
  readonly events: readonly TimelineEvent[];
  readonly next_cursor: string | null;
}

// ---------------------------------------------------------------------------
// Query parameters
// ---------------------------------------------------------------------------

/** The surface filter values; "" means all surfaces. */
export const SURFACE_FILTERS: readonly string[] = ["", "bot", "event_center", "site"];

/** Period presets of the picker, in days. */
export const PERIOD_PRESETS: readonly number[] = [7, 14, 30, 90];

export interface AnalyticsQuery {
  readonly surface: string;
  /** YYYY-MM-DD (UTC) */
  readonly from: string;
  /** YYYY-MM-DD (UTC) */
  readonly to: string;
}

const DAY_RE = /^\d{4}-\d{2}-\d{2}$/;

/** UTC calendar day of a Date as YYYY-MM-DD. */
export function utcDay(d: Date): string {
  return d.toISOString().slice(0, 10);
}

/** The last `days` UTC days up to and including today. */
export function presetRange(days: number, now: Date): { from: string; to: string } {
  const to = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate()));
  const from = new Date(to.getTime() - (days - 1) * 86_400_000);
  return { from: utcDay(from), to: utcDay(to) };
}

/** True when from/to are dates and from is not after to. */
export function isValidRange(from: string, to: string): boolean {
  if (!DAY_RE.test(from) || !DAY_RE.test(to)) return false;
  const a = Date.parse(`${from}T00:00:00Z`);
  const b = Date.parse(`${to}T00:00:00Z`);
  if (Number.isNaN(a) || Number.isNaN(b)) return false;
  return a <= b && (b - a) / 86_400_000 < 180;
}

/** The query string of the flows and errors routes. */
export function buildQuery(q: AnalyticsQuery): string {
  const p = new URLSearchParams();
  if (q.surface !== "") p.set("surface", q.surface);
  p.set("from", q.from);
  p.set("to", q.to);
  return p.toString();
}

// ---------------------------------------------------------------------------
// Fetchers
// ---------------------------------------------------------------------------

export function fetchFlows(q: AnalyticsQuery): Promise<FlowsResponse> {
  return authedFetch<FlowsResponse>({
    method: "GET",
    path: `/v1/admin/analytics/flows?${buildQuery(q)}`,
  });
}

export function fetchErrors(q: AnalyticsQuery): Promise<ErrorsResponse> {
  return authedFetch<ErrorsResponse>({
    method: "GET",
    path: `/v1/admin/analytics/errors?${buildQuery(q)}`,
  });
}

export const TIMELINE_PAGE = 50;

export function fetchTimeline(personId: string, cursor: string | null): Promise<PersonTimeline> {
  const p = new URLSearchParams({ limit: String(TIMELINE_PAGE) });
  if (cursor !== null) p.set("cursor", cursor);
  return authedFetch<PersonTimeline>({
    method: "GET",
    path: `/v1/admin/analytics/people/${encodeURIComponent(personId)}/timeline?${p.toString()}`,
  });
}

// ---------------------------------------------------------------------------
// Pure display helpers
// ---------------------------------------------------------------------------

/** The zone every time of these screens is shown in. */
export const DISPLAY_ZONE = "Europe/Madrid";

/**
 * A moment in Madrid time: "2026-11-02 09:30 CET". The zone is named in the
 * text, so a time copied out of the screen is never ambiguous.
 */
export function formatMadrid(iso: string | null | undefined): string {
  if (iso === null || iso === undefined || iso === "") return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const parts = new Intl.DateTimeFormat("en-GB", {
    timeZone: DISPLAY_ZONE,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
    timeZoneName: "short",
  }).formatToParts(d);
  const get = (type: string): string => parts.find((p) => p.type === type)?.value ?? "";
  return `${get("year")}-${get("month")}-${get("day")} ${get("hour")}:${get("minute")} ${get("timeZoneName")}`;
}

/** Percent with one decimal at most: 47.5 -> "47.5%", 50 -> "50%", null -> "—". */
export function formatPct(v: number | null | undefined): string {
  if (v === null || v === undefined) return "—";
  return `${Number.isInteger(v) ? v : v.toFixed(1)}%`;
}

/** Seconds as "45 s" or "3 min 05 s"; null -> "—". */
export function formatSeconds(n: number | null | undefined): string {
  if (n === null || n === undefined) return "—";
  if (n < 60) return `${n} s`;
  const m = Math.floor(n / 60);
  const s = n % 60;
  return `${m} min ${String(s).padStart(2, "0")} s`;
}

/**
 * Change of a count against the previous period: "+50%", "-25%", "new" when
 * the previous period had none, "—" when both are zero.
 */
export function changeLabel(current: number, previous: number): string {
  if (previous <= 0) return current > 0 ? "new" : "—";
  const pct = Math.round(((current - previous) / previous) * 1000) / 10;
  if (pct === 0) return "0%";
  const text = Number.isInteger(pct) ? String(pct) : pct.toFixed(1);
  return pct > 0 ? `+${text}%` : `${text}%`;
}

/** Direction of a change for colouring: "up" is worse for losses and errors. */
export function changeDirection(current: number, previous: number): "up" | "down" | "flat" {
  if (current > previous) return "up";
  if (current < previous) return "down";
  return "flat";
}

/** Bar width in percent, clamped to 0-100. */
export function barWidth(pct: number | null | undefined): number {
  if (pct === null || pct === undefined || Number.isNaN(pct)) return 0;
  return Math.max(0, Math.min(100, pct));
}

/** A properties object as "key=value, key=value", keys sorted. */
export function propsText(props: Record<string, unknown> | null | undefined): string {
  if (!props) return "";
  return Object.keys(props)
    .sort()
    .map((k) => `${k}=${String(props[k])}`)
    .join(", ");
}

/** Milestones that are times, oldest first. gmv_30d (a money map) is not one. */
export function milestoneRows(ms: Record<string, unknown>): ReadonlyArray<{ name: string; at: string }> {
  return Object.entries(ms)
    .filter((e): e is [string, string] => typeof e[1] === "string")
    .map(([name, at]) => ({ name, at }))
    .sort((a, b) => a.at.localeCompare(b.at));
}

/** The currency-to-minor-units map of gmv_30d, or an empty list. */
export function gmvRows(ms: Record<string, unknown>): ReadonlyArray<{ currency: string; minor: number }> {
  const raw = ms["gmv_30d"];
  if (raw === null || typeof raw !== "object") return [];
  return Object.entries(raw as Record<string, unknown>)
    .filter((e): e is [string, number] => typeof e[1] === "number")
    .map(([currency, minor]) => ({ currency, minor }));
}

/** A touch object as "source / medium / campaign (ref_host)"; empty -> "—". */
export function touchText(t: Record<string, unknown> | null | undefined): string {
  if (!t) return "—";
  const bits = ["source", "medium", "campaign"]
    .map((k) => (typeof t[k] === "string" ? (t[k] as string) : ""))
    .filter((s) => s !== "");
  const host = typeof t["ref_host"] === "string" ? (t["ref_host"] as string) : "";
  if (bits.length === 0 && host === "") return "—";
  return host !== "" ? `${bits.join(" / ")} (${host})`.trim() : bits.join(" / ");
}

/** Link to a person's timeline page. */
export function timelineLink(personId: string): string {
  return `/analytics/people/${encodeURIComponent(personId)}`;
}
