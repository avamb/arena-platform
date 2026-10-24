import { describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import {
  artToReact,
  changedDescription,
  decodeEntities,
  descriptionForForm,
  descriptionPatch,
  hasDescription,
  reportMissingDescriptionHtml,
  resetMissingDescriptionHtmlReport,
  safeHref,
} from "@/lib/richtext";
import { translate } from "@/lib/i18n/catalog";

const NBSP = String.fromCharCode(0xa0);
const ZWSP = String.fromCharCode(0x200b);
const REPLACEMENT = String.fromCharCode(0xfffd);

function html(src: string): string {
  return renderToStaticMarkup(createElement("div", null, ...artToReact(src)));
}
function inner(src: string): string {
  const out = html(src);
  return out.slice("<div>".length, out.length - "</div>".length);
}

describe("artToReact: the whitelisted subset", () => {
  it("rebuilds every allowed tag", () => {
    const src =
      "<h3>T</h3><h4>S</h4><p>A <strong>b</strong> <em>i</em> <u>u</u><br/>x</p>" +
      "<blockquote><p>q</p></blockquote><ul><li>a</li><li>b</li></ul><ol><li>c</li></ol>";
    expect(inner(src)).toBe(
      "<h3>T</h3><h4>S</h4><p>A <strong>b</strong> <em>i</em> <u>u</u><br/>x</p>" +
        "<blockquote><p>q</p></blockquote><ul><li>a</li><li>b</li></ul><ol><li>c</li></ol>",
    );
  });

  it("decodes entities into text and React escapes them again", () => {
    expect(inner("<p>Tom &amp; Jerry &lt;b&gt; &#65;&#x42; &nbsp;|</p>")).toBe(
      `<p>Tom &amp; Jerry &lt;b&gt; AB ${NBSP}|</p>`,
    );
    expect(decodeEntities("&unknown; &#0; &#xD800;")).toBe(`&unknown; ${REPLACEMENT} ${REPLACEMENT}`);
  });

  it("gives a link rel and target and keeps https and mailto", () => {
    expect(inner('<p><a href="https://example.com/a?x=1&amp;y=2">go</a></p>')).toBe(
      '<p><a href="https://example.com/a?x=1&amp;y=2" rel="noopener noreferrer" target="_blank">go</a></p>',
    );
    expect(inner('<a href="mailto:a@example.com">m</a>')).toContain('href="mailto:a@example.com"');
    expect(inner('<a href="https://e.com" rel="opener" target="_self" onclick="x()">l</a>')).toBe(
      '<a href="https://e.com" rel="noopener noreferrer" target="_blank">l</a>',
    );
    expect(inner("<a href='https://e.com/q'>single</a>")).toContain('href="https://e.com/q"');
    expect(inner("<a href=https://e.com/u>bare</a>")).toContain('href="https://e.com/u"');
  });

  it.each([
    "javascript:alert(1)",
    "JaVaScRiPt:alert(1)",
    " javascript:alert(1)",
    "java&#9;script:alert(1)",
    "jav&#x61;script:alert(1)",
    "data:text/html;base64,PHNjcmlwdD4=",
    "vbscript:msgbox(1)",
    "//evil.example/x",
    "/relative",
    "relative.html",
    "http://example.com",
    "ftp://example.com",
    "https://exa mple.com",
    `https://example.com/${ZWSP}`,
    "",
  ])("drops the link but keeps the text for href %j", (href) => {
    expect(inner(`<p><a href="${href}">label</a></p>`)).toBe("<p>label</p>");
  });

  it("drops a link without href and one with a broken attribute", () => {
    expect(inner("<a>nohref</a>")).toBe("nohref");
    expect(inner('<a href="https://e.com>unterminated</a>')).not.toContain("<a ");
  });

  it("never shows scripts, handlers, styles or unknown elements as markup", () => {
    const hostile =
      '<p onclick="alert(1)" style="x:y" class="c" id="i">a</p><script>alert(1)</script>' +
      '<img src=x onerror=alert(1)><iframe src="https://evil.example"></iframe>' +
      "<svg><script>alert(1)</script></svg><style>p{display:none}</style>" +
      '<form action="https://evil.example"><input name=x><button>go</button></form>' +
      '<div class="x"><span style="position:fixed">text of div</span></div><!-- comment -->' +
      "<SCRIPT >alert(2)</SCRIPT ><textarea>ta</textarea><title>tt</title>";
    const out = inner(hostile);
    expect(out).toBe("<p>a</p>gotext of div");
    expect(out).not.toMatch(
      /<(script|img|iframe|svg|style|form|input|button|div|span|textarea|title)|onclick|onerror|style=|class=|id=|alert/i,
    );
  });

  it("shows escaped tags as text, not as elements", () => {
    expect(inner("<p>&lt;script&gt;alert(1)&lt;/script&gt;</p>")).toBe(
      "<p>&lt;script&gt;alert(1)&lt;/script&gt;</p>",
    );
    expect(inner("<p>a < b and 5 <3 you</p>")).toBe("<p>a &lt; b and 5 &lt;3 you</p>");
  });

  it("closes a paragraph at the next block and an item at the next item", () => {
    expect(inner("<p>one<p>two<ul><li>a<li>b</ul>")).toBe("<p>one</p><p>two</p><ul><li>a</li><li>b</li></ul>");
  });

  it("ignores stray closing tags and unterminated tags", () => {
    expect(inner("a</p></strong>b")).toBe("ab");
    expect(inner("<p>cut <strong")).toBe("<p>cut &lt;strong</p>");
  });

  it("flattens a very deep tree instead of nesting without limit", () => {
    const deep = "<blockquote>".repeat(200) + "bottom" + "</blockquote>".repeat(200);
    const out = inner(deep);
    expect(out.replace(/<[^>]*>/g, "")).toBe("bottom");
    expect((out.match(/<blockquote>/g) ?? []).length).toBeLessThanOrEqual(33);
  });

  it("renders nothing for empty or blank input", () => {
    expect(artToReact("")).toEqual([]);
    expect(inner("")).toBe("");
  });

  it("never emits dangerouslySetInnerHTML", () => {
    const nodes = artToReact('<p>x</p><a href="https://e.com">y</a>');
    expect(JSON.stringify(nodes)).not.toContain("dangerouslySetInnerHTML");
  });
});

describe("safeHref", () => {
  it("accepts only absolute https and mailto", () => {
    expect(safeHref("https://example.com/a")).toBe("https://example.com/a");
    expect(safeHref("mailto:a@b.co")).toBe("mailto:a@b.co");
    expect(safeHref(null)).toBeNull();
    expect(safeHref(undefined)).toBeNull();
    expect(safeHref("javascript:alert(1)")).toBeNull();
    expect(safeHref("//x.y")).toBeNull();
  });
});

describe("the shared golden corpus renders without losing text", () => {
  const corpus: { name: string; art: string }[] = JSON.parse(
    readFileSync(
      resolve(__dirname, "../../../backend/internal/platform/richtext/testdata/richtext_legacy.json"),
      "utf8",
    ),
  );
  it("reads the corpus", () => {
    expect(corpus.length).toBeGreaterThan(60);
  });
  for (const c of corpus) {
    it(c.name, () => {
      // the visible text of the ART (tags removed, entities decoded) equals
      // that of the rebuilt React tree
      const expected = decodeEntities(c.art.replace(/<[^>]*>/g, ""));
      const rendered = decodeEntities(inner(c.art).replace(/<[^>]*>/g, ""));
      expect(rendered).toBe(expected);
    });
  }
});

describe("form helpers: an untouched description is never sent", () => {
  const art = {
    description: '<p>Hello <a href="https://e.com">link</a></p><h3>Head</h3>',
    description_legacy: "Hello link (https://e.com)\n\nHead",
  };
  it("loads description_legacy into the form, else the raw description", () => {
    expect(descriptionForForm(art)).toBe("Hello link (https://e.com)\n\nHead");
    expect(descriptionForForm({ description: "raw" })).toBe("raw");
    expect(descriptionForForm({ description: null })).toBe("");
  });

  it("changedDescription: undefined while untouched, empty string to clear, text when changed", () => {
    const initial = descriptionForForm(art);
    expect(changedDescription(initial, initial)).toBeUndefined();
    expect(changedDescription(initial, `  ${initial}\n `)).toBeUndefined();
    expect(changedDescription(initial, "")).toBe("");
    expect(changedDescription(initial, "  \n ")).toBe("");
    expect(changedDescription(initial, "New **text**  ")).toBe("New **text**");
    expect(changedDescription("", "")).toBeUndefined();
    expect(changedDescription("", "first")).toBe("first");
  });

  it("descriptionPatch: clearing sends an empty string (null would mean keep), never null", () => {
    const initial = descriptionForForm(art);
    expect(descriptionPatch(initial, initial)).toEqual({});
    expect(descriptionPatch(initial, "")).toEqual({ description: "", description_format: "text" });
    expect(JSON.stringify(descriptionPatch(initial, "  "))).toBe('{"description":"","description_format":"text"}');
    expect(descriptionPatch(initial, " New ")).toEqual({ description: "New", description_format: "text" });
    expect(Object.values(descriptionPatch(initial, "")).includes(null as unknown as string)).toBe(false);
  });

  it("reports a description without description_html as an error, once", () => {
    resetMissingDescriptionHtmlReport();
    const spy = vi.spyOn(console, "error").mockImplementation(() => undefined);
    try {
      reportMissingDescriptionHtml({ description: "<p>raw</p>", description_html: null });
      reportMissingDescriptionHtml({ description: "again" });
      expect(spy).toHaveBeenCalledTimes(1);
      resetMissingDescriptionHtmlReport();
      reportMissingDescriptionHtml({ description: "x", description_html: "<p>x</p>" });
      reportMissingDescriptionHtml({ description: null });
      expect(spy).toHaveBeenCalledTimes(1);
    } finally {
      spy.mockRestore();
    }
  });

  it("hasDescription sees the html or the raw field", () => {
    expect(hasDescription({ description: null })).toBe(false);
    expect(hasDescription({ description: "x" })).toBe(true);
    expect(hasDescription({ description: null, description_html: "<p>x</p>" })).toBe(true);
  });
});

describe("artToReact: no link inside a link", () => {
  it("a new <a> closes the open one, as in HTML", () => {
    expect(inner('<p><a href="https://a.example/">one <a href="https://b.example/">two</a> three</a></p>')).toBe(
      '<p><a href="https://a.example/" rel="noopener noreferrer" target="_blank">one </a>' +
        '<a href="https://b.example/" rel="noopener noreferrer" target="_blank">two</a> three</p>',
    );
    const out = inner('<a href="https://a.example/">x<a href="https://b.example/">y<a href="javascript:alert(1)">z</a></a></a>');
    expect(out).not.toMatch(/<a [^>]*>[^<]*<a /);
    expect(out).not.toContain("javascript");
  });
});

describe("the edit form hint about lost formatting", () => {
  it("exists in Russian and English and the form shows it under the field", () => {
    expect(translate("ru", "events.descriptionHint")).toBe(
      "Правка в этом поле убирает ссылки, заголовки и подчёркивание — для полного оформления используйте ивент-центр",
    );
    expect(translate("en", "events.descriptionHint")).toMatch(/links, headings and underline/);
    const src = readFileSync(resolve(__dirname, "../routes/events.tsx"), "utf8");
    expect(src).toContain('t("events.descriptionHint")');
    expect(src).toContain("events-edit-description-hint");
  });
});
