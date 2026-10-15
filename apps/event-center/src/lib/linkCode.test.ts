import { describe, expect, it } from "vitest";
import { LINK_PATH, captureLinkCode, forgetLinkCode, linkCode, parseLinkCode } from "@/lib/linkCode";

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
