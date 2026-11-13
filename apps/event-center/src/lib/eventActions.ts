/**
 * Pure layout of the action buttons of the event card. The buttons sit in three
 * small groups, the same groups as on the Telegram bot's event card:
 *
 * - "link": the QR / link for buyers (the first group of an event on sale);
 * - "sales": orders, sales CSV, guest list, funnel;
 * - "edit": edit, sample ticket.
 *
 * A group with no button the person may use is not drawn. The order depends on the
 * event: a draft shows the status block (publish) before everything, an event that
 * is over (archived, cancelled or finished) shows "sales" first, and the status block
 * (take off sale, archive, delete) goes LAST under "More" for every event but a draft.
 * No button is added or removed here: the permissions stay with the screen.
 */
export type ActionGroupId = "link" | "sales" | "edit";
export type ActionId = "edit" | "sample" | "orders" | "csv" | "guests" | "funnel" | "qr";

/** What the person may press. The screen computes each flag from `access.can`. */
export type ActionRights = Record<ActionId, boolean>;

export interface ActionGroup {
  id: ActionGroupId;
  actions: ActionId[];
}

export interface EventCardLayout {
  /** True for a draft: the status block (publish) comes before the groups. */
  statusFirst: boolean;
  /** Groups in screen order, empty ones left out. */
  groups: ActionGroup[];
  /** The one button drawn as primary, or null. */
  primary: ActionId | null;
}

const GROUP_ACTIONS: Record<ActionGroupId, readonly ActionId[]> = {
  link: ["qr"],
  sales: ["orders", "csv", "guests", "funnel"],
  edit: ["edit", "sample"],
};

/** An event that sells no more: archived or cancelled, or the server's "archived" sales state (finished). */
export function isPastEvent(event: { status: string; sales_state?: string | null }): boolean {
  return event.status === "archived" || event.status === "cancelled" || event.sales_state === "archived";
}

export function eventCardLayout(
  event: { status: string; sales_state?: string | null },
  rights: ActionRights,
): EventCardLayout {
  const draft = event.status === "draft";
  const order: ActionGroupId[] = !draft && isPastEvent(event) ? ["sales", "link", "edit"] : ["link", "sales", "edit"];
  const groups = order
    .map((id) => ({ id, actions: GROUP_ACTIONS[id].filter((a) => rights[a]) }))
    .filter((g) => g.actions.length > 0);
  // One filled button: the first one of the first group, or Edit on a draft (its first job is publishing).
  const primary: ActionId | null = draft ? (rights.edit ? "edit" : null) : (groups[0]?.actions[0] ?? null);
  return { statusFirst: draft, groups, primary };
}
