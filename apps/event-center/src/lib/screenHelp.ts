/**
 * Help for ONE screen: what the person sees here, one line per item, in the order of the screen, and how to come back.
 * No React here.
 *
 * There is no hand-written text per screen. The lines come from the same models that draw the screens: the items of
 * `navMenu.ts` (`navGroups`, the Menu sheet and the Promotion sheet) for the section screens, and the groups and
 * sections of `eventActions.ts` (`eventCardLayout`: the five sections, the same model that draws the page) and
 * `eventStatus.ts` (`statusActions`) for the event card. A role does not get the line of an item it does not have,
 * because the model does not hold the item for it. Each item has ONE description key (`ITEM_HELP`, `CARD_HELP`,
 * `STATUS_HELP`), and the general Help page reads the same keys.
 * `screenHelp.test.ts` fails when an item of the card or a status button has no line in every language.
 */
import { eventCardLayout, isAction, type ItemId, type ItemRights, type SectionId } from "@/lib/eventActions";
import { statusActions, type StatusAction, type StatusRights } from "@/lib/eventStatus";
import type { MessageKey } from "@/lib/i18n/en";
import { navGroups, type NavGroup, type NavGroupId, type NavPerson } from "@/lib/navMenu";
import type { ShopMode } from "@/lib/shopModule";

/** The description of each item of the menu, by the id of the item (the data-testid of its link). */
export const ITEM_HELP: Record<string, MessageKey> = {
  "nav-new-event": "screenHelp.item.newEvent",
  "menu-events": "screenHelp.item.events",
  "nav-summary": "screenHelp.item.summary",
  "menu-orders": "screenHelp.item.orders",
  "menu-refunds": "screenHelp.item.refunds",
  "nav-promo": "screenHelp.item.promo",
  "nav-invitations": "screenHelp.item.invitations",
  "nav-links": "screenHelp.item.links",
  "nav-growth": "screenHelp.item.growth",
  "nav-scanner": "screenHelp.item.scanner",
  "nav-guest-list": "screenHelp.item.guestList",
  "nav-shop": "screenHelp.item.shop",
  "nav-org-data": "screenHelp.item.orgData",
  "nav-payments": "screenHelp.item.payments",
  "nav-team": "screenHelp.item.team",
  "nav-promoters": "screenHelp.item.promoters",
  "nav-venues": "screenHelp.item.venues",
  "nav-notifications": "screenHelp.item.notifications",
  "nav-billing": "screenHelp.item.billing",
  "nav-switch-org": "screenHelp.item.switchOrg",
  "nav-help": "screenHelp.item.help",
};

/** The name of each item of the event card (a button or a block, not the status block) and its description. */
export const CARD_HELP: Record<Exclude<ItemId, "status">, { label: MessageKey; text: MessageKey }> = {
  qr: { label: "links.button", text: "screenHelp.action.qr" },
  orders: { label: "event.orders", text: "screenHelp.action.orders" },
  csv: { label: "event.csvSales", text: "screenHelp.action.csv" },
  guests: { label: "guestList.downloadEvent", text: "screenHelp.action.guests" },
  funnel: { label: "funnel.button", text: "screenHelp.action.funnel" },
  edit: { label: "event.edit", text: "screenHelp.action.edit" },
  sample: { label: "event.sample", text: "screenHelp.action.sample" },
  promo: { label: "nav.promo", text: "screenHelp.action.promo" },
  invitations: { label: "nav.invitations", text: "screenHelp.action.invitations" },
  links: { label: "nav.links", text: "screenHelp.action.links" },
  totals: { label: "event.totals", text: "screenHelp.block.totals" },
  pageStats: { label: "pageStats.title", text: "screenHelp.block.pageStats" },
  poster: { label: "poster.title", text: "screenHelp.block.poster" },
  sessions: { label: "event.sessions", text: "screenHelp.block.sessions" },
  salesSwitch: { label: "eventSales.title", text: "screenHelp.block.salesSwitch" },
  materials: { label: "mat.title", text: "screenHelp.block.materials" },
  emailNote: { label: "emailNote.title", text: "screenHelp.block.emailNote" },
  referral: { label: "referral.title", text: "screenHelp.block.referral" },
};

/** The same for the buttons of the status block, the "More" group of the event card. */
export const STATUS_HELP: Record<StatusAction, { label: MessageKey; text: MessageKey }> = {
  publish: { label: "eventStatus.button.publish", text: "screenHelp.status.publish" },
  off: { label: "eventStatus.button.off", text: "screenHelp.status.off" },
  archive: { label: "eventStatus.button.archive", text: "screenHelp.status.archive" },
  delete: { label: "eventStatus.button.delete", text: "screenHelp.status.delete" },
};

/** The name of each section of the event card. The page draws the same keys. */
export const SECTION_TITLE: Record<SectionId, MessageKey> = {
  link: "event.group.link",
  sales: "event.group.sales",
  edit: "event.group.edit",
  promo: "event.group.promo",
  more: "event.group.more",
};

export type ScreenId =
  | "events"
  | "event"
  | "orders"
  | "refunds"
  | "promo"
  | "invitations"
  | "links"
  | "growth"
  | "entrance"
  | "shop"
  | "orgData"
  | "team"
  | "promoters"
  | "venues"
  | "notifications"
  | "billing";

interface ScreenDef {
  /** The item of the menu that leads to this screen: the come-back line follows its path. */
  primary: string;
  /** The items described here, in order. A group id means every item of that group. */
  items: readonly string[] | NavGroupId;
}

const SCREENS: Record<Exclude<ScreenId, "event">, ScreenDef> = {
  events: { primary: "menu-events", items: ["nav-new-event", "menu-events", "nav-summary"] },
  orders: { primary: "menu-orders", items: ["menu-orders", "menu-refunds"] },
  refunds: { primary: "menu-refunds", items: ["menu-refunds"] },
  promo: { primary: "nav-promo", items: "promotion" },
  invitations: { primary: "nav-invitations", items: "promotion" },
  links: { primary: "nav-links", items: "promotion" },
  growth: { primary: "nav-growth", items: "promotion" },
  entrance: { primary: "nav-scanner", items: ["nav-scanner", "nav-guest-list"] },
  shop: { primary: "nav-shop", items: ["nav-shop"] },
  orgData: { primary: "nav-org-data", items: ["nav-org-data", "nav-payments"] },
  team: { primary: "nav-team", items: ["nav-team"] },
  promoters: { primary: "nav-promoters", items: ["nav-promoters"] },
  venues: { primary: "nav-venues", items: ["nav-venues"] },
  notifications: { primary: "nav-notifications", items: ["nav-notifications"] },
  billing: { primary: "nav-billing", items: ["nav-billing"] },
};

export interface HelpLine {
  /** Stable id: the id of the menu item, `action-<id>` or `status-<id>`. */
  id: string;
  /** The name of the item, as the person sees it. */
  labelKey: MessageKey;
  /** What it does. */
  textKey: MessageKey;
}

export interface HelpSection {
  id: string;
  /** The heading of a group of the event card. Section screens have none. */
  titleKey?: MessageKey;
  lines: HelpLine[];
}

export interface ScreenHelp {
  screenId: ScreenId;
  sections: HelpSection[];
  /** The steps of the way back, each one a catalog key of a name the person sees. */
  back: MessageKey[];
}

export interface NavContext {
  access: NavPerson;
  shopMode: ShopMode;
  orgCount: number;
}

/** What the event card needs on top: the event and what the person may press, the same flags the card draws from. */
export interface EventCardHelp {
  event: { status: string; sales_state?: string | null };
  rights: ItemRights;
  status: StatusRights;
  /** The state that fixes the order of the sections, when it differs from the event (after a status move). */
  orderBy?: { status: string; sales_state?: string | null };
}

/** How a person gets to an item of the menu from any screen. */
function pathTo(group: NavGroup, entryId: string, labelKey: MessageKey, can: (p: string) => boolean): MessageKey[] {
  switch (entryId) {
    case "menu-events":
      return ["nav.events"];
    case "menu-orders":
      return ["nav.orders"];
    case "menu-refunds":
      return can("order.read") ? ["nav.more", group.titleKey, labelKey] : ["nav.refunds"];
    case "nav-scanner":
      return ["nav.entrance"];
    default:
      return group.id === "promotion" ? ["nav.promotion", labelKey] : ["nav.more", group.titleKey, labelKey];
  }
}

/** The help line id of an item: `action-<id>` for a button, `block-<id>` for a block, `status-<action>` for a status button. */
export function helpLineId(item: Exclude<ItemId, "status">): string {
  return `${isAction(item) ? "action" : "block"}-${item}`;
}

function eventSections(card: EventCardHelp): HelpSection[] {
  const layout = eventCardLayout(card.event, card.rights, card.orderBy);
  return layout.sections.map((s) => ({
    id: s.id,
    titleKey: SECTION_TITLE[s.id],
    lines: s.items.flatMap((item): HelpLine[] =>
      item === "status"
        ? statusActions(card.event.status, card.status).map((a) => ({ id: `status-${a}`, labelKey: STATUS_HELP[a].label, textKey: STATUS_HELP[a].text }))
        : [{ id: helpLineId(item), labelKey: CARD_HELP[item].label, textKey: CARD_HELP[item].text }],
    ),
  }));
}

/**
 * The help of one screen for this person, or null when the screen has nothing to say (the person cannot reach it).
 * The event card needs `card`; every other screen needs only `nav`.
 */
export function screenHelp(screenId: ScreenId, nav: NavContext, card?: EventCardHelp): ScreenHelp | null {
  if (screenId === "event") {
    if (card === undefined) return null;
    const sections = eventSections(card);
    if (sections.length === 0) return null;
    return { screenId, sections, back: ["nav.events", "screenHelp.step.selectEvent"] };
  }
  const def = SCREENS[screenId];
  const groups = navGroups(nav.access, nav.shopMode, nav.orgCount);
  const entries = groups.flatMap((g) => g.items.map((e) => ({ g, e })));
  const items = def.items;
  const wanted =
    typeof items === "string"
      ? entries.filter(({ g }) => g.id === items)
      : items.flatMap((id) => entries.filter(({ e }) => e.id === id));
  const own = entries.find(({ e }) => e.id === def.primary);
  if (own === undefined || wanted.length === 0) return null;
  const lines: HelpLine[] = wanted.flatMap((x) => {
    const text = ITEM_HELP[x.e.id];
    return text === undefined ? [] : [{ id: x.e.id, labelKey: x.e.labelKey, textKey: text }];
  });
  return { screenId, sections: [{ id: "items", lines }], back: pathTo(own.g, own.e.id, own.e.labelKey, nav.access.can) };
}

/** The ids of the lines in screen order: what the live twin compares with the screen. */
export function helpLineIds(help: ScreenHelp): string[] {
  return help.sections.flatMap((s) => s.lines.map((l) => l.id));
}
