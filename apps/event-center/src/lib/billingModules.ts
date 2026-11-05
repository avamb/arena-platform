/**
 * The shop module on the billing screen (spec 61, section 5): one read-only line, from
 * `GET .../billing/modules`. No React here.
 *
 * The module is the platform's, so the organization only reads it: since when it is connected and what it
 * costs per month. A price of 0 is "free". The price that applies today comes from the API
 * (`effective_monthly_minor`: the individual price of the period in force, else the price list's).
 */
import type { OrgBillingModulePeriod, OrgBillingModulesResponse } from "@/lib/api/types";

export type ShopModuleState = "active" | "scheduled" | "ended";

export type ShopModulePrice = { kind: "free" } | { kind: "amount"; minor: number; currency: string } | { kind: "unknown" };

export interface ShopModuleLine {
  state: ShopModuleState;
  /** First day of the period the line is about (YYYY-MM-DD, UTC). */
  since: string;
  /** Its last day, or null when it has none. */
  until: string | null;
  price: ShopModulePrice;
}

/** The shop periods of the answer, oldest first. */
export function shopPeriods(res: OrgBillingModulesResponse | undefined): OrgBillingModulePeriod[] {
  return (res?.items ?? []).filter((p) => p.module === "shop").sort((a, b) => a.starts_on.localeCompare(b.starts_on));
}

/**
 * The period the line speaks about: the one in force, else the next one that starts, else the last one that
 * ended. null when the organization never had the module.
 */
export function currentShopPeriod(periods: readonly OrgBillingModulePeriod[]): OrgBillingModulePeriod | null {
  const inForce = periods.find((p) => p.status === "in_force");
  if (inForce !== undefined) return inForce;
  const scheduled = periods.find((p) => p.status === "scheduled");
  if (scheduled !== undefined) return scheduled;
  const ended = periods.filter((p) => p.status === "ended");
  return ended.length > 0 ? ended[ended.length - 1] : null;
}

/** The monthly price as the line shows it. */
export function shopModulePrice(res: Pick<OrgBillingModulesResponse, "effective_monthly_minor" | "currency">): ShopModulePrice {
  if (res.effective_monthly_minor === null || res.effective_monthly_minor === undefined) return { kind: "unknown" };
  if (res.effective_monthly_minor === 0) return { kind: "free" };
  if (res.currency === null || res.currency === undefined || res.currency === "") return { kind: "unknown" };
  return { kind: "amount", minor: res.effective_monthly_minor, currency: res.currency };
}

/** The line, or null when there is nothing to say (the organization never had the module). */
export function shopModuleLine(res: OrgBillingModulesResponse | undefined): ShopModuleLine | null {
  const p = currentShopPeriod(shopPeriods(res));
  if (p === null || res === undefined) return null;
  return {
    state: p.status === "in_force" ? "active" : p.status === "scheduled" ? "scheduled" : "ended",
    since: p.starts_on,
    until: p.ends_on ?? null,
    price: p.status === "ended" ? { kind: "unknown" } : shopModulePrice(res),
  };
}
