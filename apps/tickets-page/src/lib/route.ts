/**
 * route.ts — parses the hosted page's own path.
 *
 *   /{org_slug}              -> promoter landing page (lists every event)
 *   /{org_slug}/{event_slug} -> single event page
 *
 * Deliberately does NOT touch `location.search` or `location.hash`: the
 * widget resumes an in-progress order from a `checkout_token` query
 * parameter on return from payment (see `getCheckoutTokenFromSearch` in
 * apps/widget/src/lib/store.ts), so the page must never strip or rewrite
 * the query string while routing.
 */

export interface EventRoute {
  kind: 'event';
  orgSlug: string;
  eventSlug: string;
}

export interface PromoterRoute {
  kind: 'promoter';
  orgSlug: string;
}

export type HostedPageRoute = EventRoute | PromoterRoute;

/**
 * parsePath extracts the route from a pathname. Tolerates a trailing slash
 * and a leading/trailing run of extra slashes. A single segment resolves to
 * the promoter page, two segments to the event page. Returns null for "/",
 * an empty path, or a path with more than two segments — all of which
 * render the branded "not found" page instead of attempting a resolve
 * call. The org_slug (and event_slug) are returned exactly as they appear
 * in the URL — case is never touched here; the API client lower-cases
 * nothing either, since the backend resolves both case-insensitively.
 */
export function parsePath(pathname: string): HostedPageRoute | null {
  const trimmed = pathname.replace(/^\/+/, '').replace(/\/+$/, '');
  if (trimmed === '') return null;

  const segments = trimmed.split('/').filter((s) => s.length > 0);
  if (segments.length < 1 || segments.length > 2) return null;

  let decoded: string[];
  try {
    decoded = segments.map((s) => decodeURIComponent(s));
  } catch {
    return null;
  }
  if (decoded.some((s) => s === '')) return null;

  if (decoded.length === 1) {
    return { kind: 'promoter', orgSlug: decoded[0] };
  }
  return { kind: 'event', orgSlug: decoded[0], eventSlug: decoded[1] };
}
