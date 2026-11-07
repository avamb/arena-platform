/**
 * Pure rules of "Replace tickets" (spec 63, stage R-3): the web twin of the
 * bot's reissue dialog. The dialog decides nothing by itself. Who may start it,
 * which screen comes first, whether an address or a note is valid, which key a
 * request carries and what a failed call means all come from here.
 *
 * The case question comes first (spec 63 section 1.2): a buyer who writes FROM
 * the order's address only wants the same tickets sent elsewhere (the existing
 * resend, same barcodes); a buyer who writes from ANOTHER address gets new
 * tickets with new barcodes, and the old ones stop admitting.
 */
import { ApiError } from "@/lib/api/http";
import type {
  ComplimentaryListItem,
  InvitationReissueOptions,
  OrderDetail,
  ReissueEntry,
  ReissueOptions,
  ReissuePending,
  ReissueProvider,
  ReissueTicketOption,
  ReissueTicketsRequest,
} from "@/lib/api/types";
import type { MessageKey } from "@/lib/i18n/en";
import { activeTickets, maskEmail, validResendEmail } from "@/lib/resend";

/** The permission the API asks for on every reissue write. */
export const REISSUE_PERMISSION = "ticket.update";

/** The longest note the API accepts. */
export const MAX_REISSUE_NOTE = 500;

export const REISSUE_REASONS = ["sent_to_wrong_address", "forwarded_to_stranger", "other"] as const;
export type ReissueReason = (typeof REISSUE_REASONS)[number];

export function isReissueReason(s: string): s is ReissueReason {
  return (REISSUE_REASONS as readonly string[]).includes(s);
}

/**
 * Whether the order card offers the button: a paid order with at least one
 * active ticket, for a person who holds ticket.update in this organization.
 * A seller site's order is not hidden here: the options say so and the dialog
 * explains it.
 */
export function reissueOffered(
  order: Pick<OrderDetail, "status" | "tickets">,
  can: (permission: string) => boolean,
): boolean {
  return (
    can(REISSUE_PERMISSION) &&
    (order.status === "paid" || order.status === "partially_refunded") &&
    activeTickets(order) > 0
  );
}

/**
 * An earlier invitation (stage R-5: a complimentary issuance without an
 * order) offers "Replace invitation" while it is issued and has an active
 * ticket, to a person who holds ticket.update. A code already used at the
 * door does not stop it, exactly like an order.
 */
export function invitationReissueOffered(
  item: Pick<ComplimentaryListItem, "status" | "tickets">,
  can: (permission: string) => boolean,
): boolean {
  return can(REISSUE_PERMISSION) && item.status === "issued" && item.tickets.some((tk) => tk.status === "active");
}

/** What the dialog replaces: an order, or an invitation that has no order. */
export type ReissueTarget =
  | { kind: "order"; id: string; number: string; timezone: string | null | undefined }
  | { kind: "invitation"; id: string; number: string; timezone: string | null | undefined };

/**
 * The options of both routes in one shape, so one dialog draws both. An
 * invitation has no seller site, no buyer name or phone and no payment.
 */
export interface ReissueView {
  route: string;
  email: string;
  buyerName: string;
  buyerPhone: string;
  tickets: ReissueTicketOption[];
  blocked: string | null;
  pending_reissue: ReissuePending | null;
  provider: ReissueProvider | null;
  /** The venue's zone to print scanned_at in; "" falls back to the target's own. */
  timezone: string;
}

export function viewFromOrderOptions(o: ReissueOptions): ReissueView {
  return {
    route: o.route,
    email: o.order_email,
    buyerName: o.buyer_name,
    buyerPhone: o.buyer_phone,
    tickets: o.tickets,
    blocked: o.blocked ?? null,
    pending_reissue: o.pending_reissue ?? null,
    provider: o.provider ?? null,
    timezone: o.venue_timezone ?? "",
  };
}

export function viewFromInvitationOptions(o: InvitationReissueOptions): ReissueView {
  return {
    route: "arena",
    email: o.guest_email,
    buyerName: "",
    buyerPhone: "",
    tickets: o.tickets,
    blocked: o.blocked ?? null,
    pending_reissue: o.pending_reissue ?? null,
    provider: null,
    timezone: o.venue_timezone ?? "",
  };
}

/**
 * Texts that read differently for an invitation (a guest, no payment, no
 * order). `reissueText(kind, key)` picks `reissue.inv.<rest>` for an
 * invitation; every other text is shared.
 */
export const INVITATION_TEXT_VARIANTS = [
  "reissue.title",
  "reissue.case.orderAddress",
  "reissue.address.label",
  "reissue.address.compare",
  "reissue.check.tickets",
  "reissue.check.to",
  "reissue.check.same",
  "reissue.check.unchanged",
  "reissue.check.address",
  "reissue.check.go",
  "reissue.word.subject",
  "reissue.done",
  "reissue.blocked",
  "reissue.pending.pending",
  "reissue.pending.send_failed",
] as const;

export type ReissueTextKey = (typeof INVITATION_TEXT_VARIANTS)[number];

export function reissueText(kind: ReissueTarget["kind"], key: ReissueTextKey): MessageKey {
  return (kind === "invitation" ? key.replace(/^reissue\./, "reissue.inv.") : key) as MessageKey;
}

/** The first screen of the dialog once the options are read. */
export type ReissueStart =
  | { kind: "site" }
  | { kind: "pending"; id: string; state: string; email: string }
  | { kind: "blocked"; code: string }
  | { kind: "case" };

export function reissueStart(o: Pick<ReissueView, "route" | "blocked" | "pending_reissue">): ReissueStart {
  if (o.route === "seller_site") return { kind: "site" };
  if (o.pending_reissue !== undefined && o.pending_reissue !== null) {
    const p = o.pending_reissue;
    return { kind: "pending", id: p.id, state: p.state, email: p.new_email };
  }
  if (o.blocked !== undefined && o.blocked !== null && o.blocked !== "") return { kind: "blocked", code: o.blocked };
  return { kind: "case" };
}

/** What the address step says about a typed address. */
export type AddressCheck = { ok: true; email: string; same: boolean } | { ok: false; note: "badEmail" };

/**
 * A bare address of the form local@domain.tld (the resend's rule, the server
 * stays the authority). The order's own address is allowed — only the barcodes
 * change then — but the check screen says so.
 */
export function checkReissueAddress(typed: string, orderEmail: string): AddressCheck {
  const email = typed.trim();
  if (!validResendEmail(email)) return { ok: false, note: "badEmail" };
  return { ok: true, email, same: email.toLowerCase() === orderEmail.trim().toLowerCase() };
}

/** Length of a note in characters, the way the server counts them (runes). */
export function noteLength(note: string): number {
  return Array.from(note.trim()).length;
}

export type ReasonCheck = { ok: true } | { ok: false; note: "reasonRequired" | "noteTooLong" };

export function checkReissueReason(reason: string, note: string): ReasonCheck {
  if (!isReissueReason(reason)) return { ok: false, note: "reasonRequired" };
  if (noteLength(note) > MAX_REISSUE_NOTE) return { ok: false, note: "noteTooLong" };
  return { ok: true };
}

/** What one operation sends. */
export interface ReissuePlan {
  email: string;
  reason: ReissueReason;
  note: string;
}

export function reissueRequest(p: ReissuePlan): ReissueTicketsRequest {
  const note = p.note.trim();
  return note === "" ? { email: p.email, reason: p.reason } : { email: p.email, reason: p.reason, note };
}

/** A stable fingerprint of what a plan sends: the same request, the same key. */
export function reissueFingerprint(p: ReissuePlan): string {
  return JSON.stringify(reissueRequest(p));
}

/** A fresh Idempotency-Key for one reissue from the web. */
export function newReissueKey(random: () => string = () => crypto.randomUUID()): string {
  return `web-${random()}`;
}

/**
 * The key of a plan: the one already held when the plan did not change, else a
 * new one. A changed address, reason or note is a new operation.
 */
export function reissueKeyFor(
  held: { key: string; fingerprint: string } | null,
  p: ReissuePlan,
  mint: () => string = newReissueKey,
): { key: string; fingerprint: string } {
  const fingerprint = reissueFingerprint(p);
  if (held !== null && held.fingerprint === fingerprint) return held;
  return { key: mint(), fingerprint };
}

/**
 * True when a failed reissue may still have done its work: the answer was lost
 * (network, timeout, a gateway), or the server broke after it recorded the
 * operation. The screen then offers "Check", which repeats the SAME request
 * with the SAME key: the server answers the first operation (`replayed`) and
 * never replaces the tickets twice.
 */
export function reissueOutcomeUnknown(err: unknown): boolean {
  if (!(err instanceof ApiError)) return false;
  if (err.status === 0 || (err.status >= 200 && err.status < 300)) return true;
  return err.status >= 500;
}

/** A refusal after which the person fixes the address and goes on. */
export function backToAddress(err: unknown): boolean {
  return err instanceof ApiError && err.code === "reissue.invalid_email";
}

/** The tickets that will be replaced. */
export function reissuableTickets(o: { tickets: readonly ReissueTicketOption[] }): ReissueTicketOption[] {
  return o.tickets.filter((tk) => tk.reissuable);
}

/** Replaced tickets whose old code was already used at the door (the warning line). */
export function scannedTickets(o: { tickets: readonly ReissueTicketOption[] }): ReissueTicketOption[] {
  return reissuableTickets(o).filter((tk) => typeof tk.scanned_at === "string" && tk.scanned_at !== "");
}

/**
 * The name of the payment system to show, when the check screen must say it
 * keeps the earlier address (it cannot be told, spec 63 section 4.10). null
 * when there is no payment or arena moves the address itself.
 */
export function providerKeepsOldAddress(o: { provider?: ReissueProvider | null }): string | null {
  const p = o.provider;
  if (p === undefined || p === null || p.email_updatable) return null;
  return p.title !== "" ? p.title : p.name;
}

/** The message key of a reason, for the history and the check screen. */
export function reasonKey(reason: string): MessageKey {
  return isReissueReason(reason) ? (`reissue.reason.${reason}` as MessageKey) : "reissue.reason.other";
}

/** The message key of a journal state. */
export function historyStateKey(state: string): MessageKey {
  switch (state) {
    case "applied":
      return "reissue.history.state.applied";
    case "send_failed":
      return "reissue.history.state.send_failed";
    default:
      return "reissue.history.state.pending";
  }
}

/**
 * What the card says about the payment system's copy of the address, or null.
 * `manual`: the provider cannot be told (AllPay, Flitt, PayPlus); `failed`:
 * arena asked and the provider refused. Both mean "by hand, if needed".
 */
export function providerNoteKey(e: Pick<ReissueEntry, "provider_email_state">): MessageKey | null {
  switch (e.provider_email_state) {
    case "manual":
      return "reissue.history.providerManual";
    case "failed":
      return "reissue.history.providerFailed";
    default:
      return null;
  }
}

/** "№12 → №15, №13 → №16": what the history and the result list. */
export function pairsText(pairs: readonly { old_number: number; new_number: number }[]): string {
  return pairs.map((p) => `№${p.old_number} → №${p.new_number}`).join(", ");
}

/** The addresses as the history shows them: hidden except the first letters. */
export function maskedOrDash(s: string | null | undefined): string {
  const v = (s ?? "").trim();
  return v === "" ? "—" : maskEmail(v);
}
