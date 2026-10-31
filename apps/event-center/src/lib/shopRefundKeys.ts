/**
 * The Idempotency-Key of an unfinished shop refund (SB-12). The key is the only protection against a second refund
 * when the answer is lost, so it must outlive the page: it sits in the tab's sessionStorage (like the step-up token),
 * by order, together with the fingerprint of the request (amount and reason). A retry after a reload with the same
 * amount and reason goes out with the SAME key; another amount or reason is another operation and gets a new one.
 * The key is dropped after an answer (success or a refusal). When the browser refuses sessionStorage the key is kept
 * in memory, as before.
 */
import { newRefundKey } from "@/lib/refunds";

export interface HeldRefundKey {
  key: string;
  fingerprint: string;
}

const PREFIX = "arena.events.shoprefund.";
const memory = new Map<string, HeldRefundKey>();

function tabStorage(): Storage | null {
  try {
    return typeof sessionStorage === "undefined" ? null : sessionStorage;
  } catch {
    return null;
  }
}

function read(orderId: string): HeldRefundKey | null {
  const mem = memory.get(orderId);
  if (mem !== undefined) return mem;
  try {
    const raw = tabStorage()?.getItem(PREFIX + orderId) ?? null;
    if (raw === null) return null;
    const v = JSON.parse(raw) as Partial<HeldRefundKey>;
    return typeof v.key === "string" && typeof v.fingerprint === "string" ? { key: v.key, fingerprint: v.fingerprint } : null;
  } catch {
    return null;
  }
}

function write(orderId: string, held: HeldRefundKey): void {
  try {
    const s = tabStorage();
    if (s !== null) {
      s.setItem(PREFIX + orderId, JSON.stringify(held));
      memory.delete(orderId);
      return;
    }
  } catch {
    // Storage full or blocked: the memory copy below still works until a reload.
  }
  memory.set(orderId, held);
}

/** The key of this order's refund with this fingerprint: the held one when the request did not change, else a new one. */
export function refundKeyFor(orderId: string, fingerprint: string, mint: () => string = newRefundKey): string {
  const held = read(orderId);
  if (held !== null && held.fingerprint === fingerprint) return held.key;
  const fresh = { key: mint(), fingerprint };
  write(orderId, fresh);
  return fresh.key;
}

/** Forgets the key of an order: the operation got its final answer. */
export function forgetRefundKey(orderId: string): void {
  memory.delete(orderId);
  try {
    tabStorage()?.removeItem(PREFIX + orderId);
  } catch {
    // Nothing to do: a stale key only matters for an identical request.
  }
}
