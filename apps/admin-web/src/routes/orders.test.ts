/**
 * Unit tests for the SuperAdmin Orders support console (SAUI-10).
 *
 * Only the pure helpers exported from `./orders` are tested here -- the
 * route component itself is exercised via the shared queryClient /
 * authedFetch path covered by the API client tests. The badge picker is
 * pinned because operators rely on the colour cue to triage incidents.
 */
import { describe, it, expect } from "vitest";
import {
  badgeForState,
  buildOrdersQuery,
  orderLabel,
  ORDER_STATES,
} from "./orders";

describe("badgeForState", () => {
  it("renders paid as success", () => {
    expect(badgeForState("paid")).toMatchObject({ color: "#166534" });
  });

  it("renders orders that ended without a sale as error", () => {
    for (const s of ["cancelled", "expired", "abandoned", "refunded"]) {
      expect(badgeForState(s)).toMatchObject({ color: "#7f1d1d" });
    }
  });

  it("renders orders that still need something as warn", () => {
    for (const s of ["pending_payment", "partially_refunded", "manual_review"]) {
      expect(badgeForState(s)).toMatchObject({ color: "#78350f" });
    }
  });

  it("falls back to neutral badge for unknown values", () => {
    expect(badgeForState("totally-unknown")).toMatchObject({
      color: "#3730a3",
    });
  });
});

describe("ORDER_STATES", () => {
  it("matches the orders.status CHECK constraint", () => {
    expect(ORDER_STATES).toEqual([
      "pending_payment",
      "paid",
      "cancelled",
      "expired",
      "abandoned",
      "refunded",
      "partially_refunded",
      "manual_review",
    ]);
  });
});

describe("buildOrdersQuery", () => {
  const filters = { orgId: "", statusValue: "paid", limit: 50, offset: 0 };

  it("adds the trimmed search, encoded", () => {
    expect(buildOrdersQuery(filters, "  anna+x@example.com ")).toBe(
      "state=paid&limit=50&offset=0&q=anna%2Bx%40example.com",
    );
  });

  it("leaves q out when the search is blank", () => {
    expect(buildOrdersQuery(filters, "   ")).toBe("state=paid&limit=50&offset=0");
  });
});

describe("orderLabel", () => {
  it("names an order by the number the site shows", () => {
    expect(
      orderLabel({ id: "01a0b8ec-fa77-77d1-9bd4-c47f7f93e4fe", system_id: 1000000991 }),
    ).toBe("#1000000991");
  });

  it("falls back to the short UUID without a number", () => {
    const id = "01a0b8ec-fa77-77d1-9bd4-c47f7f93e4fe";
    expect(orderLabel({ id, system_id: 0 })).not.toMatch(/^#/);
  });
});
