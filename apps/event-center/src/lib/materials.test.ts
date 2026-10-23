import { describe, expect, it } from "vitest";
import { LIMITS, checkHttpsUrl, checkMapPoint, cleanText, isLimitError, mapUrls, moved, reportPublicFrom } from "@/lib/materials";

describe("limits", () => {
  it("match the server", () => {
    expect([LIMITS.gallery, LIMITS.videos, LIMITS.partners, LIMITS.report]).toEqual([6, 4, 3, 30]);
  });
});

describe("checkHttpsUrl", () => {
  it("accepts https and trims", () => {
    expect(checkHttpsUrl("  https://vimeo.com/76979871 ")).toEqual({ ok: true, url: "https://vimeo.com/76979871" });
  });
  it("refuses dangerous and plain schemes", () => {
    for (const bad of ["javascript:alert(1)", "data:text/html,<b>x</b>", "http://a.example", "ftp://a.example", "file:///etc/passwd", "//a.example", "vbscript:x"]) {
      expect(checkHttpsUrl(bad).ok, bad).toBe(false);
    }
  });
  it("names the problem", () => {
    expect(checkHttpsUrl("")).toEqual({ ok: false, problem: "empty" });
    expect(checkHttpsUrl("http://a.example")).toEqual({ ok: false, problem: "notHttps" });
    expect(checkHttpsUrl("not a url")).toEqual({ ok: false, problem: "invalid" });
    expect(checkHttpsUrl("https://user:pw@a.example/")).toEqual({ ok: false, problem: "invalid" });
    expect(checkHttpsUrl("https://a.example/" + "x".repeat(600))).toEqual({ ok: false, problem: "tooLong" });
  });
});

describe("checkMapPoint", () => {
  it("both empty means no point", () => {
    expect(checkMapPoint("", " ")).toEqual({ kind: "empty" });
  });
  it("one empty coordinate is incomplete, never zero", () => {
    expect(checkMapPoint("12", "")).toEqual({ kind: "bad", problem: "incomplete" });
    expect(checkMapPoint("", "12")).toEqual({ kind: "bad", problem: "incomplete" });
  });
  it("0,0 is a valid point", () => {
    expect(checkMapPoint("0", "0")).toEqual({ kind: "ok", latitude: 0, longitude: 0 });
  });
  it("accepts a decimal comma and checks the ranges", () => {
    expect(checkMapPoint("50,0755", "14.4378")).toEqual({ kind: "ok", latitude: 50.0755, longitude: 14.4378 });
    expect(checkMapPoint("90", "180")).toMatchObject({ kind: "ok" });
    expect(checkMapPoint("-90.1", "0")).toEqual({ kind: "bad", problem: "latRange" });
    expect(checkMapPoint("0", "180.5")).toEqual({ kind: "bad", problem: "lngRange" });
    expect(checkMapPoint("abc", "1")).toEqual({ kind: "bad", problem: "number" });
    expect(checkMapPoint("1e3", "1")).toEqual({ kind: "bad", problem: "number" });
  });
});

describe("helpers", () => {
  it("cleanText blanks to null", () => {
    expect(cleanText("  ")).toBeNull();
    expect(cleanText(" a ")).toBe("a");
  });
  it("moved swaps neighbours and refuses the edges", () => {
    const items = [{ id: "a" }, { id: "b" }, { id: "c" }];
    expect(moved(items, 1, -1)).toEqual(["b", "a", "c"]);
    expect(moved(items, 1, 1)).toEqual(["a", "c", "b"]);
    expect(moved(items, 0, -1)).toEqual(["a", "b", "c"]);
    expect(moved(items, 2, 1)).toEqual(["a", "b", "c"]);
  });
  it("isLimitError reads the server code", () => {
    expect(isLimitError({ code: "event.gallery_full" })).toBe(true);
    expect(isLimitError({ code: "event.invalid_url" })).toBe(false);
    expect(isLimitError(null)).toBe(false);
  });
  it("mapUrls", () => {
    expect(mapUrls(1, 2).google).toBe("https://www.google.com/maps/search/?api=1&query=1,2");
  });
});

describe("reportPublicFrom", () => {
  it("is the LAST end of the live sessions, whatever the order or the zone", () => {
    expect(
      reportPublicFrom([
        { status: "scheduled", end_at: "2030-12-05T21:00:00+01:00" },
        { status: "scheduled", end_at: "2030-12-07T09:00:00-08:00" },
        { status: "cancelled", end_at: "2031-01-01T00:00:00Z" },
      ]),
    ).toBe("2030-12-07T17:00:00.000Z");
  });
  it("is null with no end at all (the server keeps the report hidden)", () => {
    expect(reportPublicFrom([])).toBeNull();
    expect(reportPublicFrom([{ status: "scheduled", end_at: null }])).toBeNull();
    expect(reportPublicFrom([{ status: "cancelled", end_at: "2030-01-01T00:00:00Z" }])).toBeNull();
  });
});
