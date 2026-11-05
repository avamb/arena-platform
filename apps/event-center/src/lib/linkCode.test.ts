import { describe, expect, it } from "vitest";
import { LINK_PATH, captureLinkCode, forgetLinkCode, linkCode, linkNext, parseLinkCode, parseLinkNext, safeLinkNext } from "@/lib/linkCode";

function fakeHistory() {
  const calls: (string | URL | null | undefined)[] = [];
  return { calls, state: null, replaceState: (_d: unknown, _u: string, url?: string | URL | null) => calls.push(url) };
}

describe("link code", () => {
  it("reads the code of the fragment", () => {
    expect(parseLinkCode("#code=abc_-1")).toBe("abc_-1");
    expect(parseLinkCode("#code=")).toBeNull();
    expect(parseLinkCode("#other=1")).toBeNull();
    expect(parseLinkCode("#code=" + "x".repeat(129))).toBeNull();
  });

  it("takes the code out of the address of the link page only", () => {
    forgetLinkCode();
    const h = fakeHistory();
    captureLinkCode({ pathname: "/orgs", hash: "#code=abc" }, h);
    expect(h.calls).toEqual([]);
    expect(linkCode()).toBeNull();

    captureLinkCode({ pathname: LINK_PATH, hash: "#code=abc" }, h);
    expect(h.calls).toEqual([LINK_PATH]);
    expect(linkCode()).toBe("abc");
    forgetLinkCode();
    expect(linkCode()).toBeNull();
  });

  it("clears a fragment without a code too", () => {
    const h = fakeHistory();
    captureLinkCode({ pathname: LINK_PATH, hash: "#nonsense" }, h);
    expect(h.calls).toEqual([LINK_PATH]);
    expect(linkCode()).toBeNull();
  });
});

const ORG = "11111111-1111-4111-8111-111111111111";

describe("link next (spec 61, section 4.5)", () => {
  it("accepts the shop paths of an organization", () => {
    for (const path of [`/o/${ORG}/shop`, `/o/${ORG}/shop/orders`, `/o/${ORG}/shop/orders/ab12-CD_34`, `/o/${ORG}/shop/products/new`, `/o/${ORG}/shop/settings`]) {
      expect(safeLinkNext(path), path).toBe(path);
    }
  });

  it("rejects everything else", () => {
    const bad = [
      "//evil.com",
      `//evil.com/o/${ORG}/shop`,
      `https://evil.com/o/${ORG}/shop`,
      `http://localhost/o/${ORG}/shop`,
      "javascript:alert(1)",
      `/o/${ORG}/../x`,
      `/o/${ORG}/shop/../events`,
      `/o/${ORG}/shop/./orders`,
      `/o/${ORG}/shop/%2e%2e/orders`,
      `/o/${ORG}/events`,
      `/o/${ORG}/shopping`,
      `/o/${ORG}/shop/`,
      `/o/${ORG}/shop/orders?view=to_ship`,
      `/o/${ORG}/shop/orders#x`,
      `/o/${ORG}/shop\\orders`,
      `/o/${ORG}/shop/orders\n`,
      "/o/short/shop",
      `o/${ORG}/shop`,
      "/orgs",
      "/login",
      "",
      `/o/${ORG}/shop/${"a".repeat(300)}`,
    ];
    for (const path of bad) expect(safeLinkNext(path), JSON.stringify(path)).toBeNull();
    expect(safeLinkNext(undefined)).toBeNull();
    expect(safeLinkNext(null)).toBeNull();
    expect(safeLinkNext(42)).toBeNull();
  });

  it("reads the url-encoded next of the fragment", () => {
    const enc = encodeURIComponent(`/o/${ORG}/shop/orders`);
    expect(parseLinkNext(`#code=abc&next=${enc}`)).toBe(`/o/${ORG}/shop/orders`);
    expect(parseLinkNext(`#next=${enc}&code=abc`)).toBe(`/o/${ORG}/shop/orders`);
    expect(parseLinkNext("#code=abc")).toBeNull();
    expect(parseLinkNext(`#code=abc&next=${encodeURIComponent("//evil.com")}`)).toBeNull();
    expect(parseLinkNext(`#code=abc&next=${encodeURIComponent("https://evil.com")}`)).toBeNull();
    expect(parseLinkNext(`#code=abc&next=${encodeURIComponent(`/o/${ORG}/../x`)}`)).toBeNull();
  });

  it("captures it with the code, clears the address, and forgets both together", () => {
    forgetLinkCode();
    const h = fakeHistory();
    const enc = encodeURIComponent(`/o/${ORG}/shop/orders`);
    captureLinkCode({ pathname: LINK_PATH, hash: `#code=abc&next=${enc}` }, h);
    // The whole fragment, code and next, leaves the address.
    expect(h.calls).toEqual([LINK_PATH]);
    expect(linkCode()).toBe("abc");
    expect(linkNext()).toBe(`/o/${ORG}/shop/orders`);
    forgetLinkCode();
    expect(linkCode()).toBeNull();
    expect(linkNext()).toBeNull();
  });

  it("drops an unsafe next but still takes the code", () => {
    forgetLinkCode();
    const h = fakeHistory();
    captureLinkCode({ pathname: LINK_PATH, hash: `#code=abc&next=${encodeURIComponent("//evil.com")}` }, h);
    expect(linkCode()).toBe("abc");
    expect(linkNext()).toBeNull();
    expect(h.calls).toEqual([LINK_PATH]);
    forgetLinkCode();
  });

  it("keeps a next of an earlier link from leaking into a link without one", () => {
    forgetLinkCode();
    const h = fakeHistory();
    captureLinkCode({ pathname: LINK_PATH, hash: `#code=one&next=${encodeURIComponent(`/o/${ORG}/shop`)}` }, h);
    captureLinkCode({ pathname: LINK_PATH, hash: "#code=two" }, h);
    expect(linkCode()).toBe("two");
    expect(linkNext()).toBeNull();
    forgetLinkCode();
  });

  it("reads no next when the page has no code", () => {
    forgetLinkCode();
    captureLinkCode({ pathname: LINK_PATH, hash: `#next=${encodeURIComponent(`/o/${ORG}/shop`)}` }, fakeHistory());
    expect(linkCode()).toBeNull();
    expect(linkNext()).toBeNull();
  });
});
