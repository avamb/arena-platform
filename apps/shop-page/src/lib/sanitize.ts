/**
 * sanitize.ts - a second line of defence for HTML the API hands over (product
 * descriptions, static pages). The backend sanitizes what organizers type;
 * this only makes sure nothing executable can reach the DOM even if that
 * ever regresses: no scripts, frames, plugin embeds, inline SVG / MathML (their
 * animation and namespace tricks), <noscript> and <template> bodies, forms,
 * inline event handlers or javascript: URLs. Presentation markup
 * (sections, <style>, images, links) is kept - the pages are the organizer's.
 *
 * The one form that stays is the storefront's own contact form (#ContactForm):
 * the shop handles its submit itself (mailto), and it loses its `action`.
 */

const DROP_TAGS = new Set([
  'script',
  'iframe',
  'frame',
  'frameset',
  'object',
  'embed',
  'applet',
  'base',
  'meta',
  'link',
  'svg',
  'math',
  'noscript',
  'template',
]);
/** Attributes that carry a URL or an animation target an attacker could point at script. */
const DROP_ATTRS = new Set(['formaction', 'values', 'to', 'from']);
const URL_ATTRS = new Set(['href', 'src', 'srcset', 'xlink:href', 'action', 'formaction', 'poster', 'data']);

function isUnsafeUrl(value: string): boolean {
  // eslint-disable-next-line no-control-regex
  const v = value.replace(/[\u0000- \u007f]+/g, '').toLowerCase();
  return v.startsWith('javascript:') || v.startsWith('vbscript:') || (v.startsWith('data:') && !v.startsWith('data:image/'));
}

export function sanitizeHtml(html: string): string {
  if (!html) return '';
  const doc = new DOMParser().parseFromString('<!doctype html><body>' + html, 'text/html');
  const walk = (node: Element): void => {
    for (const child of Array.from(node.children)) {
      const tag = child.tagName.toLowerCase();
      if (DROP_TAGS.has(tag)) {
        child.remove();
        continue;
      }
      if (tag === 'form') {
        if (child.id !== 'ContactForm') {
          child.remove();
          continue;
        }
        // The shop handles submits itself (contact form -> mailto); a form never posts to the origin's own URL.
        child.removeAttribute('action');
      }
      for (const attr of Array.from(child.attributes)) {
        const name = attr.name.toLowerCase();
        if (name.startsWith('on') || DROP_ATTRS.has(name)) {
          child.removeAttribute(attr.name);
        } else if (URL_ATTRS.has(name) && isUnsafeUrl(attr.value)) {
          child.removeAttribute(attr.name);
        }
      }
      walk(child);
    }
  };
  walk(doc.body);
  return doc.body.innerHTML;
}
