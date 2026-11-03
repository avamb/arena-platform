/**
 * arena-track: the one browser tracker of the platform's own analytics
 * (spec 50, 4.1). No dependencies, no cookies, no storage of its own.
 *
 * What it reports: page or screen views, engaged time, scroll depth (opt-in),
 * clicks on elements that carry `data-track`, rage clicks, flow steps and
 * errors shown to the person. What it NEVER reports: input values, texts,
 * e-mails or entity ids. That is enforced here, not trusted to callers: an
 * event outside the dictionary is dropped, a property outside its list is
 * dropped, UUIDs and long digit runs in a route are replaced by `:id`.
 *
 * Wire contract (spec 50, 4.2, shared with the collectors, change only together):
 *   POST <endpoint>  { org_id?, aid?, cid?, pv, events: [{ name, at, props }] }
 * The collector decides the identity: the body never names a user.
 *
 * With an empty `endpoint`, or `enabled: false`, `createTracker` returns a
 * tracker that does nothing. No public method ever throws.
 */

export type Surface = "site" | "event_center";
export type PropValue = string | number | boolean;
export type Props = Record<string, PropValue | null | undefined>;

export interface TrackerContext {
  /** The organization of the current screen (event center). The server verifies it. */
  org_id?: string;
  /** Visitor id, only after consent (site). */
  aid?: string;
  /** Contact code of an e-mail link (site, first batch). */
  cid?: string;
}

export interface TrackerOptions {
  /** Collector URL. Empty: the tracker does nothing. */
  endpoint: string;
  surface: Surface;
  /** `false` or a function returning `false` (an opt-out): nothing is recorded or sent. */
  enabled?: boolean | (() => boolean);
  /**
   * Request headers of an authenticated surface (Authorization, X-Client-Channel).
   * Returning null means "not signed in": the batch stays queued. Without this option
   * the surface is anonymous.
   */
  getHeaders?: () => Record<string, string> | null;
  getContext?: () => TrackerContext;
  /**
   * Who is signed in (an authenticated surface). An event is tagged with it when it happens and is
   * dropped, never sent, if somebody else (or nobody) is signed in by the time the batch leaves.
   */
  getIdentity?: () => string | null;
  /** Report scroll depth 25/50/75/100 (the site; off by default). */
  scrollDepth?: boolean;
  /** Queue flush period, default 5000 ms. */
  flushMs?: number;
  /** Test seams. */
  win?: Window;
  now?: () => number;
}

export interface Tracker {
  /** A page (site) or screen (event center) was opened. `name` is a route template, never a concrete path with ids. */
  view(name: string, extra?: Props): void;
  /** Closes the engaged time of the current screen now. Call it before changing what getContext reports. */
  settle(): void;
  /** A dictionary event with its allowed properties; anything else is dropped. */
  track(name: string, props?: Props): void;
  flowStep(flow: string, step: string): void;
  /** An error was shown to the person: where and which code. */
  error(screen: string, code: string): void;
  flush(): void;
  destroy(): void;
}

const NOOP: Tracker = Object.freeze({ view() {}, settle() {}, track() {}, flowStep() {}, error() {}, flush() {}, destroy() {} });

// Dictionary (spec 50, section 5), the part the browser may send. Allowed surfaces and properties.
const S = 1;
const E = 2;
const DICT: Record<string, [number, string[]]> = {
  page_view: [S, ["path", "title", "lang", "ref_host", "utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content"]],
  engaged: [S | E, ["path", "screen", "seconds"]],
  scroll: [S, ["path", "depth"]],
  click: [S | E, ["name", "path", "screen"]],
  consent: [S, ["analytics"]],
  screen_view: [E, ["screen"]],
  flow_step: [E, ["flow", "step"]],
  ui_error: [E, ["screen", "code"]],
  rage_click: [E, ["screen", "name"]],
};

const MAX_STR = 200;
const MAX_QUEUE = 200;
/** Collector limit is 50 events a request; a keepalive request may carry 64 KB, keep well under. */
const MAX_BATCH = 50;
/** Spec 50, 4.2: a body is at most 16 KB. Measured in BYTES, with room to spare. */
const MAX_BODY = 15_000;
/** Browsers share one 64 KB budget between all keepalive requests in flight: stay under it when leaving. */
const MAX_LEAVE_BYTES = 60_000;
const ACTIVE_MS = 30_000;
const RAGE_CLICKS = 3;
const RAGE_MS = 1000;
const UUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/gi;
const EMAIL = /[^\s@/]+@[^\s@/]+\.[^\s@/]+/g;
const LONG_NUM = /\d{6,}/g;

/** A route or path with no query, hash, id or address in it. */
function cleanRoute(s: string): string {
  return s.split(/[?#]/)[0].replace(EMAIL, ":email").replace(UUID, ":id").replace(LONG_NUM, ":id").slice(0, MAX_STR);
}

function cleanValue(key: string, v: unknown): PropValue | undefined {
  if (typeof v === "number") return Number.isFinite(v) ? v : undefined;
  if (typeof v === "boolean") return v;
  if (typeof v !== "string") return undefined;
  if (key === "path" || key === "screen") return cleanRoute(v);
  return v.replace(EMAIL, "").replace(UUID, ":id").slice(0, MAX_STR);
}

function uid(win: Window): string {
  const b = new Uint8Array(12);
  const c = win.crypto ?? (typeof crypto !== "undefined" ? crypto : undefined);
  if (c?.getRandomValues) c.getRandomValues(b);
  else for (let i = 0; i < b.length; i++) b[i] = Math.floor(Math.random() * 256);
  let out = "p_";
  for (const x of b) out += (x % 36).toString(36);
  return out;
}

interface Queued {
  pv: string;
  /** Context (org_id, aid, cid) as it was when the event happened, not when it is sent. */
  ctx: TrackerContext;
  ctxKey: string;
  /** Who was signed in when the event happened (null: nobody), or "" when the surface has no identity. */
  user: string | null;
  ev: { name: string; at: string; props: Record<string, PropValue> };
}

const enc = typeof TextEncoder !== "undefined" ? new TextEncoder() : null;
const bytes = (s: string): number => (enc ? enc.encode(s).length : s.length * 3);

export function createTracker(o: TrackerOptions): Tracker {
  try {
    const win = o.win ?? (typeof window !== "undefined" ? window : undefined);
    const doc = win?.document;
    if (!o.endpoint || o.enabled === false || win === undefined || doc === undefined) return NOOP;
    return build(o, win, doc);
  } catch {
    return NOOP;
  }
}

function build(o: TrackerOptions, win: Window, doc: Document): Tracker {
  const now = o.now ?? Date.now;
  const surfaceBit = o.surface === "site" ? S : E;
  const place = o.surface === "site" ? "path" : "screen";
  const queue: Queued[] = [];
  const undo: Array<() => void> = [];
  let pv = uid(win);
  let screen = "";
  let dead = false;
  // Engaged time: counted between ticks, only while the tab is visible and the last input is younger than 30 s.
  let engagedMs = 0;
  let lastTick: number | null = doc.visibilityState === "hidden" ? null : now();
  let lastActive = now();
  const depthSent = new Set<number>();
  let rage: { el: EventTarget | null; at: number[] } = { el: null, at: [] };

  const on = (t: EventTarget, type: string, fn: (e: Event) => void, opts: AddEventListenerOptions = {}) => {
    t.addEventListener(type, fn, opts);
    undo.push(() => t.removeEventListener(type, fn, opts));
  };
  const isOn = (): boolean => {
    try {
      return !dead && (typeof o.enabled === "function" ? o.enabled() !== false : true);
    } catch {
      return false;
    }
  };
  const safe =
    <A extends unknown[]>(fn: (...a: A) => void) =>
    (...a: A): void => {
      try {
        fn(...a);
      } catch {
        // Analytics must never break the page.
      }
    };

  const tick = () => {
    const t = now();
    if (lastTick !== null) engagedMs += Math.max(0, Math.min(t, lastActive + ACTIVE_MS) - lastTick);
    lastTick = doc.visibilityState === "hidden" ? null : t;
  };

  const push = (name: string, props: Props = {}) => {
    const def = DICT[name];
    if (!isOn() || def === undefined || (def[0] & surfaceBit) === 0) return;
    const clean: Record<string, PropValue> = {};
    for (const k of def[1]) {
      const v = cleanValue(k, props[k]);
      if (v !== undefined) clean[k] = v;
    }
    const ctx = o.getContext?.() ?? {};
    const user = o.getIdentity ? o.getIdentity() : "";
    queue.push({ pv, ctx, ctxKey: JSON.stringify(ctx), user, ev: { name, at: new Date(now()).toISOString(), props: clean } });
    if (queue.length > MAX_QUEUE) queue.shift();
  };

  const emitEngaged = () => {
    tick();
    const seconds = Math.round(engagedMs / 1000);
    engagedMs = 0;
    if (seconds >= 1 && screen !== "") push("engaged", { [place]: screen, seconds });
  };

  const post = (body: string, headers: Record<string, string> | null, leaving: boolean) => {
    if (headers === null && leaving && typeof win.navigator?.sendBeacon === "function") {
      try {
        if (win.navigator.sendBeacon(o.endpoint, new Blob([body], { type: "application/json" }))) return;
      } catch {
        // Fall through to fetch.
      }
    }
    // keepalive lets the request outlive the page. sendBeacon cannot carry an Authorization header.
    void win
      .fetch(o.endpoint, {
        method: "POST",
        keepalive: true,
        credentials: "omit",
        headers: { "Content-Type": "application/json", ...headers },
        body,
      })
      .catch(() => undefined);
  };

  const flush = (leaving = false) => {
    if (queue.length === 0) return;
    if (!isOn()) {
      queue.length = 0;
      return;
    }
    // What was recorded under another identity (sign-out, another user, before the sign-in) never goes out.
    if (o.getIdentity) {
      const who = o.getIdentity();
      for (let k = queue.length - 1; k >= 0; k--) if (queue[k].user !== who) queue.splice(k, 1);
      if (queue.length === 0) return;
    }
    const headers = o.getHeaders ? o.getHeaders() : null;
    if (o.getHeaders && headers === null) return; // signed out or token expired: keep the batch
    const items = queue.splice(0, queue.length);
    const bodies: string[] = [];
    let i = 0;
    while (i < items.length) {
      const first = items[i];
      const head = bytes(JSON.stringify({ ...first.ctx, pv: first.pv, events: [] }));
      const events: Queued["ev"][] = [];
      let size = head;
      while (i < items.length && items[i].pv === first.pv && items[i].ctxKey === first.ctxKey && events.length < MAX_BATCH) {
        const add = bytes(JSON.stringify(items[i].ev)) + (events.length > 0 ? 1 : 0);
        if (size + add > MAX_BODY && events.length > 0) break;
        events.push(items[i].ev);
        size += add;
        i++;
      }
      bodies.push(JSON.stringify({ ...first.ctx, pv: first.pv, events }));
    }
    // Leaving: only the newest bodies that fit the shared keepalive budget. The oldest are dropped.
    let from = 0;
    if (leaving) {
      let total = 0;
      from = bodies.length;
      while (from > 0 && total + bytes(bodies[from - 1]) <= MAX_LEAVE_BYTES) total += bytes(bodies[--from]);
    }
    for (let k = from; k < bodies.length; k++) post(bodies[k], headers, leaving);
  };

  const leave = safe(() => {
    emitEngaged();
    flush(true);
  });

  const view = safe((name: string, extra: Props = {}) => {
    emitEngaged();
    pv = uid(win);
    screen = cleanRoute(name);
    depthSent.clear();
    rage = { el: null, at: [] };
    lastActive = now();
    lastTick = doc.visibilityState === "hidden" ? null : now();
    push(o.surface === "site" ? "page_view" : "screen_view", { ...extra, [place]: screen });
  });

  const activity = safe(() => {
    tick();
    lastActive = now();
    if (lastTick === null && doc.visibilityState !== "hidden") lastTick = lastActive;
  });
  for (const type of ["pointerdown", "keydown", "scroll", "touchstart", "pointermove"]) {
    on(win, type, activity, { passive: true, capture: true });
  }

  on(
    doc,
    "visibilitychange",
    safe(() => {
      if (doc.visibilityState === "hidden") leave();
      else {
        lastActive = now();
        lastTick = lastActive;
      }
    }),
  );
  on(win, "pagehide", leave);

  on(
    doc,
    "click",
    safe((e: Event) => {
      const el = e.target as Element | null;
      if (el === null || typeof el.closest !== "function") return;
      const tracked = el.closest("[data-track]");
      const label = tracked?.getAttribute("data-track") ?? undefined;
      if (label !== undefined && label !== null) push("click", { name: label, [place]: screen });
      if (o.surface !== "event_center") return;
      const target = tracked ?? el;
      const t = now();
      rage = { el: target, at: (rage.el === target ? rage.at : []).filter((x) => t - x < RAGE_MS).concat(t) };
      if (rage.at.length >= RAGE_CLICKS) {
        push("rage_click", { screen, name: label ?? target.tagName.toLowerCase() });
        rage = { el: null, at: [] };
      }
    }),
    { capture: true },
  );

  if (o.scrollDepth === true) {
    on(
      win,
      "scroll",
      safe(() => {
        const h = doc.documentElement.scrollHeight;
        if (!(h > 0)) return;
        const pct = ((win.scrollY + win.innerHeight) / h) * 100;
        for (const d of [25, 50, 75, 100]) {
          if (pct >= (d === 100 ? 99 : d) && !depthSent.has(d)) {
            depthSent.add(d);
            push("scroll", { path: screen, depth: d });
          }
        }
      }),
      { passive: true },
    );
  }

  const timer = win.setInterval(
    safe(() => {
      tick();
      flush();
    }),
    o.flushMs ?? 5000,
  );

  return {
    view,
    settle: safe(emitEngaged),
    track: safe((name: string, props?: Props) => push(name, props)),
    flowStep: safe((flow: string, step: string) => push("flow_step", { flow, step })),
    error: safe((s: string, code: string) => push("ui_error", { screen: s, code })),
    flush: safe(() => flush()),
    destroy: safe(() => {
      emitEngaged();
      flush(true);
      dead = true;
      win.clearInterval(timer);
      for (const u of undo) u();
      queue.length = 0;
    }),
  };
}
