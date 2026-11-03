/**
 * funnel.ts — the buyer funnel beacon (spec 50, stage A7):
 *
 *   POST /v1/public/funnel
 *   { "feed_token": "...", "event_id": "uuid", "session_id": "uuid",
 *     "step": "view" | "session_open" | "seats_chosen",
 *     "surface": "widget",
 *     "utm_source": "...", "utm_campaign": "..." }
 *
 * What it sends and what it does not. The body names the event, the step, the
 * surface and the two utm values of the sales touch; nothing about the visitor.
 * No cookie is read or set, nothing is kept on the device (no browser
 * storage of any kind), the request is sent without credentials, and the server
 * stores a counter per event and day, never an address or a browser. A failed
 * or blocked beacon is silent: counting must never be able to break a sale.
 *
 * The widget always files its steps under the surface "widget" (the API ties a
 * feed token to that surface). Arena's own pages count their own part:
 *   - the hosted event page sends the `view` itself (surface "hosted_page"),
 *   - a promoter-page card sends it when the card scrolls into view (surface
 *     "tickets_page"), because the page mounts a widget per card and a widget
 *     that merely loaded has not been looked at.
 * The page says so with the `funnel` attribute of <arena-tickets>, and the
 * widget then leaves the `view` to the page. A session counts as opened only
 * on a buyer action, never on load.
 */

export type FunnelStep = 'view' | 'session_open' | 'seats_chosen';
/**
 * True when the `funnel` attribute says an Arena page around the widget counts
 * the event's view itself (`hosted_page`, `tickets_page`); anything else is a
 * plain embed on a seller's own site, which counts its own.
 */
export function parseFunnelHost(raw: string | null | undefined): boolean {
  const v = (raw ?? '').trim().toLowerCase();
  return v === 'hosted_page' || v === 'tickets_page';
}

/** The sales touch the beacon carries: the two utm values, both optional. */
export interface FunnelTouch {
  utm_source?: string;
  utm_campaign?: string;
}

export interface FunnelOptions {
  /** API origin, '' for same-origin. */
  apiBase: string;
  feedToken: string;
  /** The page around the widget counts the view, so the widget does not. */
  viewCountedByHost: boolean;
  /** The sales touch of this visit (the widget's stored attribution), or null. */
  touch: () => FunnelTouch | null;
  fetchImpl?: typeof fetch;
}

export interface Funnel {
  /**
   * Counts a step once per (step, event, session) for the life of this widget.
   * Returns true when a beacon was sent.
   */
  track(step: FunnelStep, eventId: string, sessionId?: string): boolean;
}

/** The beacon URL. */
export function funnelURL(apiBase: string): string {
  return `${apiBase.replace(/\/+$/, '')}/v1/public/funnel`;
}

/** The request body for one step. Empty utm values are left out. */
export function funnelBody(
  opts: Pick<FunnelOptions, 'feedToken'>,
  step: FunnelStep,
  eventId: string,
  sessionId: string | undefined,
  touch: FunnelTouch | null,
): string {
  const body: Record<string, string> = {
    feed_token: opts.feedToken,
    event_id: eventId,
    step,
    surface: 'widget',
  };
  if (sessionId) body.session_id = sessionId;
  if (touch?.utm_source) body.utm_source = touch.utm_source;
  if (touch?.utm_campaign) body.utm_campaign = touch.utm_campaign;
  return JSON.stringify(body);
}

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Builds the beacon sender of one widget instance. */
export function createFunnel(opts: FunnelOptions): Funnel {
  const sent = new Set<string>();
  return {
    track(step, eventId, sessionId) {
      // The synthetic single-session event of a session-only embed has no real
      // event id; a beacon for it would only be refused.
      if (!opts.feedToken || !UUID_RE.test(eventId)) return false;
      // The page around the widget counts the view itself.
      if (step === 'view' && opts.viewCountedByHost) return false;
      const key = `${step}|${eventId}|${sessionId ?? ''}`;
      if (sent.has(key)) return false;
      sent.add(key);
      try {
        const f = opts.fetchImpl ?? fetch;
        void f(funnelURL(opts.apiBase), {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: funnelBody(opts, step, eventId, sessionId, opts.touch()),
          keepalive: true,
          credentials: 'omit',
          mode: 'cors',
        }).catch(() => {
          /* counting is best effort */
        });
      } catch {
        /* an old browser without fetch, a blocked request: nothing to do */
      }
      return true;
    },
  };
}
