/**
 * The delivery steps of a shop order (spec 61, section 7b): `waiting` -> `packed` -> `handed_over` -> `delivered`,
 * or `failed` (it may be handed over again). The step is apart from the money status: a refund does not change it.
 * The server judges every move; this module only decides which buttons to offer and how a step reads.
 */
import type { Tone } from "@/lib/orders";
import type { MessageKey } from "@/lib/i18n";
import type { ShopOrder, ShopOrderCounts } from "@/lib/api/types";

export const FULFILLMENT_STEPS = ["waiting", "packed", "handed_over", "delivered", "failed"] as const;
export type FulfillmentStep = (typeof FULFILLMENT_STEPS)[number];

export function isFulfillmentStep(value: unknown): value is FulfillmentStep {
  return typeof value === "string" && (FULFILLMENT_STEPS as readonly string[]).includes(value);
}

/** The steps a person may set from a step (the server's table). */
const NEXT: Readonly<Record<FulfillmentStep, readonly FulfillmentStep[]>> = {
  waiting: ["packed", "handed_over", "failed"],
  packed: ["handed_over", "failed"],
  handed_over: ["delivered", "failed"],
  delivered: [],
  failed: ["handed_over"],
};

export function nextSteps(from: FulfillmentStep): readonly FulfillmentStep[] {
  return NEXT[from];
}

/** The reasons of "not delivered" (V-18). `other` asks for a text; the rest go to the API as their code. */
export const FAILED_REASONS = ["no_answer", "not_home", "wrong_address", "refused", "other"] as const;
export type FailedReason = (typeof FAILED_REASONS)[number];

/** The longest reason and tracking number the API takes, in characters. */
export const REASON_MAX = 500;
export const TRACKING_MAX = 2000;

export function isKnownReason(reason: string): reason is Exclude<FailedReason, "other"> {
  return reason === "no_answer" || reason === "not_home" || reason === "wrong_address" || reason === "refused";
}

/** The text key of a reason that has a code; a person's own text is shown as it was written. */
export function reasonLabelKey(reason: string): MessageKey | null {
  return isKnownReason(reason) ? (`shop.fulfillment.reason.${reason}` as MessageKey) : null;
}

export function stepLabelKey(step: FulfillmentStep): MessageKey {
  return `shop.fulfillment.step.${step}` as MessageKey;
}

export function stepTone(step: FulfillmentStep): Tone {
  switch (step) {
    case "delivered":
      return "ok";
    case "failed":
      return "bad";
    case "handed_over":
      return "neutral";
    default:
      return "wait";
  }
}

const VIA_KEYS: Readonly<Record<string, string>> = {
  delivery_group: "shop.fulfillment.via.delivery_group",
  telegram_bot: "shop.fulfillment.via.telegram_bot",
  event_center_web: "shop.fulfillment.via.event_center_web",
  admin_web: "shop.fulfillment.via.admin_web",
  api: "shop.fulfillment.via.api",
  system: "shop.fulfillment.via.system",
};

/** The text key of where a step was set, null for a channel this version does not know. */
export function viaLabelKey(via: string): MessageKey | null {
  return Object.prototype.hasOwnProperty.call(VIA_KEYS, via) ? (VIA_KEYS[via] as MessageKey) : null;
}

type StepOrder = Pick<ShopOrder, "status" | "delivery_kind"> & { fulfillment_step?: string | null; shipped_at?: string | null };

/**
 * The step of an order as a person reads it: the one the server stored, else (an answer from before steps, or a
 * test fixture) handed_over for a shipped order and waiting for a paid one. null when the order has nothing to
 * ship or is not paid. A refund never changes it.
 */
export function orderStep(o: StepOrder): FulfillmentStep | null {
  if (o.delivery_kind === "none") return null;
  if (isFulfillmentStep(o.fulfillment_step)) return o.fulfillment_step;
  if (o.shipped_at !== null && o.shipped_at !== undefined && o.shipped_at !== "") return "handed_over";
  if (o.status === "paid" || o.status === "shipped" || o.status === "partially_refunded") return "waiting";
  return null;
}

/** An order the money went back on in full, or that was never paid and closed: no step can be set. */
function closed(status: string): boolean {
  return status === "refunded" || status === "cancelled" || status === "expired" || status === "pending_payment";
}

/**
 * The steps the card offers as buttons. Nothing without `shop.manage`, for a closed or unpaid order or one with
 * nothing to ship. An order parked in `manual_review` can only be handed over (the operator found the goods).
 */
export function offeredSteps(o: StepOrder, canManage: boolean): readonly FulfillmentStep[] {
  if (!canManage || o.delivery_kind === "none" || closed(o.status)) return [];
  if (o.status === "manual_review") return o.shipped_at !== null && o.shipped_at !== undefined && o.shipped_at !== "" ? [] : ["handed_over"];
  const step = orderStep(o);
  return step === null ? [] : nextSteps(step);
}

/** A handed-over order can still get its tracking number changed (repeating the step only stores the number). */
export function canEditHandOver(o: StepOrder, canManage: boolean): boolean {
  if (!canManage || o.delivery_kind === "none" || closed(o.status)) return false;
  return orderStep(o) === "handed_over";
}

/**
 * Whole days a paid order has waited for packing or handing over, when that is more than two days (the badge
 * "waiting N days", spec 61 section 7b.4). null for any other step, a closed order or a fresher one.
 */
export function waitingDays(o: StepOrder & { paid_at?: string | null }, now: Date = new Date()): number | null {
  const step = orderStep(o);
  if ((step !== "waiting" && step !== "packed") || closed(o.status) || o.status === "manual_review") return null;
  if (o.paid_at === null || o.paid_at === undefined || o.paid_at === "") return null;
  const paid = Date.parse(o.paid_at);
  if (!Number.isFinite(paid)) return null;
  const hours = (now.getTime() - paid) / 3_600_000;
  return hours > 48 ? Math.floor(hours / 24) : null;
}

/** The number a step shows in the filter, or null while the counts are unknown. */
export function stepCount(step: FulfillmentStep, counts: ShopOrderCounts | undefined): number | null {
  const steps = (counts as (ShopOrderCounts & { steps?: Partial<Record<FulfillmentStep, number>> }) | undefined)?.steps;
  if (steps === undefined || steps === null) return null;
  const n = steps[step];
  return typeof n === "number" ? n : null;
}

export type StepProblem = "reason" | "reasonLong" | "trackingLong";

export interface StepForm {
  tracking: string;
  /** A code of FAILED_REASONS. */
  reason: FailedReason | "";
  /** The typed text, used with `other`. */
  reasonText: string;
}

export const EMPTY_STEP_FORM: StepForm = { tracking: "", reason: "", reasonText: "" };

/** The reason as the API takes it: the code for a button, the typed text for `other`. */
export function reasonValue(f: Pick<StepForm, "reason" | "reasonText">): string {
  if (f.reason === "") return "";
  if (f.reason === "other") return f.reasonText.trim();
  return f.reason;
}

export type StepRequest = { step: FulfillmentStep; tracking?: string; reason?: string };

/** The body of POST .../fulfillment, or the problem with what was typed. */
export function stepRequest(step: FulfillmentStep, f: StepForm): { ok: true; body: StepRequest } | { ok: false; problem: StepProblem } {
  if (step === "failed") {
    const reason = reasonValue(f);
    if (reason === "") return { ok: false, problem: "reason" };
    if ([...reason].length > REASON_MAX) return { ok: false, problem: "reasonLong" };
    return { ok: true, body: { step, reason } };
  }
  if (step === "handed_over") {
    const tracking = f.tracking.trim();
    if ([...tracking].length > TRACKING_MAX) return { ok: false, problem: "trackingLong" };
    return { ok: true, body: tracking === "" ? { step } : { step, tracking } };
  }
  return { ok: true, body: { step } };
}
