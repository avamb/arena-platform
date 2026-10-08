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
