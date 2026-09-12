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
    ["/v1/admin/users", true],
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
    // W1-A1e (#474): the gateway-credential path is gated regardless of
    // method, including a bare no-method check.
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/channels/22222222-2222-2222-2222-222222222222/gateway-credential",
      true,
    ],
    // W1-C1c (#514): the api-keys sub-resource is gated regardless of
    // method, including a bare no-method check and a sub-path (revoke).
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/api-keys",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/api-keys/33333333-3333-3333-3333-333333333333",
      true,
    ],
  ])("path %s -> %s", (path, expected) => {
    expect(requiresAdminReason(path)).toBe(expected);
  });

  // SAUI-09: operator-network and network-roster mutations need a
  // reason; GETs to the same prefixes do not.
  it.each([
    ["/v1/operator-networks", "POST", true],
    ["/v1/operator-networks/abc", "PATCH", true],
    ["/v1/operator-networks/abc/archive", "POST", true],
    ["/v1/admin/users", "POST", true],
    ["/v1/admin/users", "GET", true],
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
    // #534: cross-tenant org-scoped resource access (venues / channels /
    // payment-configs / members) requires a reason on every method,
    // including GET -- originally (SAUI-14 / #246) this was mutation-only,
    // but the backend now requires the header on org-scoped GETs too once
    // the superadmin bypass (#531/#532) is granted, so gating only
    // mutations left every read tab of the organization drawer stuck in a
    // "missing_reason ... Retry" loop for a real superadmin.
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/venues",
      "POST",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/venues/22222222-2222-2222-2222-222222222222",
      "PATCH",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/venues/22222222-2222-2222-2222-222222222222",
      "DELETE",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/channels",
      "POST",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/channels/abc",
      "PATCH",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/channels/abc",
      "DELETE",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/payment-configs",
      "POST",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/payment-configs/abc",
      "PATCH",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/payment-configs/abc",
      "DELETE",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/members",
      "POST",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/members/abc",
      "PATCH",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/members/abc",
      "DELETE",
      true,
    ],
    // Wave O / feature #256 — banking coordinate mutations.
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/bank-accounts",
      "POST",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/bank-accounts/abc",
      "PATCH",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/bank-accounts/abc",
      "DELETE",
      true,
    ],
    // Bank-account reads are now gated too (#534).
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/bank-accounts",
      "GET",
      true,
    ],
    // Reads on the same paths are gated (#534) -- the drawer's read tabs
    // need the header once the backend enforces the superadmin bypass.
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/venues",
      "GET",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/channels",
      "GET",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/payment-configs",
      "GET",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/members",
      "GET",
      true,
    ],
    // The org root itself is NOT in the new regex set so plain
    // organization PATCH/DELETE still go through the existing
    // /v1/admin/organizations admin path.
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111",
      "PATCH",
      false,
    ],
    // #534: events/sessions/customers/orders/imports under an org are now
    // gated on every method too.
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/events",
      "POST",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/events",
      "GET",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/sessions",
      "GET",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/customers",
      "GET",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/orders",
      "GET",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/imports",
      "POST",
      true,
    ],
    // Query strings are stripped before matching.
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/venues?limit=20",
      "POST",
      true,
    ],
    // W1-A1e (#474): the gateway-credential sub-resource requires
    // X-Admin-Reason on EVERY verb, including GET -- the summary read
    // exposes rotation metadata and is treated as a sensitive admin
    // action per spec (also now implied by the channels-prefix regex).
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/channels/22222222-2222-2222-2222-222222222222/gateway-credential",
      "GET",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/channels/22222222-2222-2222-2222-222222222222/gateway-credential",
      "PUT",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/channels/22222222-2222-2222-2222-222222222222/gateway-credential",
      "DELETE",
      true,
    ],
    // The plain channel list/detail path is now gated on every method too
    // (#534), including GET -- a sibling sub-resource under the same
    // channel (gateway-credential) was already gated before this change.
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/channels/22222222-2222-2222-2222-222222222222",
      "GET",
      true,
    ],
    // W1-C1c (#514): api-keys requires X-Admin-Reason on every verb,
    // including the list GET.
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/api-keys",
      "GET",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/api-keys",
      "POST",
      true,
    ],
    [
      "/v1/organizations/11111111-1111-1111-1111-111111111111/api-keys/33333333-3333-3333-3333-333333333333",
      "DELETE",
      true,
    ],
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

  it("on superadmin.missing_reason WITH a cached reason: retries once with the cached value, no prompt (#534)", async () => {
    authedSession();
    setActiveReason("cached-reason");
    let promptCalls = 0;
    setReasonResolver(async () => {
      promptCalls += 1;
      return "should-not-be-used";
    });

    const fetchMock = vi
      .fn()
      // First attempt -> a path/method combo requiresAdminReason() does
      // not (yet) recognise, so no header was attached; backend rejects.
      .mockResolvedValueOnce(
        mockResponse({
          status: 400,
          body: errorEnvelope(MISSING_REASON_CODE, "reason required"),
        }),
      )
      // Auto-retry with the cached reason -> success.
      .mockResolvedValueOnce(mockResponse({ status: 200, body: { ok: true } }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await authedFetch<{ ok: boolean }>({
      method: "GET",
      path: "/v1/organizations/11111111-1111-1111-1111-111111111111/not-yet-gated",
    });

    expect(result).toEqual({ ok: true });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    // The resolver (interactive prompt) must never fire when a reason is
    // already cached -- the retry is fully automatic.
    expect(promptCalls).toBe(0);
    const firstReq = fetchMock.mock.calls[0]?.[1] as RequestInit;
    const retryReq = fetchMock.mock.calls[1]?.[1] as RequestInit;
    expect(
      (firstReq.headers as Record<string, string>)["X-Admin-Reason"],
    ).toBeUndefined();
    expect(
      (retryReq.headers as Record<string, string>)["X-Admin-Reason"],
    ).toBe("cached-reason");
    // The cached reason is untouched by a successful auto-retry.
    expect(getActiveReason()).toBe("cached-reason");
  });

  it("on superadmin.missing_reason with NO cached reason: clears cache, prompts, retries once", async () => {
    authedSession();

    let promptCalls = 0;
    setReasonResolver(async () => {
      promptCalls += 1;
      return `fresh-reason-${promptCalls}`;
    });

    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        mockResponse({
          status: 400,
          body: errorEnvelope(MISSING_REASON_CODE, "reason expired"),
        }),
      )
      .mockResolvedValueOnce(mockResponse({ status: 200, body: { ok: true } }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await authedFetch<{ ok: boolean }>({
      method: "GET",
      path: "/v1/organizations/11111111-1111-1111-1111-111111111111/not-yet-gated",
    });

    expect(result).toEqual({ ok: true });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(promptCalls).toBe(1);
    const retryReq = fetchMock.mock.calls[1]?.[1] as RequestInit;
    expect(
      (retryReq.headers as Record<string, string>)["X-Admin-Reason"],
    ).toBe("fresh-reason-1");
  });

  it("does NOT retry the missing-reason path more than once (cached-reason branch)", async () => {
    authedSession();
    setActiveReason("seeded");

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
    // Exactly two attempts: original + one automatic retry with the
    // cached reason. No third attempt even though the retry also failed.
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(getActiveReason()).toBe("seeded");
  });

  it("does NOT retry the missing-reason path more than once (no-cached-reason branch)", async () => {
    authedSession();

    let promptCalls = 0;
    setReasonResolver(async () => {
      promptCalls += 1;
      return `fresh-${promptCalls}`;
    });

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
      authedFetch({
        method: "GET",
        path: "/v1/organizations/11111111-1111-1111-1111-111111111111/not-yet-gated",
      }),
    ).rejects.toMatchObject({ code: MISSING_REASON_CODE });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(promptCalls).toBe(1);
  });
});
