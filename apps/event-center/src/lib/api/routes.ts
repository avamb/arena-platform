/**
 * Every arena-api route the event center calls, in ONE table.
 *
 * The client builds URLs only from this table (`buildPath`), and
 * `routes.test.ts` checks that every entry exists in openapi.yaml with the
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
  orders: { method: "GET", path: "/v1/organizations/{org_id}/orders" },
  order: { method: "GET", path: "/v1/organizations/{org_id}/orders/{id}" },
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
