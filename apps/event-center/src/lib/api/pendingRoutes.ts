/**
 * Routes the event center calls that are NOT in openapi.yaml yet (another
 * session adds them, branch plan-screens-bot). Same shape as `API` in
 * routes.ts, but kept apart, so the parity test (every web call is in
 * openapi.yaml) stays honest: a route here is a promise, not a fact.
 *
 * When a route appears in openapi.yaml, `pendingRoutes.test.ts` fails. Then
 * move the line to routes.ts and add the route to a row of the parity matrix
 * (src/parity/matrix.ts), and delete it from here.
 *
 * The shapes the web assumes are in src/lib/venues/directoryApi.ts. Until the
 * server has the routes, the web falls back to the existing calls.
 */
import type { ApiRoute } from "@/lib/api/routes";

export const API_PENDING = {
  /** All countries of the world (ISO 3166) with their region; public, like the other geo reads. */
  geoWorldCountries: { method: "GET", path: "/v1/geo/world-countries" },
  /** Countries, cities and venues of the organization's scope with counters (`venue.read`). */
  venueDirectory: { method: "GET", path: "/v1/organizations/{org_id}/venue-directory" },
} as const satisfies Record<string, ApiRoute>;

export type PendingRouteName = keyof typeof API_PENDING;
