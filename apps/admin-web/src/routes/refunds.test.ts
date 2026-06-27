/**
 * Unit tests for the SuperAdmin Refunds support console (SAUI-10).
 */
import { describe, it, expect } from "vitest";
import { badgeForRefundState, REFUND_STATES } from "./refunds";

describe("badgeForRefundState", () => {
  it("renders succeeded as success", () => {
    expect(badgeForRefundState("succeeded")).toMatchObject({ color: "#166534" });
  });
  it("renders failed / cancelled as error", () => {
    expect(badgeForRefundState("failed")).toMatchObject({ color: "#7f1d1d" });
    expect(badgeForRefundState("cancelled")).toMatchObject({ color: "#7f1d1d" });
  });
  it("renders in-flight states as warn", () => {
    for (const s of ["requested", "approved", "processing"]) {
      expect(badgeForRefundState(s)).toMatchObject({ color: "#78350f" });
    }
  });
  it("falls back to neutral badge for unknown values", () => {
    expect(badgeForRefundState("totally-unknown")).toMatchObject({
      color: "#3730a3",
    });
  });
});

describe("REFUND_STATES", () => {
  it("covers the documented refund vocabulary", () => {
    expect(REFUND_STATES).toEqual([
      "requested",
      "approved",
      "processing",
      "succeeded",
      "failed",
      "cancelled",
    ]);
  });
});
