/**
 * richtext.ts — the small formatting an organizer may use in an event's
 * description (typed in the Telegram bot or the admin console, a plain text
 * field):
 *
 *   - a blank line starts a new paragraph, a single line break stays a break;
 *   - **bold** and _italic_ (italic may sit inside bold);
 *   - lines starting with "- ", "• " or "* " form a bulleted list.
 *
 * Nothing else is interpreted: no links, no HTML. The text is never put through
 * innerHTML — every node is built with createElement / createTextNode — so a
 * description cannot inject markup or script. An unmatched marker stays as typed.
 */

// **bold**: starts and ends on a non-space, stays on one line.
const BOLD = /\*\*(\S(?:[^\n]*?\S)?)\*\*/g;
// _italic_: the underscores must stand at word edges, so snake_case_names and
// file_names stay intact.
const ITALIC = /(^|[\s(["“'])_(\S(?:[^_\n]*?\S)?)_(?=$|[\s.,;:!?)\]"”'])/g;
const BULLET = /^\s*(?:[-•*])\s+(.*)$/;

function appendItalic(doc: Document, parent: Node, text: string): void {
  let last = 0;
  ITALIC.lastIndex = 0;
  for (let m = ITALIC.exec(text); m !== null; m = ITALIC.exec(text)) {
    const lead = m[1] ?? '';
    const start = m.index + lead.length;
    if (start > last) parent.appendChild(doc.createTextNode(text.slice(last, start)));
    const em = doc.createElement('em');
    em.textContent = m[2] ?? '';
    parent.appendChild(em);
    last = m.index + m[0].length;
  }
  if (last < text.length) parent.appendChild(doc.createTextNode(text.slice(last)));
}

function appendInline(doc: Document, parent: Node, text: string): void {
  let last = 0;
  BOLD.lastIndex = 0;
  for (let m = BOLD.exec(text); m !== null; m = BOLD.exec(text)) {
    if (m.index > last) appendItalic(doc, parent, text.slice(last, m.index));
    const strong = doc.createElement('strong');
    appendItalic(doc, strong, m[1] ?? '');
    parent.appendChild(strong);
    last = m.index + m[0].length;
  }
  if (last < text.length) appendItalic(doc, parent, text.slice(last));
}

/** Builds the formatted description as a fragment of <p> and <ul> elements. */
export function renderRichText(doc: Document, source: string): DocumentFragment {
  const out = doc.createDocumentFragment();
  const blocks = source.replace(/\r\n?/g, '\n').trim().split(/\n[ \t]*\n+/);
  for (const block of blocks) {
    let para: HTMLParagraphElement | null = null;
    let list: HTMLUListElement | null = null;
    for (const line of block.split('\n')) {
      if (line.trim() === '') continue;
      const bullet = BULLET.exec(line);
      if (bullet) {
        para = null;
        if (!list) {
          list = doc.createElement('ul');
          out.appendChild(list);
        }
        const li = doc.createElement('li');
        appendInline(doc, li, bullet[1] ?? '');
        list.appendChild(li);
        continue;
      }
      list = null;
      if (!para) {
        para = doc.createElement('p');
        out.appendChild(para);
      } else {
        para.appendChild(doc.createElement('br'));
      }
      appendInline(doc, para, line);
    }
  }
  return out;
}

/** The description without its markers, for <meta name="description">. */
export function plainText(source: string): string {
  return source
    .replace(BOLD, '$1')
    .replace(ITALIC, '$1$2')
    .replace(/^\s*[-•*]\s+/gm, '')
    .replace(/\s*\n\s*/g, ' ')
    .trim();
}

// ─────────────────────────────────────────────────────────────────────────────
// description_html (spec 43, RT-03a): the server's "text Arena" HTML subset.
// ─────────────────────────────────────────────────────────────────────────────

/** Elements that may appear in the output, besides <a>. Everything else is
 * replaced by its own text. */
const ART_TAGS = new Set(['p', 'br', 'strong', 'em', 'u', 'ul', 'ol', 'li', 'h3', 'h4', 'blockquote']);
/** Elements whose content is never visible text: dropped with their content. */
const ART_DROPPED = new Set(['script', 'style', 'template', 'noscript', 'iframe', 'object', 'embed', 'textarea', 'title']);
/** A deeper tree is flattened to text: the server never produces one. */
const ART_MAX_DEPTH = 32;

/** The href of a link the page may keep: absolute https or mailto, no spaces or
 * control characters. Returns null for anything else (javascript:, data:,
 * //host, relative, ...). */
export function safeHref(raw: string | null): string | null {
  if (raw === null) return null;
  const href = raw.trim();
  // eslint-disable-next-line no-control-regex
  if (href === '' || /[\u0000-\u0020\u007f-\u009f\u00a0\u200b-\u200f\u2028-\u202e\u2060-\u2069\ufeff\ufffd]/.test(href)) return null;
  if (!/^(?:https:\/\/|mailto:)/i.test(href)) return null;
  try {
    const u = new URL(href);
    if (u.protocol !== 'https:' && u.protocol !== 'mailto:') return null;
  } catch {
    return null;
  }
  return href;
}

/** The visible text of an element that is too deep to rebuild: its text nodes
 * in document order, skipping dropped elements WITH their content (a plain
 * textContent would show the text of a nested <script> or <style>). Iterative,
 * so a hostile depth cannot overflow the stack. */
function appendFlatText(doc: Document, out: Node, root: Element): void {
  const stack: Node[] = [];
  for (let c = root.lastChild; c !== null; c = c.previousSibling) stack.push(c);
  let text = '';
  for (let n = stack.pop(); n !== undefined; n = stack.pop()) {
    if (n.nodeType === 3) {
      text += n.nodeValue ?? '';
    } else if (n.nodeType === 1 && !ART_DROPPED.has((n as Element).localName.toLowerCase())) {
      for (let c = n.lastChild; c !== null; c = c.previousSibling) stack.push(c);
    }
  }
  if (text !== '') out.appendChild(doc.createTextNode(text));
}

function appendArtNode(doc: Document, out: Node, src: Node, depth: number): void {
  if (src.nodeType === 3) {
    out.appendChild(doc.createTextNode(src.nodeValue ?? ''));
    return;
  }
  if (src.nodeType !== 1) return; // comments, processing instructions
  const el = src as Element;
  const tag = el.localName.toLowerCase();
  if (ART_DROPPED.has(tag)) return;
  if (depth >= ART_MAX_DEPTH) {
    appendFlatText(doc, out, el);
    return;
  }
  let target: Node = out;
  if (ART_TAGS.has(tag)) {
    target = doc.createElement(tag);
    out.appendChild(target);
    if (tag === 'br') return;
  } else if (tag === 'a') {
    const href = safeHref(el.getAttribute('href'));
    if (href !== null) {
      const a = doc.createElement('a');
      a.setAttribute('href', href);
      a.setAttribute('rel', 'noopener noreferrer');
      a.setAttribute('target', '_blank');
      target = a;
      out.appendChild(a);
    }
    // an unsafe link keeps its text, without the link
  }
  for (let c = el.firstChild; c !== null; c = c.nextSibling) {
    appendArtNode(doc, target, c, depth + 1);
  }
}

/**
 * Builds the server's description_html (the "text Arena" subset) as a fragment.
 * The HTML is parsed by DOMParser, which creates an inert document (no script
 * runs, no image loads), and then ONLY whitelisted nodes are rebuilt with
 * createElement / createTextNode: p, br, strong, em, u, ul, ol, li, h3, h4,
 * blockquote, and a with an https or mailto href (given rel and target by us).
 * Attributes are never copied, and the server's markup is never put through
 * innerHTML, so even a hostile stored text cannot inject anything. Any other
 * element is shown as its text.
 */
export function renderArtHtml(doc: Document, html: string): DocumentFragment {
  const out = doc.createDocumentFragment();
  const parsed = new DOMParser().parseFromString(`<!doctype html><body>${html}`, 'text/html');
  for (let c = parsed.body.firstChild; c !== null; c = c.nextSibling) {
    appendArtNode(doc, out, c, 0);
  }
  return out;
}

let missingHtmlReported = false;

/** The server sent a description but no description_html: it could not read the
 * event's format (or this is an old backend or a cached response). The page
 * falls back to the raw text, which for a stored "art" description shows its
 * tags. That is safe but visible, so it is an ERROR in the console, once. */
function reportMissingDescriptionHtml(ev: DescriptionSource): void {
  if (missingHtmlReported || !ev.description) return;
  missingHtmlReported = true;
  console.error('tickets-page: description_html is missing, the description is shown from the raw text (its format is unknown)');
}

/** For tests: forget that the missing description_html was reported. */
export function resetMissingDescriptionHtmlReport(): void {
  missingHtmlReported = false;
}

/** What the page needs of an event to show its description. */
export interface DescriptionSource {
  description?: string | null;
  description_html?: string | null;
  description_text?: string | null;
}

/** True when the event has a description to show (the new field or the old). */
export function hasDescription(ev: DescriptionSource): boolean {
  return Boolean(ev.description_html) || Boolean(ev.description);
}

/** The event's description as nodes: description_html through the safe HTML
 * renderer when the server sent it, else the old `description` through the
 * legacy ** / _ / - renderer (an older backend, a cached response). */
export function renderDescription(doc: Document, ev: DescriptionSource): DocumentFragment {
  if (ev.description_html) return renderArtHtml(doc, ev.description_html);
  reportMissingDescriptionHtml(ev);
  return renderRichText(doc, ev.description ?? '');
}

/** The description as one line of plain text, for <meta name="description">:
 * description_text when the server sent it, else the old plainText of the
 * raw description. */
export function descriptionPlain(ev: DescriptionSource): string {
  if (ev.description_text) return ev.description_text.replace(/\s+/g, ' ').trim();
  return plainText(ev.description ?? '');
}
