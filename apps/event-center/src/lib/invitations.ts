/**
 * Pure rules of the invitations screen (task 4-4): the guest list a person
 * pastes, and the one status the list shows for the letters of an invitation
 * issued by the OLD route (the "Earlier invitations" section). They are ports of the bot's rules
 * (eventbot/invite_recipients.go, invite.go) so the two channels accept the
 * same lists. Nothing here logs: the lines are guests' contact details.
 *
 * vitest runs without a DOM, so every rule that a click would exercise lives
 * here and is tested in invitations.test.ts.
 */
import type { OrderLetter } from "@/lib/api/types";
import type { Tone } from "@/lib/orders";

/** The most tickets of one operation (the API's own limit, 400 `complimentary.qty_too_large`). */
export const MAX_GUESTS = 50;
/** The longest guest name the web takes (the API accepts 200). */
export const MAX_NAME_RUNES = 100;

export interface Guest {
  name: string;
  email: string;
}

/** The reasons a line is refused; each has a text `invitations.reason.<reason>`. */
export type LineReason = "email" | "name" | "repeat" | "several";

export interface LineProblem {
  /** 1-based line number in the text the person typed. */
  line: number;
  /** The line as typed, trimmed. */
  text: string;
  reason: LineReason;
}

export interface ParsedGuests {
  guests: Guest[];
  problems: LineProblem[];
}

const LIST_MARKER = /^\s*(?:\d{1,3}[.)]|[-*•–—])\s+/;
const SEPARATOR = /[,;\t<>\s]+/;

/** The API's `ValidResendEmail`: no separators, one @, a dotted domain. */
export function validEmail(s: string): boolean {
  if (s === "" || s.length > 254 || /[ \t\r\n<>,;"]/.test(s)) return false;
  const at = s.lastIndexOf("@");
  if (at < 1 || s.indexOf("@") !== at) return false;
  const domain = s.slice(at + 1);
  return domain.includes(".") && !domain.startsWith(".") && !domain.endsWith(".");
}

function stripEdges(s: string, chars: string): string {
  let start = 0;
  let end = s.length;
  while (start < end && chars.includes(s[start])) start++;
  while (end > start && chars.includes(s[end - 1])) end--;
  return s.slice(start, end);
}

function parseLine(raw: string): { guest: Guest } | { reason: LineReason } {
  const line = raw.replace(LIST_MARKER, "").trim();
  const fields = line.split(SEPARATOR).filter((f) => f !== "");
  const withAt = fields.filter((f) => f.includes("@"));
  if (withAt.length === 0) return { reason: "email" };
  if (withAt.length > 1) return { reason: "several" };
  const addr = withAt[0];
  const email = stripEdges(addr, "\"'()[].:").toLowerCase();
  if (!validEmail(email)) return { reason: "email" };
  const name = stripEdges(
    fields
      .filter((f) => f !== addr)
      .join(" ")
      .trim(),
    "\"'«»“”-–—:|",
  ).trim();
  if (Array.from(name).length > MAX_NAME_RUNES) return { reason: "name" };
  return { guest: { name, email } };
}

/**
 * Reads the text of the guest field: one guest per line, "Name, e-mail" or
 * just the e-mail, in any order. Blank lines are skipped, a pasted list
 * marker ("1.", "-") is dropped, the same e-mail twice is a problem (one
 * invitation per person per operation, which stops a doubled pasted line from
 * mailing a guest twice). Problems are named by line; `guests` holds the lines
 * that were fine so the screen can show how many it understood.
 */
export function parseGuests(text: string): ParsedGuests {
  const guests: Guest[] = [];
  const problems: LineProblem[] = [];
  const taken = new Set<string>();
  text.replace(/\r\n/g, "\n").split("\n").forEach((raw, i) => {
    const line = raw.replace(/\r/g, "").trim();
    if (line === "") return;
    const r = parseLine(line);
    if ("reason" in r) {
      problems.push({ line: i + 1, text: line, reason: r.reason });
      return;
    }
    if (taken.has(r.guest.email)) {
      problems.push({ line: i + 1, text: line, reason: "repeat" });
      return;
    }
    taken.add(r.guest.email);
    guests.push(r.guest);
  });
  return { guests, problems };
}

/** Why the operation cannot start yet, or null when it can. */
export type SendBlock = "none" | "problems" | "tooMany";

/**
 * Only what the form can know for certain blocks the button. The count of free places is NOT such a thing:
 * the server also issues from the places the organizer closed for sale (reserve places, owner decision R-5),
 * which the category's `available` does not count. Whether the places suffice is the server's answer (409
 * `invitation.not_enough_places`), shown by the error notice.
 */
export function sendBlock(parsed: ParsedGuests): SendBlock | null {
  if (parsed.problems.length > 0) return "problems";
  if (parsed.guests.length === 0) return "none";
  if (parsed.guests.length > MAX_GUESTS) return "tooMany";
  return null;
}

/** True when there are more guests than free places: a hint, never a ban (see sendBlock). */
export function overFreePlaces(parsed: ParsedGuests, free: number | null): boolean {
  return free !== null && parsed.guests.length > 0 && parsed.guests.length > free;
}

// ─── the letter status of an invitation ─────────────────────────────────────

export const LETTER_STATES = ["none", "queued", "sent", "delayed", "delivered", "opened", "failed", "stuck"] as const;
export type LetterState = (typeof LETTER_STATES)[number];

/** Weakest first: the status of an invitation is the least advanced letter. */
const RANK: Record<LetterState, number> = {
  failed: 0,
  stuck: 1,
  queued: 2,
  delayed: 3,
  sent: 4,
  delivered: 5,
  opened: 6,
  none: 7,
};

function stateOf(l: Pick<OrderLetter, "status" | "stuck">): LetterState {
  if (l.status === "failed") return "failed";
  if (l.stuck) return "stuck";
  switch (l.status) {
    case "queued":
    case "sent":
    case "delayed":
    case "delivered":
    case "opened":
      return l.status;
    default:
      return "sent";
  }
}

/**
 * The one status shown for an invitation, from `letters[]` of the list: the
 * weakest of its letters (a guest with a failed letter shows failed, even when
 * another ticket of the issuance was delivered). `none` means no letter was
 * queued, for example a delivery job that was skipped.
 */
export function letterSummary(letters: readonly Pick<OrderLetter, "status" | "stuck">[] | undefined): LetterState {
  if (letters === undefined || letters.length === 0) return "none";
  let worst: LetterState = "none";
  for (const l of letters) {
    const s = stateOf(l);
    if (RANK[s] < RANK[worst]) worst = s;
  }
  return worst;
}

export function letterTone(state: LetterState): Tone {
  switch (state) {
    case "failed":
    case "stuck":
      return "bad";
    case "delayed":
    case "queued":
      return "wait";
    case "delivered":
    case "opened":
      return "ok";
    default:
      return "neutral";
  }
}

/** True when the organizer should look at the letter: it failed or never arrived. */
export function letterNeedsAttention(state: LetterState): boolean {
  return state === "failed" || state === "stuck";
}

// ─── the state of an issuance ───────────────────────────────────────────────

export const ISSUANCE_STATES = ["valid", "used", "revoked"] as const;
export type IssuanceState = (typeof ISSUANCE_STATES)[number];

export function isIssuanceState(s: string): s is IssuanceState {
  return (ISSUANCE_STATES as readonly string[]).includes(s);
}

export function issuanceTone(state: string): Tone {
  switch (state) {
    case "valid":
      return "ok";
    case "revoked":
      return "bad";
    default:
      return "neutral";
  }
}

/**
 * Whether the annul button is offered: only a valid invitation, and only to a
 * person who may issue (the revoke route reuses `complimentary.issue`). A
 * scanned ticket (`used`) cannot be annulled: the server parks it for manual
 * review, so the web does not offer it.
 */
export function canRevoke(state: string, canIssue: boolean): boolean {
  return canIssue && state === "valid";
}

// ─── categories and dates the picker offers ────────────────────────────────

/** A general-admission category with free places; a seated hall is refused. */
export function inviteTierOk(tier: { kind?: string | null; available?: number | null }): boolean {
  return tier.kind === "ga";
}

/** A date can be picked when it is not cancelled and not over. A seated one is issued on its seat map, see `isSeatedSession`. */
export function inviteSessionOk(session: { status: string; end_at: string }, now: Date): boolean {
  if (session.status === "cancelled" || session.status === "draft") return false;
  return new Date(session.end_at).getTime() > now.getTime();
}

/**
 * A date with a seating plan takes its invitations on the seat map of that date (spec 46): the old
 * general-admission form cannot choose a seat, and the server refuses it there with 422
 * `complimentary.use_invitations_route`.
 */
export function isSeatedSession(session: { seating_plan_version_id: string | null }): boolean {
  return session.seating_plan_version_id !== null;
}
