/** Which event the mini app opens and who may touch it (spec 44, RT-12b). */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/http";
import type { EventItem } from "@/lib/api/types";
import { bindBackButton, forgetTgEditTarget, startWebApp, type TgEditTarget } from "@/lib/tgEdit";
import { findEvent, isRefusal, openTgEdit, type OpenTgEditDeps } from "@/lib/tgEditEvent";

const EVENT = "22222222-2222-4222-8222-222222222222";
const OTHER = "33333333-3333-4333-8333-333333333333";
const ev = (id: string) => ({ id, name: "Show" }) as unknown as EventItem;

beforeEach(() => forgetTgEditTarget());

describe("findEvent", () => {
  it("finds the event in the organization that lists it and says which one", async () => {
    const list = vi.fn(async (org: string) => ({ events: org === "org-b" ? [ev(OTHER), ev(EVENT)] : [ev(OTHER)] }));
    const found = await findEvent(EVENT, ["org-a", "org-b"], list);
    expect(found?.orgId).toBe("org-b");
    expect(found?.event.id).toBe(EVENT);
  });

  it("is null for an event no organization of the person lists, however the id got there", async () => {
    expect(await findEvent(EVENT, ["org-a"], async () => ({ events: [ev(OTHER)] }))).toBeNull();
    expect(await findEvent(EVENT, [], async () => ({ events: [] }))).toBeNull();
  });

  it("treats a refused organization as not theirs and goes on to the next", async () => {
    const list = async (org: string) => {
      if (org === "org-a") throw new ApiError(403, "org.forbidden", "no");
      if (org === "org-b") throw new ApiError(404, "org.not_found", "no");
      return { events: [ev(EVENT)] };
    };
    expect((await findEvent(EVENT, ["org-a", "org-b", "org-c"], list))?.orgId).toBe("org-c");
    expect(await findEvent(EVENT, ["org-a", "org-b"], list)).toBeNull();
  });

  it("does not call a network failure or a 500 'no access'", async () => {
    const boom = new ApiError(500, "event.list_failed", "x");
    await expect(
      findEvent(EVENT, ["org-a", "org-b"], async (org) => {
        if (org === "org-a") throw boom;
        return { events: [] };
      }),
    ).rejects.toBe(boom);
    // Found in another organization: the failure of the first one does not matter.
    const found = await findEvent(EVENT, ["org-a", "org-b"], async (org) => {
      if (org === "org-a") throw boom;
      return { events: [ev(EVENT)] };
    });
    expect(found?.orgId).toBe("org-b");
  });

  it("knows a refusal", () => {
    expect(isRefusal(new ApiError(403, "x", "x"))).toBe(true);
    expect(isRefusal(new ApiError(404, "x", "x"))).toBe(true);
    expect(isRefusal(new ApiError(500, "x", "x"))).toBe(false);
    expect(isRefusal(new Error("x"))).toBe(false);
  });
});

function deps(over: Partial<OpenTgEditDeps> = {}): OpenTgEditDeps {
  return {
    loadScript: vi.fn(async () => true),
    exchange: vi.fn(async () => undefined),
    organizations: vi.fn(async () => ["org-a"]),
    list: vi.fn(async () => ({ events: [ev(EVENT)] })),
    ...over,
  };
}

const target = (over: Partial<TgEditTarget> = {}): TgEditTarget => ({ code: "c1", kind: "description", eventId: EVENT, ...over });

describe("openTgEdit", () => {
  it("exchanges the code, looks the event up and opens the editor of the kind", async () => {
    const d = deps();
    const out = await openTgEdit(target({ kind: "email_note" }), d);
    expect(out).toEqual({ phase: "ready", kind: "email_note", orgId: "org-a", eventId: EVENT });
    expect(d.exchange).toHaveBeenCalledWith("c1");
  });

  it("shows the 'link does not work' text for a used or expired code, and looks nothing up", async () => {
    const d = deps({ exchange: vi.fn(async () => Promise.reject(new ApiError(400, "auth.web_link_invalid", "x"))) });
    expect(await openTgEdit(target(), d)).toEqual({ phase: "error", key: "tgEdit.error.invalid" });
    expect(d.organizations).not.toHaveBeenCalled();
    expect(d.list).not.toHaveBeenCalled();
  });

  it("asks for the bot button again when the link names no kind or no event", async () => {
    expect(await openTgEdit(target({ kind: null }), deps())).toEqual({ phase: "error", key: "tgEdit.error.missing" });
    expect(await openTgEdit(target({ eventId: null }), deps())).toEqual({ phase: "error", key: "tgEdit.error.missing" });
    expect(await openTgEdit(null, deps())).toEqual({ phase: "error", key: "tgEdit.error.missing" });
  });

  it("is 'no access' for a foreign event, whatever the person's organizations say", async () => {
    const d = deps({ list: vi.fn(async () => ({ events: [ev(OTHER)] })) });
    expect(await openTgEdit(target(), d)).toEqual({ phase: "noAccess" });
    const refused = deps({ list: vi.fn(async () => Promise.reject(new ApiError(403, "org.forbidden", "x"))) });
    expect(await openTgEdit(target(), refused)).toEqual({ phase: "noAccess" });
  });

  it("names a network failure and any other failure instead of 'no access'", async () => {
    const offline = deps({ list: vi.fn(async () => Promise.reject(new ApiError(0, "", "offline"))) });
    expect(await openTgEdit(target(), offline)).toEqual({ phase: "error", key: "common.error.network" });
    const broken = deps({ organizations: vi.fn(async () => Promise.reject(new Error("boom"))) });
    expect(await openTgEdit(target(), broken)).toEqual({ phase: "error", key: "common.error.generic" });
  });
});

describe("the Telegram bridge", () => {
  it("tells Telegram the page is up and asks for the full height, when Telegram is there", () => {
    const WebApp = { ready: vi.fn(), expand: vi.fn() };
    startWebApp({ Telegram: { WebApp } });
    expect(WebApp.ready).toHaveBeenCalledTimes(1);
    expect(WebApp.expand).toHaveBeenCalledTimes(1);
    expect(() => startWebApp({})).not.toThrow();
  });

  it("shows the back button for the page and takes it away again", () => {
    const BackButton = { show: vi.fn(), hide: vi.fn(), onClick: vi.fn(), offClick: vi.fn() };
    const handler = vi.fn();
    const off = bindBackButton(handler, { Telegram: { WebApp: { BackButton } } });
    expect(BackButton.onClick).toHaveBeenCalledWith(handler);
    expect(BackButton.show).toHaveBeenCalledTimes(1);
    off();
    expect(BackButton.offClick).toHaveBeenCalledWith(handler);
    expect(BackButton.hide).toHaveBeenCalledTimes(1);
  });

  it("does nothing without Telegram or in an old client with no back button", () => {
    expect(() => bindBackButton(() => undefined, {})()).not.toThrow();
    expect(() => bindBackButton(() => undefined, { Telegram: { WebApp: {} } })()).not.toThrow();
  });
});
