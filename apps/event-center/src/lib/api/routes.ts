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
  // 4-1: the ticket letter of a paid order again, to its own or a one-time address.
  resendTickets: { method: "POST", path: "/v1/organizations/{org_id}/orders/{order_id}/resend-tickets" },
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
  const path = route.path.replace(/\{([a-z_]+)\}/g, (_match, name: string) => {
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
