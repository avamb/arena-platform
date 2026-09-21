/**
 * api.ts — clients for the two hosted-page resolve endpoints:
 *
 *   GET /v1/public/pages/{org_slug}/{event_slug} — one event
 *   GET /v1/public/pages/{org_slug}               — promoter landing page
 *
 * Mirrors the response shapes documented in
 * apps/backend/openapi/openapi.yaml (HostedPageResponse /
 * HostedPromoterPageResponse).
 */

export interface HostedPageOrg {
  slug: string;
  name: string;
  logo_url: string | null;
}

export interface HostedPageEvent {
  id: string;
  slug: string;
  title: string;
  description: string | null;
  short_description: string | null;
  image_url: string | null;
  poster_url: string | null;
  age_rating: string | null;
  venue_names: string[];
  first_session_at: string | null;
  last_session_at: string | null;
  /** IANA time zone name of the venue of the event's earliest session, or
   * null when unavailable — lets the page show the event's own local time
   * instead of the viewer's. */
  first_session_timezone: string | null;
  /** Feed token the event is published through. Present only on the
   * promoter page's items, where every date renders its own ticket picker
   * and a picker cannot be mounted without one; the single-event response
   * keeps its token on the envelope instead. */
  feed_token?: string;
}

export interface HostedPageResponse {
  org: HostedPageOrg;
  event: HostedPageEvent;
  feed_token: string;
  default_locale: string;
}

export interface HostedPromoterPageResponse {
  org: HostedPageOrg;
  default_locale: string;
  events: HostedPageEvent[];
}

/** ApiError carries the HTTP status so callers can distinguish "not found"
 * (render the branded not-found page) from any other failure (render a
 * retryable error state). */
export class ApiError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

export async function fetchHostedPage(
  apiBase: string,
  orgSlug: string,
  eventSlug: string,
  signal?: AbortSignal,
): Promise<HostedPageResponse> {
  const base = apiBase.replace(/\/$/, '');
  const url = `${base}/v1/public/pages/${encodeURIComponent(orgSlug)}/${encodeURIComponent(eventSlug)}`;
  let res: Response;
  try {
    res = await fetch(url, { signal, headers: { Accept: 'application/json' } });
  } catch (err) {
    throw new ApiError(0, err instanceof Error ? err.message : 'network error');
  }
  if (!res.ok) {
    throw new ApiError(res.status, `request failed with status ${res.status}`);
  }
  return (await res.json()) as HostedPageResponse;
}

export async function fetchPromoterPage(
  apiBase: string,
  orgSlug: string,
  signal?: AbortSignal,
): Promise<HostedPromoterPageResponse> {
  const base = apiBase.replace(/\/$/, '');
  const url = `${base}/v1/public/pages/${encodeURIComponent(orgSlug)}`;
  let res: Response;
  try {
    res = await fetch(url, { signal, headers: { Accept: 'application/json' } });
  } catch (err) {
    throw new ApiError(0, err instanceof Error ? err.message : 'network error');
  }
  if (!res.ok) {
    throw new ApiError(res.status, `request failed with status ${res.status}`);
  }
  return (await res.json()) as HostedPromoterPageResponse;
}
