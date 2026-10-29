/** The address and the opening of the mini app page /tg/invitations (spec 46, 4.2, INV-05m). */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/http";
import type { EventItem, SessionItem } from "@/lib/api/types";
import {
  TG_INVITATIONS_PATH,
  captureTgInvitations,
  forgetTgInvitationsTarget,
  openTgInvitations,
  parseTgInvitationsAddress,
  tgInvitationsErrorKey,
  tgInvitationsTarget,
  webInvitationsPath,
  type OpenTgInvitationsDeps,
  type TgInvitationsTarget,
} from "@/lib/tgInvitations";

const SESSION = "11111111-1111-4111-8111-111111111111";
const EVENT = "22222222-2222-4222-8222-222222222222";
const ORG = "33333333-3333-4333-8333-333333333333";
const CODE = "abcDEF0123456789abcDEF0123456789abcDEF01234";

const ev = { id: EVENT, name: "Show" } as unknown as EventItem;
const se = { id: SESSION, start_at: "2026-11-01T18:00:00Z" } as unknown as SessionItem;

beforeEach(() => forgetTgInvitationsTarget());

function fakeHistory() {
  const calls: (string | URL | null | undefined)[] = [];
  return { calls, state: null, replaceState: (_d: unknown, _u: string, url?: string | URL | null) => calls.push(url) };
}

describe("the address of the bot link", () => {
  it("reads session and event from the query and the code from the fragment", () => {
    const a = parseTgInvitationsAddress(`?session=${SESSION}&event=${EVENT}`, `#code=${CODE}`);
    expect(a).toEqual({ code: CODE, sessionId: SESSION, eventId: EVENT, rest: "" });
  });

  it("takes everything from the fragment too, and the fragment wins", () => {
    const a = parseTgInvitationsAddress(`?session=${EVENT}`, `#code=${CODE}&session=${SESSION.toUpperCase()}&event=${EVENT}`);
    expect(a).toMatchObject({ code: CODE, sessionId: SESSION, eventId: EVENT });
  });

  it("never reads a code from the query string, where a server log would keep it", () => {
    expect(parseTgInvitationsAddress(`?code=${CODE}&session=${SESSION}&event=${EVENT}`, "").code).toBeNull();
  });

  it("refuses what is not a code or an id", () => {
    expect(parseTgInvitationsAddress("?session=x&event=../../etc", "#code=").sessionId).toBeNull();
    expect(parseTgInvitationsAddress("?event=nope", "").eventId).toBeNull();
    expect(parseTgInvitationsAddress("", "#code=" + "x".repeat(129)).code).toBeNull();
    expect(parseTgInvitationsAddress("", "")).toEqual({ code: null, sessionId: null, eventId: null, rest: "" });
  });

  it("leaves the Telegram part of the fragment where it is", () => {
    const a = parseTgInvitationsAddress("", `#code=${CODE}&session=${SESSION}&event=${EVENT}&tgWebAppVersion=8.0#tgWebAppData=a%3Db`);
    expect(a.rest).toBe("tgWebAppVersion=8.0&tgWebAppData=a%3Db");
    // An id never swallows what follows the second hash.
    expect(parseTgInvitationsAddress("", `#event=${EVENT}#tgWebAppData=x`).eventId).toBe(EVENT);
  });
});

describe("capturing the address", () => {
  it("takes code, session and event out of the address and out of the history", () => {
    const hist = fakeHistory();
    captureTgInvitations({ pathname: TG_INVITATIONS_PATH, search: `?session=${SESSION}&event=${EVENT}`, hash: `#code=${CODE}&tgWebAppVersion=8.0` }, hist);
    expect(tgInvitationsTarget()).toEqual({ code: CODE, sessionId: SESSION, eventId: EVENT });
    expect(hist.calls).toEqual([`${TG_INVITATIONS_PATH}#tgWebAppVersion=8.0`]);
  });

  it("leaves a clean address when Telegram added nothing", () => {
    const hist = fakeHistory();
    captureTgInvitations({ pathname: "/tg/invitations/", search: `?session=${SESSION}&event=${EVENT}`, hash: `#code=${CODE}` }, hist);
    expect(hist.calls).toEqual([TG_INVITATIONS_PATH]);
  });

  it("does nothing on any other page", () => {
    const hist = fakeHistory();
    captureTgInvitations({ pathname: "/tg/edit", search: "", hash: `#code=${CODE}` }, hist);
    captureTgInvitations({ pathname: "/o/x/events", search: "", hash: "" }, hist);
    expect(tgInvitationsTarget()).toBeNull();
    expect(hist.calls).toEqual([]);
  });
});

function deps(over: Partial<OpenTgInvitationsDeps> = {}): OpenTgInvitationsDeps {
  return {
    loadScript: vi.fn(async () => true),
    initData: () => "",
    hasSession: () => false,
    exchangeCode: vi.fn(async () => undefined),
    exchangeTelegram: vi.fn(async () => ""),
    organizations: async () => [ORG],
    list: async () => ({ events: [ev] }),
    sessions: async () => ({ sessions: [se] }),
    ...over,
  };
}
const target: TgInvitationsTarget = { code: CODE, sessionId: SESSION, eventId: EVENT };
const refuse = (status: number, code: string) => async (): Promise<never> => {
  throw new ApiError(status, code, "x");
};

describe("opening the page", () => {
  it("exchanges the code, finds the event and the session, and says which organization they belong to", async () => {
    const d = deps();
    const out = await openTgInvitations(target, d);
    expect(out).toEqual({ phase: "ready", orgId: ORG, eventId: EVENT, sessionId: SESSION, event: ev, session: se });
    expect(d.exchangeCode).toHaveBeenCalledWith(CODE);
    expect(d.exchangeTelegram).not.toHaveBeenCalled();
    // The captured link is forgotten once it is used.
    expect(tgInvitationsTarget()).toBeNull();
  });

  it("says the link does not work when the code is refused, and looks at nothing else", async () => {
    const list = vi.fn();
    const out = await openTgInvitations(target, deps({ exchangeCode: refuse(400, "auth.web_link_invalid"), list }));
    expect(out).toEqual({ phase: "error", key: "tgInv.error.invalid" });
    expect(list).not.toHaveBeenCalled();
  });

  it("falls back to the Telegram launch data when there is no code", async () => {
    const d = deps({ initData: () => "query_id=1&hash=x" });
    const out = await openTgInvitations({ code: null, sessionId: SESSION, eventId: EVENT }, d);
    expect(out.phase).toBe("ready");
    expect(d.exchangeTelegram).toHaveBeenCalledWith("query_id=1&hash=x", null);
    expect(d.exchangeCode).not.toHaveBeenCalled();
  });

  it("uses a session that is already open when there is neither a code nor launch data", async () => {
    expect((await openTgInvitations({ code: null, sessionId: SESSION, eventId: EVENT }, deps({ hasSession: () => true }))).phase).toBe("ready");
  });

  it("asks to press the bot button again when there is nothing to sign in with", async () => {
    expect(await openTgInvitations({ code: null, sessionId: SESSION, eventId: EVENT }, deps())).toEqual({ phase: "error", key: "tgInv.error.missing" });
    expect(await openTgInvitations(null, deps())).toEqual({ phase: "error", key: "tgInv.error.missing" });
  });

  it("asks to press the bot button again when the link names no session or no event", async () => {
    expect(await openTgInvitations({ code: CODE, sessionId: null, eventId: EVENT }, deps())).toEqual({ phase: "error", key: "tgInv.error.missing" });
    expect(await openTgInvitations({ code: CODE, sessionId: SESSION, eventId: null }, deps())).toEqual({ phase: "error", key: "tgInv.error.missing" });
  });

  it("is no access for an event no organization of the person lists", async () => {
    const sessions = vi.fn();
    expect(await openTgInvitations(target, deps({ list: async () => ({ events: [] }), sessions }))).toEqual({ phase: "noAccess" });
    expect(sessions).not.toHaveBeenCalled();
  });

  it("is no access for a session that is not one of that event", async () => {
    expect(await openTgInvitations(target, deps({ sessions: async () => ({ sessions: [{ id: EVENT } as unknown as SessionItem] }) }))).toEqual({ phase: "noAccess" });
    expect(await openTgInvitations(target, deps({ sessions: refuse(403, "org.forbidden") }))).toEqual({ phase: "noAccess" });
  });

  it("does not call a network failure or a 500 no access", async () => {
    expect(await openTgInvitations(target, deps({ sessions: refuse(0, "network") }))).toEqual({ phase: "error", key: "common.error.network" });
    expect(await openTgInvitations(target, deps({ list: refuse(500, "x") }))).toEqual({ phase: "error", key: "common.error.generic" });
  });
});

describe("texts and paths", () => {
  it("replaces the editor wording in the refused-link text", () => {
    expect(tgInvitationsErrorKey(new ApiError(400, "auth.web_link_invalid", "x"))).toBe("tgInv.error.invalid");
    expect(tgInvitationsErrorKey(new ApiError(0, "network", "x"))).toBe("common.error.network");
  });

  it("builds the web screen path of the same session", () => {
    expect(webInvitationsPath(ORG, EVENT, SESSION)).toBe(`/o/${ORG}/events/${EVENT}/sessions/${SESSION}/invitations`);
  });
});
