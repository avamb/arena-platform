/** The Meta connect flow without a browser: the redirects are recorded. */
import { describe, expect, it, vi } from "vitest";
import { CATALOGS, LOCALES } from "@/lib/i18n";
import { META_REASONS, REASON_KEY, metaReturn, parseMetaSearch, startRedirect, type MetaStart } from "@/lib/metaConnect";

const start: MetaStart = { app_id: "123", config_id: "cfg9", graph_version: "v25.0", state: "S", login_url: "https://www.facebook.com/dialog/oauth?x=1" };

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
  it("reads the success and every known reason", () => {
    expect(metaReturn("connected", undefined)).toEqual({ kind: "connected" });
    for (const r of META_REASONS) expect(metaReturn("error", r)).toEqual({ kind: "error", reason: r });
  });
  it("turns an unknown reason into the general message and ignores other values", () => {
    expect(metaReturn("error", "weird")).toEqual({ kind: "error", reason: "unknown" });
    expect(metaReturn("error", undefined)).toEqual({ kind: "error", reason: "unknown" });
    expect(metaReturn(undefined, "denied")).toBeNull();
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
