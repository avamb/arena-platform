// @vitest-environment jsdom
// The web's one place for counting and converting formatted texts (spec 44, RT-11). Counting reads HTML,
// which needs a DOM.
import { describe, expect, it } from "vitest";
import {
  artOfLegacy,
  artOfStored,
  countText,
  descriptionArtOf,
  limitsOf,
  overMarkupLimit,
  overVisibleLimit,
  plainOfArt,
  sameVisibleText,
  visibleLength,
} from "@/lib/richText";

describe("limits", () => {
  it("are the profile limits of the server, per text", () => {
    expect(limitsOf("description")).toEqual({ maxVisible: 4000, maxHtml: 16000, maxLinks: 20 });
    expect(limitsOf("email_note")).toEqual({ maxVisible: 2000, maxHtml: 8000, maxLinks: 5 });
    expect(limitsOf("session_message")).toEqual({ maxVisible: 1000, maxHtml: 4000, maxLinks: 3 });
  });
});

describe("counting", () => {
  it("counts the VISIBLE text: tags do not count, a break between two paragraphs counts as one character", () => {
    expect(visibleLength("", "description")).toBe(0);
    expect(visibleLength("<p>abc</p>", "description")).toBe(3);
    expect(visibleLength("<p><strong>ab</strong><em>c</em></p>", "description")).toBe(3);
    expect(visibleLength("<p>ab</p><p>cd</p>", "description")).toBe(5);
    expect(visibleLength("<p>a<br>b</p>", "description")).toBe(3);
  });
  it("counts code points, not UTF-16 units", () => {
    expect(visibleLength("<p>" + "😀".repeat(10) + "</p>", "description")).toBe(10);
  });
  it("a text the library refuses as hostile is over every limit, never an exception", () => {
    const deep = "<div>".repeat(300) + "x" + "</div>".repeat(300);
    expect(countText(deep, "description")).toBeNull();
    expect(visibleLength(deep, "description")).toBe(Number.POSITIVE_INFINITY);
    expect(overVisibleLimit(deep, "description")).toBe(true);
    expect(overMarkupLimit(deep, "description")).toBe(true);
  });
  it("tells the visible limit from the markup limit", () => {
    expect(overVisibleLimit("<p>" + "a".repeat(1000) + "</p>", "session_message")).toBe(false);
    expect(overVisibleLimit("<p>" + "a".repeat(1001) + "</p>", "session_message")).toBe(true);
    const link = (n: number) => `<a href="https://example.com/${n}">l${n}</a>`;
    expect(overMarkupLimit("<p>" + [1, 2, 3].map(link).join(" ") + "</p>", "session_message")).toBe(false);
    expect(overMarkupLimit("<p>" + [1, 2, 3, 4].map(link).join(" ") + "</p>", "session_message")).toBe(true);
    expect(overMarkupLimit("", "session_message")).toBe(false);
  });
});

describe("conversion", () => {
  it("turns the old plain form into ART, keeping a typed tag as text", () => {
    expect(artOfLegacy("One\nTwo <b>\n\n- a\n- b", "description")).toBe("<p>One<br>Two &lt;b&gt;</p><ul><li>a</li><li>b</li></ul>");
    expect(artOfLegacy("", "description")).toBe("");
  });
  it("reads a stored text by its format and never guesses it from the tags", () => {
    expect(artOfStored("<p>x</p>", "art", "description")).toBe("<p>x</p>");
    expect(artOfStored("<p>x</p>", "text", "description")).toBe("<p>&lt;p&gt;x&lt;/p&gt;</p>");
    expect(artOfStored("<p>x</p>", null, "description")).toBe("<p>&lt;p&gt;x&lt;/p&gt;</p>");
    expect(artOfStored("- a\n- b", "", "email_note")).toBe("<ul><li>a</li><li>b</li></ul>");
    expect(artOfStored(null, "art", "description")).toBe("");
    expect(artOfStored("", "text", "description")).toBe("");
  });
  it("reads the description of an event: description_html first, then description with description_format", () => {
    expect(descriptionArtOf({ description_html: '<p>Hi <strong>all</strong> <a href="https://example.com" rel="noopener noreferrer" target="_blank">x</a></p>' })).toBe(
      '<p>Hi <strong>all</strong> <a href="https://example.com">x</a></p>',
    );
    expect(descriptionArtOf({ description: "Old **text**", description_format: "text", description_html: null })).toBe("<p>Old <strong>text</strong></p>");
    expect(descriptionArtOf({ description: "<p>Stored</p>", description_format: "art" })).toBe("<p>Stored</p>");
    expect(descriptionArtOf({})).toBe("");
  });
  it("gives the visible text, and compares by it", () => {
    expect(plainOfArt("<p>One</p><ul><li>a</li></ul>", "description")).toBe("One\n\n- a");
    expect(plainOfArt("", "description")).toBe("");
    expect(sameVisibleText("<p>A <strong>bold</strong> word</p>", "<p>A bold word</p>", "description")).toBe(true);
    expect(sameVisibleText("<p>A</p>", "<p>B</p>", "description")).toBe(false);
  });
});
