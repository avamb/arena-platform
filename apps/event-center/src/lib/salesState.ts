/**
 * The rules of the session's sales state block (migration 0183), kept apart
 * from the component so they are unit-tested. The bot's twin is
 * apps/backend/internal/platform/eventbot/sessions_sales_state.go: the same
 * choices, the same one-press confirmation.
 */
import type { Tone } from "@/lib/orders";

/** What the organizer may set. "open" lifts a state set by hand. */
export type SalesStateChoice = "open" | "soon" | "sold_out";

/** What buyers see: a choice, or what the platform decided by itself. */
export type EffectiveSalesState = SalesStateChoice | "ended" | "cancelled";

const CHOICES: readonly SalesStateChoice[] = ["open", "soon", "sold_out"];
const EFFECTIVE: readonly string[] = [...CHOICES, "ended", "cancelled"];

/** The effective state as the API names it, or null for a value this build does not know. */
export function effectiveState(raw: string | null | undefined): EffectiveSalesState | null {
  return raw !== null && raw !== undefined && EFFECTIVE.includes(raw) ? (raw as EffectiveSalesState) : null;
}

/**
 * The buttons offered: every choice but the one in force. With no state set by
 * hand "open" is the one in force, even when the platform shows "soon" or
 * "sold out" by itself. Nothing is offered once the sale is over or the session
 * is canceled.
 */
export function salesStateChoices(manual: string | null | undefined, effective: string | null | undefined): SalesStateChoice[] {
  if (effective === "ended" || effective === "cancelled") return [];
  const current = manual === "soon" || manual === "sold_out" ? manual : "open";
  return CHOICES.filter((c) => c !== current);
}

export function salesStateTone(state: EffectiveSalesState | null): Tone {
  switch (state) {
    case "open":
      return "ok";
    case "soon":
      return "wait";
    case "sold_out":
    case "cancelled":
      return "bad";
    default:
      return "neutral";
  }
}

/** Delivery of the latest change to the WordPress sites. */
export type SiteSyncStatus = "none" | "pending" | "retrying" | "delivered" | "failed";

/** The status as the API names it; an unknown value reads as "none" (nothing to say). */
export function siteSyncStatus(raw: string | null | undefined): SiteSyncStatus {
  return raw === "pending" || raw === "retrying" || raw === "delivered" || raw === "failed" ? raw : "none";
}

/** The sites still have to hear about the change: the screen offers to ask again. */
export function siteSyncInFlight(status: SiteSyncStatus): boolean {
  return status === "pending" || status === "retrying";
}
