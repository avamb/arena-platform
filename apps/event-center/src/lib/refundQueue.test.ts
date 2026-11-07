import { describe, expect, it } from "vitest";
import {
  badgeText,
  canResend,
  canRetry,
  letterKindKey,
  letterProblemKey,
  letterRowKey,
  queueCount,
  refundProblem,
  retryLineKey,
  retryOutcome,
} from "@/lib/refundQueue";

describe("the badge", () => {
  it("shows nothing at zero and caps at 99", () => {
    expect(badgeText(0)).toBeNull();
    expect(badgeText(-3)).toBeNull();
    expect(badgeText(Number.NaN)).toBeNull();
    expect(badgeText(7)).toBe("7");
    expect(badgeText(99)).toBe("99");
    expect(badgeText(100)).toBe("99+");
  });
  it("counts what the server counted, and 0 before it answers", () => {
    expect(queueCount(undefined)).toBe(0);
    expect(queueCount({ refunds: [], letters: [], refunds_total: 3, letters_total: 2, count: 5 })).toBe(5);
  });
});

describe("a refund row", () => {
  it("names why the refund is in the queue", () => {
    expect(refundProblem({ state: "failed" })).toBe("failed");
    expect(refundProblem({ state: "manual_review" })).toBe("review");
    expect(refundProblem({ state: "provider_pending" })).toBe("stuck");
  });
  it("offers a retry only to who may approve, and only when the server says it can", () => {
    expect(canRetry({ retryable: true }, true)).toBe(true);
    expect(canRetry({ retryable: true }, false)).toBe(false);
    expect(canRetry({ retryable: false }, true)).toBe(false);
    expect(canRetry({}, true)).toBe(false);
  });
  it("words the state the retry started", () => {
    expect(retryLineKey(retryOutcome([{ state: "succeeded" }]))).toBe("refund.st.succeeded");
    expect(retryLineKey(retryOutcome([{ state: "provider_pending", provider_refund_id: "re_1" }]))).toBe("refund.st.pending");
    expect(retryLineKey(retryOutcome([{ state: "provider_pending" }]))).toBe("refund.st.queued");
    expect(retryLineKey(retryOutcome([{ state: "failed" }]))).toBe("queue.retry.failedAgain");
    expect(retryLineKey(retryOutcome([]))).toBe("queue.retry.started");
  });
});

describe("a letter row", () => {
  it("resends only ticket letters, only for who may update tickets", () => {
    expect(canResend({ kind: "ticket" }, true)).toBe(true);
    expect(canResend({ kind: "ticket" }, false)).toBe(false);
    expect(canResend({ kind: "refund" }, true)).toBe(false);
  });
  it("names the kind and the problem, and keys a row by order and kind", () => {
    expect(letterKindKey("ticket")).toBe("queue.letter.kind.ticket");
    expect(letterKindKey("cancel")).toBe("queue.letter.kind.cancel");
    expect(letterKindKey("referral_cashback")).toBe("queue.letter.kind.other");
    expect(letterKindKey("something new")).toBe("queue.letter.kind.other");
    expect(letterProblemKey("failed")).toBe("queue.letter.failed");
    expect(letterProblemKey("stuck")).toBe("queue.letter.stuck");
    expect(letterRowKey({ order_id: "o1", kind: "ticket" })).not.toBe(letterRowKey({ order_id: "o1", kind: "refund" }));
  });
});
