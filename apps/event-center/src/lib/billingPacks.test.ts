import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/http";
import type { BillingAccountInvoice } from "@/lib/api/types";
import {
  IDLE_FLOW,
  choose,
  clearStoredOrder,
  close,
  confirmPollDelayMs,
  finishErr,
  finishOk,
  flowFromStored,
  isSafePaymentUrl,
  keyForOrder,
  loadStoredOrder,
  newOrderKey,
  orderFingerprint,
  orderOutcome,
  parseBalanceSearch,
  parsePackReturn,
  payableMinor,
  returnStatus,
  saveStoredOrder,
  startSend,
  storedOrderKey,
} from "@/lib/billingPacks";
import type { KeyValueStore } from "@/lib/refunds";

const PACK = "11111111-1111-4111-8111-111111111111";
const OTHER = "22222222-2222-4222-8222-222222222222";
const INV = "33333333-3333-4333-8333-333333333333";

function memoryStore(): KeyValueStore & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return {
    data,
    getItem: (k) => data.get(k) ?? null,
    setItem: (k, v) => void data.set(k, v),
    removeItem: (k) => void data.delete(k),
  };
}

function minted() {
  let n = 0;
  return () => `web-pack-test-${++n}`;
}

const invoice = (state: string): BillingAccountInvoice => ({ id: INV, state }) as unknown as BillingAccountInvoice;

describe("keys", () => {
  it("mints web-pack-<uuid>", () => {
    expect(newOrderKey(() => "abc")).toBe("web-pack-abc");
  });

  it("keeps the key for the same order and mints a new one for another pack or method", () => {
    const mint = minted();
    const a = keyForOrder(null, { packId: PACK, method: "bank_transfer" }, mint);
    expect(keyForOrder(a, { packId: PACK, method: "bank_transfer" }, mint)).toBe(a);
    const b = keyForOrder(a, { packId: OTHER, method: "bank_transfer" }, mint);
    expect(b.key).not.toBe(a.key);
    const c = keyForOrder(a, { packId: PACK, method: "stripe_card" }, mint);
    expect(c.key).not.toBe(a.key);
    expect(orderFingerprint({ packId: PACK, method: "bank_transfer" })).not.toBe(orderFingerprint({ packId: PACK, method: "stripe_card" }));
  });
});

describe("what an error means", () => {
  it("a lost answer keeps the key", () => {
    expect(orderOutcome(new ApiError(0, "network", "x"))).toBe("unknown");
    expect(orderOutcome(new ApiError(502, "x", "x"))).toBe("unknown");
    expect(orderOutcome(new ApiError(504, "x", "x"))).toBe("unknown");
    expect(orderOutcome(new ApiError(503, "dependency.billing_unavailable", "x"))).toBe("unknown");
    expect(orderOutcome(new Error("boom"))).toBe("unknown");
  });
  it("card payment off is its own outcome", () => {
    expect(orderOutcome(new ApiError(503, "billing.card_payment_unavailable", "x"))).toBe("cardUnavailable");
  });
  it("a refusal drops the key", () => {
    expect(orderOutcome(new ApiError(404, "billing.pack_not_found", "x"))).toBe("rejected");
    expect(orderOutcome(new ApiError(409, "billing.idempotency_key_reused", "x"))).toBe("rejected");
    expect(orderOutcome(new ApiError(403, "permissions.denied", "x"))).toBe("rejected");
  });
});

describe("the order flow", () => {
  const intent = { packId: PACK, method: "bank_transfer" as const };

  it("sends once: a second press while sending is ignored", () => {
    const mint = minted();
    const first = startSend(choose(IDLE_FLOW, intent), mint);
    expect(first).not.toBeNull();
    expect(first?.flow.phase).toBe("sending");
    expect(startSend(first!.flow, mint)).toBeNull();
  });

  it("nothing to send before a pack is chosen", () => {
    expect(startSend(IDLE_FLOW)).toBeNull();
  });

  it("a lost answer keeps the key and the retry sends the SAME key", () => {
    const mint = minted();
    const sent = startSend(choose(IDLE_FLOW, intent), mint)!;
    const lost = finishErr(sent.flow, new ApiError(0, "network", "x"));
    expect(lost.phase).toBe("unknown");
    const retry = startSend(lost, mint)!;
    expect(retry.key).toBe(sent.key);
  });

  it("closing and choosing the same order again after a lost answer still reuses the key", () => {
    const mint = minted();
    const sent = startSend(choose(IDLE_FLOW, intent), mint)!;
    const lost = finishErr(sent.flow, new ApiError(504, "x", "x"));
    const closed = close(lost);
    expect(closed.phase).toBe("idle");
    const again = startSend(choose(closed, intent), mint)!;
    expect(again.key).toBe(sent.key);
  });

  it("another pack or method after a lost answer is a new order with a new key", () => {
    const mint = minted();
    const sent = startSend(choose(IDLE_FLOW, intent), mint)!;
    const lost = finishErr(sent.flow, new ApiError(0, "network", "x"));
    const otherPack = startSend(choose(lost, { packId: OTHER, method: "bank_transfer" }), mint)!;
    expect(otherPack.key).not.toBe(sent.key);
    const otherMethod = startSend(choose(lost, { packId: PACK, method: "stripe_card" }), mint)!;
    expect(otherMethod.key).not.toBe(sent.key);
  });

  it("a refusal drops the key, and a retry is a new order", () => {
    const mint = minted();
    const sent = startSend(choose(IDLE_FLOW, intent), mint)!;
    const refused = finishErr(sent.flow, new ApiError(404, "billing.pack_not_found", "x"));
    expect(refused.phase).toBe("failed");
    expect(refused.failure).toBe("rejected");
    expect(refused.held).toBeNull();
    expect(startSend(refused, mint)!.key).not.toBe(sent.key);
  });

  it("card payment off is a failure that names the cause", () => {
    const sent = startSend(choose(IDLE_FLOW, { packId: PACK, method: "stripe_card" }))!;
    const off = finishErr(sent.flow, new ApiError(503, "billing.card_payment_unavailable", "x"));
    expect(off.phase).toBe("failed");
    expect(off.failure).toBe("cardUnavailable");
  });

  it("an answer ends the key's life: the next order mints a new one", () => {
    const mint = minted();
    const sent = startSend(choose(IDLE_FLOW, intent), mint)!;
    const done = finishOk(sent.flow, invoice("issued"));
    expect(done.phase).toBe("done");
    expect(done.held).toBeNull();
    expect(startSend(choose(done, intent), mint)!.key).not.toBe(sent.key);
  });

  it("choosing while sending changes nothing", () => {
    const sent = startSend(choose(IDLE_FLOW, intent))!;
    expect(choose(sent.flow, { packId: OTHER, method: "bank_transfer" })).toBe(sent.flow);
    expect(close(sent.flow)).toBe(sent.flow);
  });
});

describe("a key that survives a reload", () => {
  it("round-trips and clears", () => {
    const store = memoryStore();
    const intent = { packId: PACK, method: "stripe_card" as const };
    saveStoredOrder("org", { intent, key: "web-pack-k", fingerprint: orderFingerprint(intent) }, store);
    const loaded = loadStoredOrder("org", store);
    expect(loaded?.key).toBe("web-pack-k");
    const flow = flowFromStored(loaded!);
    expect(flow.phase).toBe("unknown");
    expect(startSend(flow)!.key).toBe("web-pack-k");
    clearStoredOrder("org", store);
    expect(loadStoredOrder("org", store)).toBeNull();
  });

  it("does not trust a damaged or tampered record", () => {
    const store = memoryStore();
    store.setItem(storedOrderKey("org"), "not json");
    expect(loadStoredOrder("org", store)).toBeNull();
    store.setItem(storedOrderKey("org"), JSON.stringify({ intent: { packId: PACK, method: "cash" }, key: "k", fingerprint: "x" }));
    expect(loadStoredOrder("org", store)).toBeNull();
    store.setItem(storedOrderKey("org"), JSON.stringify({ intent: { packId: PACK, method: "bank_transfer" }, key: "k", fingerprint: "other" }));
    expect(loadStoredOrder("org", store)).toBeNull();
    expect(loadStoredOrder("org", null)).toBeNull();
  });

  it("keeps each organization's key apart", () => {
    const store = memoryStore();
    const intent = { packId: PACK, method: "bank_transfer" as const };
    saveStoredOrder("a", { intent, key: "ka", fingerprint: orderFingerprint(intent) }, store);
    expect(loadStoredOrder("b", store)).toBeNull();
  });
});

describe("the Stripe return address", () => {
  it("reads a paid return", () => {
    expect(parsePackReturn({ invoice: INV, paid: "1" })).toEqual({ invoiceId: INV, result: "paid" });
  });
  it("reads a canceled return, and canceled wins", () => {
    expect(parsePackReturn({ invoice: INV, cancelled: "1" })).toEqual({ invoiceId: INV, result: "cancelled" });
    expect(parsePackReturn({ invoice: INV, paid: "1", cancelled: "1" })?.result).toBe("cancelled");
  });
  it("is not a return without a valid invoice id or a result", () => {
    expect(parsePackReturn({})).toBeNull();
    expect(parsePackReturn({ invoice: "nope", paid: "1" })).toBeNull();
    expect(parsePackReturn({ invoice: INV })).toBeNull();
    expect(parsePackReturn({ invoice: INV, paid: "0" })).toBeNull();
  });
  it("the route search keeps only a valid return", () => {
    expect(parseBalanceSearch({ invoice: INV, paid: "1", extra: "x" })).toEqual({ invoice: INV, paid: "1" });
    expect(parseBalanceSearch({ invoice: INV, cancelled: "1" })).toEqual({ invoice: INV, cancelled: "1" });
    expect(parseBalanceSearch({ invoice: "bad", paid: "1" })).toEqual({});
  });
});

describe("what the page says after the return", () => {
  it("says paid only when the invoice says paid", () => {
    expect(returnStatus("paid", invoice("issued"), 1)).toBe("confirming");
    expect(returnStatus("paid", invoice("paid"), 1)).toBe("paid");
    expect(returnStatus("paid", null, 0)).toBe("confirming");
  });
  it("stops promising after the last read", () => {
    let reads = 1;
    while (confirmPollDelayMs(reads) !== null) reads += 1;
    expect(returnStatus("paid", invoice("issued"), reads)).toBe("stillConfirming");
    expect(reads).toBeGreaterThan(3);
  });
  it("a canceled return says canceled unless the invoice was paid anyway", () => {
    expect(returnStatus("cancelled", invoice("issued"), 1)).toBe("cancelled");
    expect(returnStatus("cancelled", invoice("paid"), 1)).toBe("paid");
    expect(returnStatus("cancelled", invoice("void"), 1)).toBe("void");
  });
  it("the poll waits longer and longer and then gives up", () => {
    expect(confirmPollDelayMs(0)).toBeNull();
    const delays: number[] = [];
    for (let i = 1; confirmPollDelayMs(i) !== null; i += 1) delays.push(confirmPollDelayMs(i) as number);
    expect(delays).toEqual([...delays].sort((a, b) => a - b));
    expect(delays[0]).toBeGreaterThanOrEqual(1000);
  });
});

describe("payment page and money", () => {
  it("opens only https pages", () => {
    expect(isSafePaymentUrl("https://checkout.stripe.com/c/pay/cs_test_1")).toBe(true);
    expect(isSafePaymentUrl("http://checkout.stripe.com/x")).toBe(false);
    expect(isSafePaymentUrl("javascript:alert(1)")).toBe(false);
    expect(isSafePaymentUrl("")).toBe(false);
    expect(isSafePaymentUrl(null)).toBe(false);
    expect(isSafePaymentUrl("not a url")).toBe(false);
  });
  it("a card adds the fee, a bank transfer does not", () => {
    expect(payableMinor(5200, 190, "stripe_card")).toBe(5390);
    expect(payableMinor(5200, 190, "bank_transfer")).toBe(5200);
    expect(payableMinor(5200, null, "stripe_card")).toBe(5200);
  });
});
