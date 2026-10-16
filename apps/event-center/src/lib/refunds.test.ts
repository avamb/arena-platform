import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/http";
import type { OrderRefundOptions, OrderRefundTicketOption } from "@/lib/api/types";
import { translate } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import {
  canOfferRefund,
  emptyRefundDraft,
  keyFor,
  newRefundKey,
  outcomeUnknown,
  partialAllowed,
  planRefund,
  providerName,
  refundEntry,
  refundLineState,
  refundRequest,
  refundableTickets,
  scannedRefusal,
  scannedTickets,
  selectAll,
  ticketLabel,
  toggleTicket,
  type RefundDraft,
} from "@/lib/refunds";

const t = (key: MessageKey) => translate("en", key);

function ticket(id: string, n: number, over: Partial<OrderRefundTicketOption> = {}): OrderRefundTicketOption {
  return { ticket_id: id, status: "active", paid: 2500, refunded: 0, refundable: 2500, in_progress: false, ticket_number: n, seat_label: null, not_refundable_reason: null, ...over };
}

function options(over: Partial<OrderRefundOptions> = {}): OrderRefundOptions {
  return {
    order_id: "o1",
    route: "arena_provider",
    provider: "stripe",
    currency: "EUR",
    partial_refund: true,
    payment_left: 7500,
    tickets: [
      ticket("a", 101),
      ticket("b", 102),
      ticket("c", 103, { refundable: 0, not_refundable_reason: "ticket_scanned" }),
    ],
    active_refunds: [],
    ...over,
  };
}

function draft(over: Partial<RefundDraft>): RefundDraft {
  return { ...emptyRefundDraft(), reason: "buyer", ...over };
}

describe("who sees the button", () => {
  it("needs refund.create and a paid or partly refunded order", () => {
    expect(canOfferRefund("paid", true)).toBe(true);
    expect(canOfferRefund("partially_refunded", true)).toBe(true);
    expect(canOfferRefund("paid", false)).toBe(false);
    for (const s of ["pending_payment", "refunded", "cancelled", "expired"]) expect(canOfferRefund(s, true)).toBe(false);
  });
});

describe("the server's route decides the first screen", () => {
  it("branches like the bot (EC-22)", () => {
    expect(refundEntry(options())).toBe("pick");
    expect(refundEntry(options({ route: "seller_site" }))).toBe("site");
    expect(refundEntry(options({ route: "unsupported" }))).toBe("unsupported");
    expect(refundEntry(options({ route: "none" }))).toBe("none");
    expect(refundEntry(options({ route: "unknown_provider" }))).toBe("unknown_provider");
    expect(refundEntry(options({ route: "something new" }))).toBe("unknown_provider");
  });
  it("says nothing is left, or that everything was scanned", () => {
    const done = options({ tickets: [ticket("a", 1, { refundable: 0, refunded: 2500 })] });
    expect(refundEntry(done)).toBe("nothing");
    const scanned = options({ tickets: [ticket("a", 1, { refundable: 0, not_refundable_reason: "ticket_scanned" })] });
    expect(refundEntry(scanned)).toBe("scanned_all");
  });
  it("lists only refundable tickets, and the scanned ones apart", () => {
    const o = options({ tickets: [ticket("a", 1), ticket("b", 2, { in_progress: true }), ticket("c", 3, { refundable: 0, not_refundable_reason: "ticket_scanned" })] });
    expect(refundableTickets(o).map((x) => x.ticket_id)).toEqual(["a"]);
    expect(scannedTickets(o).map((x) => x.ticket_id)).toEqual(["c"]);
  });
  it("names tickets and providers without a literal", () => {
    expect(ticketLabel(ticket("abcdef0123", 349))).toBe("№349");
    expect(ticketLabel(ticket("abcdef0123", 0, { ticket_number: null }))).toBe("abcdef01");
    expect(providerName("stripe")).toBe("Stripe");
    expect(providerName("")).toBe("");
  });
});

describe("the choice", () => {
  it("starts with nothing ticked and the letter on", () => {
    const d = emptyRefundDraft();
    expect(d.selected).toEqual([]);
    expect(d.notifyBuyer).toBe(true);
  });
  it("select all takes only refundable tickets and drops a part", () => {
    const d = selectAll(draft({ selected: ["a"], partial: true, partText: "5" }), options());
    expect(d.selected).toEqual(["a", "b"]);
    expect(d.partial).toBe(false);
  });
  it("offers a part only for one ticket and a provider that can", () => {
    expect(partialAllowed(draft({ selected: ["a"] }), options())).toBe(true);
    expect(partialAllowed(draft({ selected: ["a", "b"] }), options())).toBe(false);
    expect(partialAllowed(draft({ selected: ["a"] }), options({ partial_refund: false }))).toBe(false);
    // ticking a second ticket drops the part
    expect(toggleTicket(draft({ selected: ["a"], partial: true }), "b").partial).toBe(false);
    expect(toggleTicket(draft({ selected: ["a", "b"] }), "b").selected).toEqual(["a"]);
  });
});

describe("the plan", () => {
  it("refuses nothing ticked, no reason, a bad part and a stale tick", () => {
    expect(planRefund(draft({}), options(), t)).toEqual({ ok: false, error: "refund.err.noneSelected" });
    expect(planRefund(draft({ selected: ["a"], reason: null }), options(), t)).toEqual({ ok: false, error: "refund.err.reason" });
    expect(planRefund(draft({ selected: ["a"], reason: "other", reasonText: "  " }), options(), t)).toEqual({ ok: false, error: "refund.err.reason" });
    expect(planRefund(draft({ selected: ["a"], reason: "other", reasonText: "x".repeat(301) }), options(), t).ok).toBe(false);
    for (const part of ["0", "25.01", "abc", "1,000", "-1"]) {
      expect(planRefund(draft({ selected: ["a"], partial: true, partText: part }), options(), t), part).toEqual({ ok: false, error: "refund.err.part" });
    }
    // the scanned ticket, or one that left the list, can never be sent
    expect(planRefund(draft({ selected: ["c"] }), options(), t)).toEqual({ ok: false, error: "refund.err.changed" });
  });
  it("sends the amount of every ticket explicitly, in the order of the screen", () => {
    const p = planRefund(draft({ selected: ["b", "a"] }), options(), t);
    expect(p.ok).toBe(true);
    if (!p.ok) return;
    expect(p.value.total).toBe(5000);
    expect(refundRequest(p.value)).toEqual({
      items: [
        { ticket_id: "a", amount: 2500 },
        { ticket_id: "b", amount: 2500 },
      ],
      reason: "Buyer asked for a refund",
      notify_buyer: true,
    });
  });
  it("sends a part of one ticket and the letter choice", () => {
    const p = planRefund(draft({ selected: ["a"], partial: true, partText: "10,50", notifyBuyer: false, reason: "other", reasonText: " Late start " }), options(), t);
    expect(p.ok).toBe(true);
    if (!p.ok) return;
    expect(refundRequest(p.value)).toEqual({ items: [{ ticket_id: "a", amount: 1050 }], reason: "Late start", notify_buyer: false });
    expect(p.value.partial).toBe(true);
  });
  it("ignores a part the provider cannot do", () => {
    const p = planRefund(draft({ selected: ["a"], partial: true, partText: "10" }), options({ partial_refund: false }), t);
    expect(p.ok && p.value.total).toBe(2500);
  });
});

describe("one key per operation", () => {
  it("keeps the key while the plan is the same and mints a new one when it changes", () => {
    let n = 0;
    const mint = () => `k${++n}`;
    const p1 = planRefund(draft({ selected: ["a"] }), options(), t);
    const p2 = planRefund(draft({ selected: ["a"], notifyBuyer: false }), options(), t);
    if (!p1.ok || !p2.ok) throw new Error("plans");
    const first = keyFor(null, p1.value, mint);
    expect(first.key).toBe("k1");
    expect(keyFor(first, p1.value, mint)).toBe(first);
    expect(keyFor(first, p2.value, mint).key).toBe("k2");
  });
  it("is a web key", () => {
    expect(newRefundKey(() => "x")).toBe("web-x");
  });
});

describe("the answer", () => {
  it("reads each refund's state like the bot", () => {
    expect(refundLineState({ state: "succeeded", provider_refund_id: "re_1" })).toBe("succeeded");
    expect(refundLineState({ state: "provider_pending", provider_refund_id: "re_1" })).toBe("pending");
    expect(refundLineState({ state: "provider_pending", provider_refund_id: null })).toBe("queued");
    expect(refundLineState({ state: "approved", provider_refund_id: null })).toBe("queued");
    expect(refundLineState({ state: "requested", provider_refund_id: null })).toBe("awaiting_approval");
    expect(refundLineState({ state: "failed", provider_refund_id: null })).toBe("failed");
    expect(refundLineState({ state: "manual_review", provider_refund_id: null })).toBe("review");
    expect(refundLineState({ state: "rejected", provider_refund_id: null })).toBe("other");
  });
  it("calls a lost answer unknown, never a refusal", () => {
    expect(outcomeUnknown(new ApiError(0, "network.timeout", "x"))).toBe(true);
    expect(outcomeUnknown(new ApiError(524, "http.524", "x"))).toBe(true);
    expect(outcomeUnknown(new ApiError(500, "refund.create_failed", "x"))).toBe(true);
    expect(outcomeUnknown(new ApiError(503, "refund.engine_unavailable", "x"))).toBe(false);
    expect(outcomeUnknown(new ApiError(409, "refund.in_progress", "x"))).toBe(false);
    expect(outcomeUnknown(new Error("x"))).toBe(false);
  });
  it("reads the scanned refusal and the owners' notice", () => {
    const e = (notice: unknown) => new ApiError(409, "refund.ticket_scanned", "x", undefined, { ticket_id: "t1", owners_notice: notice });
    expect(scannedRefusal(e("queued"))).toEqual({ ticketId: "t1", notice: "notified" });
    expect(scannedRefusal(e("throttled"))).toEqual({ ticketId: "t1", notice: "notified" });
    expect(scannedRefusal(e("no_recipient"))).toEqual({ ticketId: "t1", notice: "nobody" });
    expect(scannedRefusal(e(undefined))).toEqual({ ticketId: "t1", notice: "silent" });
    expect(scannedRefusal(new ApiError(409, "refund.in_progress", "x"))).toBeNull();
  });
});
