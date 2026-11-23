import { describe, expect, it } from "vitest";
import {
  bundleExternalSale,
  emptyExternal,
  externalFormOf,
  externalProblems,
  isExternalSession,
  salesModePatch,
  sessionExternalInput,
} from "@/lib/externalSale";

const ok = { url: "https://www.eventim.de/event/12345", seller: "Eventim", price: "", currency: "" };

describe("externalProblems", () => {
  it("accepts a https link and a name", () => {
    expect(externalProblems(ok, "EUR")).toEqual({});
  });

  it("asks for the link and the name", () => {
    expect(externalProblems(emptyExternal(), "EUR")).toEqual({ url: "external.err.urlRequired", seller: "external.err.sellerRequired" });
  });

  it("refuses http, an address of numbers, a bare word and a text that is no link", () => {
    for (const url of ["http://www.eventim.de/x", "https://127.0.0.1/x", "https://localhost/x", "eventim.de", "https://"]) {
      expect(externalProblems({ ...ok, url }, "EUR").url, url).toBe("external.err.urlShape");
    }
  });

  it("refuses a link over 2048 characters and a name over 80", () => {
    expect(externalProblems({ ...ok, url: `https://a.example/${"x".repeat(2040)}` }, "EUR").url).toBe("external.err.urlShape");
    expect(externalProblems({ ...ok, seller: "x".repeat(81) }, "EUR").seller).toBe("external.err.sellerLong");
    expect(externalProblems({ ...ok, seller: "x".repeat(80) }, "EUR")).toEqual({});
  });

  it("judges a price, and its currency only when a price is typed", () => {
    expect(externalProblems({ ...ok, price: "abc" }, "EUR").price).toBe("external.err.priceShape");
    expect(externalProblems({ ...ok, price: "39,50" }, "EUR")).toEqual({});
    expect(externalProblems({ ...ok, price: "39" }, "").currency).toBe("external.err.currencyShape");
    expect(externalProblems({ ...ok, price: "39", currency: "usd" }, "")).toEqual({});
    expect(externalProblems({ ...ok, currency: "zz" }, "EUR")).toEqual({});
  });
});

describe("what goes on the wire", () => {
  it("bundle: no price, no currency", () => {
    expect(bundleExternalSale({ ...ok, url: ` ${ok.url} `, seller: " Eventim " }, "EUR")).toEqual({ url: ok.url, sellerName: "Eventim" });
  });

  it("bundle: a price comes with a currency, its own or the event's", () => {
    expect(bundleExternalSale({ ...ok, price: "39,5" }, "eur")).toEqual({ url: ok.url, sellerName: "Eventim", priceFrom: 39.5, currency: "EUR" });
    expect(bundleExternalSale({ ...ok, price: "39", currency: "usd" }, "EUR").currency).toBe("USD");
  });

  it("session input is snake_case", () => {
    expect(sessionExternalInput({ ...ok, price: "12" }, "EUR")).toEqual({ url: ok.url, seller_name: "Eventim", price_from: 12, currency: "EUR" });
  });

  it("the PATCH to the platform names no box office, the PATCH to a partner names it", () => {
    expect(salesModePatch("platform", ok, "EUR")).toEqual({ sales_mode: "platform", external: null });
    expect(salesModePatch("external", ok, "EUR")).toEqual({ sales_mode: "external", external: { url: ok.url, seller_name: "Eventim" } });
  });
});

describe("reading a session", () => {
  it("reads the box office of an external session and an empty form for a platform one", () => {
    const ext = { url: ok.url, seller_name: "Eventim", price_from: 39.5, currency: "EUR" };
    expect(externalFormOf({ external: ext })).toEqual({ url: ok.url, seller: "Eventim", price: "39.5", currency: "EUR" });
    expect(externalFormOf({ external: null })).toEqual(emptyExternal());
    expect(isExternalSession({ sales_mode: "external" })).toBe(true);
    expect(isExternalSession({ sales_mode: "platform" })).toBe(false);
    expect(isExternalSession(undefined)).toBe(false);
  });
});
