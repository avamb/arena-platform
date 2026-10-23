/**
 * track.ts - the page statistics beacon (task 2-3, HP-2a):
 *
 *   POST /v1/public/pages/{slug}/{event_slug}/track
 *   { "kind": "page_view" | "video_play" | "partner_click",
 *     "utm_source": "...", "utm_campaign": "..." }
 *
 * What it sends and what it does not. The body holds the kind and the two
 * utm values of the page address, nothing else: no cookie is read or set, no
 * identifier is stored in the browser, the request is sent without
 * credentials. The server stores a counter per event, day, kind and utm pair,
 * never the visitor's address or browser. A failed or blocked beacon is
 * silent: counting must never be able to break the page.
 *
 * `trackPageView` has ONE call site, in main.ts, after the event page has
 * rendered. `trackVideoPlay` and `trackPartnerClick` are the hooks for
 * render.ts (the video block and the partner links); they take the same
 * context, so the call site is one line each.
 */

export interface TrackContext {
  /** API origin, '' for same-origin (like the other clients of this app). */
  apiBase: string;
  /** The first path segment: an organization's or a promoter's slug. */
  slug: string;
  eventSlug: string;
  /** window.location.search: the utm values are read from it. */
  search: string;
}

export type TrackKind = 'page_view' | 'video_play' | 'partner_click';

const MAX_UTM = 64;

/** utm_source / utm_campaign of the address, trimmed and cut; '' when absent. */
export function utmFromSearch(search: string): { utm_source?: string; utm_campaign?: string } {
  const q = new URLSearchParams(search);
  const out: { utm_source?: string; utm_campaign?: string } = {};
  const source = (q.get('utm_source') ?? '').trim().slice(0, MAX_UTM);
  const campaign = (q.get('utm_campaign') ?? '').trim().slice(0, MAX_UTM);
  if (source !== '') out.utm_source = source;
  if (campaign !== '') out.utm_campaign = campaign;
  return out;
}

/** The request body for one hit. */
export function trackBody(kind: TrackKind, search: string): string {
  return JSON.stringify({ kind, ...utmFromSearch(search) });
}

/** The beacon URL. Slugs are encoded, so a slug can never change the path. */
export function trackURL(ctx: Pick<TrackContext, 'apiBase' | 'slug' | 'eventSlug'>): string {
  const base = ctx.apiBase.replace(/\/+$/, '');
  return `${base}/v1/public/pages/${encodeURIComponent(ctx.slug)}/${encodeURIComponent(ctx.eventSlug)}/track`;
}

/**
 * Sends one hit. `keepalive` lets it finish while the visitor leaves the page
 * (a partner link opens another site); the answer is never read.
 */
export function sendTrack(ctx: TrackContext, kind: TrackKind, fetchImpl: typeof fetch = fetch): void {
  try {
    void fetchImpl(trackURL(ctx), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: trackBody(kind, ctx.search),
      keepalive: true,
      credentials: 'omit',
      mode: 'cors',
    }).catch(() => {
      /* counting is best effort */
    });
  } catch {
    /* an old browser without fetch, a blocked request: nothing to do */
  }
}

/** The event page was shown. Called once from main.ts. */
export function trackPageView(ctx: TrackContext): void {
  sendTrack(ctx, 'page_view');
}

/** A video of the page was started. Hook for render.ts. */
export function trackVideoPlay(ctx: TrackContext): void {
  sendTrack(ctx, 'video_play');
}

/** A "where else to buy" link was followed. Hook for render.ts. */
export function trackPartnerClick(ctx: TrackContext): void {
  sendTrack(ctx, 'partner_click');
}
