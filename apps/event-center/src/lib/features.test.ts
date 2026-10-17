import { describe, expect, it } from "vitest";
import { flagOn, wizardEnabled } from "@/lib/features";

describe("feature flags", () => {
  it("are on only for the exact value 1", () => {
    expect(flagOn("1")).toBe(true);
    for (const off of [undefined, "", "0", "true", "yes", " 1", 1, true, null]) expect(flagOn(off), String(off)).toBe(false);
  });
  it("keep the wizard off unless the build asks for it", () => {
    expect(wizardEnabled(undefined)).toBe(false);
    expect(wizardEnabled("1")).toBe(true);
  });
});
