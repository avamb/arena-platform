/**
 * Pure helpers of the orders screens.
 */
import type { OrderDetail, OrderDeliveryEntry } from "@/lib/api/types";

export type Tone = "ok" | "wait" | "bad" | "neutral";

/** The visual tone of an order status. A tone always comes with a word. */
export function orderTone(status: string): Tone {
  switch (status) {
    case "paid":
      return "ok";
    case "pending_payment":
    case "manual_review":
      return "wait";
    case "expired":
    case "cancelled":
    case "abandoned":
      return "bad";
    default:
      return "neutral";
  }
}

export const ORDER_STATUSES = [
  "paid",
  "partially_refunded",
  "refunded",
  "pending_payment",
  "expired",
  "cancelled",
  "abandoned",
  "manual_review",
] as const;

export const DELIVERY_STATES = ["sent", "pending", "failed", "none"] as const;

export const UNPAID_REASONS = [
  "payment_failed",
  "payment_abandoned",
  "awaiting_payment",
  "hold_expired",
  "cancelled",
  "manual_review",
] as const;

/** The letter status of one ticket, "none" when no letter was queued. */
export function ticketDelivery(order: OrderDetail, ticketId: string): OrderDeliveryEntry | null {
  return order.delivery.find((d) => d.ticket_id === ticketId) ?? null;
}

/** Order number as people see it: the system id, never the UUID. */
export function orderNumber(systemId: number): string {
  return `№${systemId}`;
}

/** A seat in words: "Sector A, row 3, seat 12", or the label the API made. */
export function seatText(t: {
  seat_label: string | null;
  seat_sector: string | null;
  seat_row: string | null;
  seat_number: string | null;
}): string {
  if (t.seat_label !== null && t.seat_label.length > 0) return t.seat_label;
  return [t.seat_sector, t.seat_row, t.seat_number].filter((p): p is string => p !== null && p.length > 0).join(" · ");
}

/**
 * 4-2: the "Cancel order" button exists only for an order that waits for
 * payment (the one status the API cancels) and only for a person who holds
 * `order.write` in THIS organization. Any other status has no button.
 */
export function canCancelOrder(status: string, canWrite: boolean): boolean {
  return canWrite && status === "pending_payment";
}
