/**
 * Review findings M1 and L7: sign-out revokes the server session even when
 * the access token has expired, and a network error during a refresh keeps
 * the person signed in.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { createSession, logout, refreshSession, useSessionForTests } from "@/lib/api/http";
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

interface Call {
  path: string;
  auth: string | null;
  body: Record<string, string>;
}

/** arena-api's auth routes: logout needs a VALID access token. */
function fakeApi(opts: { offline?: boolean } = {}) {
  const calls: Call[] = [];
  let valid = "a-new";
  const fetchMock = vi.fn(async (url: string, init: RequestInit) => {
    if (opts.offline) throw new TypeError("Failed to fetch");
    const path = new URL(url).pathname;
    const headers = init.headers as Record<string, string>;
    const body = JSON.parse(String(init.body ?? "{}")) as Record<string, string>;
    calls.push({ path, auth: headers.Authorization ?? null, body });
    if (path === "/v1/auth/refresh") {
      if (body.refresh_token !== "r-old") return json(401, { error: { code: "auth.invalid_refresh_token", message: "no" } });
      valid = "a-new";
      return json(200, { access_token: "a-new", refresh_token: "r-new", expires_at: new Date(Date.now() + 900_000).toISOString(), user_id: "u1" });
    }
    if (path === "/v1/auth/logout") {
      if (headers.Authorization !== `Bearer ${valid}`) return json(401, { error: { code: "auth.unauthenticated", message: "no" } });
      return new Response(null, { status: 204 });
    }
    return json(404, {});
  });
  vi.stubGlobal("fetch", fetchMock);
  return calls;
}

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

/** A session whose access token expired a minute ago. */
async function expiredSession() {
  const store = memoryStore();
  const s = createSession({ store, bus: null, locks: null });
  useSessionForTests(s);
  await s.signedIn({ accessToken: "a-expired", refreshToken: "r-old", expiresAt: new Date(Date.now() - 60_000).toISOString(), userId: "u1" });
  return { s, store };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("logout", () => {
  it("refreshes an expired access token and revokes the LATEST refresh token", async () => {
    const calls = fakeApi();
    const { s, store } = await expiredSession();
    await logout();
    expect(calls.map((c) => c.path)).toEqual(["/v1/auth/refresh", "/v1/auth/logout"]);
    expect(calls[1].auth).toBe("Bearer a-new");
    expect(calls[1].body.refresh_token).toBe("r-new");
    expect(store.value).toBeNull();
    expect(s.hasSession()).toBe(false);
  });

  it("clears the local session even when the API cannot be reached", async () => {
    fakeApi({ offline: true });
    const { s } = await expiredSession();
    await logout();
    expect(s.hasSession()).toBe(false);
  });
});

describe("refreshSession", () => {
  it("keeps the session on a network error", async () => {
    fakeApi({ offline: true });
    const { s, store } = await expiredSession();
    await expect(refreshSession()).rejects.toMatchObject({ status: 0, code: "network.failure" });
    expect(s.hasSession()).toBe(true);
    expect(store.value?.refreshToken).toBe("r-old");
  });

  it("signs out when the API refuses the refresh token", async () => {
    fakeApi();
    const { s, store } = await expiredSession();
    store.value = { ...(store.value as Stored), refreshToken: "r-revoked" };
    await expect(refreshSession()).rejects.toMatchObject({ status: 401 });
    expect(s.hasSession()).toBe(false);
  });
});
