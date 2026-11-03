import { describe, expect, it } from "vitest";
import type { PromoCodeItem } from "@/lib/api/types";
import { formChanged, formFromSettings, isRewardCandidate, rewardBroken, rewardChoices, settingsBody } from "@/lib/growth";

const code = (id: string, status: "active" | "paused", perBuyer: number | null) =>
  ({ id, code: id.toUpperCase(), status, max_uses_per_customer: perBuyer }) as unknown as PromoCodeItem;

describe("growth settings rules", () => {
  const codes = [code("a", "active", 1), code("b", "active", null), code("c", "paused", 1)];

  it("offers only live one-per-buyer codes, plus the stored one", () => {
    expect(isRewardCandidate(codes[0]!)).toBe(true);
    expect(isRewardCandidate(codes[1]!)).toBe(false);
    expect(isRewardCandidate(codes[2]!)).toBe(false);
    expect(rewardChoices(codes, null).map((c) => c.id)).toEqual(["a"]);
    expect(rewardChoices(codes, "c").map((c) => c.id)).toEqual(["a", "c"]);
  });

  it("flags a stored reward that no longer qualifies", () => {
    expect(rewardBroken(codes, null)).toBe(false);
    expect(rewardBroken(codes, "a")).toBe(false);
    expect(rewardBroken(codes, "c")).toBe(true);
    expect(rewardBroken(codes, "gone")).toBe(true);
  });

  it("clears the reward when the tick is not offered", () => {
    expect(settingsBody({ cartReminder: true, optIn: false, rewardId: "a" })).toEqual({
      cart_reminder_enabled: true,
      marketing_optin_enabled: false,
      consent_promo_code_id: null,
    });
    expect(settingsBody({ cartReminder: false, optIn: true, rewardId: "a" }).consent_promo_code_id).toBe("a");
  });

  it("knows when the form changed", () => {
    const f = formFromSettings({
      cart_reminder_enabled: true,
      marketing_optin_enabled: false,
      consent_promo_code_id: null,
      updated_at: null,
    });
    expect(formChanged(f, { ...f })).toBe(false);
    expect(formChanged(f, { ...f, optIn: true })).toBe(true);
  });
});
