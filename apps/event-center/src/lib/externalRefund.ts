/**
 * "Refund outside the platform" (spec 62 section 14.6): the rules of the dialog,
 * kept apart from the screen so they can be tested.
 *
 * The person has already returned the money in the payment provider's own
 * cabinet. Here they tick the tickets and type the FINAL amount returned for
 * each (at most the ticket's price) and the provider's refund number. The
 * server voids the tickets and records one final refund per ticket.
 */
import { ApiError } from "@/lib/api/http";
import type { ExternalRefundRequest, OrderDetail } from "@/lib/api/types";
import { currencyFractionDigits } from "@/lib/format";
import { parseAmountMinor } from "@/lib/promo";
import type { MessageKey } from "@/lib/i18n/en";

export const MAX_PROVIDER_REF = 120;

type TicketOf = OrderDetail["tickets"][number];

/** The tickets that can still be refunded: active ones. */
export function refundableLines(order: Pick<OrderDetail, "tickets">): TicketOf[] {
  return order.tickets.filter((tk) => tk.status === "active");
}

/** The button exists for a paid order that still has an active ticket. */
export function canOfferExternalRefund(order: Pick<OrderDetail, "status" | "tickets">, canCreate: boolean): boolean {
  return canCreate && (order.status === "paid" || order.status === "partially_refunded") && refundableLines(order).length > 0;
}

/** What the person typed: the ticked ticket ids and the amount (major units) per ticket id. */
export interface ExternalRefundDraft {
  selected: string[];
  amounts: Record<string, string>;
  providerRef: string;
}

export function emptyDraft(): ExternalRefundDraft {
  return { selected: [], amounts: {}, providerRef: "" };
}

export interface DraftProblem {
  key: MessageKey;
  params?: Record<string, string | number>;
}

export type Planned =
  | { ok: true; request: ExternalRefundRequest; total: number; lines: { ticket: TicketOf; amount: number }[] }
  | { ok: false; problem: DraftProblem };

/** Checks the draft the way the server will and builds the request. */
export function planExternalRefund(order: Pick<OrderDetail, "tickets">, draft: ExternalRefundDraft): Planned {
  const lines = refundableLines(order).filter((tk) => draft.selected.includes(tk.id));
  if (lines.length === 0) return { ok: false, problem: { key: "extref.err.none" } };
  const ref = draft.providerRef.trim();
  if (ref === "" || [...ref].length > MAX_PROVIDER_REF) return { ok: false, problem: { key: "extref.err.ref" } };
  const out: { ticket: TicketOf; amount: number }[] = [];
  let total = 0;
  for (const tk of lines) {
    const p = parseAmountMinor(draft.amounts[tk.id] ?? "", tk.currency);
    if (!p.ok) return { ok: false, problem: { key: "extref.err.amount", params: { number: tk.system_ticket_id } } };
    if (p.value > tk.price) {
      return { ok: false, problem: { key: "extref.err.tooMuch", params: { number: tk.system_ticket_id } } };
    }
    out.push({ ticket: tk, amount: p.value });
    total += p.value;
  }
  return {
    ok: true,
    total,
    lines: out,
    request: { items: out.map((l) => ({ ticket_id: l.ticket.id, amount_minor: l.amount, provider_refund_ref: ref })) },
  };
}

/** Ticking a ticket prefills the whole price, the usual case. */
export function toggleLine(draft: ExternalRefundDraft, tk: TicketOf): ExternalRefundDraft {
  if (draft.selected.includes(tk.id)) {
    return { ...draft, selected: draft.selected.filter((id) => id !== tk.id) };
  }
  const has = draft.amounts[tk.id];
  const amounts =
    has !== undefined && has !== ""
      ? draft.amounts
      : { ...draft.amounts, [tk.id]: majorText(tk.price, currencyFractionDigits(tk.currency)) };
  return { ...draft, selected: [...draft.selected, tk.id], amounts };
}

/** 2500 with 2 digits -> "25.00" (a plain input value, no grouping). */
export function majorText(minor: number, digits: number): string {
  if (digits === 0) return String(minor);
  const s = String(minor).padStart(digits + 1, "0");
  return s.slice(0, -digits) + "." + s.slice(-digits);
}

/** The refusals the dialog explains itself. Anything else goes to the generic notice. */
export type ExternalRefundRefusal =
  | { kind: "exists"; ticketId: string; amount: number; currency: string }
  | { kind: "notActive"; ticketId: string }
  | { kind: "notRefundable" }
  | { kind: "tooMuch"; ticketId: string }
  | null;

export function externalRefundRefusal(err: unknown): ExternalRefundRefusal {
  if (!(err instanceof ApiError)) return null;
  const d = err.details ?? {};
  const ticketId = typeof d.ticket_id === "string" ? d.ticket_id : "";
  switch (err.code) {
    case "order.external_refund_exists":
      return {
        kind: "exists",
        ticketId,
        amount: typeof d.amount_minor === "number" ? d.amount_minor : 0,
        currency: typeof d.currency === "string" ? d.currency : "",
      };
    case "ticket.not_active":
      return { kind: "notActive", ticketId };
    case "order.not_refundable":
      return { kind: "notRefundable" };
    case "order.refund_exceeds_price":
      return { kind: "tooMuch", ticketId };
    default:
      return null;
  }
}
