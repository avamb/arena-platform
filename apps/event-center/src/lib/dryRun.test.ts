import { describe, expect, it } from "vitest";
import { canConfirm, initialDryRunState, isBlocked, reduceDryRun, type DryRunEvent, type DryRunResult, type DryRunState } from "@/lib/dryRun";

const OK: DryRunResult = { summary: "Moves 12 orders", figures: [{ label: "Orders", value: 12 }] };
const BLOCKED: DryRunResult = { blocked: "Some buyers bought through a partner site. Write to support." };

function run(events: DryRunEvent[], from: DryRunState = initialDryRunState): DryRunState {
  return events.reduce(reduceDryRun, from);
}

describe("DryRunConfirm state", () => {
  it("starts by loading and offers no button", () => {
    expect(initialDryRunState.phase).toBe("loading");
    expect(canConfirm(initialDryRunState)).toBe(false);
  });

  it("offers the button when the dry run answers and does not block", () => {
    const s = run([{ type: "loaded", result: OK }]);
    expect(s.phase).toBe("ready");
    expect(canConfirm(s)).toBe(true);
  });

  it("offers no button when the change is blocked, and a press does nothing", () => {
    const s = run([{ type: "loaded", result: BLOCKED }]);
    expect(isBlocked(BLOCKED)).toBe(true);
    expect(canConfirm(s)).toBe(false);
    expect(run([{ type: "press" }], s)).toBe(s);
  });

  it("an empty or null reason is not a block", () => {
    expect(isBlocked({ blocked: null })).toBe(false);
    expect(isBlocked({ blocked: "" })).toBe(false);
    expect(isBlocked({})).toBe(false);
  });

  it("one press goes to working, and a second press while working does nothing", () => {
    const working = run([{ type: "loaded", result: OK }, { type: "press" }]);
    expect(working.phase).toBe("working");
    expect(canConfirm(working)).toBe(false);
    expect(run([{ type: "press" }], working)).toBe(working);
  });

  it("success ends in done", () => {
    expect(run([{ type: "loaded", result: OK }, { type: "press" }, { type: "succeeded" }]).phase).toBe("done");
  });

  it("a failed action keeps the result and allows another press", () => {
    const err = new Error("x");
    const s = run([{ type: "loaded", result: OK }, { type: "press" }, { type: "actionFailed", error: err }]);
    expect(s).toEqual({ phase: "actionFailed", result: OK, error: err });
    expect(canConfirm(s)).toBe(true);
    expect(run([{ type: "press" }], s).phase).toBe("working");
  });

  it("a failed dry run offers a retry through load", () => {
    const failed = run([{ type: "loadFailed", error: new Error("x") }]);
    expect(failed.phase).toBe("failed");
    expect(canConfirm(failed)).toBe(false);
    expect(run([{ type: "load" }], failed).phase).toBe("loading");
  });

  it("a reload while the action works is ignored", () => {
    const working = run([{ type: "loaded", result: OK }, { type: "press" }]);
    expect(run([{ type: "load" }], working)).toBe(working);
  });

  it("an answer that arrives after a reload started does not overwrite a finished state", () => {
    const done = run([{ type: "loaded", result: OK }, { type: "press" }, { type: "succeeded" }]);
    expect(run([{ type: "loaded", result: BLOCKED }], done)).toBe(done);
  });
});
