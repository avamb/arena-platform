/**
 * richtext.ts - safe display of an event description (spec 43, RT-03a).
 *
 * The API returns `description_html`: the "text Arena" HTML subset, already
 * sanitised by the server. The console still never trusts it. artToReact reads
 * the HTML with a small tokenizer (no DOM, no innerHTML, no
 * dangerouslySetInnerHTML) and BUILDS React elements for whitelisted tags only:
 * p, br, strong, em, u, ul, ol, li, h3, h4, blockquote, and a with an https or
 * mailto href (given rel="noopener noreferrer" and target="_blank" here).
 * Attributes are never copied except that checked href; any other element is
 * shown as its text, and script/style content is dropped. React escapes every
 * text node, so what reaches the page is elements we created and text.
 *
 * The same rules as the sales page renderer (apps/tickets-page/src/lib/
 * richtext.ts), which uses DOMParser; this one needs no DOM, so it runs in the
 * console's Node unit tests too.
 */
import { createElement, type ReactNode } from "react";

/** Elements that may appear in the output, besides <a>. */
const ART_TAGS = new Set([
  "p",
  "br",
  "strong",
  "em",
  "u",
  "ul",
  "ol",
  "li",
  "h3",
  "h4",
  "blockquote",
]);
/** Elements whose content is never visible text: dropped with their content. */
const RAW_DROPPED = new Set(["script", "style", "template", "noscript", "iframe", "textarea", "title", "object", "embed"]);
/** Block elements that end an open paragraph or heading, as in HTML. */
const BLOCKS = new Set(["p", "ul", "ol", "h3", "h4", "blockquote"]);
const TEXT_BLOCKS = new Set(["p", "h3", "h4"]);
/** A deeper tree is flattened to text: the server never produces one. */
const MAX_DEPTH = 32;

/**
 * The href of a link that may be kept: absolute https or mailto, no spaces,
 * control or invisible characters. null for javascript:, data:, //host,
 * relative and everything else.
 */
export function safeHref(raw: string | null | undefined): string | null {
  if (raw === null || raw === undefined) return null;
  const href = raw.trim();
  // eslint-disable-next-line no-control-regex
  if (href === "" || /[\u0000-\u0020\u007f-\u009f\u00a0\u200b-\u200f\u2028-\u202e\u2060-\u2069\ufeff\ufffd]/.test(href)) {
    return null;
  }
  if (!/^(?:https:\/\/|mailto:)/i.test(href)) return null;
  try {
    const u = new URL(href);
    if (u.protocol !== "https:" && u.protocol !== "mailto:") return null;
  } catch {
    return null;
  }
  return href;
}

const NAMED_ENTITIES: Readonly<Record<string, string>> = {
  amp: "&",
  lt: "<",
  gt: ">",
  quot: '"',
  apos: "'",
  nbsp: "\u00a0",
};

/** Decodes the entities of a text or attribute value; unknown ones stay as typed. */
export function decodeEntities(s: string): string {
  if (!s.includes("&")) return s;
  return s.replace(/&(?:#(\d{1,7})|#[xX]([0-9a-fA-F]{1,6})|([A-Za-z][A-Za-z0-9]{1,7}));/g, (whole, dec, hex, name) => {
    if (name !== undefined) return NAMED_ENTITIES[name as string] ?? whole;
    const code = dec !== undefined ? Number(dec) : parseInt(hex as string, 16);
    if (code === 0 || code > 0x10ffff || (code >= 0xd800 && code <= 0xdfff)) return "\ufffd";
    return String.fromCodePoint(code);
  });
}

interface ArtElement {
  readonly tag: string;
  readonly href: string | null;
  readonly children: ArtChild[];
}
type ArtChild = string | ArtElement;

interface OpenTag {
  readonly name: string;
  readonly closing: boolean;
  readonly attrs: ReadonlyMap<string, string>;
  /** Index just after the closing ">" of the tag. */
  readonly end: number;
}

const NAME_START = /[A-Za-z]/;
const NAME_CHAR = /[A-Za-z0-9:-]/;

/** Reads the tag that starts at src[at] === "<", or null when it is not a tag. */
function readTag(src: string, at: number): OpenTag | null {
  let i = at + 1;
  let closing = false;
  if (src[i] === "/") {
    closing = true;
    i++;
  }
  if (i >= src.length || !NAME_START.test(src[i] as string)) return null;
  const nameStart = i;
  while (i < src.length && NAME_CHAR.test(src[i] as string)) i++;
  const name = src.slice(nameStart, i).toLowerCase();
  const attrs = new Map<string, string>();
  for (;;) {
    while (i < src.length && /[\s/]/.test(src[i] as string)) i++;
    if (i >= src.length) return null; // an unterminated tag is text
    if (src[i] === ">") return { name, closing, attrs, end: i + 1 };
    const aStart = i;
    while (i < src.length && !/[\s/=>]/.test(src[i] as string)) i++;
    const attr = src.slice(aStart, i).toLowerCase();
    while (i < src.length && /\s/.test(src[i] as string)) i++;
    let value = "";
    if (src[i] === "=") {
      i++;
      while (i < src.length && /\s/.test(src[i] as string)) i++;
      const q = src[i];
      if (q === '"' || q === "'") {
        const close = src.indexOf(q, i + 1);
        if (close < 0) return null;
        value = src.slice(i + 1, close);
        i = close + 1;
      } else {
        const vStart = i;
        while (i < src.length && !/[\s>]/.test(src[i] as string)) i++;
        value = src.slice(vStart, i);
      }
    }
    if (attr !== "" && !attrs.has(attr)) attrs.set(attr, decodeEntities(value));
  }
}

/** Parses the HTML into a tree of whitelisted elements and text. */
function parse(src: string): ArtChild[] {
  const root: ArtElement = { tag: "", href: null, children: [] };
  const stack: ArtElement[] = [root];
  const top = (): ArtElement => stack[stack.length - 1] as ArtElement;
  const addText = (t: string): void => {
    if (t === "") return;
    const kids = top().children;
    const last = kids[kids.length - 1];
    if (typeof last === "string") kids[kids.length - 1] = last + t;
    else kids.push(t);
  };
  const closeTo = (name: string): void => {
    for (let k = stack.length - 1; k > 0; k--) {
      if ((stack[k] as ArtElement).tag === name) {
        stack.length = k;
        return;
      }
    }
  };
  // How many open elements are real (not flattened) - depth guard.
  let i = 0;
  while (i < src.length) {
    const lt = src.indexOf("<", i);
    if (lt < 0) {
      addText(decodeEntities(src.slice(i)));
      break;
    }
    addText(decodeEntities(src.slice(i, lt)));
    if (src.startsWith("<!--", lt)) {
      const end = src.indexOf("-->", lt + 4);
      i = end < 0 ? src.length : end + 3;
      continue;
    }
    if (src[lt + 1] === "!" || src[lt + 1] === "?") {
      const end = src.indexOf(">", lt + 2);
      i = end < 0 ? src.length : end + 1;
      continue;
    }
    const tag = readTag(src, lt);
    if (tag === null) {
      addText("<");
      i = lt + 1;
      continue;
    }
    i = tag.end;
    if (RAW_DROPPED.has(tag.name)) {
      if (!tag.closing) {
        const m = new RegExp(`</${tag.name}[\\s/>]`, "i").exec(src.slice(i));
        i = m === null ? src.length : i + m.index;
      }
      continue;
    }
    if (tag.closing) {
      if (tag.name === "br") continue;
      if (ART_TAGS.has(tag.name) || tag.name === "a") closeTo(tag.name);
      continue;
    }
    if (tag.name === "br") {
      top().children.push({ tag: "br", href: null, children: [] });
      continue;
    }
    const isLink = tag.name === "a";
    if (!ART_TAGS.has(tag.name) && !isLink) continue; // unknown element: its text stays
    if (stack.length > MAX_DEPTH) continue; // too deep: flatten to text
    // As in HTML, a block ends an open paragraph/heading, and an <li> ends the previous one.
    if (BLOCKS.has(tag.name) && TEXT_BLOCKS.has(top().tag)) stack.pop();
    if (tag.name === "li" && top().tag === "li") stack.pop();
    // As in HTML, a link never holds a link: a new <a> closes the open one.
    if (isLink) closeTo("a");
    const href = isLink ? safeHref(tag.attrs.get("href")) : null;
    if (isLink && href === null) continue; // an unsafe link keeps its text only
    const el: ArtElement = { tag: tag.name, href, children: [] };
    top().children.push(el);
    stack.push(el);
  }
  return root.children;
}

function build(children: readonly ArtChild[], prefix: string): ReactNode[] {
  return children.map((c, idx) => {
    const key = `${prefix}${idx}`;
    if (typeof c === "string") return c;
    if (c.tag === "br") return createElement("br", { key });
    if (c.tag === "a") {
      return createElement(
        "a",
        { key, href: c.href as string, rel: "noopener noreferrer", target: "_blank" },
        ...build(c.children, `${key}.`),
      );
    }
    return createElement(c.tag, { key }, ...build(c.children, `${key}.`));
  });
}

/**
 * The React nodes of an ART / description_html string: whitelisted elements
 * only, everything else as text. Never throws; an empty or blank string gives
 * an empty array.
 */
export function artToReact(html: string): ReactNode[] {
  return build(parse(html), "r");
}

/** What the console needs of an event to show its description. */
export interface DescriptionFields {
  readonly description: string | null;
  readonly description_html?: string | null;
  readonly description_text?: string | null;
  readonly description_legacy?: string | null;
}

let missingHtmlReported = false;

/**
 * Logs, once, that an event came with a description but no description_html:
 * the server could not read the event's format (or it is an old backend). The
 * card then shows the legacy or raw text, which for a stored "art" description
 * may carry tags. That is safe but visible, so it is an error, not a warning.
 */
export function reportMissingDescriptionHtml(e: DescriptionFields): void {
  if (missingHtmlReported || e.description_html || !e.description) return;
  missingHtmlReported = true;
  console.error("admin-web: description_html is missing, the description is shown as plain text (its format is unknown)");
}

/** For tests: forget that the missing description_html was reported. */
export function resetMissingDescriptionHtmlReport(): void {
  missingHtmlReported = false;
}

/** True when the event has a description to show. */
export function hasDescription(e: DescriptionFields): boolean {
  return Boolean(e.description_html) || Boolean(e.description);
}

/**
 * The description of an event as one text for an edit form: the old
 * "**bold**, - list" form the server derives from the stored ART
 * (description_legacy), else the raw description for a server that does not
 * send it. An untouched value is never sent back (see changedDescription).
 */
export function descriptionForForm(e: DescriptionFields): string {
  return e.description_legacy ?? e.description ?? "";
}

/**
 * What a save sends for `description`: undefined (leave the field out of the
 * PATCH) while the person has not changed the text, so a description with
 * links or headings is never degraded by an unrelated edit; "" to clear it (the
 * server reads a null as "leave it as it is", and an empty string as "no
 * description"); else the trimmed text (to be sent with description_format
 * "text").
 */
export function changedDescription(initial: string, current: string): string | undefined {
  if (current.trim() === initial.trim()) return undefined;
  return current.trim();
}

/**
 * The description part of an event PATCH body: nothing while the text is
 * untouched, else `description` (the text, or "" to clear it) with
 * description_format "text" (what the person typed is the old plain text).
 */
export function descriptionPatch(initial: string, current: string): Record<string, string> {
  const description = changedDescription(initial, current);
  if (description === undefined) return {};
  return { description, description_format: "text" };
}
