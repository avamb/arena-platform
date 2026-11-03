/**
 * growth.ts — the rules of the "Mailings and reminders" screen (spec 51-1),
 * without React.
 *
 * The reward for the marketing tick is one of the organization's promo codes.
 * The platform accepts only a live code limited to one use per buyer, so the
 * picker offers only those (and keeps the stored one visible even when it no
 * longer qualifies, so the organizer sees why the discount stopped).
 */
import type { GrowthSettings, GrowthSettingsUpdate, PromoCodeItem } from "@/lib/api/types";

/** Whether a code can be the reward: active, not deleted, one use per buyer. */
export function isRewardCandidate(code: PromoCodeItem): boolean {
  return code.status === "active" && code.max_uses_per_customer === 1;
}

/** The codes the picker offers, the stored one kept even when it no longer qualifies. */
export function rewardChoices(codes: readonly PromoCodeItem[], storedId: string | null): PromoCodeItem[] {
  return codes.filter((c) => isRewardCandidate(c) || c.id === storedId);
}

/** Whether the stored reward code no longer qualifies (paused, deleted, limit changed). */
export function rewardBroken(codes: readonly PromoCodeItem[], storedId: string | null): boolean {
  if (storedId === null) return false;
  const c = codes.find((x) => x.id === storedId);
  return c === undefined || !isRewardCandidate(c);
}

/** The form state of the settings card. */
export interface GrowthForm {
  cartReminder: boolean;
  optIn: boolean;
  rewardId: string | null;
}

export function formFromSettings(s: GrowthSettings): GrowthForm {
  return {
    cartReminder: s.cart_reminder_enabled,
    optIn: s.marketing_optin_enabled,
    rewardId: s.consent_promo_code_id ?? null,
  };
}

export function formChanged(a: GrowthForm, b: GrowthForm): boolean {
  return a.cartReminder !== b.cartReminder || a.optIn !== b.optIn || a.rewardId !== b.rewardId;
}

/** The PUT body. The reward is cleared when the tick is not offered. */
export function settingsBody(f: GrowthForm): GrowthSettingsUpdate {
  return {
    cart_reminder_enabled: f.cartReminder,
    marketing_optin_enabled: f.optIn,
    consent_promo_code_id: f.optIn ? f.rewardId : null,
  };
}

export type SubscriberFilter = "confirmed" | "pending" | "withdrawn" | "all";

export const SUBSCRIBER_FILTERS: readonly SubscriberFilter[] = ["confirmed", "pending", "withdrawn", "all"];
