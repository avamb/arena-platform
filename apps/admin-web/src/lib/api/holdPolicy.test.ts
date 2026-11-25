import { describe, expect, it } from "vitest";
import {
  buildHoldRequest,
  formFromProfile,
  millisToSeconds,
  secondsToMinutes,
  type HoldForm,
  type HoldProfileRequest,
} from "@/lib/api/holdPolicy";

const standard: HoldProfileRequest = {
  select_seconds: 600,
  payment_seconds: 900,
  extend_seconds: 300,
  extend_count: 1,
  paying_seconds: 600,
  max_seats_per_hold: 10,
  max_active_per_device: 2,
  max_active_per_ip: 6,
  rehold_pause_seconds: 300,
  poll_interval_ms: 3000,
};

describe("seconds and minutes", () => {
  it("shows whole and fractional minutes", () => {
    expect(secondsToMinutes(600)).toBe("10");
    expect(secondsToMinutes(150)).toBe("2.5");
    expect(millisToSeconds(1500)).toBe("1.5");
  });

  it("round trips a profile through the form", () => {
    const form = formFromProfile(standard);
    expect(form.select_minutes).toBe("10");
    expect(form.payment_minutes).toBe("15");
    expect(form.poll_interval_seconds).toBe("3");
    const built = buildHoldRequest(form, "tune window");
    expect(built).toEqual({ ok: true, request: standard, reason: "tune window" });
  });

  it("turns minutes into seconds and seconds into milliseconds", () => {
    const form: HoldForm = { ...formFromProfile(standard), select_minutes: "7", payment_minutes: "12,5", poll_interval_seconds: "1.5" };
    const built = buildHoldRequest(form, "r");
    expect(built.ok).toBe(true);
    if (built.ok) {
      expect(built.request.select_seconds).toBe(420);
      expect(built.request.payment_seconds).toBe(750);
      expect(built.request.poll_interval_ms).toBe(1500);
    }
  });
});

describe("the reason is required", () => {
  it("refuses an empty or blank reason and names the field", () => {
    for (const reason of ["", "   "]) {
      const built = buildHoldRequest(formFromProfile(standard), reason);
      expect(built.ok).toBe(false);
      if (!built.ok) expect(built.errors.map((e) => e.field)).toEqual(["reason"]);
    }
  });

  it("trims the reason", () => {
    const built = buildHoldRequest(formFromProfile(standard), "  busy sale ");
    expect(built.ok && built.reason).toBe("busy sale");
  });
});

describe("limits", () => {
  const bad = (patch: Partial<HoldForm>) => buildHoldRequest({ ...formFromProfile(standard), ...patch }, "r");
  const fields = (r: ReturnType<typeof bad>) => (r.ok ? [] : r.errors.map((e) => e.field));

  it("checks the ranges of the table", () => {
    expect(fields(bad({ select_minutes: "1" }))).toEqual(["select_minutes"]);
    expect(fields(bad({ select_minutes: "31" }))).toEqual(["select_minutes"]);
    expect(fields(bad({ payment_minutes: "4" }))).toEqual(["payment_minutes"]);
    expect(fields(bad({ extend_count: "4" }))).toEqual(["extend_count"]);
    expect(fields(bad({ poll_interval_seconds: "0.5" }))).toEqual(["poll_interval_seconds"]);
    expect(fields(bad({ poll_interval_seconds: "11" }))).toEqual(["poll_interval_seconds"]);
    expect(fields(bad({ max_seats_per_hold: "0" }))).toEqual(["max_seats_per_hold"]);
  });

  it("refuses text, fractions of a count and fractions of a second", () => {
    expect(fields(bad({ select_minutes: "abc" }))).toEqual(["select_minutes"]);
    expect(fields(bad({ max_active_per_ip: "2.5" }))).toEqual(["max_active_per_ip"]);
    expect(fields(bad({ extend_minutes: "0.333" }))).toEqual(["extend_minutes"]);
  });
});
