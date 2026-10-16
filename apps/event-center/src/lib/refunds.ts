/**
 * The pure rules of the web refund (task 8-1). They are the rules of the
 * Telegram bot's refund dialog (eventbot/refund_dialog.go, EC-21/22), ported
 * without its Telegram shape. The web computes nothing about money on its
 * own: what can be refunded, by whom and how is the SERVER's answer
 * (`GET .../orders/{order_id}/refund-options`); this file only turns a choice
 * on the screen into the request the server checks again.
 *
 * - Nothing is ticked at the start (owner decision 2026-10-13).
 * - The amount of EVERY selected ticket is sent explicitly, so the number on
 *   the screen is the number sent.
 * - A part of the amount is possible for ONE ticket, and only when the
 *   provider can return a part (`partial_refund`).
 * - The tickets always stop being valid, even for a part of their price
 *   (`cancel_tickets` is left at its default, true), exactly as in the bot.
 * - One operation has one Idempotency-Key. It is minted for a plan and kept
 *   while the plan stays the same, so a repeated press or a "check again"
 *   after a lost answer replays the first operation instead of refunding twice.
 */
import { ApiError } from "@/lib/api/http";
import type {
  CreateOrderRefundsRequest,
  CreateOrderRefundsResponse,
  OrderRefundItem,
  OrderRefundOptions,
  OrderRefundTicketOption,
} from "@/lib/api/types";
import type { MessageKey } from "@/lib/i18n/en";
import { parseAmountMinor, type Parsed } from "@/lib/promo";

/** The longest reason the bot accepts; the web keeps the same limit. */
export const MAX_REFUND_REASON = 300;

/** The ready reasons, the same three as the bot's buttons (bot.ref.reason1..3). */
export const REFUND_REASONS = ["buyer", "event", "duplicate"] as const;
export type RefundReasonChoice = (typeof REFUND_REASONS)[number] | "other";

/** Order statuses whose card offers a refund (the bot's `refundOffered`). */
export function canOfferRefund(status: string, canCreate: boolean): boolean {
  return canCreate && (status === "paid" || status === "partially_refunded");
}

// ─── what the server said ────────────────────────────────────────────

/** What the refund panel shows first, decided by the server's `route`. */
export type RefundEntry =
  | "pick" // arena returns the money: the person chooses tickets
  | "nothing" // arena could, but nothing is left to return
  | "scanned_all" // every ticket with money left was scanned at the entrance
  | "site" // the seller's own site returns it
  | "unsupported" // arena cannot return through this provider
  | "none" // arena took no money for the order
  | "unknown_provider";

export function refundEntry(o: OrderRefundOptions): RefundEntry {
  switch (o.route) {
    case "arena_provider":
      if (refundableTickets(o).length > 0) return "pick";
      return scannedTickets(o).length > 0 ? "scanned_all" : "nothing";
    case "seller_site":
      return "site";
    case "none":
      return "none";
    case "unsupported":
      return "unsupported";
    default:
      return "unknown_provider";
  }
}

/** Tickets that can still be refunded: money left and no refund under way. */
export function refundableTickets(o: OrderRefundOptions): OrderRefundTicketOption[] {
  return o.tickets.filter((t) => t.refundable > 0 && !t.in_progress);
}

/** Active tickets with money left that were scanned at the entrance: never refunded. */
export function scannedTickets(o: OrderRefundOptions): OrderRefundTicketOption[] {
  return o.tickets.filter((t) => t.not_refundable_reason === "ticket_scanned");
}

/** Tickets whose refund is already under way. */
export function inProgressTickets(o: OrderRefundOptions): OrderRefundTicketOption[] {
  return o.tickets.filter((t) => t.in_progress);
}

/** "№349", or the first characters of the id for a ticket without a number. */
export function ticketLabel(t: Pick<OrderRefundTicketOption, "ticket_number" | "ticket_id">): string {
  return t.ticket_number !== null && t.ticket_number !== undefined ? `№${t.ticket_number}` : t.ticket_id.slice(0, 8);
}

/** The provider's name as people read it: the registry name, capitalized (no literal). */
export function providerName(provider: string): string {
  if (provider === "") return "";
  return provider.charAt(0).toUpperCase() + provider.slice(1);
}

// ─── the person's choice ──────────────────────────────────────────────

export interface RefundDraft {
  /** Ticket ids the person ticked. */
  selected: string[];
  /** True when the person returns only a part of ONE ticket. */
  partial: boolean;
  /** The typed part, as typed. */
  partText: string;
  reason: RefundReasonChoice | null;
  /** The person's own reason when `reason` is "other". */
  reasonText: string;
  /** Send the buyer a letter about the refund. On by default, like the bot. */
  notifyBuyer: boolean;
}

export function emptyRefundDraft(): RefundDraft {
  return { selected: [], partial: false, partText: "", reason: null, reasonText: "", notifyBuyer: true };
}

/** Ticks or unticks one ticket. Leaving a single ticket keeps a typed part; more drops it. */
export function toggleTicket(d: RefundDraft, ticketId: string): RefundDraft {
  const selected = d.selected.includes(ticketId) ? d.selected.filter((x) => x !== ticketId) : [...d.selected, ticketId];
  return { ...d, selected, partial: selected.length === 1 ? d.partial : false };
}

/** "Select all": every refundable ticket, and no part of an amount. */
export function selectAll(d: RefundDraft, o: OrderRefundOptions): RefundDraft {
  return { ...d, selected: refundableTickets(o).map((t) => t.ticket_id), partial: false };
}

/** True when the screen may offer "part of the amount". */
export function partialAllowed(d: RefundDraft, o: OrderRefundOptions): boolean {
  return o.partial_refund && d.selected.length === 1;
}

/** The reason text that goes to the server, or "" when none is chosen yet. */
export function reasonText(d: RefundDraft, t: (key: MessageKey) => string): string {
  if (d.reason === null) return "";
  if (d.reason === "other") return d.reasonText.trim();
  return t(`refund.reason.${d.reason}` as MessageKey);
}

/** One ticket of a finished plan. */
export interface PlannedTicket {
  ticket: OrderRefundTicketOption;
  amount: number;
}

/** A plan the server can be asked to carry out. */
export interface RefundPlan {
  tickets: PlannedTicket[];
  total: number;
  currency: string;
  reason: string;
  notifyBuyer: boolean;
  partial: boolean;
}

/**
 * The plan a draft stands for, or the problem that keeps it from being one.
 * Every selected ticket must still be refundable in `o` (the options the
 * screen shows), so a stale tick can never be sent.
 */
export function planRefund(
  d: RefundDraft,
  o: OrderRefundOptions,
  t: (key: MessageKey) => string,
): Parsed<RefundPlan> {
  const open = new Map(refundableTickets(o).map((x) => [x.ticket_id, x]));
  const chosen: OrderRefundTicketOption[] = [];
  for (const id of d.selected) {
    const tk = open.get(id);
    if (tk === undefined) return { ok: false, error: "refund.err.changed" };
    chosen.push(tk);
  }
  if (chosen.length === 0) return { ok: false, error: "refund.err.noneSelected" };
  // Order of the screen, not of the clicks.
  chosen.sort((a, b) => o.tickets.indexOf(a) - o.tickets.indexOf(b));

  const tickets: PlannedTicket[] = chosen.map((tk) => ({ ticket: tk, amount: tk.refundable }));
  const partial = d.partial && partialAllowed(d, o);
  if (partial) {
    const part = parseAmountMinor(d.partText, o.currency);
    if (!part.ok || part.value > tickets[0].ticket.refundable) return { ok: false, error: "refund.err.part" };
    tickets[0] = { ...tickets[0], amount: part.value };
  }
  const reason = reasonText(d, t);
  if (reason === "" || [...reason].length > MAX_REFUND_REASON) return { ok: false, error: "refund.err.reason" };
  const total = tickets.reduce((sum, x) => sum + x.amount, 0);
  return { ok: true, value: { tickets, total, currency: o.currency, reason, notifyBuyer: d.notifyBuyer, partial } };
}

/** The request body of a plan. `cancel_tickets` stays at its default (true). */
export function refundRequest(p: RefundPlan): CreateOrderRefundsRequest {
  return {
    items: p.tickets.map((x) => ({ ticket_id: x.ticket.ticket_id, amount: x.amount })),
    reason: p.reason,
    notify_buyer: p.notifyBuyer,
  };
}

/** A stable fingerprint of what a plan sends: the same request, the same key. */
export function planFingerprint(p: RefundPlan): string {
  return JSON.stringify(refundRequest(p));
}

/** A fresh Idempotency-Key for one refund operation from the web. */
export function newRefundKey(random: () => string = () => crypto.randomUUID()): string {
  return `web-${random()}`;
}

/**
 * The key of a plan: the one already held when the plan did not change,
 * else a new one. Changing the tickets, an amount, the reason or the letter
 * makes a new operation, exactly like the bot.
 */
export function keyFor(
  held: { key: string; fingerprint: string } | null,
  p: RefundPlan,
  mint: () => string = newRefundKey,
): { key: string; fingerprint: string } {
  const fingerprint = planFingerprint(p);
  if (held !== null && held.fingerprint === fingerprint) return held;
  return { key: mint(), fingerprint };
}

// ─── the answer ───────────────────────────────────────────────────────

export type RefundLineState = "succeeded" | "pending" | "queued" | "awaiting_approval" | "failed" | "review" | "other";

/** What one refund of the answer means for the person (the bot's refundLines). */
export function refundLineState(r: Pick<OrderRefundItem, "state" | "provider_refund_id">): RefundLineState {
  switch (r.state) {
    case "succeeded":
      return "succeeded";
    case "provider_pending":
      // Accepted by the provider only once it gave the refund an id.
      return r.provider_refund_id !== null && r.provider_refund_id !== undefined && r.provider_refund_id !== ""
        ? "pending"
        : "queued";
    case "approved":
      return "queued";
    case "requested":
      return "awaiting_approval";
    case "failed":
      return "failed";
    case "manual_review":
      return "review";
    default:
      return "other";
  }
}

/** True when every refund of the answer already returned its money. */
export function allSucceeded(res: CreateOrderRefundsResponse): boolean {
  return res.refunds.length > 0 && res.refunds.every((r) => r.state === "succeeded");
}

/**
 * True when a failed create may still have done its work: the answer was
 * lost on the way (network, timeout, a gateway), or the server broke after
 * it recorded the operation. The screen then offers "Check the refund", which
 * repeats the SAME request with the SAME key: the server answers the first
 * operation and never returns the money twice.
 */
export function outcomeUnknown(err: unknown): boolean {
  if (!(err instanceof ApiError)) return false;
  if (err.status === 0) return true;
  if (err.code === "refund.engine_unavailable") return false; // refused before anything was written
  return err.status >= 500;
}

/** The scanned-ticket refusal: which ticket, and whether the owners were told. */
export interface ScannedRefusal {
  ticketId: string | null;
  notice: "notified" | "nobody" | "silent";
}

export function scannedRefusal(err: unknown): ScannedRefusal | null {
  if (!(err instanceof ApiError) || err.code !== "refund.ticket_scanned") return null;
  const d = err.details ?? {};
  const ticketId = typeof d.ticket_id === "string" ? d.ticket_id : null;
  const n = d.owners_notice;
  const notice = n === "queued" || n === "throttled" ? "notified" : n === "no_recipient" ? "nobody" : "silent";
  return { ticketId, notice };
}
