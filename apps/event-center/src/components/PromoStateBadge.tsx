import type { PromoCodeItem } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import type { Tone } from "@/lib/orders";
import { promoState, type PromoState } from "@/lib/promo";
import { Badge } from "@/components/ui";

const TONE: Record<PromoState, Tone> = { active: "ok", paused: "neutral", expired: "neutral", exhausted: "bad", scheduled: "wait" };

/** The state of a code as a word, never colour only. */
export function PromoStateBadge({ item }: { item: PromoCodeItem }) {
  const { t } = useI18n();
  const state = promoState(item);
  return <Badge tone={TONE[state]}>{t(`promo.state.${state}`)}</Badge>;
}
