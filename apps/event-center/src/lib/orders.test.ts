import { describe, expect, it } from "vitest";
import { ORDER_STATUSES, canCancelOrder } from "@/lib/orders";

describe("canCancelOrder", () => {
  it("offers the button only for an unpaid order and a person with order.write", () => {
    expect(canCancelOrder("pending_payment", true)).toBe(true);
    expect(canCancelOrder("pending_payment", false)).toBe(false);
  });

  it("never offers it for any other status", () => {
    for (const status of ORDER_STATUSES) {
      if (status === "pending_payment") continue;
      expect(canCancelOrder(status, true), status).toBe(false);
    }
    expect(canCancelOrder("something_new", true)).toBe(false);
  });
});
