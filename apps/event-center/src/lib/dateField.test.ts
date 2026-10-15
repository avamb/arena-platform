import { describe, expect, it } from "vitest";
import { dateBounds } from "@/lib/dates";
import {
  initialDateFieldState,
  noDaysToPick,
  reduceDateField,
  type DateFieldAction,
  type DateFieldContext,
  type DateFieldState,
} from "@/lib/dateField";

const TODAY = "2026-10-01";
const ctx = (min?: string, max?: string): DateFieldContext => ({ today: TODAY, bounds: dateBounds(TODAY, min, max) });

function run(actions: DateFieldAction[], c = ctx()): { state: DateFieldState; picked: string[] } {
  let state = initialDateFieldState(null, c);
  const picked: string[] = [];
  for (const a of actions) {
    const r = reduceDateField(state, a, c);
    state = r.state;
    if (r.picked !== null) picked.push(r.picked);
  }
  return { state, picked };
}

describe("DateField state", () => {
  it("starts on the month of the value, or of today", () => {
    expect(initialDateFieldState(null, ctx()).month).toBe("2026-10");
    expect(initialDateFieldState("2026-12-24", ctx()).month).toBe("2026-12");
    // A value outside the range does not drag the grid out of it.
    expect(initialDateFieldState("2025-01-01", ctx()).month).toBe("2026-10");
  });

  it("a pressed day inside the range is the answer at once", () => {
    expect(run([{ type: "pick", iso: "2026-10-20" }]).picked).toEqual(["2026-10-20"]);
  });

  it("a pressed day outside the range is refused with the reason", () => {
    const past = run([{ type: "pick", iso: "2026-09-30" }]);
    expect(past.picked).toEqual([]);
    expect(past.state.note).toBe("dateField.error.past");
    const late = run([{ type: "pick", iso: "2026-11-20" }], ctx(undefined, "2026-11-10"));
    expect(late.picked).toEqual([]);
    expect(late.state.note).toBe("dateField.error.late");
    const early = run([{ type: "pick", iso: "2026-10-05" }], ctx("2026-10-10"));
    expect(early.state.note).toBe("dateField.error.early");
  });

  it("a typed date is NEVER taken directly", () => {
    const r = run([
      { type: "type", text: "20.10.2026" },
      { type: "submit" },
    ]);
    expect(r.picked).toEqual([]);
    expect(r.state.pending).toEqual(["2026-10-20"]);
    expect(r.state.raw).toBe("20.10.2026");
  });

  it("a typed date is chosen only after the person confirms a reading", () => {
    const r = run([
      { type: "type", text: "20.10.2026" },
      { type: "submit" },
      { type: "confirm", iso: "2026-10-20" },
    ]);
    expect(r.picked).toEqual(["2026-10-20"]);
    expect(r.state.pending).toEqual([]);
  });

  it("keeps only the readings that fit the range", () => {
    // 4 November and 11 April: April is already past on 2026-10-01.
    const r = run([
      { type: "type", text: "04.11.2026" },
      { type: "submit" },
    ]);
    expect(r.state.pending).toEqual(["2026-11-04"]);
  });

  it("offers two readings when both are in range", () => {
    const r = run([
      { type: "type", text: "04.11.2026" },
      { type: "submit" },
    ], { today: "2026-01-01", bounds: dateBounds("2026-01-01") });
    expect(r.state.pending).toEqual(["2026-11-04", "2026-04-11"]);
    const confirmed = reduceDateField(r.state, { type: "confirm", iso: "2026-04-11" }, { today: "2026-01-01", bounds: dateBounds("2026-01-01") });
    expect(confirmed.picked).toBe("2026-04-11");
    expect(confirmed.state.pending).toEqual([]);
  });

  it("a reading that was not offered cannot be confirmed", () => {
    const r = run([
      { type: "type", text: "20.10.2026" },
      { type: "submit" },
      { type: "confirm", iso: "2026-10-21" },
    ]);
    expect(r.picked).toEqual([]);
    expect(r.state.pending).toEqual(["2026-10-20"]);
  });

  it("a confirmed reading is checked against the range again", () => {
    const c = ctx();
    const stale: DateFieldState = { month: "2026-10", typed: "", pending: ["2026-09-01"], raw: "01.09.2026", note: null };
    expect(reduceDateField(stale, { type: "confirm", iso: "2026-09-01" }, c).picked).toBeNull();
  });

  it("says why a typed date is refused", () => {
    expect(run([{ type: "type", text: "banana" }, { type: "submit" }]).state.note).toBe("dateField.error.notDate");
    expect(run([{ type: "type", text: "  " }, { type: "submit" }]).state.note).toBe("dateField.error.empty");
    expect(run([{ type: "type", text: "01.09.2026" }, { type: "submit" }]).state.note).toBe("dateField.error.past");
    expect(run([{ type: "type", text: "20.12.2026" }, { type: "submit" }], ctx(undefined, "2026-11-30")).state.note).toBe("dateField.error.late");
  });

  it("typing again drops readings made from the older text", () => {
    const r = run([
      { type: "type", text: "20.10.2026" },
      { type: "submit" },
      { type: "type", text: "21.10" },
    ]);
    expect(r.state.pending).toEqual([]);
    expect(r.state.raw).toBe("");
  });

  it("'a different date' clears the readings and the text", () => {
    const r = run([
      { type: "type", text: "20.10.2026" },
      { type: "submit" },
      { type: "retry" },
    ]);
    expect(r.state.pending).toEqual([]);
    expect(r.state.typed).toBe("");
  });

  it("month paging stays inside the range", () => {
    const c = ctx(undefined, "2026-12-31");
    expect(run([{ type: "month", month: "2026-05" }], c).state.month).toBe("2026-10");
    expect(run([{ type: "month", month: "2027-02" }], c).state.month).toBe("2026-12");
    expect(run([{ type: "month", month: "2026-11" }], c).state.month).toBe("2026-11");
  });

  it("knows when no day is left", () => {
    expect(noDaysToPick(ctx("2026-10-09", "2026-10-05"))).toBe(true);
    expect(noDaysToPick(ctx())).toBe(false);
  });
});
