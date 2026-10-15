import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/http";
import type { OrderDetail } from "@/lib/api/types";
import {
  activeTickets,
  maskEmail,
  orderAddress,
  resendFailure,
  resendFailureKey,
  resendOffered,
  resendRetryable,
  validResendEmail,
} from "@/lib/resend";

type Tk = { status: string; holder_email: string | null };
const order = (status: string, tickets: Tk[], buyer: string | null = "anna@example.test") =>
  ({ status, tickets, buyer_email: buyer }) as unknown as OrderDetail;

describe("validResendEmail (the bot's rule)", () => {
  it("accepts a plain address", () => {
    expect(validResendEmail("name@example.com")).toBe(true);
    expect(validResendEmail("a.b+tag@mail.example.org")).toBe(true);
  });
  it("refuses what the bot refuses", () => {
    for (const bad of [
      "",
      "not an e-mail",
      "a@b",
      "@example.com",
      "a@@example.com",
      "a@example..com",
      "a@.example.com",
      "a@example.com.",
      "a b@example.com",
      "Anna <a@example.com>",
      "a@example.com, b@example.com",
      "a@example.com;b@example.com",
    ]) {
      expect(validResendEmail(bad), bad).toBe(false);
    }
    expect(validResendEmail("a@" + "x".repeat(250) + ".com")).toBe(false);
  });
});

describe("maskEmail", () => {
  it("keeps the first letter of the local part and of the first domain label", () => {
    expect(maskEmail("anna@example.com")).toBe("a***@e******.com");
    expect(maskEmail("x@y.io")).toBe("x@y.io");
  });
  it("hides an address it cannot read", () => {
    expect(maskEmail("nope")).toBe("***");
  });
  it("never leaks the typed address", () => {
    expect(maskEmail("anna.orderova@example.test")).not.toContain("orderova");
  });
});

describe("orderAddress", () => {
  it("takes the buyer's address first", () => {
    expect(orderAddress(order("paid", [{ status: "active", holder_email: "h@x.io" }]))).toBe("anna@example.test");
  });
  it("falls back to the first ACTIVE holder", () => {
    const o = order(
      "paid",
      [
        { status: "cancelled", holder_email: "gone@x.io" },
        { status: "active", holder_email: "holder@x.io" },
      ],
      null,
    );
    expect(orderAddress(o)).toBe("holder@x.io");
  });
  it("is empty when the order has no address", () => {
    expect(orderAddress(order("paid", [{ status: "active", holder_email: null }], "  "))).toBe("");
  });
});

describe("resendOffered", () => {
  const can = (p: string) => p === "ticket.update";
  const none = () => false;
  const live: Tk[] = [{ status: "active", holder_email: null }];
  it("is offered on a paid order with an active ticket to a person who holds ticket.update", () => {
    expect(resendOffered(order("paid", live), can)).toBe(true);
    expect(resendOffered(order("partially_refunded", live), can)).toBe(true);
  });
  it("is hidden by the permission, never by the role name", () => {
    expect(resendOffered(order("paid", live), none)).toBe(false);
  });
  it("is hidden when there is nothing to send", () => {
    expect(resendOffered(order("pending_payment", live), can)).toBe(false);
    expect(resendOffered(order("refunded", live), can)).toBe(false);
    expect(resendOffered(order("paid", [{ status: "cancelled", holder_email: null }]), can)).toBe(false);
    expect(activeTickets(order("paid", [{ status: "cancelled", holder_email: null }, ...live]))).toBe(1);
  });
});

describe("resendFailure", () => {
  const api = (status: number, code: string) => new ApiError(status, code, "x");
  it("maps the codes of the resend route", () => {
    expect(resendFailure(api(409, "order.seller_site_order"))).toBe("site");
    expect(resendFailure(api(409, "order.not_paid"))).toBe("notPaid");
    expect(resendFailure(api(409, "order.no_active_tickets"))).toBe("noTickets");
    expect(resendFailure(api(400, "order.invalid_email"))).toBe("badEmail");
    expect(resendFailure(api(422, "order.no_recipient"))).toBe("noRecipient");
    expect(resendFailure(api(403, "permissions.denied"))).toBe("forbidden");
  });
  it("leaves anything else to the generic error", () => {
    expect(resendFailure(api(500, "internal"))).toBeNull();
    expect(resendFailure(new Error("boom"))).toBeNull();
  });
  it("lets the person fix a bad address only", () => {
    expect(resendRetryable("badEmail")).toBe(true);
    expect(resendRetryable("noRecipient")).toBe(true);
    expect(resendRetryable(null)).toBe(true);
    expect(resendRetryable("site")).toBe(false);
  });
  it("has a text for every failure", () => {
    expect(resendFailureKey("site")).toBe("resend.site");
  });
});
