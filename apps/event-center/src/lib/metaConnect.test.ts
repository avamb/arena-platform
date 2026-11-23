/** The Meta connect flow without a browser: the redirects are recorded. */
import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/http";
import { CATALOGS, LOCALES } from "@/lib/i18n";
import {
  META_FINISH_KEY,
  META_REASONS,
  captureMetaFinish,
  takeMetaFinish,
  REASON_KEY,
  finishReason,
  metaReturn,
  parseMetaSearch,
  readFinishHash,
  startRedirect,
  type MetaStart,
} from "@/lib/metaConnect";

const start: MetaStart = { app_id: "123", config_id: "cfg9", graph_version: "v25.0", login_url: "https://www.facebook.com/dialog/oauth?x=1" };

describe("startRedirect", () => {
  it("sends the browser to login_url", () => {
    const navigate = vi.fn();
    expect(startRedirect(start, navigate)).toBe("redirected");
    expect(navigate).toHaveBeenCalledWith(start.login_url);
  });
  it("does nothing when the server has no login_url", () => {
    const navigate = vi.fn();
    expect(startRedirect({ ...start, login_url: "" }, navigate)).toBe("notConfigured");
    expect(navigate).not.toHaveBeenCalled();
  });
});

describe("the address after the callback", () => {
  it("reads the finish and every known reason", () => {
    expect(metaReturn("finish", undefined)).toEqual({ kind: "finish" });
    for (const r of META_REASONS) expect(metaReturn("error", r)).toEqual({ kind: "error", reason: r });
  });
  it("turns an unknown reason into the general message and ignores other values", () => {
    expect(metaReturn("error", "weird")).toEqual({ kind: "error", reason: "unknown" });
    expect(metaReturn("error", undefined)).toEqual({ kind: "error", reason: "unknown" });
    expect(metaReturn(undefined, "denied")).toBeNull();
    expect(metaReturn("connected", undefined)).toBeNull();
    expect(metaReturn("other", "denied")).toBeNull();
  });
  it("keeps only the two search keys", () => {
    expect(parseMetaSearch({ meta: "error", reason: "denied", x: 1 })).toEqual({ meta: "error", reason: "denied" });
    expect(parseMetaSearch({ meta: 5 })).toEqual({});
  });
  it("has a text for every reason in every language", () => {
    for (const locale of LOCALES) {
      const texts = Object.values(REASON_KEY).map((k) => CATALOGS[locale][k]);
      expect(new Set(texts).size).toBe(texts.length);
    }
  });
});

describe("the fragment and the finish answer", () => {
  it("reads code and state from the fragment", () => {
    expect(readFinishHash("#code=AQ%2Bx&state=p.s")).toEqual({ code: "AQ+x", state: "p.s" });
    expect(readFinishHash("code=c&state=s")).toEqual({ code: "c", state: "s" });
    expect(readFinishHash("#code=c")).toBeNull();
    expect(readFinishHash("")).toBeNull();
  });
  it("maps the error envelope code to a reason", () => {
    expect(finishReason(new ApiError(403, "meta.state_mismatch", "x"))).toBe("state_mismatch");
    expect(finishReason(new ApiError(502, "meta.exchange_failed", "x"))).toBe("exchange_failed");
    expect(finishReason(new ApiError(400, "meta.invalid_body", "x"))).toBe("unknown");
    expect(finishReason(new ApiError(500, "http.500", "x"))).toBe("unknown");
    expect(finishReason(new Error("x"))).toBe("unknown");
  });
});

describe("captureMetaFinish", () => {
  function run(pathname: string, search: string, hash: string, storage?: { setItem: (k: string, v: string) => void }) {
    const replaceState = vi.fn();
    const store = new Map<string, string>();
    const st = storage ?? { setItem: (k: string, v: string) => void store.set(k, v) };
    captureMetaFinish({ pathname, search, hash }, { state: null, replaceState }, () => st);
    return { replaceState, store };
  }

  it("stores code, state and org and removes the fragment from the address", () => {
    const { replaceState, store } = run("/o/org1/ads/meta", "?meta=finish", "#code=C1&state=S1");
    expect(JSON.parse(store.get(META_FINISH_KEY) ?? "null")).toEqual({ code: "C1", state: "S1", org: "org1" });
    expect(replaceState).toHaveBeenCalledWith(null, "", "/o/org1/ads/meta?meta=finish");
  });

  it("any other fragment with a code on that path is only removed", () => {
    const a = run("/o/org1/ads/meta", "?x=1", "#code=C1&state=S1");
    expect(a.store.size).toBe(0);
    expect(a.replaceState).toHaveBeenCalledWith(null, "", "/o/org1/ads/meta?x=1");
    const b = run("/o/org1/ads/meta", "?meta=finish", "#code=C1");
    expect(b.store.size).toBe(0);
    expect(b.replaceState).toHaveBeenCalledWith(null, "", "/o/org1/ads/meta?meta=finish");
  });

  it("other paths and fragments without a code are untouched", () => {
    expect(run("/login/link", "", "#code=C1&state=S1").replaceState).not.toHaveBeenCalled();
    expect(run("/o/org1/ads/meta", "?meta=finish", "").replaceState).not.toHaveBeenCalled();
    expect(run("/o/org1/ads/meta", "?meta=finish", "#top").replaceState).not.toHaveBeenCalled();
  });

  it("a throwing sessionStorage still removes the fragment", () => {
    const { replaceState } = run("/o/org1/ads/meta", "?meta=finish", "#code=C1&state=S1", {
      setItem: () => {
        throw new Error("denied");
      },
    });
    expect(replaceState).toHaveBeenCalledWith(null, "", "/o/org1/ads/meta?meta=finish");
  });
});

describe("takeMetaFinish", () => {
  it("reads once and deletes", () => {
    const m = new Map<string, string>([[META_FINISH_KEY, JSON.stringify({ code: "c", state: "s", org: "o" })]]);
    const st = { getItem: (k: string) => m.get(k) ?? null, removeItem: (k: string) => void m.delete(k) };
    expect(takeMetaFinish(() => st)).toEqual({ code: "c", state: "s", org: "o" });
    expect(takeMetaFinish(() => st)).toBeNull();
  });
  it("garbage or a throwing storage gives null", () => {
    const st = { getItem: () => "{nope", removeItem: () => undefined };
    expect(takeMetaFinish(() => st)).toBeNull();
    expect(
      takeMetaFinish(() => {
        throw new Error("denied");
      }),
    ).toBeNull();
  });
});
