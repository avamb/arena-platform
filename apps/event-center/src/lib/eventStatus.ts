/**
 * Pure rules of the "Status" block of the event card (task 5-1, the twin of
 * the bot's `eventStatusRows` in eventbot/evstatus.go).
 *
 * The lifecycle is the server's: `EVENT_TRANSITIONS` is a copy of
 * `ValidEventTransitions` (domain/catalog/event_state.go), and
 * `eventStatus.test.ts` READS that Go file, so a transition added or removed on
 * the server fails the web build until this table follows. The API still decides
 * every press (422 `event.invalid_transition`).
 */
import type { EventDeleteImpact } from "@/lib/api/types";

export const EVENT_STATUSES = ["draft", "published", "cancelled", "archived"] as const;
export type EventStatus = (typeof EVENT_STATUSES)[number];

export const EVENT_TRANSITIONS: Record<EventStatus, readonly EventStatus[]> = {
  draft: ["published", "cancelled"],
  published: ["draft", "cancelled", "archived"],
  cancelled: ["archived"],
  archived: [],
};

export function isEventStatus(value: string): value is EventStatus {
  return (EVENT_STATUSES as readonly string[]).includes(value);
}

/** True when the lifecycle allows the move (the server's `IsValidEventTransition`). */
export function canMoveEvent(from: string, to: EventStatus): boolean {
  return isEventStatus(from) && EVENT_TRANSITIONS[from].includes(to);
}

/** A button of the block. "cancel" is not offered: the bot does not either. */
export type StatusAction = "publish" | "off" | "archive" | "delete";

export interface StatusRights {
  /** `event.publish`: publish, take off sale, archive. */
  canPublish: boolean;
  /** `event.delete`. */
  canDelete: boolean;
}

/** The buttons for the current status and the person's rights, in screen order. */
export function statusActions(status: string, rights: StatusRights): StatusAction[] {
  const out: StatusAction[] = [];
  if (rights.canPublish) {
    if (canMoveEvent(status, "published")) out.push("publish");
    if (canMoveEvent(status, "draft")) out.push("off");
    if (canMoveEvent(status, "archived")) out.push("archive");
  }
  if (rights.canDelete) out.push("delete");
  return out;
}

/** The status each moving button sends the event to. */
export const ACTION_TARGET: Record<Exclude<StatusAction, "delete">, EventStatus> = {
  publish: "published",
  off: "draft",
  archive: "archived",
};

/** What the delete dry run means for the screen. */
export type DeleteVerdict = "allowed" | "blocked";

export function deleteVerdict(impact: Pick<EventDeleteImpact, "can_delete" | "blocked">): DeleteVerdict {
  return impact.can_delete && impact.blocked === "" ? "allowed" : "blocked";
}

/** The archive is offered next to a refused delete only when the lifecycle allows it. */
export function offerArchiveInstead(impact: Pick<EventDeleteImpact, "can_delete" | "blocked" | "can_archive">): boolean {
  return deleteVerdict(impact) === "blocked" && impact.can_archive;
}
