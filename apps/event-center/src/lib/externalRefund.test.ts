import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/http";
import type { OrderDetail } from "@/lib/api/types";
import {
  canOfferExternalRefund,
  emptyDraft,
  externalRefundRefusal,
  majorText,
  planExternalRefund,
  toggleLine,
} from "@/lib/externalRefund";

type T = OrderDetail["tickets"][number];

function ticket(id: string, n: number, price: number, status = "active"): T {
  return { id, system_ticket_id: n, price, currency: "ILS", status } as unknown as T;
}

const order = { status: "paid", tickets: [ticket("a", 11, 5000), ticket("b", 12, 3000), ticket("c", 13, 3000, "cancelled")] };

describe("external refund draft", () => {
  it("is offered for a paid order with an active ticket and the permission", () => {
    expect(canOfferExternalRefund(order, true)).toBe(true);
    expect(canOfferExternalRefund(order, false)).toBe(false);
    expect(canOfferExternalRefund({ ...order, status: "refunded" }, true)).toBe(false);
    expect(canOfferExternalRefund({ status: "paid", tickets: [ticket("c", 13, 1, "cancelled")] }, true)).toBe(false);
  });

  it("prefills the whole price when a ticket is ticked", () => {
    const d = toggleLine(emptyDraft(), order.tickets[0]);
    expect(d.selected).toEqual(["a"]);
    expect(d.amounts.a).toBe("50.00");
    expect(toggleLine(d, order.tickets[0]).selected).toEqual([]);
    expect(majorText(5, 2)).toBe("0.05");
    expect(majorText(500, 0)).toBe("500");
  });

  it("builds a request with minor units, the final amount per ticket", () => {
    let d = toggleLine(emptyDraft(), order.tickets[0]);
    d = toggleLine(d, order.tickets[1]);
    d = { ...d, amounts: { ...d.amounts, a: "20,5" }, providerRef: " AP-77 " };
    const p = planExternalRefund(order, d);
    expect(p.ok).toBe(true);
    if (p.ok) {
      expect(p.total).toBe(2050 + 3000);
      expect(p.request.items).toEqual([
        { ticket_id: "a", amount_minor: 2050, provider_refund_ref: "AP-77" },
        { ticket_id: "b", amount_minor: 3000, provider_refund_ref: "AP-77" },
      ]);
    }
  });

  it("refuses nothing ticked, no reference, a bad amount and an amount above the price", () => {
    const base = toggleLine(emptyDraft(), order.tickets[0]);
    expect(planExternalRefund(order, emptyDraft())).toEqual({ ok: false, problem: { key: "extref.err.none" } });
    expect(planExternalRefund(order, base)).toEqual({ ok: false, problem: { key: "extref.err.ref" } });
    const withRef = { ...base, providerRef: "AP-1" };
    expect(planExternalRefund(order, { ...withRef, amounts: { a: "abc" } })).toEqual({
      ok: false,
      problem: { key: "extref.err.amount", params: { number: 11 } },
    });
    expect(planExternalRefund(order, { ...withRef, amounts: { a: "50.01" } })).toEqual({
      ok: false,
      problem: { key: "extref.err.tooMuch", params: { number: 11 } },
    });
  });

  it("reads the server's refusals", () => {
    const exists = new ApiError(409, "order.external_refund_exists", "x", undefined, { ticket_id: "a", amount_minor: 1000, currency: "ILS" });
    expect(externalRefundRefusal(exists)).toEqual({ kind: "exists", ticketId: "a", amount: 1000, currency: "ILS" });
    expect(externalRefundRefusal(new ApiError(409, "order.not_refundable", "x"))).toEqual({ kind: "notRefundable" });
    expect(externalRefundRefusal(new ApiError(500, "other", "x"))).toBeNull();
    expect(externalRefundRefusal(new Error("x"))).toBeNull();
  });
});
