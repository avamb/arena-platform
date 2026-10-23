/**
 * B3-3/B4: the pure rules of ordering a credit pack. No React here, so every
 * rule can be tested without a DOM.
 *
 * One CONFIRMED order has one Idempotency-Key. The key is minted when the
 * person presses "Order", is kept while the answer is missing (a retry sends
 * the same key and the server answers the first invoice, never a second one),
 * and is dropped once a final answer arrives. A different pack or another
 * payment method is a different order and gets a new key.
 *
 * The Stripe return address is the balance page itself: it reads
 * `?invoice=<id>&paid=1` or `&cancelled=1`. The page says the payment is being
 * confirmed until the invoice itself says "paid": the server confirms the
 * payment, not the browser address.
 */
import { ApiError } from "@/lib/api/http";
import type { BillingAccountInvoice } from "@/lib/api/types";
import type { KeyValueStore } from "@/lib/refunds";

export type PayMethod = "bank_transfer" | "stripe_card";

export const PAY_METHODS: readonly PayMethod[] = ["bank_transfer", "stripe_card"];

export interface OrderIntent {
  packId: string;
  method: PayMethod;
}

/** A stable fingerprint of what an order asks for: the same order, the same key. */
export function orderFingerprint(i: OrderIntent): string {
  return `${i.packId}|${i.method}`;
}

/** A fresh Idempotency-Key for one confirmed pack order from the web. */
export function newOrderKey(random: () => string = () => crypto.randomUUID()): string {
  return `web-pack-${random()}`;
}

export interface HeldKey {
  key: string;
  fingerprint: string;
}

/** The key already held when the order did not change, else a new one. */
export function keyForOrder(held: HeldKey | null, i: OrderIntent, mint: () => string = newOrderKey): HeldKey {
  const fingerprint = orderFingerprint(i);
  if (held !== null && held.fingerprint === fingerprint) return held;
  return { key: mint(), fingerprint };
}

// ─── what an error means for the key ─────────────────────────────────────

/**
 * - `unknown`: the answer may be lost, the order may exist. Keep the key.
 * - `rejected`: the server refused. Nothing was ordered. Drop the key.
 * - `cardUnavailable`: card payment is off. Nothing was ordered.
 */
export type OrderOutcome = "unknown" | "rejected" | "cardUnavailable";

export function orderOutcome(err: unknown): OrderOutcome {
  if (!(err instanceof ApiError)) return "unknown";
  if (err.status === 503 && err.code === "billing.card_payment_unavailable") return "cardUnavailable";
  // No answer, a gateway timeout, a server failure: the order may have been written.
  if (err.status === 0 || err.status >= 500 || err.status === 408 || err.status === 429) return "unknown";
  return "rejected";
}

// ─── the order flow ───────────────────────────────────────────────────

export type OrderPhase = "idle" | "confirm" | "sending" | "unknown" | "failed" | "done";

export interface OrderFlow {
  phase: OrderPhase;
  intent: OrderIntent | null;
  /** The key of the order whose answer is missing or under way. */
  held: HeldKey | null;
  invoice: BillingAccountInvoice | null;
  /** Why the last attempt failed (phase "failed"). */
  failure: Exclude<OrderOutcome, "unknown"> | null;
  /** The error of the last attempt, for the message. */
  error: unknown;
}

export const IDLE_FLOW: OrderFlow = { phase: "idle", intent: null, held: null, invoice: null, failure: null, error: null };

/** The person picked a pack and a method and sees the confirmation. */
export function choose(flow: OrderFlow, intent: OrderIntent): OrderFlow {
  if (flow.phase === "sending") return flow;
  const keep = flow.held !== null && flow.held.fingerprint === orderFingerprint(intent) ? flow.held : null;
  return { ...IDLE_FLOW, phase: "confirm", intent, held: keep };
}

/** Closes the confirmation. A key whose answer is missing is kept. */
export function close(flow: OrderFlow): OrderFlow {
  if (flow.phase === "sending") return flow;
  return { ...IDLE_FLOW, held: flow.phase === "unknown" || flow.phase === "confirm" ? flow.held : null };
}

/**
 * The person pressed "Order" (or "Check the order" after a lost answer).
 * Returns the flow now sending and the key to send, or null when a send is
 * already under way (a double click) or there is nothing to send.
 */
export function startSend(flow: OrderFlow, mint: () => string = newOrderKey): { flow: OrderFlow; key: string } | null {
  if (flow.intent === null) return null;
  if (flow.phase !== "confirm" && flow.phase !== "unknown" && flow.phase !== "failed") return null;
  const held = keyForOrder(flow.held, flow.intent, mint);
  return { flow: { ...flow, phase: "sending", held, invoice: null, failure: null, error: null }, key: held.key };
}

/** The server answered with the invoice: the key has done its job. */
export function finishOk(flow: OrderFlow, invoice: BillingAccountInvoice): OrderFlow {
  return { ...flow, phase: "done", held: null, invoice, failure: null, error: null };
}

/** The attempt failed: keep the key when the order may exist, drop it otherwise. */
export function finishErr(flow: OrderFlow, err: unknown): OrderFlow {
  const outcome = orderOutcome(err);
  if (outcome === "unknown") return { ...flow, phase: "unknown", error: err, failure: null };
  return { ...flow, phase: "failed", held: null, failure: outcome, error: err };
}

// ─── a key that survives a reload (the answer may be missing) ──────────

export interface StoredOrder {
  intent: OrderIntent;
  key: string;
  fingerprint: string;
}

export function storedOrderKey(orgId: string): string {
  return `arena.ec.billing.pack.pending.${orgId}`;
}

function browserSessionStore(): KeyValueStore | null {
  try {
    return typeof sessionStorage === "undefined" ? null : sessionStorage;
  } catch {
    return null;
  }
}

/** Written right before the request leaves; removed on a final answer. */
export function saveStoredOrder(orgId: string, s: StoredOrder, store: KeyValueStore | null = browserSessionStore()): void {
  try {
    store?.setItem(storedOrderKey(orgId), JSON.stringify(s));
  } catch {
    /* the in-memory flow still holds the key */
  }
}

export function loadStoredOrder(orgId: string, store: KeyValueStore | null = browserSessionStore()): StoredOrder | null {
  try {
    const raw = store?.getItem(storedOrderKey(orgId));
    if (raw === null || raw === undefined) return null;
    const s = JSON.parse(raw) as Partial<StoredOrder> | null;
    if (s === null || typeof s.key !== "string" || s.key === "" || s.intent === undefined || s.intent === null) return null;
    const { packId, method } = s.intent as Partial<OrderIntent>;
    if (typeof packId !== "string" || (method !== "bank_transfer" && method !== "stripe_card")) return null;
    const intent: OrderIntent = { packId, method };
    // A record that no longer matches its fingerprint is not trusted.
    if (s.fingerprint !== orderFingerprint(intent)) return null;
    return { intent, key: s.key, fingerprint: s.fingerprint };
  } catch {
    return null;
  }
}

export function clearStoredOrder(orgId: string, store: KeyValueStore | null = browserSessionStore()): void {
  try {
    store?.removeItem(storedOrderKey(orgId));
  } catch {
    /* nothing to do */
  }
}

/** A flow that waits for the person to check an order whose answer was lost before a reload. */
export function flowFromStored(s: StoredOrder): OrderFlow {
  return { ...IDLE_FLOW, phase: "unknown", intent: s.intent, held: { key: s.key, fingerprint: s.fingerprint } };
}

// ─── the Stripe return address ─────────────────────────────────────────

export interface PackReturn {
  invoiceId: string;
  result: "paid" | "cancelled";
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function isUuid(v: string): boolean {
  return UUID.test(v);
}

/** `?invoice=<id>&paid=1` / `&cancelled=1`, or null when the address is not a return. */
export function parsePackReturn(raw: { invoice?: unknown; paid?: unknown; cancelled?: unknown }): PackReturn | null {
  if (typeof raw.invoice !== "string" || !UUID.test(raw.invoice)) return null;
  const on = (v: unknown) => v === "1" || v === 1 || v === true || v === "true";
  if (on(raw.cancelled)) return { invoiceId: raw.invoice, result: "cancelled" };
  if (on(raw.paid)) return { invoiceId: raw.invoice, result: "paid" };
  return null;
}

/** The search of the balance route: only a valid return survives. */
export interface BalanceSearch {
  invoice?: string;
  paid?: string;
  cancelled?: string;
}

export function parseBalanceSearch(raw: Record<string, unknown>): BalanceSearch {
  const r = parsePackReturn(raw);
  if (r === null) return {};
  return r.result === "paid" ? { invoice: r.invoiceId, paid: "1" } : { invoice: r.invoiceId, cancelled: "1" };
}

/** How many times the invoice is read again after the person comes back from Stripe. */
const POLL_DELAYS_MS = [2000, 3000, 5000, 8000, 12000, 15000] as const;

/** The wait before the next read, or null when the page stops asking. `attempt` counts reads already made. */
export function confirmPollDelayMs(attempt: number): number | null {
  return attempt >= 1 && attempt <= POLL_DELAYS_MS.length ? POLL_DELAYS_MS[attempt - 1] : null;
}

export type ReturnStatus = "paid" | "confirming" | "stillConfirming" | "cancelled" | "void";

/**
 * What the page tells the person after the return. "paid" only when the
 * INVOICE says paid. A return with `paid=1` alone is "confirming", and after
 * the last read "stillConfirming" (the server confirms with Stripe by itself).
 */
export function returnStatus(
  result: PackReturn["result"],
  invoice: Pick<BillingAccountInvoice, "state"> | null,
  readsDone: number,
): ReturnStatus {
  if (invoice !== null && invoice.state === "paid") return "paid";
  if (invoice !== null && invoice.state === "void") return "void";
  if (result === "cancelled") return "cancelled";
  return confirmPollDelayMs(readsDone) === null && readsDone > 0 ? "stillConfirming" : "confirming";
}

/** Only an https address is opened as a payment page. */
export function isSafePaymentUrl(url: string | null | undefined): url is string {
  if (typeof url !== "string" || url === "") return false;
  try {
    return new URL(url).protocol === "https:";
  } catch {
    return false;
  }
}

/** What the person pays: the pack's price, plus the card fee for a card payment. */
export function payableMinor(face: number, cardFee: number | null, method: PayMethod): number {
  return method === "stripe_card" ? face + (cardFee ?? 0) : face;
}
