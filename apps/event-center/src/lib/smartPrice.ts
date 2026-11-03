/**
 * Smart price (spec 08_architecture/51_4_2_smart_price_ru.md): the rules of
 * the preset form. The server computes every step and every price (preview
 * route); this file only reads what the person typed and says which preset a
 * category follows. It computes no price.
 */
import type { SmartPriceRequest, SmartPriceRule, TicketTierItem } from "@/lib/api/types";
import type { MessageKey } from "@/lib/i18n/en";

export type SmartKind = "by_sales" | "by_days" | "last_days";

export const SMART_KINDS: readonly SmartKind[] = ["by_sales", "by_days", "last_days"];

/** What the person typed, as text. */
export interface SmartForm {
  kind: SmartKind;
  step: string;
  every: string;
  cap: string;
  /** YYYY-MM-DD for by_days, null counts from today. */
  start: string | null;
}

/** Proposals only: the person changes them before anything is written. */
export function defaultSmartForm(kind: SmartKind): SmartForm {
  return {
    kind,
    step: kind === "last_days" ? "20" : "10",
    every: kind === "by_sales" ? "50" : kind === "by_days" ? "7" : "3",
    cap: "50",
    start: null,
  };
}

export interface FieldError {
  key: MessageKey;
  params?: Record<string, string | number>;
}

export type SmartFormResult =
  | { ok: true; body: Omit<SmartPriceRequest, "tier_ids"> }
  | { ok: false; errors: Partial<Record<"step" | "every" | "cap", FieldError>> };

/** The same limits the server checks (smartprice.Params.Validate). */
const LIMITS = {
  step: [1, 100],
  every_by_sales: [1, 100000],
  every_days: [1, 365],
  cap: [1, 500],
} as const;

function whole(text: string): number | null {
  const t = text.trim();
  if (!/^\d{1,6}$/.test(t)) return null;
  return Number(t);
}

/** parseSmartForm checks the numbers before the preview is asked. */
export function parseSmartForm(f: SmartForm): SmartFormResult {
  const errors: Partial<Record<"step" | "every" | "cap", FieldError>> = {};
  const inRange = (field: "step" | "every" | "cap", text: string, min: number, max: number): number => {
    const n = whole(text);
    if (n === null) {
      errors[field] = { key: "smartPrice.err.number" };
      return 0;
    }
    if (n < min || n > max) {
      errors[field] = { key: "smartPrice.err.range", params: { min, max } };
    }
    return n;
  };
  const step = inRange("step", f.step, LIMITS.step[0], LIMITS.step[1]);
  const everyLimits = f.kind === "by_sales" ? LIMITS.every_by_sales : LIMITS.every_days;
  const every = inRange("every", f.every, everyLimits[0], everyLimits[1]);
  let cap = 0;
  if (f.kind !== "last_days") {
    cap = inRange("cap", f.cap, LIMITS.cap[0], LIMITS.cap[1]);
    if (errors.cap === undefined && errors.step === undefined && cap < step) {
      errors.cap = { key: "smartPrice.err.cap" };
    }
  }
  if (Object.keys(errors).length > 0) return { ok: false, errors };
  const body: Omit<SmartPriceRequest, "tier_ids"> = { kind: f.kind, step_percent: step, every };
  if (f.kind !== "last_days") body.cap_percent = cap;
  if (f.kind === "by_days" && f.start !== null) body.start_date = f.start;
  return { ok: true, body };
}

/** The preset a category follows now (it is the first step of a chain), if any. */
export function activeRuleOf(rules: readonly SmartPriceRule[], tierId: string): SmartPriceRule | undefined {
  return rules.find((r) => belongsTo(r, tierId) && r.status === "active");
}

/** A preset drives its category and, by tickets sold, the steps it made. */
function belongsTo(rule: SmartPriceRule, tierId: string): boolean {
  return rule.tier_id === tierId || (rule.step_tier_ids ?? []).includes(tierId);
}

/** The latest preset of the category that ended, to say why the price stopped rising. */
export function endedRuleOf(rules: readonly SmartPriceRule[], tierId: string): SmartPriceRule | undefined {
  return rules.find((r) => belongsTo(r, tierId) && r.status !== "active");
}

export type SmartEligibility = "ok" | "chain" | "free";

/** Whether a category can take a preset. The server checks it again. */
export function smartEligibility(tier: TicketTierItem, inChain: boolean): SmartEligibility {
  if (inChain) return "chain";
  if (tier.pricing_mode !== "fixed" || (tier.price_amount ?? 0) <= 0) return "free";
  return "ok";
}

/** Which kinds a category may use: a seated one rises by days only (owner decision В-10). */
export function allowedKinds(seated: boolean): readonly SmartKind[] {
  return seated ? SMART_KINDS.filter((k) => k !== "by_sales") : SMART_KINDS;
}
