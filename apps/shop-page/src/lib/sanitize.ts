/**
 * sanitize.ts - a second line of defence for HTML the API hands over (product
 * descriptions, static pages). The backend sanitizes what organizers type;
 * this only makes sure nothing executable can reach the DOM even if that
 * ever regresses: no scripts, frames, plugin embeds, a form's own action,
 * inline event handlers or javascript: URLs. Presentation markup
 * (sections, <style>, images, links) is kept - the pages are the organizer's.
 */

const DROP_TAGS = new Set(['script', 'iframe', 'frame', 'frameset', 'object', 'embed', 'applet', 'base', 'meta', 'link']);
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
        // The shop handles submits itself (contact form -> mailto); a form never posts to the origin's own URL.
        child.removeAttribute('action');
      }
      for (const attr of Array.from(child.attributes)) {
        const name = attr.name.toLowerCase();
        if (name.startsWith('on')) {
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
