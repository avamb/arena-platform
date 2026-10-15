/**
 * Pure rules of "Resend tickets" (4-1), the web twin of the bot's resend.go
 * (spec 35 section 6.4). The dialog decides nothing by itself: who may use it,
 * whether there is anything to send, and which address is valid come from here.
 */
import type { OrderDetail } from "@/lib/api/types";
import { ApiError } from "@/lib/api/http";
import type { MessageKey } from "@/lib/i18n/en";

/** The permission the API asks for on POST .../resend-tickets. */
export const RESEND_PERMISSION = "ticket.update";

/** The longest address the API accepts. */
export const MAX_RESEND_EMAIL_LEN = 254;

/** Tickets of an order that would be sent: refunded and cancelled ones are not. */
export function activeTickets(order: Pick<OrderDetail, "tickets">): number {
  return order.tickets.filter((tk) => tk.status === "active").length;
}

/**
 * Whether the order card offers the button: a paid order with at least one
 * active ticket, for a person who holds ticket.update in this organization.
 * (A seller site's order is NOT hidden here: the API answers it, like the bot.)
 */
export function resendOffered(
  order: Pick<OrderDetail, "status" | "tickets">,
  can: (permission: string) => boolean,
): boolean {
  return can(RESEND_PERMISSION) && (order.status === "paid" || order.status === "partially_refunded") && activeTickets(order) > 0;
}

/**
 * A bare address of the form local@domain.tld, the same rule the bot applies,
 * so a typo is caught before a round trip. The server stays the authority.
 */
export function validResendEmail(raw: string): boolean {
  const s = raw;
  if (s === "" || s.length > MAX_RESEND_EMAIL_LEN || /[\s<>,;"]/.test(s)) return false;
  const at = s.lastIndexOf("@");
  if (at < 1 || s.indexOf("@") !== at) return false;
  const domain = s.slice(at + 1);
  return domain.includes(".") && !domain.startsWith(".") && !domain.endsWith(".") && !domain.includes("..");
}

/** Hides all but the first character of the local part and of the domain's first label. */
export function maskEmail(s: string): string {
  const at = s.lastIndexOf("@");
  if (at < 1) return "***";
  const word = (w: string) => {
    const chars = Array.from(w);
    return chars.length === 0 ? "" : chars[0] + "*".repeat(chars.length - 1);
  };
  const domain = s.slice(at + 1);
  const dot = domain.indexOf(".");
  const first = dot > 0 ? domain.slice(0, dot) : domain;
  const rest = dot > 0 ? domain.slice(dot) : "";
  return `${word(s.slice(0, at))}@${word(first)}${rest}`;
}

/**
 * The address the letter goes to when none is typed: the buyer's, else the
 * first active holder's. The same rule the API applies. "" when there is none.
 */
export function orderAddress(order: Pick<OrderDetail, "buyer_email" | "tickets">): string {
  const buyer = (order.buyer_email ?? "").trim();
  if (buyer !== "") return buyer;
  for (const tk of order.tickets) {
    if (tk.status !== "active") continue;
    const holder = (tk.holder_email ?? "").trim();
    if (holder !== "") return holder;
  }
  return "";
}

/** What a failed resend means for the person: which text to show, or null for the generic error. */
export type ResendFailure = "site" | "notPaid" | "noTickets" | "badEmail" | "noRecipient" | "forbidden";

export function resendFailure(err: unknown): ResendFailure | null {
  if (!(err instanceof ApiError)) return null;
  switch (err.code) {
    case "order.seller_site_order":
      return "site";
    case "order.not_paid":
      return "notPaid";
    case "order.no_active_tickets":
      return "noTickets";
    case "order.invalid_email":
      return "badEmail";
    case "order.no_recipient":
      return "noRecipient";
  }
  if (err.status === 403) return "forbidden";
  return null;
}

/** The message key of a known failure, for `t(key, { number })`. */
export function resendFailureKey(f: ResendFailure): MessageKey {
  switch (f) {
    case "site":
      return "resend.site";
    case "notPaid":
      return "resend.notPaid";
    case "noTickets":
      return "resend.noTickets";
    case "badEmail":
      return "resend.badEmail";
    case "noRecipient":
      return "resend.noRecipient";
    case "forbidden":
      return "resend.forbidden";
  }
}

/** A failure after which the person can fix something and send again. */
export function resendRetryable(f: ResendFailure | null): boolean {
  return f === null || f === "badEmail" || f === "noRecipient";
}
