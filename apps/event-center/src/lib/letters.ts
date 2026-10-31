/**
 * The order card's lines about the letters the buyer got (4-1), the web twin of
 * the bot's order_letters.go (owner decision 2026-10-13). Every letter goes
 * through ONE rendering from a LetterStatus. The status comes from the order's
 * `letters[]` (the e-mail provider's delivery events: delivered, opened,
 * delayed, bounced, stuck); the older `delivery[]` fold is used only for an
 * order card that carries no ticket letters.
 *
 * A proxy open (Apple Mail Privacy Protection) is delivery, never reading:
 * the API already folds it into `delivered`, this file never calls it opened.
 */
import type { OrderDetail, OrderLetter } from "@/lib/api/types";

export type LetterState =
  | "none"
  | "off"
  | "queued"
  | "partial_queued"
  | "sent"
  | "delivered"
  | "opened"
  | "partial"
  | "failed"
  | "delayed"
  | "stuck"
  | "bounced";

/** The steps a sent letter goes through, so the card names the step EVERY ticket's letter reached. */
const RANK: Record<string, number> = { sent: 1, delivered: 2, opened: 3 };
const RANK_STATE: Record<number, LetterState> = { 1: "sent", 2: "delivered", 3: "opened" };

export interface LetterStatus {
  state: LetterState;
  /** When the latest step happened (ISO), if known. */
  at: string | null;
  /** The address it went to. */
  to: string;
  /** Tickets (or refunds) whose letter went out. */
  done: number;
  /** Tickets (or refunds) a letter was meant for. */
  total: number;
  /** The last failure, cut to a line. */
  why: string;
}

const EMPTY: LetterStatus = { state: "none", at: null, to: "", done: 0, total: 0, why: "" };

/** True for a letter the organizer should act on. */
export function letterFailed(s: LetterStatus): boolean {
  return s.state === "failed" || s.state === "partial" || s.state === "bounced" || s.state === "stuck";
}

function oneLine(text: string | null | undefined, max: number): string {
  const line = (text ?? "")
    .split(/\s+/)
    .filter((w) => w !== "")
    .join(" ");
  const chars = Array.from(line);
  return chars.length > max ? chars.slice(0, max).join("") + "…" : line;
}

function later(a: string | null, b: string): string {
  return a === null || Date.parse(b) > Date.parse(a) ? b : a;
}

/** When a sent letter reached the step its status names. */
export function letterStepTime(l: OrderLetter): string | null {
  switch (l.status) {
    case "opened":
      if (l.opened_at) return l.opened_at;
      break;
    case "delivered":
      return l.delivered_at ?? l.proxy_opened_at ?? l.clicked_at ?? l.sent_at;
    case "delayed":
      if (l.deferred_at) return l.deferred_at;
      break;
  }
  return l.sent_at;
}

/** When a letter entry happened: sent, else queued, else failed (ms, -Infinity when unknown). */
function letterTime(l: OrderLetter): number {
  const at = l.sent_at ?? l.queued_at ?? l.failed_at;
  const ms = at ? Date.parse(at) : NaN;
  return Number.isNaN(ms) ? -Infinity : ms;
}

/**
 * The newest ticket letter of every ticket. A resend adds a letter for tickets
 * that already had one; the card speaks about the letter the buyer got last,
 * so an old bounce no longer counts once a later letter for the same tickets
 * went out. An entry that names no ticket is always kept.
 */
export function latestTicketLetters(letters: readonly OrderLetter[]): OrderLetter[] {
  const ticket = letters.filter((l) => l.kind === "ticket");
  const newest = new Map<string, OrderLetter>();
  for (const l of ticket) {
    for (const id of l.ticket_ids) {
      const cur = newest.get(id);
      if (cur === undefined || letterTime(l) > letterTime(cur)) newest.set(id, l);
    }
  }
  const keep = new Set(newest.values());
  return ticket.filter((l) => l.ticket_ids.length === 0 || keep.has(l));
}

/** The ticket letters of the order, folded into one status. */
export function ticketLetterStatus(order: Pick<OrderDetail, "letters" | "delivery" | "buyer_email">): LetterStatus {
  const letters = latestTicketLetters(order.letters ?? []);
  if (letters.length === 0) return ticketLetterStatusFromDelivery(order);
  const st: LetterStatus = { ...EMPTY, to: (order.buyer_email ?? "").trim() };
  let pending = 0;
  let failed = 0;
  let bounced = 0;
  let low = 0;
  let delayed = false;
  let stuck = false;
  for (const l of letters) {
    const n = l.ticket_ids.length === 0 ? 1 : l.ticket_ids.length;
    st.total += n;
    if (l.recipient.trim() !== "") st.to = l.recipient.trim();
    if (l.status === "queued") {
      pending += n;
    } else if (l.status === "failed") {
      if (l.sent_at) bounced += n;
      else failed += n;
      if (st.why === "") st.why = oneLine(l.failure_reason, 120);
    } else if (RANK[l.status] !== undefined || l.status === "delayed") {
      st.done += n;
      let rank = RANK[l.status] ?? 0;
      if (l.status === "delayed") {
        rank = RANK.sent;
        delayed = true;
      }
      if (low === 0 || rank < low) low = rank;
      stuck = stuck || l.stuck;
      const at = letterStepTime(l);
      if (at !== null) st.at = later(st.at, at);
    }
  }
  if (st.total === 0) st.state = "none";
  else if (st.done === st.total && stuck) st.state = "stuck";
  else if (st.done === st.total && delayed) st.state = "delayed";
  else if (st.done === st.total) st.state = RANK_STATE[low] ?? "sent";
  else if (pending > 0 && st.done > 0) st.state = "partial_queued";
  else if (pending > 0) st.state = "queued";
  else if (st.done > 0) st.state = "partial";
  else if (failed === 0 && bounced > 0) st.state = "bounced";
  else st.state = "failed";
  return st;
}

/** The older fold of delivery[] (the delivery jobs): only queued, sent and failed are known there. */
export function ticketLetterStatusFromDelivery(order: Pick<OrderDetail, "delivery" | "buyer_email">): LetterStatus {
  const st: LetterStatus = { ...EMPTY, to: (order.buyer_email ?? "").trim() };
  let pending = 0;
  for (const e of order.delivery ?? []) {
    const status = e.status === "processing" || e.status === "pending" ? "queued" : e.status;
    if (status === "sent") {
      st.total++;
      st.done++;
      if (e.sent_at) st.at = later(st.at, e.sent_at);
      const to = (e.recipient_email ?? "").trim();
      if (to !== "") st.to = to;
    } else if (status === "queued") {
      st.total++;
      pending++;
    } else if (status === "failed") {
      st.total++;
      if (e.last_error && st.why === "") st.why = oneLine(e.last_error, 120);
    }
  }
  if (st.total === 0) st.state = "none";
  else if (st.done === st.total) st.state = "sent";
  else if (pending > 0 && st.done > 0) st.state = "partial_queued";
  else if (pending > 0) st.state = "queued";
  else if (st.done > 0) st.state = "partial";
  else st.state = "failed";
  return st;
}

/** The refund letter of the order, or null when the order has no refund operation that could write to the buyer. */
export function refundLetterStatus(order: Pick<OrderDetail, "refund_letter" | "letters" | "buyer_email">): LetterStatus | null {
  const r = order.refund_letter;
  if (r === null || r === undefined) return null;
  const st: LetterStatus = { ...EMPTY, at: r.at, to: (order.buyer_email ?? "").trim(), done: r.sent, total: r.refunds };
  switch (r.state) {
    case "off":
      st.state = "off";
      break;
    case "queued":
      st.state = "queued";
      break;
    case "partial":
      st.state = "partial_queued";
      break;
    case "sent":
      st.state = "sent";
      withLatestRefundLetter(st, order.letters ?? []);
      break;
    case "failed":
      st.state = "failed";
      break;
    default:
      return null;
  }
  return st;
}

/** Moves a sent refund letter on to what the e-mail provider reported about the newest one. */
function withLatestRefundLetter(st: LetterStatus, letters: readonly OrderLetter[]): void {
  let latest: OrderLetter | null = null;
  for (const l of letters) {
    if (l.kind !== "refund" || !l.sent_at) continue;
    if (latest === null || Date.parse(l.sent_at) > Date.parse(latest.sent_at as string)) latest = l;
  }
  if (latest === null) return;
  if (latest.recipient.trim() !== "") st.to = latest.recipient.trim();
  if (latest.status === "failed") {
    st.state = "bounced";
    st.at = latest.failed_at;
    st.why = oneLine(latest.failure_reason, 120);
  } else if (latest.stuck) {
    st.state = "stuck";
    st.at = latest.sent_at;
  } else if (latest.status === "delayed" || (RANK[latest.status] ?? 0) > RANK.sent) {
    st.state = latest.status as LetterState;
    st.at = letterStepTime(latest);
  }
}

/** One point of the letter scale: sent, delivered, opened, or the problem that stopped it. */
export type LetterStepKey = "sent" | "delivered" | "opened" | "delayed" | "stuck" | "bounced" | "failed";
export interface LetterStep {
  key: LetterStepKey;
  mark: "done" | "todo" | "wait" | "bad";
  /** When every letter reached this point (ISO), if known. */
  at: string | null;
}

function latest(values: readonly (string | null | undefined)[]): string | null {
  let out: string | null = null;
  for (const v of values) if (v) out = later(out, v);
  return out;
}

/**
 * The three points of the ticket letter scale for a status, with the time each
 * was reached by the newest letters. null when there is no letter to show.
 */
export function ticketLetterSteps(order: Pick<OrderDetail, "letters">, st: LetterStatus): LetterStep[] | null {
  const ls = latestTicketLetters(order.letters ?? []);
  const sent = latest(ls.map((l) => l.sent_at));
  const delivered = latest(ls.map((l) => l.delivered_at ?? l.proxy_opened_at ?? l.clicked_at ?? (l.opened_at ? l.sent_at : null)));
  const opened = latest(ls.map((l) => l.opened_at));
  const todo = (key: LetterStepKey): LetterStep => ({ key, mark: "todo", at: null });
  const done = (key: LetterStepKey, at: string | null): LetterStep => ({ key, mark: "done", at });
  switch (st.state) {
    case "queued":
      return [todo("sent"), todo("delivered"), todo("opened")];
    case "partial_queued":
    case "partial":
      return [done("sent", null), todo("delivered"), todo("opened")];
    case "sent":
      return [done("sent", sent ?? st.at), todo("delivered"), todo("opened")];
    case "delivered":
      return [done("sent", sent), done("delivered", delivered ?? st.at), todo("opened")];
    case "opened":
      return [done("sent", sent), done("delivered", delivered), done("opened", opened ?? st.at)];
    case "delayed":
      return [done("sent", sent), { key: "delayed", mark: "wait", at: latest(ls.map((l) => l.deferred_at)) }, todo("opened")];
    case "stuck":
      return [done("sent", sent), { key: "stuck", mark: "bad", at: null }, todo("opened")];
    case "bounced":
      return [done("sent", sent), { key: "bounced", mark: "bad", at: latest(ls.map((l) => l.failed_at)) }, todo("opened")];
    case "failed":
      return [{ key: "failed", mark: "bad", at: latest(ls.map((l) => l.failed_at)) }, todo("delivered"), todo("opened")];
    default:
      return null;
  }
}

/** One thing that happened to a letter, for the letter history. */
export type LetterEventKey = "queued" | "sent" | "delayed" | "delivered" | "proxy" | "opened" | "clicked" | "failed";
export interface LetterEvent {
  key: LetterEventKey;
  at: string;
}

/** What happened to one letter, oldest first. */
export function letterEvents(l: OrderLetter): LetterEvent[] {
  const all: [LetterEventKey, string | null | undefined][] = [
    ["queued", l.sent_at ? null : l.queued_at],
    ["sent", l.sent_at],
    ["delayed", l.deferred_at],
    ["delivered", l.delivered_at],
    ["proxy", l.proxy_opened_at],
    ["opened", l.opened_at],
    ["clicked", l.clicked_at],
    ["failed", l.failed_at],
  ];
  return all
    .filter((e): e is [LetterEventKey, string] => typeof e[1] === "string" && e[1] !== "")
    .map(([key, at]) => ({ key, at }))
    .sort((a, b) => Date.parse(a.at) - Date.parse(b.at));
}

export interface LetterHistoryEntry {
  letter: OrderLetter;
  /** A ticket letter for tickets an earlier ticket letter already carried. */
  resend: boolean;
  /** Sent to an address other than the order's own. */
  otherAddress: boolean;
}

/** Every letter of the order, oldest first, each marked as a resend or a letter to another address. */
export function letterHistory(order: Pick<OrderDetail, "letters" | "buyer_email">): LetterHistoryEntry[] {
  const own = (order.buyer_email ?? "").trim().toLowerCase();
  const sorted = [...(order.letters ?? [])].filter((l) => l.kind !== "invitation").sort((a, b) => letterTime(a) - letterTime(b));
  const seen = new Set<string>();
  return sorted.map((l) => {
    const resend = l.kind === "ticket" && l.ticket_ids.some((id) => seen.has(id));
    if (l.kind === "ticket") for (const id of l.ticket_ids) seen.add(id);
    const to = l.recipient.trim().toLowerCase();
    return { letter: l, resend, otherAddress: to !== "" && own !== "" && to !== own };
  });
}

/** The visual tone of a letter line. A tone always comes with words. */
export function letterTone(s: LetterStatus): "ok" | "bad" | "neutral" {
  if (letterFailed(s)) return "bad";
  if (s.state === "delivered" || s.state === "opened" || s.state === "sent") return "ok";
  return "neutral";
}
