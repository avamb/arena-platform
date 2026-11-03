/**
 * The screen name analytics reports: the route TEMPLATE, never the concrete
 * address. `/o/$orgId/events/$eventId` becomes `/events/$eventId`: the
 * organization travels as `org_id` (which the server checks), and no id,
 * query string or hash ever reaches an event.
 */

export interface RouteMatchLike {
  routeId: string;
  params?: Record<string, unknown>;
}

const ORG_PREFIX = "/o/$orgId";

/** `/o/$orgId/events/$eventId/` becomes `/events/$eventId`. The root route (not found) is `/not_found`. */
export function screenTemplate(routeId: string): string {
  if (routeId === "__root__" || routeId === "") return "/not_found";
  let id = routeId;
  if (id === ORG_PREFIX || id === `${ORG_PREFIX}/`) return "/";
  if (id.startsWith(`${ORG_PREFIX}/`)) id = id.slice(ORG_PREFIX.length);
  if (id.length > 1 && id.endsWith("/")) id = id.slice(0, -1);
  return id;
}

/** The deepest match is the screen the person sees. */
export function leafMatch<T extends RouteMatchLike>(matches: readonly T[]): T | undefined {
  return matches.length === 0 ? undefined : matches[matches.length - 1];
}

export function screenOf(matches: readonly RouteMatchLike[]): string {
  const m = leafMatch(matches);
  return m === undefined ? "/not_found" : screenTemplate(m.routeId);
}

/** The organization of the screen, when the route has one. */
export function orgIdOf(matches: readonly RouteMatchLike[]): string | undefined {
  const raw = leafMatch(matches)?.params?.orgId;
  return typeof raw === "string" && raw.length > 0 ? raw : undefined;
}
