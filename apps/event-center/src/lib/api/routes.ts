/**
 * Every arena-api route the event center calls, in ONE table.
 *
 * The client builds URLs only from this table (`buildPath`), and
 * `src/parity/parity.test.ts` checks that every entry exists in openapi.yaml with the
 * same method. A new web call therefore needs a line here first, and a
 * route that the API does not document fails the test.
 *
 * Only organization-scoped routes and the user's own auth routes belong
 * here. Superadmin routes (/v1/admin/*) never do: the event center is for
 * organizers, admin-web is the superadmin console.
 */

export type HttpMethod = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

export interface ApiRoute {
  readonly method: HttpMethod;
  /** OpenAPI path template, for example /v1/organizations/{org_id}/events. */
  readonly path: string;
}

export const API = {
  login: { method: "POST", path: "/v1/auth/login" },
  refresh: { method: "POST", path: "/v1/auth/refresh" },
  logout: { method: "POST", path: "/v1/auth/logout" },
  passwordResetRequest: { method: "POST", path: "/v1/auth/password-reset/request" },
  // The password again before a money action (spec 37 §4.6).
  stepUp: { method: "POST", path: "/v1/auth/step-up" },
  // The bot's "Open in browser" link (spec 37 §4.3).
  webLink: { method: "POST", path: "/v1/auth/web/link" },
  passwordResetConfirm: { method: "POST", path: "/v1/auth/password-reset/confirm" },
  me: { method: "GET", path: "/v1/me" },
  orgMe: { method: "GET", path: "/v1/organizations/{org_id}/me" },
  events: { method: "GET", path: "/v1/organizations/{org_id}/events" },
  eventSessions: { method: "GET", path: "/v1/organizations/{org_id}/events/{event_id}/sessions" },
  sessionTiers: {
    method: "GET",
    path: "/v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers",
  },
  eventSummary: { method: "GET", path: "/v1/organizations/{org_id}/events/{event_id}/summary" },
  sessionSummary: { method: "GET", path: "/v1/organizations/{org_id}/sessions/{session_id}/summary" },
  eventSalesCsv: { method: "GET", path: "/v1/organizations/{org_id}/events/{event_id}/sales.csv" },
  sessionSalesCsv: { method: "GET", path: "/v1/organizations/{org_id}/sessions/{session_id}/sales.csv" },
  sessionSummaryCsv: {
    method: "GET",
    path: "/v1/organizations/{org_id}/sessions/{session_id}/summary.csv",
  },
  sampleTicket: { method: "GET", path: "/v1/organizations/{org_id}/sessions/{session_id}/sample-ticket" },
  // The search travels in the body, never in the URL: a buyer's e-mail or
  // phone in a query string is written by every proxy log (spec 37 §13).
  orderSearch: { method: "POST", path: "/v1/organizations/{org_id}/orders/search" },
  order: { method: "GET", path: "/v1/organizations/{org_id}/orders/{id}" },
  // 4-6
  promoters: { method: "GET", path: "/v1/organizations/{org_id}/promoters" },
  promoterCreate: { method: "POST", path: "/v1/organizations/{org_id}/promoters" },
  promoterUpdate: { method: "PATCH", path: "/v1/organizations/{org_id}/promoters/{id}" },
  // 4-3: promo codes.
  promoCodes: { method: "GET", path: "/v1/organizations/{org_id}/promo-codes" },
  promoCode: { method: "GET", path: "/v1/organizations/{org_id}/promo-codes/{id}" },
  promoCodeCreate: { method: "POST", path: "/v1/organizations/{org_id}/promo-codes" },
  promoCodeUpdate: { method: "PATCH", path: "/v1/organizations/{org_id}/promo-codes/{id}" },
  promoCodeDelete: { method: "DELETE", path: "/v1/organizations/{org_id}/promo-codes/{id}" },
  promoRedemptions: { method: "GET", path: "/v1/organizations/{org_id}/promo-code-redemptions" },
  promoRedemptionsCsv: {
    method: "GET",
    path: "/v1/organizations/{org_id}/promo-codes/{promo_code_id}/redemptions.csv",
  },
  // 4-1: the ticket letter of a paid order again, to its own or a one-time address.
  resendTickets: { method: "POST", path: "/v1/organizations/{org_id}/orders/{order_id}/resend-tickets" },
  // 4-2: cancel an unpaid order. Asks for the password again (spec 37 4.6).
  cancelOrder: { method: "POST", path: "/v1/organizations/{org_id}/orders/{id}/cancel" },
  // 8-1: refund tickets of an order. The create asks for the password again (spec 37 4.6).
  refundOptions: { method: "GET", path: "/v1/organizations/{org_id}/orders/{order_id}/refund-options" },
  refundCreate: { method: "POST", path: "/v1/organizations/{org_id}/orders/{order_id}/refunds" },
  // 4-7: the owner's "Organization data".
  orgDetail: { method: "GET", path: "/v1/organizations/{id}" },
  orgUpdate: { method: "PATCH", path: "/v1/organizations/{id}" },
  sellerProfile: { method: "GET", path: "/v1/organizations/{org_id}/seller-profile" },
  sellerProfileUpdate: { method: "PATCH", path: "/v1/organizations/{org_id}/seller-profile" },
  sellerDisplayUpdate: { method: "PATCH", path: "/v1/organizations/{org_id}/channels/{channel_id}/seller-display" },
  paymentConfigs: { method: "GET", path: "/v1/organizations/{org_id}/payment-configs" },
  // 4-4: invitations (free tickets for named guests).
  complimentaryList: { method: "GET", path: "/v1/organizations/{org_id}/complimentary" },
  complimentaryIssue: { method: "POST", path: "/v1/organizations/{org_id}/complimentary" },
  // A flat route: the row's own organization is checked on the server.
  complimentaryRevoke: { method: "POST", path: "/v1/complimentary/{id}/revoke" },
  // 4-5: the team. The invitation has two channels (web, telegram); removing a
  // member names the ONE role to revoke in the body (AGENTS.md).
  botTeam: { method: "GET", path: "/v1/organizations/{org_id}/bot-team" },
  inviteMember: { method: "POST", path: "/v1/organizations/{org_id}/bot-invitations" },
  revokeInvitation: { method: "DELETE", path: "/v1/organizations/{org_id}/bot-invitations/{id}" },
  resendInvitation: { method: "POST", path: "/v1/organizations/{org_id}/bot-invitations/{id}/resend" },
  removeMember: { method: "DELETE", path: "/v1/organizations/{org_id}/members/{user_id}" },
  // 5-1: publish, take off sale, archive and delete an event.
  eventStatus: { method: "POST", path: "/v1/organizations/{org_id}/events/{id}/status" },
  eventDeleteImpact: { method: "GET", path: "/v1/organizations/{org_id}/events/{event_id}/delete-impact" },
  eventDelete: { method: "DELETE", path: "/v1/organizations/{org_id}/events/{id}" },
  // 7-1: the poster. The picture is uploaded once (multipart), then the event
  // points at it with ONE PATCH of `poster_media_id`; GET gives a short-lived
  // signed address of the bytes.
  mediaUpload: { method: "POST", path: "/v1/media" },
  mediaGet: { method: "GET", path: "/v1/media/{id}" },
  // Poster-first creation: reads the uploaded poster with the vision model on the
  // server (the model key never reaches the browser). Costs a daily slot of the organization.
  posterFacts: { method: "POST", path: "/v1/organizations/{org_id}/media/{media_id}/poster-facts" },
  eventUpdate: { method: "PATCH", path: "/v1/organizations/{org_id}/events/{id}" },
  // 5-3: the sales end and doors-open time of a session
  sessionUpdate: { method: "PATCH", path: "/v1/organizations/{org_id}/events/{event_id}/sessions/{id}" },
  // 5-4: a date's categories. PATCH changes ONE thing of a category (open,
  // base price, sale window, quantity). The price schedule is read and put
  // back whole when the window in force now is the price being changed.
  tierUpdate: {
    method: "PATCH",
    path: "/v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}",
  },
  tierSchedule: {
    method: "GET",
    path: "/v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}/price-schedule",
  },
  tierSchedulePut: {
    method: "PUT",
    path: "/v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}/price-schedule",
  },
  // 6-1a: the event wizard. The check (?dry_run=true) writes nothing; the
  // geo reads are public and feed the venue form.
  eventBundleCheck: { method: "POST", path: "/v1/organizations/{org_id}/imports/event-bundle" },
  venues: { method: "GET", path: "/v1/organizations/{org_id}/venues" },
  geoCountries: { method: "GET", path: "/v1/geo/countries" },
  geoCities: { method: "GET", path: "/v1/geo/cities" },
  geoTimezones: { method: "GET", path: "/v1/geo/countries/{iso2}/timezones" },
  geoCurrency: { method: "GET", path: "/v1/geo/countries/{iso2}/currency" },
  // 5-2: move or cancel a session. The dry run first, then ONE PATCH of the
  // existing session (a cancellation is status "cancelled", never a delete).
  sessionChangeImpact: { method: "GET", path: "/v1/organizations/{org_id}/sessions/{session_id}/change-impact" },
  eventContact: { method: "PUT", path: "/v1/organizations/{org_id}/events/{event_id}/contact" },
} as const satisfies Record<string, ApiRoute>;

export type ApiRouteName = keyof typeof API;

/**
 * Fills a path template. Every `{name}` must have a value, and every value
 * is URL-encoded, so an id can never change the shape of the path.
 */
export function buildPath(
  route: ApiRoute,
  params: Record<string, string> = {},
  query: Record<string, string | number | undefined> = {},
): string {
  const path = route.path.replace(/\{([a-z0-9_]+)\}/g, (_match, name: string) => {
    const value = params[name];
    if (value === undefined || value.length === 0) {
      throw new Error(`buildPath: missing value for {${name}} in ${route.path}`);
    }
    return encodeURIComponent(value);
  });
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === "") continue;
    search.set(key, String(value));
  }
  const qs = search.toString();
  return qs.length > 0 ? `${path}?${qs}` : path;
}
