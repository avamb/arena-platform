/**
 * funnel.ts — the buyer funnel beacon (spec 50, stage A7):
 *
 *   POST /v1/public/funnel
 *   { "feed_token": "...", "event_id": "uuid", "session_id": "uuid",
 *     "step": "view" | "session_open" | "seats_chosen",
 *     "surface": "widget" | "hosted_page" | "tickets_page",
 *     "utm_source": "...", "utm_campaign": "..." }
 *
 * What it sends and what it does not. The body names the event, the step, the
 * surface and the two utm values of the sales touch; nothing about the visitor.
 * No cookie is read or set, nothing is kept on the device (no browser
 * storage of any kind), the request is sent without credentials, and the server
 * stores a counter per event and day, never an address or a browser. A failed
 * or blocked beacon is silent: counting must never be able to break a sale.
 *
 * The widget sits in three places and counts a step once per place:
 *   - on a seller's own site        surface "widget" (the default)
 *   - inside the hosted event page  surface "hosted_page": the PAGE already
 *     counted the view, so the widget sends session_open and seats_chosen only
 *   - inside a promoter-page card   surface "tickets_page": the widget is the
 *     only thing that knows the event was opened, so it sends all three
 * The host sets this with the `funnel` attribute of <arena-tickets>.
 */

export type FunnelStep = 'view' | 'session_open' | 'seats_chosen';
export type FunnelSurface = 'widget' | 'hosted_page' | 'tickets_page';

const SURFACES: readonly FunnelSurface[] = ['widget', 'hosted_page', 'tickets_page'];

/** The surface named by the `funnel` attribute; anything else is a plain embed. */
export function parseFunnelSurface(raw: string | null | undefined): FunnelSurface {
  const v = (raw ?? '').trim().toLowerCase();
  return (SURFACES as readonly string[]).includes(v) ? (v as FunnelSurface) : 'widget';
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
  surface: FunnelSurface;
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
  opts: Pick<FunnelOptions, 'feedToken' | 'surface'>,
  step: FunnelStep,
  eventId: string,
  sessionId: string | undefined,
  touch: FunnelTouch | null,
): string {
  const body: Record<string, string> = {
    feed_token: opts.feedToken,
    event_id: eventId,
    step,
    surface: opts.surface,
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
      // The hosted page counts the view itself.
      if (step === 'view' && opts.surface === 'hosted_page') return false;
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
