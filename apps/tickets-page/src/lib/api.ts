/**
 * api.ts — client for GET /v1/public/pages/{org_slug}/{event_slug}, the
 * hosted-page resolve endpoint. Mirrors the response shape documented in
 * apps/backend/openapi/openapi.yaml (HostedPageResponse).
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
}

export interface HostedPageResponse {
  org: HostedPageOrg;
  event: HostedPageEvent;
  feed_token: string;
  default_locale: string;
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
