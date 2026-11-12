/**
 * What the menu of the event center holds, per person. No React here.
 *
 * The same seven sections as the Telegram bot's menu (owner decision
 * 2026-11-12), plus Help. One function decides which sections and items a
 * person sees: the Menu sheet, the Promotion sheet and the Help screen all
 * read it, so they can never disagree. An item a person cannot use is not in
 * the list, and a section with no item is not in it either. The server stays
 * the judge: every screen still checks its own permission.
 */
import { wizardGate } from "@/lib/features";
import type { MessageKey } from "@/lib/i18n/en";
import { showShopNav, type ShopMode } from "@/lib/shopModule";

export type NavGroupId = "new" | "events" | "orders" | "promotion" | "entrance" | "shop" | "settings" | "help";

export interface NavEntry {
  /** The data-testid of the link. */
  id: string;
  labelKey: MessageKey;
}

export interface NavGroup {
  id: NavGroupId;
  titleKey: MessageKey;
  items: NavEntry[];
}

export interface NavPerson {
  can: (permission: string) => boolean;
}

export function navGroups(access: NavPerson, shopMode: ShopMode, orgCount: number): NavGroup[] {
  const can = access.can;
  const groups: NavGroup[] = [
    {
      id: "new",
      titleKey: "nav.section.new",
      items: wizardGate(can) === "ok" ? [{ id: "nav-new-event", labelKey: "wizard.events.create" }] : [],
    },
    {
      id: "events",
      titleKey: "nav.section.events",
      items: [
        { id: "menu-events", labelKey: "nav.events" },
        { id: "nav-summary", labelKey: "nav.summary" },
      ],
    },
    {
      id: "orders",
      titleKey: "nav.section.orders",
      items: [
        ...(can("order.read") ? [{ id: "menu-orders", labelKey: "nav.orders" } as NavEntry] : []),
        ...(can("refund.read") ? [{ id: "nav-refunds", labelKey: "nav.refunds" } as NavEntry] : []),
      ],
    },
    { id: "promotion", titleKey: "nav.section.promotion", items: promotionItems(can) },
    {
      id: "entrance",
      titleKey: "nav.section.entrance",
      items: [
        { id: "nav-scanner", labelKey: "nav.scanner" },
        ...(can("order.read") ? [{ id: "nav-guest-list", labelKey: "nav.guestList" } as NavEntry] : []),
      ],
    },
    {
      id: "shop",
      titleKey: "nav.section.shop",
      items: showShopNav(shopMode, can("shop.read")) ? [{ id: "nav-shop", labelKey: "nav.shop" }] : [],
    },
    {
      id: "settings",
      titleKey: "nav.section.settings",
      items: [
        ...(can("org.update") ? [{ id: "nav-org-data", labelKey: "nav.orgData" } as NavEntry] : []),
        ...(can("org.update") && can("payment_config.read") ? [{ id: "nav-payments", labelKey: "nav.payments" } as NavEntry] : []),
        ...(can("membership.read") ? [{ id: "nav-team", labelKey: "nav.team" } as NavEntry] : []),
        ...(can("promoter.read") ? [{ id: "nav-promoters", labelKey: "nav.promoters" } as NavEntry] : []),
        ...(can("venue.read") ? [{ id: "nav-venues", labelKey: "nav.venues" } as NavEntry] : []),
        { id: "nav-notifications", labelKey: "nav.notifications" },
        ...(can("billing.read") ? [{ id: "nav-billing", labelKey: "nav.billing" } as NavEntry] : []),
        ...(orgCount > 1 ? [{ id: "nav-switch-org", labelKey: "nav.switchOrg" } as NavEntry] : []),
      ],
    },
    { id: "help", titleKey: "nav.section.help", items: [{ id: "nav-help", labelKey: "nav.help" }] },
  ];
  return groups.filter((g) => g.items.length > 0);
}

/** The Promotion items alone: the sheet of the bar's «Promotion» button. */
export function promotionItems(can: (permission: string) => boolean): NavEntry[] {
  return [
    ...(can("promo.read") ? [{ id: "nav-promo", labelKey: "nav.promo" } as NavEntry] : []),
    ...(can("complimentary.read") ? [{ id: "nav-invitations", labelKey: "nav.invitations" } as NavEntry] : []),
    ...(can("sales_link.read") ? [{ id: "nav-links", labelKey: "nav.links" } as NavEntry] : []),
    ...(can("promo.read") || can("order.read") ? [{ id: "nav-growth", labelKey: "nav.growth" } as NavEntry] : []),
  ];
}
