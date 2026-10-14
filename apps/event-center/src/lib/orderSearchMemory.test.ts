import { describe, expect, it } from "vitest";
import { forgetOrderSearches, rememberOrderSearch, rememberedOrderSearch } from "@/lib/orderSearchMemory";

describe("orders search memory", () => {
  it("keeps one text per organization and forgets all of them at sign-out", () => {
    rememberOrderSearch("a", "anna@example.test");
    rememberOrderSearch("b", "+34600111222");
    expect(rememberedOrderSearch("a")).toBe("anna@example.test");
    expect(rememberedOrderSearch("b")).toBe("+34600111222");
    rememberOrderSearch("a", "");
    expect(rememberedOrderSearch("a")).toBe("");
    forgetOrderSearches();
    expect(rememberedOrderSearch("b")).toBe("");
  });
});
