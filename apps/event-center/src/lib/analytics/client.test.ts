// @vitest-environment jsdom
/**
 * The event center's tracker end to end, up to the request the collector would receive:
 * the build flag, the endpoint, the headers, the organization, the opt-out, and the
 * whole event_create chain with no id anywhere in it.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createSession, useSessionForTests } from "@/lib/api/http";
import { resetAnalyticsForTests, trackFlow, trackScreen, trackUiError } from "@/lib/analytics/client";
import { createFlowMarker, uiErrorCode } from "@/lib/analytics/hooks";
import { OPT_OUT_KEY, setOptedOut } from "@/lib/analytics/optOut";
import { ApiError } from "@/lib/api/http";
import { wizardStepOfField } from "@/lib/analytics/flows";

const ORG = "3c1a7d5e-0000-4000-8000-000000000001";

interface Call {
  url: string;
  headers: Record<string, string>;
  body: { org_id?: string; pv: string; events: Array<{ name: string; props: Record<string, unknown> }> };
}

let calls: Call[];

async function signIn(over: { token?: string; userId?: string; expiresInMs?: number } = {}) {
  const s = createSession({ store: { get: () => null, set: () => undefined }, bus: null, locks: null });
  useSessionForTests(s);
  await s.signedIn({
    accessToken: over.token ?? "tok-1",
    refreshToken: "r-1",
    expiresAt: new Date(Date.now() + (over.expiresInMs ?? 900_000)).toISOString(),
    userId: over.userId ?? "u1",
  });
  return s;
}

beforeEach(async () => {
  calls = [];
  (window as unknown as { fetch: unknown }).fetch = vi.fn((url: string, init: RequestInit) => {
    calls.push({ url, headers: init.headers as Record<string, string>, body: JSON.parse(String(init.body)) });
    return Promise.resolve(new Response(null, { status: 204 }));
  });
  await signIn();
});

afterEach(() => {
  resetAnalyticsForTests();
  vi.unstubAllEnvs();
  localStorage.removeItem(OPT_OUT_KEY);
  setOptedOut(false);
});

const all = () => calls.flatMap((c) => c.body.events);

describe("the build flag", () => {
  it("sends nothing while VITE_ANALYTICS is not 1", () => {
    vi.stubEnv("VITE_ANALYTICS", "");
    trackScreen("/events", ORG);
    trackFlow("event_create", "started");
    window.dispatchEvent(new Event("pagehide"));
    expect(calls).toHaveLength(0);
  });

  it("only the exact value 1 turns it on", () => {
    vi.stubEnv("VITE_ANALYTICS", "true");
    trackScreen("/events", ORG);
    window.dispatchEvent(new Event("pagehide"));
    expect(calls).toHaveLength(0);
  });
});

describe("with VITE_ANALYTICS=1", () => {
  beforeEach(() => {
    vi.stubEnv("VITE_ANALYTICS", "1");
  });

  it("posts to the API base + /v1/me/t with the API client's headers and the organization", () => {
    trackScreen("/events/$eventId", ORG);
    window.dispatchEvent(new Event("pagehide"));
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe("http://test.invalid/v1/me/t");
    expect(calls[0].headers).toMatchObject({ Authorization: "Bearer tok-1", "X-Client-Channel": "event_center_web" });
    expect(calls[0].body.org_id).toBe(ORG);
    expect(calls[0].body.events[0]).toMatchObject({ name: "screen_view", props: { screen: "/events/$eventId" } });
  });

  it("sends the batch of the previous organization before switching to another", () => {
    const OTHER = "8a8a8a8a-0000-4000-8000-000000000002";
    trackScreen("/events", ORG);
    trackScreen("/events", OTHER);
    window.dispatchEvent(new Event("pagehide"));
    expect(calls.map((c) => c.body.org_id)).toEqual([ORG, OTHER]);
  });

  it("keeps the batch while the access token has expired and sends it after a refresh", async () => {
    await signIn({ expiresInMs: -1000 });
    trackScreen("/events", ORG);
    window.dispatchEvent(new Event("pagehide"));
    expect(calls).toHaveLength(0);
    await signIn({ token: "tok-2" });
    window.dispatchEvent(new Event("pagehide"));
    expect(calls).toHaveLength(1);
    expect(calls[0].headers.Authorization).toBe("Bearer tok-2");
  });

  it("events of a user who signed out are not sent under the next user", async () => {
    trackScreen("/events", ORG);
    trackFlow("event_create", "started");
    await signIn({ token: "tok-b", userId: "u2" });
    trackFlow("event_create", "venue");
    window.dispatchEvent(new Event("pagehide"));
    expect(all().map((e) => e.props.step)).toEqual(["venue"]);
    expect(calls[0].headers.Authorization).toBe("Bearer tok-b");
  });

  it("the engaged time of the screen left goes to the organization it was in", () => {
    vi.useFakeTimers();
    try {
      const OTHER = "8a8a8a8a-0000-4000-8000-000000000002";
      trackScreen("/events", ORG);
      document.dispatchEvent(new Event("pointerdown"));
      window.dispatchEvent(new Event("pointerdown"));
      vi.advanceTimersByTime(12_000);
      trackScreen("/events", OTHER);
      window.dispatchEvent(new Event("pagehide"));
      const mine = calls.flatMap((c) => c.body.events.map((e) => `${c.body.org_id}:${e.name}`));
      expect(mine).toEqual([`${ORG}:screen_view`, `${ORG}:engaged`, `${OTHER}:screen_view`]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("an opt-out stops everything, and turning it back on resumes", () => {
    setOptedOut(true);
    expect(localStorage.getItem(OPT_OUT_KEY)).toBe("1");
    trackScreen("/events", ORG);
    trackFlow("event_create", "started");
    window.dispatchEvent(new Event("pagehide"));
    expect(calls).toHaveLength(0);
    setOptedOut(false);
    trackFlow("event_create", "venue");
    window.dispatchEvent(new Event("pagehide"));
    expect(all().map((e) => e.name)).toEqual(["flow_step"]);
  });

  it("a full event_create run gives the whole chain of flow_step events and no id", () => {
    trackScreen("/events/new", ORG);
    const flow = createFlowMarker("event_create");
    flow.mark("started");
    for (const field of ["name", "venueName", "d1.day", "d1.c:cat1.price"]) flow.mark(wizardStepOfField(field));
    flow.mark("event"); // already reported: not sent twice
    flow.mark("poster");
    flow.mark("checked");
    flow.mark("submit");
    flow.mark("created");
    trackScreen("/events/$eventId", ORG);
    window.dispatchEvent(new Event("pagehide"));
    const steps = all()
      .filter((e) => e.name === "flow_step")
      .map((e) => e.props.step);
    expect(steps).toEqual(["started", "event", "venue", "dates", "tickets", "poster", "checked", "submit", "created"]);
    expect(all().every((e) => e.props.flow === undefined || e.props.flow === "event_create")).toBe(true);
    const wire = JSON.stringify(calls.map((c) => c.body.events));
    expect(wire).not.toMatch(/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}/);
    // The organization travels once per batch, in the body, never in an event.
    expect(wire).not.toContain(ORG);
  });

  it("reports a shown API error as the screen and the error code, not its message", () => {
    trackScreen("/orders/$orderId", ORG);
    const err = new ApiError(409, "order.not_refundable", "Order 1000000500 of buyer a@b.co cannot be refunded");
    trackUiError(uiErrorCode(err));
    trackUiError(uiErrorCode(new Error("boom with a@b.co")));
    window.dispatchEvent(new Event("pagehide"));
    const errors = all().filter((e) => e.name === "ui_error");
    expect(errors.map((e) => e.props)).toEqual([
      { screen: "/orders/$orderId", code: "order.not_refundable" },
      { screen: "/orders/$orderId", code: "client.error" },
    ]);
    expect(JSON.stringify(calls)).not.toMatch(/a@b\.co|1000000500/);
  });

  it("rage clicks and engaged time ride the same channel", () => {
    vi.useFakeTimers();
    try {
      document.body.innerHTML = `<button id="b">Go</button>`;
      trackScreen("/events", ORG);
      const b = document.querySelector("#b")!;
      for (let i = 0; i < 3; i++) b.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      vi.advanceTimersByTime(7_000);
      expect(all().map((e) => e.name)).toContain("rage_click");
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("createFlowMarker", () => {
  it("reports a step once, and again after a reset", () => {
    const seen: string[] = [];
    const m = createFlowMarker("refund", (flow, step) => seen.push(`${flow}:${step}`));
    m.mark("open");
    m.mark("open");
    m.mark("review");
    m.reset();
    m.mark("open");
    expect(seen).toEqual(["refund:open", "refund:review", "refund:open"]);
  });
});
