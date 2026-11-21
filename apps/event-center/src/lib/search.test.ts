import { describe, expect, it } from "vitest";
import { parseEventsSearch, parseLoginSearch, parseOrdersSearch, safeRedirect } from "@/lib/search";

describe("search parsers", () => {
  it("accepts only redirects inside the app", () => {
    expect(safeRedirect("/o/x/events")).toBe("/o/x/events");
    expect(safeRedirect("//evil.example")).toBeUndefined();
    expect(safeRedirect("https://evil.example")).toBeUndefined();
    expect(safeRedirect("/\\evil.example")).toBeUndefined();
    expect(parseLoginSearch({ redirect: "https://x", expired: "1" })).toEqual({ expired: true });
  });
  it("falls back to defaults on bad values", () => {
    expect(parseEventsSearch({ f: "nope", page: "-3", q: "  " })).toEqual({});
    expect(parseEventsSearch({ f: "arc", page: "2", q: "rock" })).toEqual({ f: "arc", page: 2, q: "rock" });
    expect(parseOrdersSearch({ tab: "all", event: "not-a-uuid" })).toEqual({});
    expect(
      parseOrdersSearch({ tab: "paid", q: " #1000000500 ", session: "0193f01a-1234-7000-8000-000000000001" }),
    ).toEqual({ tab: "paid", session: "0193f01a-1234-7000-8000-000000000001" });
  });
});

describe("parseLoginSearch email", () => {
  it("keeps the account a letter's link names", () => {
    expect(parseLoginSearch({ email: " anna@example.test " }).email).toBe("anna@example.test");
  });
  it("drops a value that is not an e-mail", () => {
    expect(parseLoginSearch({ email: "nobody" }).email).toBeUndefined();
    expect(parseLoginSearch({ email: 42 }).email).toBeUndefined();
    expect(parseLoginSearch({ email: "a@" + "x".repeat(400) }).email).toBeUndefined();
  });
});
