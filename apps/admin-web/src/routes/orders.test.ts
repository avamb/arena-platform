/**
 * Unit tests for the SuperAdmin Orders support console (SAUI-10).
 *
 * Only the pure helpers exported from `./orders` are tested here -- the
 * route component itself is exercised via the shared queryClient /
 * authedFetch path covered by the API client tests. The badge picker is
 * pinned because operators rely on the colour cue to triage incidents.
 */
import { describe, it, expect } from "vitest";
import { badgeForState, ORDER_STATES } from "./orders";

describe("badgeForState", () => {
  it("renders completed as success", () => {
    expect(badgeForState("completed")).toMatchObject({ color: "#166534" });
  });

  it("renders terminal failure states as error", () => {
    for (const s of ["failed", "cancelled", "expired"]) {
      expect(badgeForState(s)).toMatchObject({ color: "#7f1d1d" });
    }
  });

  it("renders in-flight states as warn", () => {
    for (const s of ["created", "in_progress"]) {
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
  it("covers the documented lifecycle vocabulary", () => {
    expect(ORDER_STATES).toEqual([
      "created",
      "in_progress",
      "completed",
      "expired",
      "cancelled",
      "failed",
    ]);
  });
});
