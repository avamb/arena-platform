import { afterEach, describe, expect, it, vi } from "vitest";
import { createSession, loginWithTelegramMiniapp, useSessionForTests } from "@/lib/api/http";
import type { SessionStore, Stored } from "@/lib/api/tabSession";

function memoryStore(): SessionStore & { value: Stored | null } {
  const s = {
    value: null as Stored | null,
    get: () => s.value,
    set: (v: Stored | null) => {
      s.value = v;
    },
  };
  return s;
}

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

const OK = {
  access_token: "a1",
  refresh_token: "r1",
  token_type: "Bearer",
  expires_at: new Date(Date.now() + 900_000).toISOString(),
  user_id: "u1",
  next: "/o/x/events",
};

afterEach(() => vi.unstubAllGlobals());

describe("loginWithTelegramMiniapp", () => {
  it("posts the launch data and next, keeps the session like a login and answers next", async () => {
    const store = memoryStore();
    const s = createSession({ store, bus: null, locks: null });
    useSessionForTests(s);
    const fetchMock = vi.fn(async (_url: string, _init: RequestInit) => json(200, OK));
    vi.stubGlobal("fetch", fetchMock);
    const next = await loginWithTelegramMiniapp("query_id=A&hash=h1", "/o/x/seats");
    expect(next).toBe("/o/x/events");
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(new URL(url).pathname).toBe("/v1/auth/telegram-miniapp");
    expect(JSON.parse(String(init.body))).toEqual({ init_data: "query_id=A&hash=h1", next: "/o/x/seats" });
    expect((init.headers as Record<string, string>).Authorization).toBeUndefined();
    expect(s.hasSession()).toBe(true);
    expect(store.value?.refreshToken).toBe("r1");
  });

  it("omits next when there is none and makes one exchange for the same launch data (StrictMode)", async () => {
    useSessionForTests(createSession({ store: memoryStore(), bus: null, locks: null }));
    const fetchMock = vi.fn(async (_url: string, _init: RequestInit) => json(200, OK));
    vi.stubGlobal("fetch", fetchMock);
    const [a, b] = await Promise.all([loginWithTelegramMiniapp("d2", null), loginWithTelegramMiniapp("d2", null)]);
    expect(a).toBe(b);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(JSON.parse(String((fetchMock.mock.calls[0] as [string, RequestInit])[1].body))).toEqual({ init_data: "d2" });
  });

  it("rejects with the server's code and lets the person try again", async () => {
    const s = createSession({ store: memoryStore(), bus: null, locks: null });
    useSessionForTests(s);
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(json(403, { error: { code: "auth.telegram_not_linked", message: "no" } }))
      .mockResolvedValueOnce(json(200, OK));
    vi.stubGlobal("fetch", fetchMock);
    await expect(loginWithTelegramMiniapp("d3", null)).rejects.toMatchObject({ status: 403, code: "auth.telegram_not_linked" });
    expect(s.hasSession()).toBe(false);
    await expect(loginWithTelegramMiniapp("d3", null)).resolves.toBe("/o/x/events");
  });
});
