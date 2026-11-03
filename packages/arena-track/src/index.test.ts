import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createTracker, type Tracker, type TrackerOptions } from "./index";

interface Sent {
  url: string;
  init: RequestInit;
  body: { org_id?: string; pv: string; events: Array<{ name: string; at: string; props: Record<string, unknown> }> };
}

let sent: Sent[];
let fetchMock: ReturnType<typeof vi.fn>;
let visibility: "visible" | "hidden";
let tracker: Tracker | null;

function make(over: Partial<TrackerOptions> = {}): Tracker {
  tracker = createTracker({
    endpoint: "https://api.test/v1/me/t",
    surface: "event_center",
    getHeaders: () => ({ Authorization: "Bearer t", "X-Client-Channel": "event_center_web" }),
    ...over,
  });
  return tracker;
}

const events = () => sent.flatMap((s) => s.body.events);
const names = () => events().map((e) => e.name);

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-11-01T10:00:00Z"));
  sent = [];
  visibility = "visible";
  tracker = null;
  fetchMock = vi.fn((url: string, init: RequestInit) => {
    sent.push({ url, init, body: JSON.parse(String(init.body)) });
    return Promise.resolve(new Response(null, { status: 204 }));
  });
  (window as unknown as { fetch: unknown }).fetch = fetchMock;
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => visibility });
  document.body.innerHTML = "";
});

afterEach(() => {
  tracker?.destroy();
  vi.useRealTimers();
  delete (window.navigator as unknown as { sendBeacon?: unknown }).sendBeacon;
});

function setVisibility(v: "visible" | "hidden") {
  visibility = v;
  document.dispatchEvent(new Event("visibilitychange"));
}

describe("disabled tracker", () => {
  it("does nothing and never throws without an endpoint", () => {
    const t = make({ endpoint: "" });
    expect(() => {
      t.view("/events");
      t.track("click", { name: "x" });
      t.flowStep("event_create", "venue");
      t.error("/events", "http.500");
      t.flush();
      t.destroy();
    }).not.toThrow();
    vi.advanceTimersByTime(20_000);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("does nothing when enabled is false, and adds no listeners", () => {
    const add = vi.spyOn(document, "addEventListener");
    const t = make({ enabled: false });
    t.view("/events");
    t.flush();
    vi.advanceTimersByTime(20_000);
    expect(fetchMock).not.toHaveBeenCalled();
    expect(add).not.toHaveBeenCalled();
    add.mockRestore();
  });

  it("an opt-out function drops events and empties the queue", () => {
    let optedOut = false;
    const t = make({ enabled: () => !optedOut });
    t.view("/events");
    optedOut = true;
    t.flowStep("event_create", "venue");
    t.flush();
    expect(fetchMock).not.toHaveBeenCalled();
    optedOut = false;
    t.flush();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("swallows a failing fetch and a throwing header function", () => {
    fetchMock.mockImplementation(() => Promise.reject(new Error("offline")));
    const t = make();
    t.view("/events");
    expect(() => t.flush()).not.toThrow();
    const u = make({ getHeaders: () => { throw new Error("boom"); } });
    u.view("/events");
    expect(() => u.flush()).not.toThrow();
  });
});

describe("queue and flush", () => {
  it("sends nothing before the period and one batch at 5 s", () => {
    const t = make();
    t.view("/events/$eventId");
    t.flowStep("event_create", "venue");
    vi.advanceTimersByTime(4_900);
    expect(fetchMock).not.toHaveBeenCalled();
    vi.advanceTimersByTime(200);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(names()).toEqual(["screen_view", "flow_step"]);
    // Nothing left to send on the next period.
    vi.advanceTimersByTime(10_000);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("posts with keepalive and the caller's headers, JSON body with pv and at", () => {
    const t = make({ getContext: () => ({ org_id: "3c1a7d5e-0000-4000-8000-000000000001" }) });
    t.view("/events");
    t.flush();
    const s = sent[0];
    expect(s.url).toBe("https://api.test/v1/me/t");
    expect(s.init.method).toBe("POST");
    expect(s.init.keepalive).toBe(true);
    expect(s.init.credentials).toBe("omit");
    expect(s.init.headers).toMatchObject({ Authorization: "Bearer t", "X-Client-Channel": "event_center_web", "Content-Type": "application/json" });
    expect(s.body.org_id).toBe("3c1a7d5e-0000-4000-8000-000000000001");
    expect(s.body.pv).toMatch(/^p_[0-9a-z]{12}$/);
    expect(s.body.events[0].at).toBe("2026-11-01T10:00:00.000Z");
  });

  it("keeps the batch while signed out and sends it after sign-in", () => {
    let token: string | null = null;
    const t = make({ getHeaders: () => (token === null ? null : { Authorization: `Bearer ${token}` }) });
    t.view("/login");
    t.flush();
    expect(fetchMock).not.toHaveBeenCalled();
    token = "abc";
    t.flush();
    expect(names()).toEqual(["screen_view"]);
  });

  it("flushes on pagehide and on a hidden tab, with an authenticated fetch (no sendBeacon)", () => {
    const beacon = vi.fn(() => true);
    (window.navigator as unknown as { sendBeacon: unknown }).sendBeacon = beacon;
    const t = make();
    t.view("/events");
    window.dispatchEvent(new Event("pagehide"));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(beacon).not.toHaveBeenCalled();
    t.flowStep("event_create", "venue");
    setVisibility("hidden");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("an anonymous surface leaves through sendBeacon", () => {
    const beacon = vi.fn(() => true);
    (window.navigator as unknown as { sendBeacon: unknown }).sendBeacon = beacon;
    const t = createTracker({ endpoint: "https://api.test/v1/t", surface: "site" });
    tracker = t;
    t.view("/pricing", { lang: "ru" });
    window.dispatchEvent(new Event("pagehide"));
    expect(beacon).toHaveBeenCalledTimes(1);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("splits a big queue into batches of at most 50 events and under 16 KB measured in bytes", () => {
    const t = make();
    t.view("/events");
    for (let i = 0; i < 120; i++) t.flowStep("event_create", `step-${i}`.padEnd(200, "x"));
    t.flush();
    expect(sent.length).toBeGreaterThan(2);
    for (const s of sent) {
      expect(s.body.events.length).toBeLessThanOrEqual(50);
      expect(new TextEncoder().encode(String(s.init.body)).length).toBeLessThanOrEqual(16_000);
    }
    expect(events().length).toBe(121);
  });

  it("caps the in-memory queue", () => {
    const t = make({ getHeaders: () => null });
    for (let i = 0; i < 500; i++) t.flowStep("f", String(i));
    (t as unknown as { destroy(): void }).destroy();
    // Switch to a tracker that can send, with the same cap rule: the oldest are dropped.
    const u = make();
    for (let i = 0; i < 500; i++) u.flowStep("f", String(i));
    u.flush();
    expect(events().length).toBe(200);
    expect(events()[0].props.step).toBe("300");
  });

  it("groups events by page view id: a screen change starts a new pv", () => {
    const t = make();
    t.view("/events");
    t.flowStep("event_create", "started");
    t.view("/events/$eventId");
    t.flush();
    expect(sent.length).toBe(2);
    expect(sent[0].body.pv).not.toBe(sent[1].body.pv);
    expect(sent[1].body.events[0].props.screen).toBe("/events/$eventId");
  });

  it("destroy flushes once and then stops", () => {
    const t = make();
    t.view("/events");
    t.destroy();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    t.flowStep("a", "b");
    vi.advanceTimersByTime(20_000);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});

describe("context and identity travel with the event", () => {
  it("an event keeps the organization it happened in, even if the context changes before the send", () => {
    let org = "org-1";
    const t = make({ getContext: () => ({ org_id: org }) });
    t.view("/events");
    document.dispatchEvent(new Event("pointerdown"));
    vi.advanceTimersByTime(10_000);
    // The screen is left for another organization: the engaged time of the old screen is closed first.
    t.settle();
    org = "org-2";
    t.view("/events");
    t.flush();
    const pairs = sent.flatMap((s) => s.body.events.map((e) => `${s.body.org_id}:${e.name}`));
    expect(pairs).toEqual(["org-1:screen_view", "org-1:engaged", "org-2:screen_view"]);
    expect(events().find((e) => e.name === "engaged")?.props).toEqual({ screen: "/events", seconds: 10 });
  });

  it("a context change without settle still keeps events recorded earlier under their own context", () => {
    let org = "org-1";
    const t = make({ getContext: () => ({ org_id: org }) });
    t.flowStep("event_create", "started");
    org = "org-2";
    t.flowStep("event_create", "venue");
    t.flush();
    expect(sent.map((s) => s.body.org_id)).toEqual(["org-1", "org-2"]);
  });

  it("drops events recorded for a user who signed out, and sends the next user's own", () => {
    let user: string | null = "u1";
    const t = make({ getIdentity: () => user, getHeaders: () => (user === null ? null : { Authorization: `Bearer ${user}` }) });
    t.view("/events");
    t.flowStep("event_create", "started");
    user = null; // sign-out
    t.flush();
    expect(fetchMock).not.toHaveBeenCalled();
    user = "u2"; // the next person signs in on the same tab
    t.flowStep("event_create", "venue");
    t.flush();
    expect(events().map((e) => e.name)).toEqual(["flow_step"]);
    expect(events()[0].props.step).toBe("venue");
    expect(sent[0].init.headers).toMatchObject({ Authorization: "Bearer u2" });
  });

  it("keeps events recorded while signed out until the same state, and never gives them to a user who signs in", () => {
    let user: string | null = null;
    const t = make({ getIdentity: () => user, getHeaders: () => (user === null ? null : { Authorization: "Bearer t" }) });
    t.view("/login");
    t.flush();
    expect(fetchMock).not.toHaveBeenCalled();
    user = "u1";
    t.flush();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("body size", () => {
  it("measures bytes, not characters: multi-byte text stays under 16 KB", () => {
    const t = make();
    t.view("/events");
    for (let i = 0; i < 60; i++) t.error("/events", "ж".repeat(150));
    t.flush();
    for (const s of sent) expect(new TextEncoder().encode(String(s.init.body)).length).toBeLessThanOrEqual(16_000);
    expect(events().length).toBe(61);
  });

  it("when leaving, the keepalive posts together stay under 60 KB and the oldest are dropped", () => {
    const t = make({ getHeaders: () => ({ Authorization: "Bearer t" }) });
    t.view("/events");
    // About 200 events of 400+ bytes: over 80 KB of queue in several bodies of at most 15 KB.
    for (let i = 0; i < 199; i++) t.error("/events", `e${String(i).padStart(3, "0")}` + "ж".repeat(195));
    window.dispatchEvent(new Event("pagehide"));
    const total = sent.reduce((n, s) => n + new TextEncoder().encode(String(s.init.body)).length, 0);
    expect(total).toBeLessThanOrEqual(60_000);
    expect(sent.length).toBeGreaterThan(1);
    const codes = events().filter((e) => e.name === "ui_error").map((e) => String(e.props.code).slice(0, 4));
    expect(codes[codes.length - 1]).toBe("e198");
    expect(codes.length).toBeLessThan(199);
  });
});

describe("engaged time", () => {
  it("counts visible time with input in the last 30 s and stops after that", () => {
    const t = make();
    t.view("/events");
    vi.advanceTimersByTime(10_000);
    document.dispatchEvent(new Event("pointerdown"));
    window.dispatchEvent(new Event("pointerdown"));
    vi.advanceTimersByTime(20_000); // input 20 s ago: still active (10 s of the window left)
    vi.advanceTimersByTime(40_000); // counts up to 30 s after the input, so 10 s + 30 s = 40 s in all
    t.view("/events/$eventId");
    t.flush();
    const e = events().find((x) => x.name === "engaged");
    expect(e?.props).toEqual({ screen: "/events", seconds: 40 });
  });

  it("does not count a hidden tab", () => {
    const t = make();
    t.view("/events");
    vi.advanceTimersByTime(5_000);
    setVisibility("hidden"); // flushes the 5 s
    vi.advanceTimersByTime(25_000);
    setVisibility("visible");
    vi.advanceTimersByTime(3_000);
    window.dispatchEvent(new Event("pagehide"));
    const seconds = events().filter((x) => x.name === "engaged").map((x) => x.props.seconds);
    expect(seconds).toEqual([5, 3]);
  });

  it("reports nothing for a screen opened for under a second", () => {
    const t = make();
    t.view("/events");
    vi.advanceTimersByTime(300);
    t.view("/orders");
    t.flush();
    expect(names()).toEqual(["screen_view", "screen_view"]);
  });
});

describe("clicks", () => {
  it("records a click only on an element with data-track and sends its name, not its text", () => {
    document.body.innerHTML = `<button id="a" data-track="create_event"><span>Secret text</span></button><button id="b">Other</button>`;
    const t = make();
    t.view("/events");
    document.querySelector("#a span")!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    document.querySelector("#b")!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    t.flush();
    const clicks = events().filter((e) => e.name === "click");
    expect(clicks).toHaveLength(1);
    expect(clicks[0].props).toEqual({ name: "create_event", screen: "/events" });
    expect(JSON.stringify(sent)).not.toContain("Secret text");
  });

  it("three clicks on one element within a second are a rage click", () => {
    document.body.innerHTML = `<button id="a">Save</button>`;
    const el = document.querySelector("#a")!;
    const t = make();
    t.view("/events");
    for (let i = 0; i < 3; i++) {
      el.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      vi.advanceTimersByTime(200);
    }
    t.flush();
    const rage = events().filter((e) => e.name === "rage_click");
    expect(rage).toHaveLength(1);
    expect(rage[0].props).toEqual({ screen: "/events", name: "button" });
  });

  it("slow clicks, or clicks on different elements, are not a rage click", () => {
    document.body.innerHTML = `<button id="a">A</button><button id="b" data-track="b">B</button>`;
    const a = document.querySelector("#a")!;
    const b = document.querySelector("#b")!;
    const t = make();
    t.view("/events");
    for (let i = 0; i < 3; i++) {
      a.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      vi.advanceTimersByTime(600);
    }
    a.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    b.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    a.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    t.flush();
    expect(names()).not.toContain("rage_click");
  });

  it("names a rage click on a tracked element by its data-track", () => {
    document.body.innerHTML = `<button id="a" data-track="pay">Pay</button>`;
    const el = document.querySelector("#a")!;
    const t = make();
    t.view("/o");
    for (let i = 0; i < 3; i++) el.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    t.flush();
    expect(events().find((e) => e.name === "rage_click")?.props.name).toBe("pay");
  });
});

describe("scroll depth", () => {
  function scrollTo(y: number) {
    Object.defineProperty(window, "scrollY", { configurable: true, value: y });
    window.dispatchEvent(new Event("scroll"));
  }
  beforeEach(() => {
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 500 });
    Object.defineProperty(document.documentElement, "scrollHeight", { configurable: true, value: 2000 });
  });

  it("reports each depth once, only when opted in", () => {
    const t = createTracker({ endpoint: "https://api.test/v1/t", surface: "site", scrollDepth: true });
    tracker = t;
    t.view("/blog/a");
    scrollTo(0); // 25 %
    scrollTo(500); // 50 %
    scrollTo(500);
    scrollTo(1500); // 100 %
    t.flush();
    const depths = events().filter((e) => e.name === "scroll").map((e) => e.props.depth);
    expect(depths).toEqual([25, 50, 75, 100]);
  });

  it("is off by default", () => {
    const t = createTracker({ endpoint: "https://api.test/v1/t", surface: "site" });
    tracker = t;
    t.view("/blog/a");
    scrollTo(1500);
    t.flush();
    expect(names()).toEqual(["page_view"]);
  });
});

describe("what may leave the browser", () => {
  it("drops events outside the dictionary and properties outside their list", () => {
    const t = make();
    t.view("/events");
    t.track("purchase", { total: 100 });
    t.track("flow_step", { flow: "event_create", step: "venue", email: "a@b.co", value: "x", name: "Ann" });
    t.track("click", { name: "save", text: "Save", value: "hunter2" });
    t.flush();
    const flow = events().find((e) => e.name === "flow_step");
    expect(flow?.props).toEqual({ flow: "event_create", step: "venue" });
    expect(events().find((e) => e.name === "click")?.props).toEqual({ name: "save" });
    expect(names()).not.toContain("purchase");
  });

  it("keeps an event on its own surface", () => {
    const ec = make();
    ec.view("/events");
    ec.track("page_view", { path: "/x" });
    ec.track("scroll", { depth: 25 });
    ec.track("consent", { analytics: true });
    ec.flush();
    expect(names()).toEqual(["screen_view"]);
    ec.destroy();
    sent = [];
    const site = createTracker({ endpoint: "https://api.test/v1/t", surface: "site" });
    tracker = site;
    site.view("/pricing");
    site.flowStep("event_create", "venue");
    site.error("/pricing", "x");
    site.flush();
    expect(names()).toEqual(["page_view"]);
  });

  it("reports routes as templates: no query, no hash, no ids, no addresses", () => {
    const t = make();
    t.view("/events/3c1a7d5e-0000-4000-8000-000000000001?tab=a#top");
    t.error("/orders/1000000500/refund", "order.refund");
    t.error("/o/a@b.co", "x");
    t.flush();
    const props = events().map((e) => JSON.stringify(e.props));
    expect(props.join(" ")).not.toMatch(/3c1a7d5e|1000000500|a@b\.co|tab=|top/);
    expect(events()[0].props.screen).toBe("/events/:id");
    expect(events()[1].props.screen).toBe("/orders/:id/refund");
  });

  it("strips e-mail addresses and UUIDs from ordinary values, truncates long ones", () => {
    const t = make();
    t.view("/events");
    t.error("/events", "see a@b.co 3c1a7d5e-0000-4000-8000-000000000001 " + "z".repeat(400));
    t.flush();
    const code = String(events()[1].props.code);
    expect(code).not.toMatch(/@|3c1a7d5e/);
    expect(code.length).toBeLessThanOrEqual(200);
  });

  it("keeps no identity in the body: no user id, no token", () => {
    const t = make();
    t.view("/events");
    t.flush();
    const body = sent[0].body as unknown as Record<string, unknown>;
    expect(Object.keys(body).sort()).toEqual(["events", "pv"]);
  });
});
