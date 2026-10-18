/**
 * Pure rules of "Move or cancel a session" (task 5-2), the web twin of the
 * bot's Sessions dialog (eventbot/sessions_dialog.go, spec 30).
 *
 * The server owns the decision (internal/platform/sessionchange): the dry run
 * `GET .../sessions/{id}/change-impact` says which kinds of change buyers see,
 * how many orders get a letter, and whether the change is blocked. This file
 * only (1) turns a wall-clock date and time in the venue's zone into the
 * instant the API wants, (2) says which buttons a person sees, (3) builds the
 * PATCH bodies and the dry-run query, and (4) turns the dry-run answer into the
 * lines of the shared DryRunConfirm. Nothing here talks to the network.
 */
import type { SessionChangeImpact } from "@/lib/api/types";
import type { DryRunResult } from "@/lib/dryRun";
import type { Params } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";

// ─── Permissions and visibility ─────────────────────────────────────────────

/** The permission PATCH .../sessions/{id} asks for (the manager holds it since migration 0122). */
export const SESSION_UPDATE_PERMISSION = "session.update";
/** The permission PUT .../events/{id}/contact asks for. */
export const EVENT_UPDATE_PERMISSION = "event.update";
/** The longest message to buyers the API accepts, in characters (sessionchange.MaxMessageRunes). */
export const MAX_NOTICE_CHARS = 1000;

export interface ChangeButtons {
  move: boolean;
  cancel: boolean;
}

/**
 * Which of the two buttons a person sees on a session card. A cancelled or
 * finished session offers none. A draft can be moved but not cancelled: the
 * state machine allows scheduled -> cancelled only (PATCH answers 422
 * session.invalid_transition otherwise).
 */
export function changeButtons(status: string | null | undefined, can: (permission: string) => boolean): ChangeButtons {
  if (!can(SESSION_UPDATE_PERMISSION)) return { move: false, cancel: false };
  if (status === "scheduled") return { move: true, cancel: true };
  if (status === "draft") return { move: true, cancel: false };
  return { move: false, cancel: false };
}

// ─── Wall clock in the venue's zone <-> instant ─────────────────────────────

const TIME = /^([01]\d|2[0-3]):([0-5]\d)$/;
const DAY = /^(\d{4})-(\d{2})-(\d{2})$/;

/** True for "HH:MM" on a 24-hour clock. */
export function validTime(hhmm: string): boolean {
  return TIME.test(hhmm);
}

interface WallParts {
  year: number;
  month: number;
  day: number;
  hour: number;
  minute: number;
}

/** The wall clock of the instant `ms` in the zone, or null for an unknown zone. */
function wallClock(ms: number, zone: string): WallParts | null {
  try {
    const parts = new Intl.DateTimeFormat("en-CA", {
      timeZone: zone,
      hourCycle: "h23",
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
    }).formatToParts(new Date(ms));
    const get = (type: string): number => Number(parts.find((p) => p.type === type)?.value ?? "NaN");
    const out = { year: get("year"), month: get("month"), day: get("day"), hour: get("hour"), minute: get("minute") };
    return Object.values(out).some((n) => Number.isNaN(n)) ? null : out;
  } catch {
    return null;
  }
}

/** Offset of the zone from UTC at the instant `ms`, in milliseconds. */
function zoneOffset(ms: number, zone: string): number | null {
  const w = wallClock(ms, zone);
  if (w === null) return null;
  return Date.UTC(w.year, w.month - 1, w.day, w.hour, w.minute) - Math.floor(ms / 60000) * 60000;
}

/**
 * The instant (RFC 3339, UTC, whole seconds) at which the wall clock of
 * `zone` reads `day` + `hhmm`. Null when the input is malformed, the zone is
 * unknown, or the time does not exist there (the hour skipped when the clocks
 * go forward): the person must pick another time, nothing is guessed. An hour
 * that happens twice (the clocks go back) is read as its first occurrence.
 */
export function zonedToInstant(day: string, hhmm: string, zone: string): string | null {
  const d = DAY.exec(day);
  const tm = TIME.exec(hhmm);
  if (d === null || tm === null) return null;
  const [year, month, dd] = [Number(d[1]), Number(d[2]), Number(d[3])];
  const [hour, minute] = [Number(tm[1]), Number(tm[2])];
  const asUtc = Date.UTC(year, month - 1, dd, hour, minute);
  const oneDay = 24 * 3600 * 1000;
  const before = zoneOffset(asUtc - oneDay, zone);
  const after = zoneOffset(asUtc + oneDay, zone);
  if (before === null || after === null) return null;
  let guess: number | null = null;
  for (const off of new Set([before, after])) {
    const cand = asUtc - off;
    const check = wallClock(cand, zone);
    if (check === null || check.year !== year || check.month !== month || check.day !== dd || check.hour !== hour || check.minute !== minute) continue;
    if (guess === null || cand < guess) guess = cand;
  }
  if (guess === null) return null;
  return new Date(guess).toISOString().replace(/\.\d{3}Z$/, "Z");
}

/** The wall clock of an instant in a zone as an ISO day and "HH:MM" (UTC when the zone is unknown). */
export function instantToWall(iso: string, zone: string): { day: string; time: string } {
  const ms = Date.parse(iso);
  if (Number.isNaN(ms)) return { day: "", time: "" };
  const w = wallClock(ms, zone) ?? wallClock(ms, "UTC");
  if (w === null) return { day: "", time: "" };
  const p2 = (n: number) => String(n).padStart(2, "0");
  return { day: `${String(w.year).padStart(4, "0")}-${p2(w.month)}-${p2(w.day)}`, time: `${p2(w.hour)}:${p2(w.minute)}` };
}

/** True when two RFC 3339 instants are the same moment. */
export function sameInstant(a: string, b: string): boolean {
  const x = Date.parse(a);
  const y = Date.parse(b);
  return !Number.isNaN(x) && x === y;
}

// ─── What the person asked for ──────────────────────────────────────────────

export interface MoveRequest {
  /** The session now. */
  currentStartAt: string;
  currentVenueId: string | null;
  /** Wall clock the person chose, in the zone of the chosen venue. */
  day: string;
  time: string;
  /** Whether the person touched the day or the time. An untouched schedule keeps the instant. */
  scheduleTouched: boolean;
  /** The venue chosen, or "" for "keep". */
  venueId: string;
  /** The zone the day and time are read in. */
  zone: string;
}

export type MoveProblem = "none" | "invalidTime" | "gapTime";

export interface MoveProposal {
  problem: MoveProblem;
  /** The new start, or null when it stays (or cannot be read). */
  startAt: string | null;
  /** The new venue, or null when it stays. */
  venueId: string | null;
  /** True when something a buyer can see would change. */
  changed: boolean;
}

/** Works out the proposed start and venue from the form. */
export function proposeMove(req: MoveRequest): MoveProposal {
  let startAt: string | null = null;
  let problem: MoveProblem = "none";
  if (req.scheduleTouched) {
    if (!validTime(req.time) || req.day === "") {
      problem = "invalidTime";
    } else {
      const instant = zonedToInstant(req.day, req.time, req.zone);
      if (instant === null) problem = "gapTime";
      else if (!sameInstant(instant, req.currentStartAt)) startAt = instant;
    }
  }
  const venueId = req.venueId !== "" && req.venueId !== req.currentVenueId ? req.venueId : null;
  return { problem, startAt, venueId, changed: problem === "none" && (startAt !== null || venueId !== null) };
}

// ─── API bodies and query ───────────────────────────────────────────────────

export interface ImpactQuery {
  startAt?: string | null;
  venueId?: string | null;
  cancel?: boolean;
  locale: string;
}

/** The query of GET .../change-impact. Only what changes is sent. */
export function impactQuery(q: ImpactQuery): Record<string, string> {
  const out: Record<string, string> = { locale: q.locale };
  if (q.startAt !== undefined && q.startAt !== null) out.start_at = q.startAt;
  if (q.venueId !== undefined && q.venueId !== null) out.venue_id = q.venueId;
  if (q.cancel === true) out.status = "cancelled";
  return out;
}

export interface SessionPatchBody {
  start_at?: string;
  venue_id?: string;
  status?: "cancelled";
  notice?: { message: string };
}

/**
 * The body of PATCH .../sessions/{id} for a move. The notice travels only when
 * buyers get a letter; whatever it holds is what they read (an empty text is a
 * deliberate choice).
 */
export function moveBody(p: MoveProposal, message: string | null): SessionPatchBody {
  const body: SessionPatchBody = {};
  if (p.startAt !== null) body.start_at = p.startAt;
  if (p.venueId !== null) body.venue_id = p.venueId;
  if (message !== null) body.notice = { message };
  return body;
}

/** The body of PATCH .../sessions/{id} for a cancellation, same as the bot's. */
export function cancelBody(message: string | null): SessionPatchBody {
  const body: SessionPatchBody = { status: "cancelled" };
  if (message !== null) body.notice = { message };
  return body;
}

/** The characters of a message the way the API counts them (code points). */
export function noticeLength(message: string): number {
  return Array.from(message).length;
}

export function noticeTooLong(message: string): boolean {
  return noticeLength(message) > MAX_NOTICE_CHARS;
}

/** The message to send: null when no buyer gets a letter, else the text as typed. */
export function noticeToSend(impact: Pick<SessionChangeImpact, "orders"> | null, message: string): string | null {
  return impact !== null && impact.orders > 0 ? message : null;
}

// ─── The message to buyers ──────────────────────────────────────────────────

export interface MessageState {
  text: string;
  /** True once the person typed in the field. The suggested text no longer replaces theirs. */
  touched: boolean;
}

export const emptyMessage: MessageState = { text: "", touched: false };

/** A new suggested text arrived: it fills an untouched field and never overwrites a typed one. */
export function withSuggested(state: MessageState, suggested: string): MessageState {
  return state.touched ? state : { text: suggested, touched: false };
}

/** The person typed. */
export function typeMessage(text: string): MessageState {
  return { text, touched: true };
}

/** The person asked for the suggested text back. */
export function resetToSuggested(suggested: string): MessageState {
  return { text: suggested, touched: false };
}

// ─── Dry run → the lines of DryRunConfirm ───────────────────────────────────

export type SessionChangeMode = "move" | "cancel";

type Translate = (key: MessageKey, params?: Params) => string;

const KIND_KEYS: Record<string, MessageKey> = {
  date: "sessionChange.kind.date",
  time: "sessionChange.kind.time",
  venue: "sessionChange.kind.venue",
  cancelled: "sessionChange.kind.cancelled",
};

/** What the server blocks the change for, as the codes the API uses. */
export const BLOCKED_CONTACT = "contact_missing";
export const BLOCKED_SITE = "site_route_unsupported";

export function isContactBlocked(impact: Pick<SessionChangeImpact, "blocked"> | null): boolean {
  return impact !== null && impact.blocked === BLOCKED_CONTACT;
}

/**
 * The dry run as the shared component shows it. The texts say what happens to
 * the buyers; a cancellation says again that no money moves.
 */
export function impactToDryRun(impact: SessionChangeImpact, mode: SessionChangeMode, t: Translate): DryRunResult {
  const result: DryRunResult = {};
  const kinds = impact.kinds.map((k) => (KIND_KEYS[k] !== undefined ? t(KIND_KEYS[k]) : k));

  if (impact.blocked === BLOCKED_SITE) {
    result.blocked = t("sessionChange.blocked.site", { count: impact.site_orders });
  } else if (impact.blocked === BLOCKED_CONTACT) {
    result.blocked = t("sessionChange.blocked.contact");
  } else if (impact.blocked !== "") {
    result.blocked = impact.blocked;
  }

  if (mode === "move" && impact.kinds.length === 0) {
    result.summary = t("sessionChange.impact.noVisible");
    return result;
  }
  if (impact.orders === 0) {
    result.summary = t(mode === "cancel" ? "sessionChange.impact.noBuyersCancel" : "sessionChange.impact.noBuyersMove");
    return result;
  }

  result.summary = t(mode === "cancel" ? "sessionChange.impact.buyersCancel" : "sessionChange.impact.buyersMove");
  const figures: NonNullable<DryRunResult["figures"]> = [
    { label: t("sessionChange.fig.kinds"), value: kinds.join(", ") },
    { label: t("sessionChange.fig.orders"), value: impact.orders },
    { label: t("sessionChange.fig.tickets"), value: impact.tickets },
    { label: t("sessionChange.fig.arena"), value: impact.arena_orders },
  ];
  if (impact.site_orders > 0) figures.push({ label: t("sessionChange.fig.site"), value: impact.site_orders });
  if (impact.contact.complete) figures.push({ label: t("sessionChange.fig.contact"), value: impact.contact.email });
  result.figures = figures;

  const warnings: string[] = [];
  if (impact.no_address > 0) warnings.push(t("sessionChange.warn.noAddress", { count: impact.no_address }));
  if (mode === "cancel") warnings.push(t("sessionChange.cancel.refundNote"));
  if (warnings.length > 0) result.warnings = warnings;
  return result;
}
