/**
 * Pure rules of the organization-wide invitations screen on the staff-invitation
 * routes (spec 46, INV-04b): the cache keys of its list, the body of a general
 * admission issue for one guest, one request id per guest that survives a retry,
 * and what a row of the organization list says about its session.
 *
 * The seat-map screen of ONE date is `staffInvitations.ts`; the guest list the
 * person pastes is `invitations.ts`. vitest runs without a DOM, so every rule a
 * click would exercise lives here and is tested in orgInvitations.test.ts.
 * Nothing here logs: a guest's contact details pass through.
 */
import type { OrgInvitationState } from "@/lib/api/endpoints";
import type { StaffInvitationIssueRequest, StaffInvitationOrgListItem } from "@/lib/api/types";
import type { Guest } from "@/lib/invitations";
import { STAFF_INVITATIONS_KEY, buildIssueBody, emptyForm, newRequestId, withRequestId } from "@/lib/staffInvitations";

/** Rows per page of the list; the server's own maximum is 200. */
export const ORG_PAGE_SIZE = 20;

/** The tabs of the list: what the server calls `state`. `active` is its default. */
export const ORG_STATES: readonly OrgInvitationState[] = ["active", "cancelled", "all"];

export interface OrgFilters {
  state: OrgInvitationState;
  /** "" = every event. */
  eventId: string;
  /** "" = every date of the event. */
  sessionId: string;
}

export const DEFAULT_FILTERS: OrgFilters = { state: "active", eventId: "", sessionId: "" };

/**
 * The React Query key of the organization list. It starts with the key of the
 * session list (`[STAFF_INVITATIONS_KEY, orgId]`), so one invalidation of that
 * prefix refreshes both, and the third element "org" can never be a session id.
 */
export function orgListKey(orgId: string, f: OrgFilters): readonly unknown[] {
  return [STAFF_INVITATIONS_KEY, orgId, "org", f.state, f.eventId, f.sessionId];
}

/** A filter on a date needs its event: choosing another event drops the date. */
export function withEvent(f: OrgFilters, eventId: string): OrgFilters {
  return { ...f, eventId, sessionId: "" };
}

/** The query the server takes for these filters (empty ones are left out). */
export function filtersToQuery(f: OrgFilters): { state: OrgInvitationState; eventId?: string; sessionId?: string } {
  return {
    state: f.state,
    eventId: f.eventId === "" ? undefined : f.eventId,
    sessionId: f.sessionId === "" ? undefined : f.sessionId,
  };
}

/**
 * The name printed on the ticket. The API requires one, so a guest typed as an
 * e-mail only is printed under the address (the bot does the same).
 */
export function guestNameFor(g: Guest): string {
  const name = g.name.trim();
  return name !== "" ? name : g.email.trim();
}

/** The body of ONE guest's issue: one place of the category, the letter to the guest's e-mail. */
export function gaIssueBody(g: Guest, tierId: string, locale: string): Omit<StaffInvitationIssueRequest, "request_id"> {
  const form = { ...emptyForm(locale), name: guestNameFor(g), email: g.email.trim(), delivery: "guest_email" as const };
  return buildIssueBody(form, [], [{ tier_id: tierId, qty: 1 }]);
}

/** The request of guest `index` of an operation, with its VIP mark. */
export function gaIssueRequest(
  g: Guest,
  tierId: string,
  locale: string,
  requestId: string,
  vip: boolean,
): StaffInvitationIssueRequest {
  const req = withRequestId(gaIssueBody(g, tierId, locale), requestId);
  return vip ? { ...req, vip: true } : req;
}

/**
 * One request id per guest of an operation. The ids are minted when the
 * confirmation is first shown and kept while the guest list, the category and
 * the VIP mark stay as they are, so a retry (a dropped answer, a double press)
 * reaches the server as the SAME request and the API answers the first order
 * again instead of mailing the guest twice. A refusal by the server (a 4xx)
 * ends that attempt: `renew` gives the guest a new id for the next try.
 */
export class RequestIds {
  private ids: string[] = [];

  constructor(private readonly make: () => string = newRequestId) {}

  /** The id of guest `index`, minted on the first ask. */
  at(index: number): string {
    let id = this.ids[index];
    if (id === undefined) {
      id = this.make();
      this.ids[index] = id;
    }
    return id;
  }

  /** The next try of guest `index` is a new request. */
  renew(index: number): void {
    delete this.ids[index];
  }

  /** Everything changed: all guests get new ids. */
  reset(): void {
    this.ids = [];
  }

  get size(): number {
    return this.ids.filter((x) => x !== undefined).length;
  }
}

/** True for an invitation that came from a CSV file (spec 46, 3.5). */
export function isFromCsv(item: Pick<StaffInvitationOrgListItem, "issued_via">): boolean {
  return item.issued_via === "staff_bulk";
}

/**
 * The tickets of the loaded pages are still being minted: the list asks again.
 * A page is never dropped, the whole loaded range is read again.
 */
export function allItems(pages: readonly { items: readonly StaffInvitationOrgListItem[] }[] | undefined): StaffInvitationOrgListItem[] {
  return pages === undefined ? [] : pages.flatMap((p) => [...p.items]);
}

/** The venue half of a row's second line: the venue name when the session has one. */
export function venueOf(item: Pick<StaffInvitationOrgListItem, "session">): string {
  return (item.session.venue_name ?? "").trim();
}

/**
 * The organizer's private VIP mark of a place's ticket. `places[].vip` joins the list contract with INV-05,
 * and a server that predates it sends nothing, which reads as "not VIP".
 */
export function placeVip(place: object): boolean {
  return (place as { vip?: unknown }).vip === true;
}
