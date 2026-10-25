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
