// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from "vitest";
import { forgetRefundKey, refundKeyFor } from "@/lib/shopRefundKeys";

beforeEach(() => sessionStorage.clear());

describe("the held refund key (SB-12)", () => {
  it("returns the same key for the same request and survives a reload (storage only)", () => {
    const a = refundKeyFor("o1", '{"amount_minor":1000}');
    expect(sessionStorage.length).toBe(1);
    expect(refundKeyFor("o1", '{"amount_minor":1000}')).toBe(a);
  });

  it("mints a new key when the amount or the reason changes, and per order", () => {
    const a = refundKeyFor("o1", '{"amount_minor":1000}');
    const b = refundKeyFor("o1", '{"amount_minor":1200}');
    expect(b).not.toBe(a);
    expect(refundKeyFor("o2", '{"amount_minor":1200}')).not.toBe(b);
  });

  it("forgets the key after an answer", () => {
    const a = refundKeyFor("o1", "x");
    forgetRefundKey("o1");
    expect(sessionStorage.length).toBe(0);
    expect(refundKeyFor("o1", "x")).not.toBe(a);
  });

  it("ignores a damaged record", () => {
    sessionStorage.setItem("arena.events.shoprefund.o1", "{nope");
    expect(refundKeyFor("o1", "x")).toMatch(/^web-/);
  });
});
