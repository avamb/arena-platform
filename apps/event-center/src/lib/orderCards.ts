/**
 * Pure helpers of the expanded order cards in the orders list (EC-V3, spec 62
 * section 12.2): the date filters and the letter state of a ticket.
 */
import type { MessageKey } from "@/lib/i18n/en";
import type { Tone } from "@/lib/orders";

const DAY = /^(\d{4})-(\d{2})-(\d{2})$/;

/** A calendar day typed in a date field -> a local Date at the given time, or undefined. */
function localDay(day: string, h: number, m: number, s: number, ms: number): Date | undefined {
  const parts = DAY.exec(day.trim());
  if (parts === null) return undefined;
  const [y, mo, d] = [Number(parts[1]), Number(parts[2]), Number(parts[3])];
  const date = new Date(y, mo - 1, d, h, m, s, ms);
  // 2026-02-31 rolls into March: that is not the day the person typed.
  if (date.getFullYear() !== y || date.getMonth() !== mo - 1 || date.getDate() !== d) return undefined;
  return date;
}

/** The first instant of a day (YYYY-MM-DD) in the browser's zone as RFC 3339, undefined for a blank or invalid day. */
export function dayStartIso(day: string): string | undefined {
  return localDay(day, 0, 0, 0, 0)?.toISOString();
}

/** The last instant of a day (YYYY-MM-DD) in the browser's zone as RFC 3339, undefined for a blank or invalid day. */
export function dayEndIso(day: string): string | undefined {
  return localDay(day, 23, 59, 59, 999)?.toISOString();
}

/** The four date filters of the list, as typed. */
export interface OrderDates {
  orderFrom: string;
  orderTo: string;
  sessionFrom: string;
  sessionTo: string;
}

export const NO_DATES: OrderDates = { orderFrom: "", orderTo: "", sessionFrom: "", sessionTo: "" };

export function hasDates(d: OrderDates): boolean {
  return d.orderFrom !== "" || d.orderTo !== "" || d.sessionFrom !== "" || d.sessionTo !== "";
}

/** The query fields of the date filters. A range that runs backwards is the caller's to flag (see datesBackwards). */
export function datesQuery(d: OrderDates): { from?: string; to?: string; sessionFrom?: string; sessionTo?: string } {
  return {
    from: dayStartIso(d.orderFrom),
    to: dayEndIso(d.orderTo),
    sessionFrom: dayStartIso(d.sessionFrom),
    sessionTo: dayEndIso(d.sessionTo),
  };
}

/** True when a range ends before it starts: it can match nothing, so the screen says so instead of an empty list. */
export function datesBackwards(d: OrderDates): boolean {
  return (d.orderFrom !== "" && d.orderTo !== "" && d.orderFrom > d.orderTo) || (d.sessionFrom !== "" && d.sessionTo !== "" && d.sessionFrom > d.sessionTo);
}

/**
 * The words for the state of one ticket's letter: a waiting letter job
 * (pending, processing), the mail provider's status of the last letter that
 * carried the ticket (sending, sent, delayed, delivered, opened, failed —
 * also a letter the selling site sent), the job's own status, or "none".
 */
export function ticketLetterKey(status: string): MessageKey {
  switch (status) {
    case "sent":
      return "delivery.sentShort";
    case "delivered":
      return "delivery.deliveredShort";
    case "opened":
      return "delivery.openedShort";
    case "delayed":
      return "delivery.delayedShort";
    case "pending":
    case "processing":
    case "sending":
      return "delivery.pending";
    case "failed":
      return "delivery.failed";
    case "skipped":
    case "disabled":
      return "delivery.skipped";
    default:
      return "delivery.none";
  }
}

export function ticketLetterTone(status: string): Tone {
  switch (status) {
    case "sent":
    case "delivered":
    case "opened":
      return "ok";
    case "pending":
    case "processing":
    case "sending":
    case "delayed":
      return "wait";
    case "failed":
      return "bad";
    default:
      return "neutral";
  }
}
