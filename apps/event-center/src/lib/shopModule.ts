/**
 * The shop as a paid module of the organization (spec 61, section 5). No React here.
 *
 * `GET /v1/me` tells, per membership, which paid modules the organization has today (`modules`) and
 * how much of the shop is left (`shop_mode`):
 *
 * - `full`: the module is connected, the whole shop works;
 * - `orders_only`: the module ended, but the organization has a paid or an open shop order, so only the
 *   orders (list, card, shipping, refunds, notes) stay open, for good;
 * - null: there is no shop section at all.
 *
 * The web decides what to draw from these two fields and from `shop.read`, never from a role.
 */
import type { MeOrganizationMembership } from "@/lib/api/types";

export type ShopMode = "full" | "orders_only" | "none";

/** How much of the shop the person's memberships in this organization give. */
export function shopModeFor(memberships: readonly MeOrganizationMembership[] | undefined, orgId: string): ShopMode {
  let mode: ShopMode = "none";
  for (const m of memberships ?? []) {
    if (m.org_id !== orgId) continue;
    const hasModule = Array.isArray(m.modules) && m.modules.includes("shop");
    if (hasModule || m.shop_mode === "full") return "full";
    if (m.shop_mode === "orders_only") mode = "orders_only";
  }
  return mode;
}

/** Whether the "Shop" item belongs in the menu: the shop exists for the organization and the person may read it. */
export function showShopNav(mode: ShopMode, canRead: boolean): boolean {
  return mode !== "none" && canRead;
}

/** Where the "Shop" menu item leads: the orders when that is all that is left. */
export function shopNavTarget(mode: ShopMode): "orders" | "home" {
  return mode === "orders_only" ? "orders" : "home";
}

/** The part of the address after `/o/<org>/shop`, without a query or a hash; null when the path is not a shop path. */
function shopRest(pathname: string): string | null {
  const m = /^\/o\/[^/]+\/shop(\/.*)?$/.exec(pathname.split(/[?#]/)[0]);
  if (m === null) return null;
  return (m[1] ?? "").replace(/\/+$/, "");
}

/** Whether a path is one of the order screens of the shop (the list or a card). */
export function isShopOrdersPath(pathname: string): boolean {
  const rest = shopRest(pathname);
  return rest !== null && (rest === "/orders" || rest.startsWith("/orders/"));
}

/**
 * Where a shop path must go in this mode: null to stay, "orders" when only the orders are left and the path
 * is anything else (the catalog, the pages, the settings).
 */
export function shopRedirect(mode: ShopMode, pathname: string): "orders" | null {
  if (mode !== "orders_only") return null;
  return isShopOrdersPath(pathname) ? null : "orders";
}
