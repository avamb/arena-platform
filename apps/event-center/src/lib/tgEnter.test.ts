import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/http";
import { CATALOGS } from "@/lib/i18n";

const enMessages = CATALOGS.en;
import {
  TG_HOME,
  enterTelegram,
  markEnteredThroughTelegram,
  parseTgEnterSearch,
  resetTelegramEntry,
  safeNext,
  tgEnterErrorKey,
  wasEnteredThroughTelegram,
  type TgEnterDeps,
} from "@/lib/tgEnter";

const ORG = "11111111-2222-3333-4444-555555555555";
const SEATS = `/o/${ORG}/events/aaaaaaaa-0000-0000-0000-000000000001/sessions/bbbbbbbb-0000-0000-0000-000000000002/seats?embed=1`;

describe("safeNext", () => {
  it("accepts an event-center path of an organization, with a query", () => {
    expect(safeNext(SEATS)).toBe(SEATS);
    expect(safeNext(`/o/${ORG}`)).toBe(`/o/${ORG}`);
    expect(safeNext(`/o/${ORG}/events/`)).toBe(`/o/${ORG}/events/`);
    expect(safeNext(`/o/${ORG.toUpperCase()}/orders`)).toBe(`/o/${ORG.toUpperCase()}/orders`);
  });

  it.each([
    "",
    "/",
    "/orgs",
    "/o/",
    "/o/not-a-uuid/events",
    `//evil.example/o/${ORG}`,
    `https://evil.example/o/${ORG}/events`,
    `/\evil.example`,
    `/o/${ORG}/../../etc`,
    `/o/${ORG}/./events`,
    `/o/${ORG}/%2e%2e/events`,
    `/o/${ORG}/events#frag`,
    `/o/${ORG}/events?x=<script>`,
    `/o/${ORG}/events?x=1 2`,
    `javascript:alert(1)`,
    `/o/${ORG}/` + "a".repeat(300),
  ])("drops %j", (raw) => {
    expect(safeNext(raw)).toBe("");
  });

  it("drops what is not text", () => {
    expect(safeNext(undefined)).toBe("");
    expect(safeNext(42)).toBe("");
    expect(safeNext(["/o/" + ORG])).toBe("");
  });
});

describe("parseTgEnterSearch", () => {
  it("keeps a safe next and drops the rest, never failing", () => {
    expect(parseTgEnterSearch({ next: SEATS })).toEqual({ next: SEATS });
    expect(parseTgEnterSearch({ next: "https://evil.example" })).toEqual({});
    expect(parseTgEnterSearch({})).toEqual({});
  });
});

describe("tgEnterErrorKey", () => {
  const api = (status: number, code: string) => new ApiError(status, code, "m");
  it.each([
    [api(401, "auth.telegram_init_data_invalid"), "tgEnter.error.invalid"],
    [api(401, "auth.telegram_init_data_expired"), "tgEnter.error.expired"],
    [api(403, "auth.telegram_not_linked"), "tgEnter.error.notLinked"],
    [api(403, "auth.user_deactivated"), "login.error.deactivated"],
    [api(403, "auth.web_link_superadmin"), "linkLogin.error.superadmin"],
    [api(429, "auth.rate_limited"), "login.error.rateLimited"],
    [api(503, "auth.telegram_miniapp_not_configured"), "tgEnter.error.unavailable"],
    [api(503, "dependency.database_unavailable"), "tgEnter.error.unavailable"],
    [api(0, "network.failure"), "common.error.network"],
    [api(500, "internal"), "common.error.generic"],
    [new Error("boom"), "common.error.generic"],
  ])("maps %j to %s", (err, key) => {
    expect(tgEnterErrorKey(err)).toBe(key);
  });

  it("has a plain text for every key and shows no technical code", () => {
    for (const key of [
      "tgEnter.error.invalid",
      "tgEnter.error.expired",
      "tgEnter.error.notLinked",
      "tgEnter.error.unavailable",
      "tgEnter.error.outside",
    ] as const) {
      expect(enMessages[key]).not.toMatch(/auth\.|init_?data|HTTP|\b40\d\b|\b50\d\b/i);
    }
  });
});

function deps(over: Partial<TgEnterDeps> = {}): TgEnterDeps & { exchange: ReturnType<typeof vi.fn> } {
  return {
    loadScript: vi.fn(async () => true),
    initData: () => "query_id=AAH&user=%7B%7D&hash=abc",
    exchange: vi.fn(async () => SEATS),
    ...over,
  } as TgEnterDeps & { exchange: ReturnType<typeof vi.fn> };
}

describe("enterTelegram", () => {
  it("loads the script, sends the launch data and our next, and opens the server's next", async () => {
    const d = deps();
    const out = await enterTelegram(SEATS, d);
    expect(out).toEqual({ ok: true, to: SEATS });
    expect(d.loadScript).toHaveBeenCalledTimes(1);
    expect(d.exchange).toHaveBeenCalledWith("query_id=AAH&user=%7B%7D&hash=abc", SEATS);
  });

  it("sends no next when the address had none or an unsafe one, and opens the organizations on an empty answer", async () => {
    const d = deps({ exchange: vi.fn(async () => "") });
    expect(await enterTelegram("https://evil.example", d)).toEqual({ ok: true, to: TG_HOME });
    expect(d.exchange).toHaveBeenCalledWith(expect.any(String), null);
    const d2 = deps({ exchange: vi.fn(async () => "") });
    expect(await enterTelegram(undefined, d2)).toEqual({ ok: true, to: TG_HOME });
  });

  it("never opens an unsafe path, even if the server's answer were one", async () => {
    const d = deps({ exchange: vi.fn(async () => "https://evil.example/o/x") });
    expect(await enterTelegram(SEATS, d)).toEqual({ ok: true, to: TG_HOME });
  });

  it("says the page opens from the bot when Telegram gave no launch data, and calls nothing", async () => {
    const d = deps({ initData: () => "" });
    expect(await enterTelegram(SEATS, d)).toEqual({ ok: false, key: "tgEnter.error.outside" });
    expect(d.exchange).not.toHaveBeenCalled();
  });

  it("still tries to sign in when the script file did not load", async () => {
    const d = deps({ loadScript: vi.fn(async () => false) });
    expect((await enterTelegram(SEATS, d)).ok).toBe(true);
  });

  it.each([
    [401, "auth.telegram_init_data_invalid", "tgEnter.error.invalid"],
    [401, "auth.telegram_init_data_expired", "tgEnter.error.expired"],
    [403, "auth.telegram_not_linked", "tgEnter.error.notLinked"],
    [403, "auth.user_deactivated", "login.error.deactivated"],
    [403, "auth.web_link_superadmin", "linkLogin.error.superadmin"],
    [429, "auth.rate_limited", "login.error.rateLimited"],
    [503, "auth.telegram_miniapp_not_configured", "tgEnter.error.unavailable"],
  ])("turns the refusal %i %s into the text %s", async (status, code, key) => {
    const d = deps({
      exchange: vi.fn(async () => {
        throw new ApiError(status, code, "m");
      }),
    });
    expect(await enterTelegram(SEATS, d)).toEqual({ ok: false, key });
  });
});

describe("the entry flag", () => {
  it("is off until a sign-in through /tg marks it", () => {
    resetTelegramEntry();
    expect(wasEnteredThroughTelegram()).toBe(false);
    markEnteredThroughTelegram();
    expect(wasEnteredThroughTelegram()).toBe(true);
    resetTelegramEntry();
  });
});
