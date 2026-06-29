/**
 * Unit tests for the typed API client + token store.
 *
 * Covers SAUI-02 step 9: "401 handling, refresh failure, missing permissions".
 *
 * These tests stub global.fetch directly rather than using msw -- the
 * client surface is small enough that a hand-rolled mock keeps the test
 * file tight and removes a runtime dependency from CI.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  __TEST_ONLY_resetTokenStore,
  getAccessToken,
  getRefreshToken,
  setSession,
} from "@/lib/api/tokenStore";
import { ApiError, authedFetch, fetchMe, login, logout, refresh } from "@/lib/api/client";

interface MockResponseInit {
  status: number;
  body?: unknown;
}

function mockResponse(init: MockResponseInit): Response {
  const text = init.body === undefined ? "" : JSON.stringify(init.body);
  return new Response(text, {
    status: init.status,
    headers: { "Content-Type": "application/json" },
  });
}

function errorEnvelope(code: string, message = "test error"): unknown {
  return {
    error: {
      code,
      message,
      request_id: "req_test",
      trace_id: "trace_test",
    },
  };
}

beforeEach(() => {
  __TEST_ONLY_resetTokenStore();
  vi.restoreAllMocks();
});

afterEach(() => {
  __TEST_ONLY_resetTokenStore();
});

describe("login()", () => {
  it("stores access + refresh tokens on success", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValueOnce(
        mockResponse({
          status: 200,
          body: {
            access_token: "access-1",
            refresh_token: "refresh-1",
            token_type: "Bearer",
            expires_at: new Date(Date.now() + 60_000).toISOString(),
            user_id: "user-uuid-1",
          },
        }),
      ),
    );

    await login({ email: "op@example.com", password: "hunter2" });

    expect(getAccessToken()).toBe("access-1");
    expect(getRefreshToken()).toBe("refresh-1");
  });

  it("rejects with ApiError carrying the server envelope on 401", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValueOnce(
        mockResponse({
          status: 401,
          body: errorEnvelope("auth.invalid_credentials", "bad password"),
        }),
      ),
    );

    await expect(
      login({ email: "op@example.com", password: "wrong" }),
    ).rejects.toMatchObject({
      name: "ApiError",
      status: 401,
      code: "auth.invalid_credentials",
      message: "bad password",
    });
    expect(getAccessToken()).toBeNull();
  });
});

describe("authedFetch() 401 handling", () => {
  it("transparently refreshes the access token and retries once", async () => {
    setSession({
      accessToken: "stale-access",
      refreshToken: "refresh-good",
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
      userId: "u",
    });

    const fetchMock = vi
      .fn()
      // First call -> 401 with envelope
      .mockResolvedValueOnce(
        mockResponse({ status: 401, body: errorEnvelope("auth.token_expired") }),
      )
      // Refresh -> 200
      .mockResolvedValueOnce(
        mockResponse({
          status: 200,
          body: {
            access_token: "new-access",
            token_type: "Bearer",
            expires_at: new Date(Date.now() + 60_000).toISOString(),
            user_id: "u",
          },
        }),
      )
      // Retry of the original request -> 200
      .mockResolvedValueOnce(mockResponse({ status: 200, body: { ok: true } }));
    vi.stubGlobal("fetch", fetchMock);

    const out = await authedFetch<{ ok: boolean }>({
      method: "GET",
      path: "/v1/me",
    });

    expect(out).toEqual({ ok: true });
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(getAccessToken()).toBe("new-access");
    // Retried request used the refreshed token.
    const lastCall = fetchMock.mock.calls[2];
    expect(lastCall).toBeDefined();
    const headers = (lastCall![1] as RequestInit).headers as Record<string, string>;
    expect(headers.Authorization).toBe("Bearer new-access");
  });

  it("clears session and rethrows the original 401 when refresh fails", async () => {
    setSession({
      accessToken: "stale-access",
      refreshToken: "refresh-bad",
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
      userId: "u",
    });

    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        mockResponse({ status: 401, body: errorEnvelope("auth.token_expired") }),
      )
      .mockResolvedValueOnce(
        mockResponse({
          status: 401,
          body: errorEnvelope("auth.refresh_invalid"),
        }),
      );
    vi.stubGlobal("fetch", fetchMock);

    await expect(
      authedFetch({ method: "GET", path: "/v1/me" }),
    ).rejects.toMatchObject({ status: 401, code: "auth.token_expired" });

    expect(getAccessToken()).toBeNull();
    expect(getRefreshToken()).toBeNull();
  });

  it("does not retry when there is no refresh token", async () => {
    setSession({
      accessToken: "stale-access",
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
      userId: "u",
    });

    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        mockResponse({ status: 401, body: errorEnvelope("auth.token_expired") }),
      );
    vi.stubGlobal("fetch", fetchMock);

    await expect(fetchMe()).rejects.toMatchObject({ status: 401 });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(getAccessToken()).toBeNull();
  });
});

describe("refresh()", () => {
  it("propagates ApiError on refresh failure", async () => {
    setSession({
      accessToken: "x",
      refreshToken: "rt",
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
      userId: "u",
    });
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce(
          mockResponse({ status: 401, body: errorEnvelope("auth.refresh_invalid") }),
        ),
    );
    await expect(refresh()).rejects.toBeInstanceOf(ApiError);
  });

  it("throws when no refresh token is present without calling fetch", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    await expect(refresh()).rejects.toMatchObject({ code: "auth.no_refresh_token" });
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("logout()", () => {
  it("clears the local session even when the server call fails", async () => {
    setSession({
      accessToken: "a",
      refreshToken: "r",
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
      userId: "u",
    });
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce(
          mockResponse({ status: 500, body: errorEnvelope("server.boom") }),
        ),
    );

    await logout();

    expect(getAccessToken()).toBeNull();
    expect(getRefreshToken()).toBeNull();
  });
});

describe("missing-permission surface", () => {
  it("propagates a 403 permissions.denied envelope as ApiError", async () => {
    setSession({
      accessToken: "a",
      refreshToken: "r",
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
      userId: "u",
    });
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce(
          mockResponse({
            status: 403,
            body: errorEnvelope("permissions.denied", "missing network.read"),
          }),
        ),
    );

    await expect(
      authedFetch({ method: "GET", path: "/v1/operator-networks" }),
    ).rejects.toMatchObject({
      status: 403,
      code: "permissions.denied",
      message: "missing network.read",
    });
  });

  it("surfaces network failures as ApiError code=network.failure", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockRejectedValueOnce(new TypeError("offline")),
    );
    await expect(
      login({ email: "u@example.com", password: "pw" }),
    ).rejects.toMatchObject({ code: "network.failure", status: 0 });
  });

  it("aborts hung requests with ApiError code=network.timeout", async () => {
    vi.useFakeTimers();
    try {
      vi.stubGlobal(
        "fetch",
        vi.fn((_url: string | URL | Request, init?: RequestInit) => {
          const signal = init?.signal;
          return new Promise<Response>((_resolve, reject) => {
            signal?.addEventListener("abort", () => {
              reject(Object.assign(new Error("aborted"), { name: "AbortError" }));
            });
          });
        }),
      );

      const promise = login({ email: "u@example.com", password: "pw" });
      void promise.catch(() => undefined);
      await vi.advanceTimersByTimeAsync(30_000);

      await expect(promise).rejects.toMatchObject({
        code: "network.timeout",
        status: 0,
      });
    } finally {
      vi.useRealTimers();
    }
  });
});
