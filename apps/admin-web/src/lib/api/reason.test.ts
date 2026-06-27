/**
 * Unit tests for SAUI-04 audit-reason wiring.
 *
 * Covers:
 *   - requiresAdminReason() path predicate
 *   - active-reason store: read/write/clear + sessionStorage persistence
 *   - subscribeReason() pub-sub
 *   - resolveReasonFor() resolver registration + fallback paths
 *   - X-Admin-Reason header injection in the API client
 *   - missing_reason retry path: clears stored reason, prompts again,
 *     retries once with the new reason
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  __TEST_ONLY_resetReason,
  MISSING_REASON_CODE,
  clearActiveReason,
  getActiveReason,
  requiresAdminReason,
  resolveReasonFor,
  setActiveReason,
  setReasonResolver,
  subscribeReason,
} from "@/lib/api/reason";
import {
  __TEST_ONLY_resetTokenStore,
  setSession,
} from "@/lib/api/tokenStore";
import { authedFetch } from "@/lib/api/client";

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

function authedSession(): void {
  setSession({
    accessToken: "access-1",
    refreshToken: "refresh-1",
    expiresAt: new Date(Date.now() + 60_000).toISOString(),
    userId: "user-1",
  });
}

beforeEach(() => {
  __TEST_ONLY_resetReason();
  __TEST_ONLY_resetTokenStore();
  vi.restoreAllMocks();
});

afterEach(() => {
  __TEST_ONLY_resetReason();
  __TEST_ONLY_resetTokenStore();
});

describe("requiresAdminReason()", () => {
  it.each([
    ["/v1/admin/organizations", true],
    ["/v1/admin/organizations?limit=50", true],
    ["/v1/admin/orders", true],
    ["/v1/admin/orders/01HXYZ", true],
    ["/v1/admin/tickets", true],
    ["/v1/admin/refunds", true],
    ["/v1/admin/impersonate", true],
    ["/v1/admin/geo/countries", false],
    ["/v1/admin/geo", false],
    ["/v1/me", false],
    // No method supplied -> mutation-only prefixes are NOT matched,
    // mirroring the pre-SAUI-09 "would a GET need a reason?" predicate.
    ["/v1/operator-networks", false],
    ["/v1/admin/networks/abc/users", false],
    ["/v1/auth/login", false],
    ["/v1/admin", false],
    ["/", false],
  ])("path %s -> %s", (path, expected) => {
    expect(requiresAdminReason(path)).toBe(expected);
  });

  // SAUI-09: operator-network and network-roster mutations need a
  // reason; GETs to the same prefixes do not.
  it.each([
    ["/v1/operator-networks", "POST", true],
    ["/v1/operator-networks/abc", "PATCH", true],
    ["/v1/operator-networks/abc/archive", "POST", true],
    ["/v1/admin/networks/abc/users", "POST", true],
    ["/v1/admin/networks/abc/users/def", "DELETE", true],
    ["/v1/admin/networks/abc/organizers", "POST", true],
    ["/v1/admin/networks/abc/agents/def", "DELETE", true],
    // GETs to the same prefixes are read-only and not gated.
    ["/v1/operator-networks", "GET", false],
    ["/v1/operator-networks/abc", "GET", false],
    ["/v1/admin/networks/abc/users", "GET", false],
    ["/v1/admin/networks/abc/organizers", "GET", false],
    // Method case is normalised.
    ["/v1/operator-networks", "post", true],
    // Superadmin read prefixes always match regardless of method.
    ["/v1/admin/organizations", "GET", true],
    ["/v1/admin/organizations", "POST", true],
  ])("path %s + method %s -> %s", (path, method, expected) => {
    expect(requiresAdminReason(path, method)).toBe(expected);
  });
});

describe("active-reason store", () => {
  it("starts empty when nothing is persisted", () => {
    expect(getActiveReason()).toBeNull();
  });

  it("round-trips a reason through sessionStorage", () => {
    setActiveReason("Investigating support ticket #4827");
    expect(getActiveReason()).toBe("Investigating support ticket #4827");
    expect(sessionStorage.getItem("arena.admin.adminReason")).toBe(
      "Investigating support ticket #4827",
    );
  });

  it("collapses empty/whitespace input to null", () => {
    setActiveReason("   ");
    expect(getActiveReason()).toBeNull();
    expect(sessionStorage.getItem("arena.admin.adminReason")).toBeNull();
  });

  it("clearActiveReason() removes the persisted value", () => {
    setActiveReason("temp");
    clearActiveReason();
    expect(getActiveReason()).toBeNull();
    expect(sessionStorage.getItem("arena.admin.adminReason")).toBeNull();
  });

  it("notifies subscribers on change", () => {
    const calls: (string | null)[] = [];
    const unsub = subscribeReason((r) => calls.push(r));
    // subscribeReason fires once on subscribe with current state.
    expect(calls).toEqual([null]);
    setActiveReason("first");
    setActiveReason("second");
    setActiveReason("second"); // no-op (idempotent)
    clearActiveReason();
    expect(calls).toEqual([null, "first", "second", null]);
    unsub();
    setActiveReason("after-unsub");
    expect(calls).toEqual([null, "first", "second", null]);
  });
});

describe("resolveReasonFor()", () => {
  it("returns the persisted reason when no resolver is registered", async () => {
    setActiveReason("pre-seeded");
    await expect(resolveReasonFor("/v1/admin/orders")).resolves.toBe(
      "pre-seeded",
    );
  });

  it("rejects when no resolver and no persisted reason", async () => {
    await expect(resolveReasonFor("/v1/admin/orders")).rejects.toThrow(
      /no admin reason resolver/i,
    );
  });

  it("delegates to the registered resolver and trims its output", async () => {
    setReasonResolver(async () => "   resolver-supplied   ");
    await expect(resolveReasonFor("/v1/admin/orders")).resolves.toBe(
      "resolver-supplied",
    );
  });

  it("rejects when the resolver returns empty", async () => {
    setReasonResolver(async () => "   ");
    await expect(resolveReasonFor("/v1/admin/orders")).rejects.toThrow(
      /empty/,
    );
  });
});

describe("authedFetch() X-Admin-Reason injection", () => {
  it("adds the header for cross-tenant paths and omits it elsewhere", async () => {
    authedSession();
    setActiveReason("Audit trail demo");

    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(mockResponse({ status: 200, body: { ok: true } }))
      .mockResolvedValueOnce(mockResponse({ status: 200, body: { ok: true } }));
    vi.stubGlobal("fetch", fetchMock);

    await authedFetch({ method: "GET", path: "/v1/admin/organizations" });
    await authedFetch({ method: "GET", path: "/v1/operator-networks" });

    const reqA = fetchMock.mock.calls[0]?.[1] as RequestInit;
    const reqB = fetchMock.mock.calls[1]?.[1] as RequestInit;
    expect((reqA.headers as Record<string, string>)["X-Admin-Reason"]).toBe(
      "Audit trail demo",
    );
    expect(
      (reqB.headers as Record<string, string>)["X-Admin-Reason"],
    ).toBeUndefined();
  });

  it("fails fast with superadmin.reason_required when no reason is available", async () => {
    authedSession();
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    await expect(
      authedFetch({ method: "GET", path: "/v1/admin/orders" }),
    ).rejects.toMatchObject({
      code: "superadmin.reason_required",
      status: 0,
    });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("prompts via the registered resolver when no reason is cached", async () => {
    authedSession();
    let promptedPath: string | null = null;
    setReasonResolver(async (path) => {
      promptedPath = path;
      return "Operator decided to look";
    });
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(mockResponse({ status: 200, body: { ok: true } }));
    vi.stubGlobal("fetch", fetchMock);

    await authedFetch({ method: "GET", path: "/v1/admin/refunds" });

    expect(promptedPath).toBe("/v1/admin/refunds");
    const req = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect((req.headers as Record<string, string>)["X-Admin-Reason"]).toBe(
      "Operator decided to look",
    );
  });

  it("on superadmin.missing_reason: clears cache, re-prompts, retries once", async () => {
    authedSession();
    setActiveReason("stale-reason");

    // Mirror the real ReasonContext resolver behaviour: short-circuit
    // when an active reason is cached, only "prompt" when it is not.
    let promptCalls = 0;
    setReasonResolver(async () => {
      const cached = getActiveReason();
      if (cached !== null) {
        return cached;
      }
      promptCalls += 1;
      return `fresh-reason-${promptCalls}`;
    });

    const fetchMock = vi
      .fn()
      // First attempt -> backend rejects the stale reason.
      .mockResolvedValueOnce(
        mockResponse({
          status: 400,
          body: errorEnvelope(MISSING_REASON_CODE, "reason expired"),
        }),
      )
      // Retry with fresh reason -> success.
      .mockResolvedValueOnce(mockResponse({ status: 200, body: { ok: true } }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await authedFetch<{ ok: boolean }>({
      method: "GET",
      path: "/v1/admin/tickets",
    });

    expect(result).toEqual({ ok: true });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(promptCalls).toBe(1);
    const firstReq = fetchMock.mock.calls[0]?.[1] as RequestInit;
    const retryReq = fetchMock.mock.calls[1]?.[1] as RequestInit;
    expect(
      (firstReq.headers as Record<string, string>)["X-Admin-Reason"],
    ).toBe("stale-reason");
    expect(
      (retryReq.headers as Record<string, string>)["X-Admin-Reason"],
    ).toBe("fresh-reason-1");
    // Cached reason updated to the fresh value (the resolver persisted it
    // via the React layer; here we just verify the API path replaced
    // whatever was there).
    // Resolver itself does not write to the store, so we expect the
    // cleared state.
    expect(getActiveReason()).toBeNull();
  });

  it("does NOT retry the missing-reason path more than once", async () => {
    authedSession();
    setActiveReason("seeded");

    // Mirror the real ReasonContext resolver: cached reason short-circuits.
    let promptCalls = 0;
    setReasonResolver(async () => {
      const cached = getActiveReason();
      if (cached !== null) {
        return cached;
      }
      promptCalls += 1;
      return `fresh-${promptCalls}`;
    });

    // Use mockImplementation so each call gets a fresh Response (the
    // body of a Response can only be consumed once).
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(
        mockResponse({
          status: 400,
          body: errorEnvelope(MISSING_REASON_CODE, "still bad"),
        }),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(
      authedFetch({ method: "GET", path: "/v1/admin/orders" }),
    ).rejects.toMatchObject({ code: MISSING_REASON_CODE });
    // Exactly two attempts: original + one retry. The original used the
    // pre-seeded reason; the retry triggered a single prompt after the
    // cache was cleared.
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(promptCalls).toBe(1);
  });
});
