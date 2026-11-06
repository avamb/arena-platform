/**
 * The delivery steps of a shop order (spec 61 section 7b): which steps a card offers, how the step reads when the
 * server sends none, the waiting-days badge, and the body of a step request.
 */
import { describe, expect, it } from "vitest";
import {
  EMPTY_STEP_FORM,
  FAILED_REASONS,
  FULFILLMENT_STEPS,
  canEditHandOver,
  isFulfillmentStep,
  nextSteps,
  offeredSteps,
  orderStep,
  reasonLabelKey,
  reasonValue,
  stepCount,
  stepLabelKey,
  stepRequest,
  viaLabelKey,
  waitingDays,
  type FulfillmentStep,
} from "@/lib/shopFulfillment";
import { EMPTY_FILTERS, ordersSearchBody, parseShopOrdersSearch, resendLetters } from "@/lib/shop";
import type { ShopOrderCounts } from "@/lib/api/types";

const NOW = new Date("2026-11-06T10:00:00Z");

function o(over: Record<string, unknown> = {}) {
  return { status: "paid", delivery_kind: "shipping", shipped_at: null, ...over } as Parameters<typeof orderStep>[0] & { paid_at?: string | null };
}

describe("the transition table", () => {
  it("mirrors the server's", () => {
    expect(nextSteps("waiting")).toEqual(["packed", "handed_over", "failed"]);
    expect(nextSteps("packed")).toEqual(["handed_over", "failed"]);
    expect(nextSteps("handed_over")).toEqual(["delivered", "failed"]);
    expect(nextSteps("failed")).toEqual(["handed_over"]);
    expect(nextSteps("delivered")).toEqual([]);
  });

  it("knows the five steps and nothing else", () => {
    for (const s of FULFILLMENT_STEPS) expect(isFulfillmentStep(s)).toBe(true);
    expect(isFulfillmentStep("shipped")).toBe(false);
    expect(isFulfillmentStep(undefined)).toBe(false);
  });

  it("has a text key and a tone-free label for every step", () => {
    expect(stepLabelKey("handed_over")).toBe("shop.fulfillment.step.handed_over");
    expect(reasonLabelKey("not_home")).toBe("shop.fulfillment.reason.not_home");
    expect(reasonLabelKey("the gate was locked")).toBeNull();
    expect(viaLabelKey("telegram_bot")).toBe("shop.fulfillment.via.telegram_bot");
    expect(viaLabelKey("something_new")).toBeNull();
  });
});

describe("orderStep", () => {
  it("takes the step the server sent", () => {
    expect(orderStep(o({ fulfillment_step: "packed" }))).toBe("packed");
  });

  it("reads an order from before steps: shipped is handed over, paid waits", () => {
    expect(orderStep(o({ shipped_at: "2026-11-01T10:00:00Z", status: "shipped" }))).toBe("handed_over");
    expect(orderStep(o())).toBe("waiting");
    expect(orderStep(o({ status: "partially_refunded" }))).toBe("waiting");
  });

  it("is none for an unpaid order, a closed one with no step and a digital order", () => {
    expect(orderStep(o({ status: "pending_payment" }))).toBeNull();
    expect(orderStep(o({ status: "manual_review" }))).toBeNull();
    expect(orderStep(o({ delivery_kind: "none", fulfillment_step: "waiting" }))).toBeNull();
  });
});

describe("offeredSteps", () => {
  const steps = (over: Record<string, unknown>, manage = true) => offeredSteps(o(over), manage);

  it("offers what follows the current step", () => {
    expect(steps({ fulfillment_step: "waiting" })).toEqual(["packed", "handed_over", "failed"]);
    expect(steps({ fulfillment_step: "handed_over", status: "shipped", shipped_at: "x" })).toEqual(["delivered", "failed"]);
    expect(steps({ fulfillment_step: "failed" })).toEqual(["handed_over"]);
    expect(steps({ fulfillment_step: "delivered", status: "shipped" })).toEqual([]);
  });

  it("offers nothing without the right to manage, for a closed order or a digital one", () => {
    expect(steps({ fulfillment_step: "waiting" }, false)).toEqual([]);
    for (const status of ["refunded", "cancelled", "expired", "pending_payment"]) expect(steps({ status, fulfillment_step: "waiting" })).toEqual([]);
    expect(steps({ delivery_kind: "none" })).toEqual([]);
  });

  it("keeps the steps of a partly refunded order: a refund does not change the step", () => {
    expect(steps({ status: "partially_refunded", fulfillment_step: "packed" })).toEqual(["handed_over", "failed"]);
  });

  it("lets an order in review only be handed over, once", () => {
    expect(steps({ status: "manual_review" })).toEqual(["handed_over"]);
    expect(steps({ status: "manual_review", shipped_at: "2026-11-01T10:00:00Z" })).toEqual([]);
  });

  it("lets the tracking number be changed only while the parcel is with the courier", () => {
    expect(canEditHandOver(o({ status: "shipped", fulfillment_step: "handed_over" }), true)).toBe(true);
    expect(canEditHandOver(o({ status: "shipped", fulfillment_step: "delivered" }), true)).toBe(false);
    expect(canEditHandOver(o({ status: "shipped", fulfillment_step: "handed_over" }), false)).toBe(false);
    expect(canEditHandOver(o({ status: "refunded", fulfillment_step: "handed_over", shipped_at: "x" }), true)).toBe(false);
  });
});

describe("waitingDays", () => {
  const paid = (hoursAgo: number) => new Date(NOW.getTime() - hoursAgo * 3_600_000).toISOString();

  it("counts whole days an order has waited, once it is more than two", () => {
    expect(waitingDays(o({ fulfillment_step: "waiting", paid_at: paid(47) }), NOW)).toBeNull();
    expect(waitingDays(o({ fulfillment_step: "waiting", paid_at: paid(48) }), NOW)).toBeNull();
    expect(waitingDays(o({ fulfillment_step: "waiting", paid_at: paid(49) }), NOW)).toBe(2);
    expect(waitingDays(o({ fulfillment_step: "packed", paid_at: paid(24 * 5 + 3) }), NOW)).toBe(5);
  });

  it("is for waiting and packed only", () => {
    for (const step of ["handed_over", "delivered", "failed"]) expect(waitingDays(o({ fulfillment_step: step, paid_at: paid(24 * 9) }), NOW)).toBeNull();
    expect(waitingDays(o({ status: "refunded", fulfillment_step: "waiting", paid_at: paid(24 * 9) }), NOW)).toBeNull();
    expect(waitingDays(o({ fulfillment_step: "waiting" }), NOW)).toBeNull();
    expect(waitingDays(o({ fulfillment_step: "waiting", paid_at: "not a date" }), NOW)).toBeNull();
  });
});

describe("stepCount", () => {
  it("reads the numbers behind the filter", () => {
    const counts = { all: 9, steps: { waiting: 3, packed: 2, handed_over: 1, delivered: 0, failed: 4 } } as unknown as ShopOrderCounts;
    const wanted: Array<[FulfillmentStep, number]> = [["waiting", 3], ["packed", 2], ["handed_over", 1], ["delivered", 0], ["failed", 4]];
    for (const [s, n] of wanted) expect(stepCount(s, counts)).toBe(n);
    expect(stepCount("waiting", undefined)).toBeNull();
    expect(stepCount("waiting", { all: 1 } as unknown as ShopOrderCounts)).toBeNull();
  });
});

describe("stepRequest", () => {
  it("sends a plain step as it is", () => {
    expect(stepRequest("packed", EMPTY_STEP_FORM)).toEqual({ ok: true, body: { step: "packed" } });
    expect(stepRequest("delivered", EMPTY_STEP_FORM)).toEqual({ ok: true, body: { step: "delivered" } });
  });

  it("sends the tracking number only when there is one", () => {
    expect(stepRequest("handed_over", EMPTY_STEP_FORM)).toEqual({ ok: true, body: { step: "handed_over" } });
    expect(stepRequest("handed_over", { ...EMPTY_STEP_FORM, tracking: "  TRACK-1 " })).toEqual({ ok: true, body: { step: "handed_over", tracking: "TRACK-1" } });
    expect(stepRequest("handed_over", { ...EMPTY_STEP_FORM, tracking: "x".repeat(2001) })).toEqual({ ok: false, problem: "trackingLong" });
  });

  it("needs a reason for a failed delivery: a code, or the typed text of other", () => {
    expect(stepRequest("failed", EMPTY_STEP_FORM)).toEqual({ ok: false, problem: "reason" });
    expect(stepRequest("failed", { ...EMPTY_STEP_FORM, reason: "not_home" })).toEqual({ ok: true, body: { step: "failed", reason: "not_home" } });
    expect(stepRequest("failed", { ...EMPTY_STEP_FORM, reason: "other", reasonText: "   " })).toEqual({ ok: false, problem: "reason" });
    expect(stepRequest("failed", { ...EMPTY_STEP_FORM, reason: "other", reasonText: " gate locked " })).toEqual({ ok: true, body: { step: "failed", reason: "gate locked" } });
    expect(stepRequest("failed", { ...EMPTY_STEP_FORM, reason: "other", reasonText: "x".repeat(501) })).toEqual({ ok: false, problem: "reasonLong" });
  });

  it("offers the five reasons of the delivery group", () => {
    expect(FAILED_REASONS).toEqual(["no_answer", "not_home", "wrong_address", "refused", "other"]);
    expect(reasonValue({ reason: "", reasonText: "x" })).toBe("");
  });
});

describe("the list and the letters", () => {
  it("puts the step filter in the body of the search and in the address", () => {
    expect(ordersSearchBody({ ...EMPTY_FILTERS, step: "failed" }, 50, 0)).toEqual({ limit: 50, fulfillment_step: "failed" });
    expect(ordersSearchBody(EMPTY_FILTERS, 50, 0)).not.toHaveProperty("fulfillment_step");
    expect(parseShopOrdersSearch({ step: "packed", status: "paid" })).toEqual({ status: "paid", step: "packed" });
    expect(parseShopOrdersSearch({ step: "nonsense" })).toEqual({});
  });

  it("offers the delivered letter only for a delivered order", () => {
    const base = { status: "shipped", buyer: { name: "A", email: "a@example.test" }, delivery_kind: "shipping", shipped_at: "2026-11-01T10:00:00Z" };
    expect(resendLetters({ ...base, fulfillment_step: "delivered" } as never, true)).toEqual(["received", "shipped", "delivered"]);
    expect(resendLetters({ ...base, fulfillment_step: "handed_over" } as never, true)).toEqual(["received", "shipped"]);
    expect(resendLetters({ ...base, delivery_kind: "none", fulfillment_step: "delivered" } as never, true)).toEqual(["received"]);
  });
});
