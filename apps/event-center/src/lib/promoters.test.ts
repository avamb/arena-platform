import { describe, expect, it } from "vitest";
import { canClear, parseField, slugChangeNeedsConfirm, slugFromInput, validSlug } from "./promoters";

describe("promoter field shapes", () => {
  it("folds a name and refuses an empty one", () => {
    expect(parseField("name", "  Teatr   Kolibel ")).toEqual({ ok: true, value: "Teatr Kolibel" });
    expect(parseField("name", "   ").ok).toBe(false);
    expect(parseField("name", "x".repeat(121)).ok).toBe(false);
  });
  it("reads a page address from a pasted link", () => {
    expect(slugFromInput("https://tickets.example/Teatr-Kolibel/")).toBe("teatr-kolibel");
    expect(validSlug("teatr-kolibel")).toBe(true);
    for (const bad of ["a", "-ab", "ab-", "a--b", "a_b", "x".repeat(65)]) expect(validSlug(bad)).toBe(false);
  });
  it("checks phone, e-mail and website by shape only", () => {
    expect(parseField("phone", "+420 123 456 789").ok).toBe(true);
    expect(parseField("phone", "call me").ok).toBe(false);
    expect(parseField("email", "A@B.co")).toEqual({ ok: true, value: "a@b.co" });
    expect(parseField("email", "a@b").ok).toBe(false);
    expect(parseField("website", "partner.example").ok).toBe(true);
    expect(parseField("website", "no spaces.example").ok).toBe(false);
  });
  it("never clears a name or a page address", () => {
    expect(canClear("name")).toBe(false);
    expect(canClear("slug")).toBe(false);
    expect(canClear("website")).toBe(true);
  });
  it("asks about a page address only when a working one is replaced", () => {
    expect(slugChangeNeedsConfirm(null, "new")).toBe(false);
    expect(slugChangeNeedsConfirm("old", "old")).toBe(false);
    expect(slugChangeNeedsConfirm("old", "new")).toBe(true);
  });
});
