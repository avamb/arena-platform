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
  /** Number of active, non-cancelled sessions. A clock time is printed
   * next to the date only when this is exactly 1 — with several sessions
   * the buyer picks one (and its time) in the selector below, and the
   * first session's time in a heading misled buyers into thinking the
   * event had one start. Absent on an older backend, which means "one". */
  session_count?: number;
  /** When the venue opens its doors for the first session (RFC 3339,
   * migration 0128), null when the organizer gave none. Printed only for an
   * event with one session, like its clock time. Absent on an older backend. */
  first_session_doors_at?: string | null;
  /** Feed token the event is published through. Present only on the
   * promoter page's items, where every date renders its own ticket picker
   * and a picker cannot be mounted without one; the single-event response
   * keeps its token on the envelope instead. */
  feed_token?: string;
}

/** Postal address of the seller. Every part is optional. */
export interface HostedPageSellerAddress {
  line1?: string | null;
  line2?: string | null;
  city?: string | null;
  postal_code?: string | null;
  country?: string | null;
}

/** Public contact details of the seller. Both parts are optional. */
export interface HostedPageSellerContact {
  email?: string | null;
  phone?: string | null;
}

/** Who the buyer's contract is with. Present on a hosted-page response only
 * when the sales channel has the legal-details switch on; an older backend
 * (or a channel without the switch) simply omits it and the page renders
 * exactly as before. */
export interface HostedPageSeller {
  legal_name: string;
  tax_id?: string | null;
  /** Kind of tax number (`eu_vat`, `gb_vat`, `il_vat`, `us_ein`, `other`,
   * ...) — picks the label printed next to `tax_id`; unknown means "Tax ID". */
  tax_scheme?: string | null;
  registration_number?: string | null;
  address?: HostedPageSellerAddress | null;
  country?: string | null;
  public_contact?: HostedPageSellerContact | null;
}

/** The event's promoter, shown next to the seller (event page only). */
export interface HostedPagePromoter {
  name: string;
}

export interface HostedPageResponse {
  org: HostedPageOrg;
  event: HostedPageEvent;
  feed_token: string;
  default_locale: string;
  seller?: HostedPageSeller | null;
  promoter?: HostedPagePromoter | null;
}

export interface HostedPromoterPageResponse {
  org: HostedPageOrg;
  default_locale: string;
  events: HostedPageEvent[];
  seller?: HostedPageSeller | null;
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
