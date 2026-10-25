import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import path from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/http";
import {
  TELEGRAM_SCRIPT_SRC,
  TG_EDIT_PATH,
  captureTgEdit,
  closeMiniApp,
  enterMiniApp,
  forgetTgEditTarget,
  parseTgEditFragment,
  tgEditErrorKey,
  tgEditTarget,
} from "@/lib/tgEdit";

const EVENT = "0b7f3c1e-2f4a-4c1d-9d3e-5a6b7c8d9e0f";
const CODE = "abcDEF0123456789abcDEF0123456789abcDEF01234";

function fakeHistory() {
  const calls: (string | URL | null | undefined)[] = [];
  return { calls, state: null, replaceState: (_d: unknown, _u: string, url?: string | URL | null) => calls.push(url) };
}

beforeEach(() => forgetTgEditTarget());

describe("the fragment of the bot's link", () => {
  it("reads code, kind and event", () => {
    const f = parseTgEditFragment(`#code=${CODE}&kind=description&event=${EVENT}`);
    expect(f).toEqual({ code: CODE, kind: "description", eventId: EVENT, rest: "" });
    expect(parseTgEditFragment(`#code=${CODE}&kind=email_note&event=${EVENT.toUpperCase()}`).eventId).toBe(EVENT);
  });

  it("refuses what is not a code, a kind or an event id", () => {
    expect(parseTgEditFragment("#code=")).toMatchObject({ code: null });
    expect(parseTgEditFragment("#code=" + "x".repeat(129))).toMatchObject({ code: null });
    expect(parseTgEditFragment("#kind=poster&event=not-a-uuid")).toMatchObject({ kind: null, eventId: null });
    expect(parseTgEditFragment("")).toEqual({ code: null, kind: null, eventId: null, rest: "" });
  });

  it("keeps what Telegram added, takes out only ours", () => {
    const f = parseTgEditFragment(`#code=${CODE}&tgWebAppVersion=9.1&kind=description&tgWebAppPlatform=web&event=${EVENT}`);
    expect(new URLSearchParams(f.rest).get("tgWebAppVersion")).toBe("9.1");
    expect(new URLSearchParams(f.rest).get("tgWebAppPlatform")).toBe("web");
    expect(f.rest).not.toContain("code");
    expect(f.rest).not.toContain("event");
    expect(f.rest).not.toContain("kind");
  });

  // Telegram clients add their launch parameters to the address of a web app.
  // Whatever the way, our three parameters are read whole and nothing of
  // Telegram's is taken for ours.
  it("reads our three parameters when Telegram appends its data after &", () => {
    const tg = "tgWebAppData=query_id%3DAAH%26user%3D%257B%2522id%2522%253A42%257D&tgWebAppVersion=9.1&tgWebAppPlatform=android";
    const f = parseTgEditFragment(`#code=${CODE}&kind=description&event=${EVENT}&${tg}`);
    expect(f).toMatchObject({ code: CODE, kind: "description", eventId: EVENT });
    // Telegram's part is left as it came (only decoded and written back the same way).
    const rest = new URLSearchParams(f.rest);
    expect([...rest.keys()]).toEqual(["tgWebAppData", "tgWebAppVersion", "tgWebAppPlatform"]);
    expect(rest.get("tgWebAppData")).toBe("query_id=AAH&user=%7B%22id%22%3A42%7D");
  });

  it("stops at a second # that Telegram appended, and keeps what is after it", () => {
    const f = parseTgEditFragment(`#code=${CODE}&kind=email_note&event=${EVENT}#tgWebAppData=query_id%3DAAH&tgWebAppVersion=9.1`);
    // The event id is not glued to the second fragment.
    expect(f).toMatchObject({ code: CODE, kind: "email_note", eventId: EVENT });
    expect(f.rest).toBe("tgWebAppData=query_id%3DAAH&tgWebAppVersion=9.1");
    expect(f.rest).not.toContain("#");
    // The second fragment is never read for our names, not even as the last word.
    const spoof = parseTgEditFragment(`#kind=description#code=${CODE}&event=${EVENT}`);
    expect(spoof).toMatchObject({ code: null, kind: "description", eventId: null });
    expect(spoof.rest).toBe(`code=${CODE}&event=${EVENT}`);
  });

  it("copes with a missing piece, each alone", () => {
    expect(parseTgEditFragment(`#kind=description&event=${EVENT}`)).toEqual({ code: null, kind: "description", eventId: EVENT, rest: "" });
    expect(parseTgEditFragment(`#code=${CODE}&event=${EVENT}`)).toEqual({ code: CODE, kind: null, eventId: EVENT, rest: "" });
    expect(parseTgEditFragment(`#code=${CODE}&kind=description`)).toEqual({ code: CODE, kind: "description", eventId: null, rest: "" });
    expect(parseTgEditFragment(`#code=${CODE}&kind=description&event=${EVENT}&tgWebAppData=x`).rest).toBe("tgWebAppData=x");
    // Nothing of ours at all: Telegram's data only, a bare "#", "##", an empty hash.
    expect(parseTgEditFragment("#tgWebAppData=x&tgWebAppVersion=9.1")).toEqual({ code: null, kind: null, eventId: null, rest: "tgWebAppData=x&tgWebAppVersion=9.1" });
    expect(parseTgEditFragment("#")).toEqual({ code: null, kind: null, eventId: null, rest: "" });
    expect(parseTgEditFragment("##")).toEqual({ code: null, kind: null, eventId: null, rest: "" });
    expect(parseTgEditFragment("#code=")).toMatchObject({ code: null, rest: "" });
    // A repeated parameter: the first one counts, all of them leave the address.
    const twice = parseTgEditFragment(`#code=${CODE}&code=other&kind=description&event=${EVENT}`);
    expect(twice.code).toBe(CODE);
    expect(twice.rest).toBe("");
  });
});

describe("capture", () => {
  it("clears the fragment of /tg/edit and keeps the parameters in memory", () => {
    const h = fakeHistory();
    captureTgEdit({ pathname: TG_EDIT_PATH, hash: `#code=${CODE}&kind=email_note&event=${EVENT}` }, h);
    expect(h.calls).toEqual([TG_EDIT_PATH]);
    expect(tgEditTarget()).toEqual({ code: CODE, kind: "email_note", eventId: EVENT });
    forgetTgEditTarget();
    expect(tgEditTarget()).toBeNull();
  });

  it("leaves Telegram's own parameters in the address", () => {
    const h = fakeHistory();
    captureTgEdit({ pathname: TG_EDIT_PATH, hash: `#code=${CODE}&tgWebAppPlatform=android&kind=description&event=${EVENT}` }, h);
    expect(h.calls).toEqual([`${TG_EDIT_PATH}#tgWebAppPlatform=android`]);
    expect(String(h.calls[0])).not.toContain(CODE);
  });

  it("keeps the event and clears the address when Telegram appended a second #", () => {
    const h = fakeHistory();
    captureTgEdit({ pathname: TG_EDIT_PATH, hash: `#code=${CODE}&kind=description&event=${EVENT}#tgWebAppData=x&tgWebAppVersion=9.1` }, h);
    expect(tgEditTarget()).toEqual({ code: CODE, kind: "description", eventId: EVENT });
    expect(h.calls).toEqual([`${TG_EDIT_PATH}#tgWebAppData=x&tgWebAppVersion=9.1`]);
    expect(String(h.calls[0])).not.toContain(CODE);
  });

  it("does nothing on any other page, and takes /tg/edit/ too", () => {
    const h = fakeHistory();
    captureTgEdit({ pathname: "/login/link", hash: `#code=${CODE}` }, h);
    captureTgEdit({ pathname: "/orgs", hash: "" }, h);
    expect(h.calls).toEqual([]);
    expect(tgEditTarget()).toBeNull();
    captureTgEdit({ pathname: "/tg/edit/", hash: `#code=${CODE}` }, h);
    expect(h.calls).toEqual([TG_EDIT_PATH]);
    expect(tgEditTarget()?.code).toBe(CODE);
  });
});

describe("entering the mini app", () => {
  it("exchanges the code once, starts the Telegram script and forgets the code", async () => {
    captureTgEdit({ pathname: TG_EDIT_PATH, hash: `#code=${CODE}&kind=description&event=${EVENT}` }, fakeHistory());
    const exchange = vi.fn().mockResolvedValue(undefined);
    const loadScript = vi.fn().mockResolvedValue(true);
    const out = await enterMiniApp(tgEditTarget(), { exchange, loadScript });
    expect(out).toEqual({ ok: true });
    expect(exchange).toHaveBeenCalledTimes(1);
    expect(exchange).toHaveBeenCalledWith(CODE);
    expect(loadScript).toHaveBeenCalledOnce();
    expect(tgEditTarget()).toBeNull();
  });

  it("works when the Telegram script is missing", async () => {
    const exchange = vi.fn().mockResolvedValue(undefined);
    const out = await enterMiniApp({ code: CODE, kind: null, eventId: null }, { exchange, loadScript: () => Promise.resolve(false) });
    expect(out).toEqual({ ok: true });
  });

  it("answers a page without a code, and sends nothing", async () => {
    const exchange = vi.fn();
    expect(await enterMiniApp({ code: null, kind: null, eventId: null }, { exchange, loadScript: () => Promise.resolve(true) })).toEqual({
      ok: false,
      key: "tgEdit.error.missing",
    });
    expect(await enterMiniApp(null, { exchange, loadScript: () => Promise.resolve(true) })).toEqual({ ok: false, key: "tgEdit.error.missing" });
    expect(exchange).not.toHaveBeenCalled();
  });

  it("says the link is spent in the mini app's own words", async () => {
    const refused = new ApiError(400, "auth.web_link_invalid", "no");
    expect(tgEditErrorKey(refused)).toBe("tgEdit.error.invalid");
    const out = await enterMiniApp(
      { code: CODE, kind: null, eventId: null },
      { exchange: () => Promise.reject(refused), loadScript: () => Promise.resolve(true) },
    );
    expect(out).toEqual({ ok: false, key: "tgEdit.error.invalid" });
    expect(tgEditErrorKey(new ApiError(403, "auth.web_link_superadmin", "no"))).toBe("linkLogin.error.superadmin");
    expect(tgEditErrorKey(new ApiError(0, "", "offline"))).toBe("common.error.network");
    expect(tgEditErrorKey(new Error("boom"))).toBe("common.error.generic");
  });
});

describe("closing", () => {
  it("calls Telegram.WebApp.close() when it is there", () => {
    const close = vi.fn();
    expect(closeMiniApp({ Telegram: { WebApp: { close } } })).toBe(true);
    expect(close).toHaveBeenCalledOnce();
  });

  it("reports false in a plain browser, so the page goes back to the app", () => {
    expect(closeMiniApp({})).toBe(false);
    expect(closeMiniApp({ Telegram: { WebApp: {} } })).toBe(false);
  });
});

// The Telegram script is a pinned copy: its name carries the first 8 hex digits
// of its sha256 and the README names the whole hash. A changed file, or a
// hash that was not updated, fails here.
describe("the vendored Telegram script", () => {
  const root = path.resolve(__dirname, "../..");
  const file = path.join(root, "public", TELEGRAM_SCRIPT_SRC);
  const sha = createHash("sha256").update(readFileSync(file)).digest("hex");

  it("has the hash its file name and the README record", () => {
    expect(path.basename(file)).toBe(`telegram-web-app.${sha.slice(0, 8)}.js`);
    expect(readFileSync(path.join(root, "README.md"), "utf8")).toContain(sha);
  });

  it("is a script that needs no other host", () => {
    const text = readFileSync(file, "utf8");
    expect(text).toContain("Telegram.WebApp");
    expect(text).not.toMatch(/<script/i);
  });
});
