import { describe, expect, it } from "vitest";
import {
  codeFromShortUrl,
  parseLabel,
  parseLinkNewSearch,
  parseLinksSearch,
  parseUrl,
  parseUtm,
  reasonKey,
  retargetHelps,
  sameTarget,
  scanSums,
  targetBody,
  targetComplete,
  utmFields,
} from "@/lib/salesLinks";

describe("parseLabel", () => {
  it("folds spaces and refuses empty or long labels", () => {
    expect(parseLabel("  Poster   at the  entrance ")).toEqual({ ok: true, value: "Poster at the entrance" });
    expect(parseLabel("   ")).toEqual({ ok: false, error: "links.err.labelEmpty" });
    expect(parseLabel("x".repeat(121))).toEqual({ ok: false, error: "links.err.labelLong" });
    expect(parseLabel("я".repeat(120)).ok).toBe(true);
  });
});

describe("parseUrl", () => {
  it("takes http and https without spaces, like the bot", () => {
    expect(parseUrl(" https://example.com/shop ")).toEqual({ ok: true, value: "https://example.com/shop" });
    expect(parseUrl("HTTP://example.com").ok).toBe(true);
    expect(parseUrl("example.com")).toEqual({ ok: false, error: "links.err.url" });
    expect(parseUrl("https://exa mple.com")).toEqual({ ok: false, error: "links.err.url" });
    expect(parseUrl("https://" + "a".repeat(2050))).toEqual({ ok: false, error: "links.err.urlLong" });
  });
});

describe("parseUtm", () => {
  it("drops empty values and names the bad key", () => {
    expect(parseUtm({ utm_source: " qr ", utm_medium: "", utm_campaign: "spring.2026" })).toEqual({
      ok: true,
      value: { utm_source: "qr", utm_campaign: "spring.2026" },
    });
    expect(parseUtm({ utm_source: "with space" })).toEqual({ ok: false, error: "links.err.utm", key: "utm_source" });
    expect(parseUtm({ utm_term: "x".repeat(65) })).toEqual({ ok: false, error: "links.err.utm", key: "utm_term" });
    expect(parseUtm({})).toEqual({ ok: true, value: {} });
  });
  it("reads a link's labels back into the five fields", () => {
    expect(utmFields({ utm_source: "qr", other: "x" })).toEqual({
      utm_source: "qr",
      utm_medium: "",
      utm_campaign: "",
      utm_content: "",
      utm_term: "",
    });
    expect(utmFields(null).utm_source).toBe("");
  });
});

describe("targets", () => {
  it("compares by kind and its one id", () => {
    expect(sameTarget({ kind: "event", event_id: "a" }, { kind: "event", event_id: "a", session_id: "zzz" })).toBe(true);
    expect(sameTarget({ kind: "event", event_id: "a" }, { kind: "event", event_id: "b" })).toBe(false);
    expect(sameTarget({ kind: "org_page" }, { kind: "org_page", event_id: "a" })).toBe(true);
    expect(sameTarget({ kind: "url", url: "https://x" }, { kind: "url", url: "https://y" })).toBe(false);
    expect(sameTarget({ kind: "event", event_id: "a" }, { kind: "session", session_id: "a" })).toBe(false);
  });
  it("sends only the id of the kind", () => {
    expect(targetBody({ kind: "session", session_id: "s", event_id: "e" })).toEqual({ kind: "session", session_id: "s" });
    expect(targetBody({ kind: "org_page", event_id: "e" })).toEqual({ kind: "org_page" });
  });
  it("knows when a target can be sent", () => {
    const id = "0199a000-0000-7000-8000-000000000001";
    expect(targetComplete({ kind: "event", event_id: id })).toBe(true);
    expect(targetComplete({ kind: "event", event_id: null })).toBe(false);
    expect(targetComplete({ kind: "url", url: "https://example.com" })).toBe(true);
    expect(targetComplete({ kind: "url", url: "nope" })).toBe(false);
    expect(targetComplete({ kind: "org_page" })).toBe(true);
    expect(targetComplete(null)).toBe(false);
  });
});

describe("reasonKey (the bot's slWhyKey)", () => {
  const base = { status: "active", destination_reason: "", destination_degraded: false };
  it("says nothing for a code that leads to its target", () => {
    expect(reasonKey(base)).toBeNull();
    expect(retargetHelps(base)).toBe(false);
  });
  it("the archive wins over every reason", () => {
    expect(reasonKey({ ...base, status: "archived", destination_reason: "session_not_live" })).toBe("links.why.archived");
    expect(retargetHelps({ ...base, status: "archived", destination_reason: "session_not_live" })).toBe(false);
  });
  it("offers a retarget only where another target changes the address", () => {
    expect(reasonKey({ ...base, destination_reason: "event_not_published", destination_degraded: true })).toBe("links.why.eventUnpublished");
    expect(retargetHelps({ ...base, destination_reason: "event_not_published", destination_degraded: true })).toBe(false);
    expect(reasonKey({ ...base, destination_reason: "event_no_slug", destination_degraded: true })).toBe("links.why.eventNoPage");
    expect(reasonKey({ ...base, destination_reason: "session_not_live", destination_degraded: true })).toBe("links.why.session");
    expect(retargetHelps({ ...base, destination_reason: "session_not_live", destination_degraded: true })).toBe(true);
    expect(reasonKey({ ...base, destination_reason: "promoter_unavailable", destination_degraded: true })).toBe("links.why.promoter");
    expect(retargetHelps({ ...base, destination_reason: "promoter_unavailable", destination_degraded: true })).toBe(true);
    expect(reasonKey({ ...base, destination_reason: "no_page_address", destination_degraded: true })).toBe("links.why.noAddress");
    expect(reasonKey({ ...base, destination_reason: "org_not_found", destination_degraded: true })).toBe("links.why.noAddress");
    expect(reasonKey({ ...base, destination_reason: "something_new", destination_degraded: true })).toBe("links.why.degraded");
    expect(reasonKey({ ...base, destination_degraded: true })).toBe("links.why.degraded");
    expect(retargetHelps({ ...base, destination_degraded: true })).toBe(true);
  });
});

describe("scanSums", () => {
  it("adds today and the last seven UTC days", () => {
    const now = new Date("2026-10-25T10:00:00Z");
    const days = [
      { day: "2026-10-25", hits: 3, suspect_hits: 0 },
      { day: "2026-10-19", hits: 2, suspect_hits: 0 },
      { day: "2026-10-18", hits: 7, suspect_hits: 0 },
    ];
    expect(scanSums(days, now)).toEqual({ today: 3, week: 5 });
  });
});

describe("addresses", () => {
  it("reads the list filters and ignores junk", () => {
    const id = "0199a000-0000-7000-8000-000000000001";
    expect(parseLinksSearch({ f: "archived", event: id.toUpperCase() })).toEqual({ f: "archived", event: id });
    expect(parseLinksSearch({ f: "x", event: "not-a-uuid" })).toEqual({});
    expect(parseLinkNewSearch({ event: id, session: "nope" })).toEqual({ event: id });
    expect(parseLinkNewSearch({})).toEqual({});
  });
  it("takes the code off a short address", () => {
    expect(codeFromShortUrl("https://go.arenasoldout.com/abc2345")).toBe("abc2345");
    expect(codeFromShortUrl("abc2345")).toBe("abc2345");
  });
});
