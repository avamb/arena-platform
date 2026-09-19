/**
 * route.ts — parses the hosted page's own path, `/{org_slug}/{event_slug}`.
 *
 * Deliberately does NOT touch `location.search` or `location.hash`: the
 * widget resumes an in-progress order from a `checkout_token` query
 * parameter on return from payment (see `getCheckoutTokenFromSearch` in
 * apps/widget/src/lib/store.ts), so the page must never strip or rewrite
 * the query string while routing.
 */

export interface HostedPageRoute {
  orgSlug: string;
  eventSlug: string;
}

/**
 * parsePath extracts {org_slug, event_slug} from a pathname. Tolerates a
 * trailing slash and a leading/trailing run of extra slashes. Returns null
 * for "/", an empty path, a single-segment path, or a path with more than
 * two segments — all of which render the branded "not found" page instead
 * of attempting a resolve call.
 */
export function parsePath(pathname: string): HostedPageRoute | null {
  const trimmed = pathname.replace(/^\/+/, '').replace(/\/+$/, '');
  if (trimmed === '') return null;

  const segments = trimmed.split('/').filter((s) => s.length > 0);
  if (segments.length !== 2) return null;

  const [orgSlugRaw, eventSlugRaw] = segments;
  let orgSlug: string;
  let eventSlug: string;
  try {
    orgSlug = decodeURIComponent(orgSlugRaw);
    eventSlug = decodeURIComponent(eventSlugRaw);
  } catch {
    return null;
  }
  if (orgSlug === '' || eventSlug === '') return null;

  return { orgSlug, eventSlug };
}
