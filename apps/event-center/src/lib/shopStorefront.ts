/**
 * The pure rules of the storefront switch (spec 61, section 3.4). No React here.
 *
 * `open_blockers` of the settings (and `details.blockers` of a refused opening, 422 `shop.cannot_open`)
 * name what keeps the storefront from opening, in this order:
 *
 * - `settings_not_saved`: the shop settings were never saved;
 * - `no_active_product`: no product has the status active;
 * - `payment_not_configured`: no payment method the provider has verified on the shop's sales channel.
 *
 * A code the web does not know (a newer server) is dropped from the list, not shown raw.
 */
import { ApiError } from "@/lib/api/http";
import type { ShopSettings } from "@/lib/api/types";
import type { MessageKey } from "@/lib/i18n";

export const OPEN_BLOCKERS = ["settings_not_saved", "no_active_product", "payment_not_configured"] as const;
export type OpenBlocker = (typeof OPEN_BLOCKERS)[number];

/** The known blockers of a list the API sent, in the API's order, without repeats. */
export function knownBlockers(raw: unknown): OpenBlocker[] {
  if (!Array.isArray(raw)) return [];
  const out: OpenBlocker[] = [];
  for (const v of raw) {
    if (typeof v === "string" && (OPEN_BLOCKERS as readonly string[]).includes(v) && !out.includes(v as OpenBlocker)) out.push(v as OpenBlocker);
  }
  return out;
}

/** The text that says what is missing, in plain words. */
export function blockerKey(b: OpenBlocker): MessageKey {
  switch (b) {
    case "settings_not_saved":
      return "shop.storefront.blocker.settings_not_saved";
    case "no_active_product":
      return "shop.storefront.blocker.no_active_product";
    case "payment_not_configured":
      return "shop.storefront.blocker.payment_not_configured";
  }
}

/** Where the web fixes a blocker: the product list, the organization data (the payment note), or nowhere (the form below). */
export function blockerTarget(b: OpenBlocker): "products" | "orgData" | null {
  if (b === "no_active_product") return "products";
  if (b === "payment_not_configured") return "orgData";
  return null;
}

/** The blockers of a refused opening (422 `shop.cannot_open`), or null for any other failure. */
export function blockersOfError(err: unknown): OpenBlocker[] | null {
  if (!(err instanceof ApiError) || err.code !== "shop.cannot_open") return null;
  return knownBlockers(err.details?.blockers);
}

/** The blockers of the settings as read. A server that does not send them yet means none. */
export function blockersOf(s: { open_blockers?: readonly string[] | null }): OpenBlocker[] {
  return knownBlockers(s.open_blockers);
}

/**
 * The settings after a refused opening: the form is untouched, the blockers are the ones the API named
 * (they are fresher than the list loaded with the page).
 */
export function withBlockers(s: ShopSettings, blockers: readonly OpenBlocker[]): ShopSettings {
  return { ...s, open_blockers: [...blockers], can_open: blockers.length === 0 && s.module_active !== false };
}
