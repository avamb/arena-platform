/**
 * The ONE model of the event card: which sections it has, in which order, what each one holds and which section
 * is open. The page (`EventScreen.tsx`) and the "What is here?" help (`screenHelp.ts`) both read it, so they cannot
 * disagree. No React here.
 *
 * The card has five sections, the same names and the same order as the Telegram bot's event card:
 *
 * - "link": the link and the QR code for buyers;
 * - "sales": orders, sales CSV, guest list, funnel, the totals and the page statistics;
 * - "edit": edit, poster, sessions (with the sales state), the sales page materials, the sample ticket and the
 *   text in the e-mail;
 * - "promo": promo codes, free tickets, the links of this event and "Refer a friend";
 * - "more": the status block (publish, take off sale, archive, delete).
 *
 * The sales state of the sessions sits in "edit" next to the sessions list: it works on the sessions, and the
 * organizer reaches both together. A section with no item the person may use is not drawn. The order depends on the
 * event: a draft shows "more" first (it holds Publish), an event that is over (archived, cancelled or finished)
 * shows "sales" first. The first section that is drawn is the only one open by default. No item is added or removed
 * here: the permissions stay with the screen, which passes one flag per item.
 */
export type SectionId = "link" | "sales" | "edit" | "promo" | "more";

/** A button or a link: it has its own line in the help. */
export type ActionId = "edit" | "sample" | "orders" | "csv" | "guests" | "funnel" | "qr" | "promo" | "invitations" | "links";

/** A block of the page that is not a button. "status" is the status block: its lines come from `eventStatus.ts`. */
export type BlockId = "totals" | "pageStats" | "poster" | "sessions" | "salesSwitch" | "materials" | "emailNote" | "referral" | "status";

export type ItemId = ActionId | BlockId;

/** What the person may use. The screen computes each flag from `access.can`. */
export type ItemRights = Record<ItemId, boolean>;

export interface EventSection {
  id: SectionId;
  items: ItemId[];
  /** True for the one section that is open when the person has not chosen yet. */
  defaultOpen: boolean;
}

export interface EventCardLayout {
  /** Sections in screen order, empty ones left out. */
  sections: EventSection[];
  /** The one button drawn as primary, or null. */
  primary: ActionId | null;
}

export const SECTION_IDS: readonly SectionId[] = ["link", "sales", "edit", "promo", "more"];

const SECTION_ITEMS: Record<SectionId, readonly ItemId[]> = {
  link: ["qr"],
  sales: ["orders", "csv", "guests", "funnel", "totals", "pageStats"],
  edit: ["edit", "poster", "sessions", "salesSwitch", "materials", "sample", "emailNote"],
  promo: ["promo", "invitations", "links", "referral"],
  more: ["status"],
};

const ACTIONS: ReadonlySet<ItemId> = new Set<ItemId>(["edit", "sample", "orders", "csv", "guests", "funnel", "qr", "promo", "invitations", "links"]);

export function isAction(id: ItemId): id is ActionId {
  return ACTIONS.has(id);
}

/** The section that holds an item. */
export function sectionOf(id: ItemId): SectionId {
  return SECTION_IDS.find((s) => SECTION_ITEMS[s].includes(id)) as SectionId;
}

/** An event that sells no more: archived or cancelled, or the server's "archived" sales state (finished). */
export function isPastEvent(event: { status: string; sales_state?: string | null }): boolean {
  return event.status === "archived" || event.status === "cancelled" || event.sales_state === "archived";
}

/** The order of the sections for this event. */
export function sectionOrder(event: { status: string; sales_state?: string | null }): SectionId[] {
  if (event.status === "draft") return ["more", "link", "sales", "edit", "promo"];
  if (isPastEvent(event)) return ["sales", "link", "edit", "promo", "more"];
  return ["link", "sales", "edit", "promo", "more"];
}

/**
 * `orderBy` is the event whose state fixes the ORDER of the sections. The screen passes the state the card had
 * when the person made a status move, so the sections do not jump under the person after the move.
 */
export function eventCardLayout(
  event: { status: string; sales_state?: string | null },
  rights: ItemRights,
  orderBy: { status: string; sales_state?: string | null } = event,
): EventCardLayout {
  const draft = event.status === "draft";
  const sections = sectionOrder(orderBy)
    .map((id) => ({ id, items: SECTION_ITEMS[id].filter((i) => rights[i]) }))
    .filter((s) => s.items.length > 0)
    .map((s, index) => ({ ...s, defaultOpen: index === 0 }));
  // One filled button: Edit on a draft (its first job is publishing), else the first button of the card.
  const firstAction = sections.flatMap((s) => s.items).find(isAction) ?? null;
  const primary: ActionId | null = draft ? (rights.edit ? "edit" : null) : (firstAction as ActionId | null);
  return { sections, primary };
}

/** The anchors the page had before the sections: each one lives in one section now. */
const LEGACY_ANCHORS: Record<string, ItemId> = {
  "event-totals": "totals",
  "event-page-stats": "pageStats",
  "event-poster-title": "poster",
  "event-sessions": "sessions",
  "event-sales-switch": "salesSwitch",
  "event-materials": "materials",
  "email-note-title": "emailNote",
  "event-status-heading": "status",
};

/**
 * The section an address hash points into: `#event-section-<id>`, `#event-item-<item>`, or one of the anchors the
 * blocks had before the sections. Null when the hash points nowhere we know (the page then does nothing).
 */
export function sectionForAnchor(hash: string): SectionId | null {
  const id = hash.replace(/^#/, "");
  const section = /^event-section-(.+)$/.exec(id)?.[1];
  if (section !== undefined && (SECTION_IDS as readonly string[]).includes(section)) return section as SectionId;
  const item = /^event-item-(.+)$/.exec(id)?.[1] ?? (Object.prototype.hasOwnProperty.call(LEGACY_ANCHORS, id) ? LEGACY_ANCHORS[id] : undefined);
  return item !== undefined && SECTION_IDS.some((s) => (SECTION_ITEMS[s] as readonly string[]).includes(item)) ? sectionOf(item as ItemId) : null;
}

/** Remembered state of a section on this device: per section id, not per event. Never throws. */
const STORAGE_PREFIX = "arena.ec.eventSection.";

export function readSectionOpen(id: SectionId): boolean | null {
  try {
    const v = localStorage.getItem(STORAGE_PREFIX + id);
    return v === "1" ? true : v === "0" ? false : null;
  } catch {
    return null;
  }
}

export function writeSectionOpen(id: SectionId, open: boolean): void {
  try {
    localStorage.setItem(STORAGE_PREFIX + id, open ? "1" : "0");
  } catch {
    // Private mode or a full store: the choice lives until the page is left.
  }
}
