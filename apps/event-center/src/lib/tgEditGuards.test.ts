/** RT-12b review: the small rules behind the mini app editor (canonical text, a site's HTML, the window guards). */
import { describe, expect, it, vi } from "vitest";
import { looksLikeSiteHtml } from "@/lib/richText";
import { insideTelegram, setClosingConfirmation } from "@/lib/tgEdit";
import { descriptionBaseOf } from "@/lib/tgEditEvent";

describe("descriptionBaseOf", () => {
  it("is the canonical text when the server sends it, even when the shown description is a translation", () => {
    expect(descriptionBaseOf({ description: "Привет", description_base: "Hello", description_translated: true })).toBe("Hello");
  });
  it("is null for an event whose only text is a translation", () => {
    expect(descriptionBaseOf({ description: "Привет", description_base: null, description_translated: true })).toBeNull();
  });
  it("is the description of a server that does not send the base yet", () => {
    expect(descriptionBaseOf({ description: "Hello" })).toBe("Hello");
    expect(descriptionBaseOf({ description: null })).toBeNull();
    // An old server cannot have said it was translated; a new one that did is not trusted to have no base.
    expect(descriptionBaseOf({ description: "Привет", description_translated: true })).toBeNull();
  });
});

describe("looksLikeSiteHtml", () => {
  it("sees the tags a website writes in a description kept as text", () => {
    expect(looksLikeSiteHtml("<p>Hello</p>", "text")).toBe(true);
    expect(looksLikeSiteHtml("Hello<br>world", "text")).toBe(true);
    expect(looksLikeSiteHtml("<B>Hello</B>", "")).toBe(true);
    expect(looksLikeSiteHtml("a <strong>b</strong>", null)).toBe(true);
    expect(looksLikeSiteHtml("<div class='x'>y</div>", undefined)).toBe(true);
  });
  it("does not see a less-than sign, a made-up tag or ART", () => {
    expect(looksLikeSiteHtml("Tickets < 20 EUR", "text")).toBe(false);
    expect(looksLikeSiteHtml("a <3 b", "text")).toBe(false);
    expect(looksLikeSiteHtml("<unknown>x</unknown>", "text")).toBe(false);
    expect(looksLikeSiteHtml("<p>Hello</p>", "art")).toBe(false);
    expect(looksLikeSiteHtml("", "text")).toBe(false);
    expect(looksLikeSiteHtml(null, "text")).toBe(false);
  });
});

describe("the window guards", () => {
  it("asks for the closing confirmation and takes it back", () => {
    const app = { enableClosingConfirmation: vi.fn(), disableClosingConfirmation: vi.fn() };
    setClosingConfirmation(true, { Telegram: { WebApp: app } });
    expect(app.enableClosingConfirmation).toHaveBeenCalledTimes(1);
    setClosingConfirmation(false, { Telegram: { WebApp: app } });
    expect(app.disableClosingConfirmation).toHaveBeenCalledTimes(1);
  });
  it("does nothing without Telegram, or with an old client that has no such call", () => {
    expect(() => setClosingConfirmation(true, {})).not.toThrow();
    expect(() => setClosingConfirmation(true, { Telegram: { WebApp: {} } })).not.toThrow();
  });
  it("knows a Telegram window from the script running in an ordinary browser", () => {
    expect(insideTelegram({})).toBe(false);
    expect(insideTelegram({ Telegram: { WebApp: { platform: "unknown" } } })).toBe(false);
    expect(insideTelegram({ Telegram: { WebApp: { platform: "android" } } })).toBe(true);
  });
});
