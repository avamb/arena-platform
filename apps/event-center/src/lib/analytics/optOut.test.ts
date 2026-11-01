import { afterEach, describe, expect, it, vi } from "vitest";
import { OPT_OUT_KEY, isOptedOut, setOptedOut } from "@/lib/analytics/optOut";

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.removeItem(OPT_OUT_KEY);
  setOptedOut(false);
});

describe("analytics opt-out", () => {
  it("is ON (not opted out) by default", () => {
    expect(isOptedOut()).toBe(false);
  });

  it("stores the choice under arena_analytics_opt_out and removes it again", () => {
    setOptedOut(true);
    expect(localStorage.getItem("arena_analytics_opt_out")).toBe("1");
    expect(isOptedOut()).toBe(true);
    setOptedOut(false);
    expect(localStorage.getItem("arena_analytics_opt_out")).toBeNull();
    expect(isOptedOut()).toBe(false);
  });

  it("reads a choice another tab stored", () => {
    localStorage.setItem(OPT_OUT_KEY, "1");
    expect(isOptedOut()).toBe(true);
  });

  it("survives a blocked storage: no throw, the choice lasts in memory", () => {
    const boom = () => {
      throw new Error("blocked");
    };
    vi.spyOn(localStorage, "getItem").mockImplementation(boom);
    vi.spyOn(localStorage, "setItem").mockImplementation(boom);
    vi.spyOn(localStorage, "removeItem").mockImplementation(boom);
    expect(() => isOptedOut()).not.toThrow();
    expect(isOptedOut()).toBe(false);
    expect(() => setOptedOut(true)).not.toThrow();
    expect(isOptedOut()).toBe(true);
    expect(() => setOptedOut(false)).not.toThrow();
    expect(isOptedOut()).toBe(false);
  });
});
