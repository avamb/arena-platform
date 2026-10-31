/**
 * svg-sanitize.ts — strict allowlist sanitizer for organizer-uploaded decor SVG.
 *
 * The backend stores decor as an SVG *fragment* (no root <svg> element,
 * deterministic non-namespaced serialisation).  Because the fragment
 * originates from organizer uploads it must be treated as untrusted input:
 * the widget renders the seat map via `{@html}`, so anything that survives
 * this sanitizer executes with full DOM access on the host page.
 *
 * Strategy (fail closed):
 *   1. Parse the fragment as strict XML inside an SVG-namespaced wrapper
 *      using `DOMParser` — no regex parsing.  Malformed input → empty output.
 *   2. Walk the resulting tree and re-serialize ONLY:
 *        • elements in the SVG namespace whose local name is allowlisted
 *          (basic shapes, grouping, and text);
 *        • attributes (no namespace/prefix) whose name is allowlisted
 *          (geometry + a minimal presentation set);
 *        • text nodes inside <text>/<tspan> (re-escaped on output).
 *      Everything else — `script`, `foreignObject`, `use`, `image`, `style`,
 *      `a`, event handlers (`on*`), `href`/`xlink:href`, `style` attributes,
 *      comments, processing instructions — is dropped, including entire
 *      subtrees of disallowed elements.
 *   3. Attribute values that could trigger resource loading (`url(...)`)
 *      are stripped as well.
 *
 * The output is a deterministic, fully re-serialized fragment — nothing from
 * the input string is ever copied through verbatim.
 *
 * In non-DOM environments (no `DOMParser`, e.g. SSR / plain Node) the
 * sanitizer fails closed and returns an empty string.
 */

const SVG_NS = 'http://www.w3.org/2000/svg';

/**
 * Prefix declarations for the wrapper. The backend re-emits decor with the
 * prefixes it knows (`svg:text`, `inkscape:...`, `sodipodi:...`) and leaves the
 * declarations on the outer <svg> it does not store, so a strict XML parse of
 * the bare fragment dies on "Namespace prefix svg ... is not defined" and the
 * whole decor layer (row and seat numbers, the stage, the hall outline) was
 * dropped. Declaring them here lets the fragment resolve; elements outside the
 * SVG namespace (inkscape:, sodipodi:) are still rejected by the allowlist.
 */
const DECOR_XMLNS =
  `xmlns:svg="${SVG_NS}" ` +
  'xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" ' +
  'xmlns:sodipodi="http://sodipodi.sourceforge.net/DTD/sodipodi-0.dtd"';

/** Elements allowed in decor fragments (SVG namespace only). */
const ALLOWED_ELEMENTS: ReadonlySet<string> = new Set([
  'g',
  'path',
  'rect',
  'circle',
  'ellipse',
  'line',
  'polyline',
  'polygon',
  'text',
  'tspan',
]);

/** Elements whose text-node children are preserved (re-escaped). */
const TEXT_CONTAINERS: ReadonlySet<string> = new Set(['text', 'tspan']);

/** Attributes allowed on decor elements — geometry + minimal presentation. */
const ALLOWED_ATTRIBUTES: ReadonlySet<string> = new Set([
  // Geometry
  'd',
  'x',
  'y',
  'width',
  'height',
  'cx',
  'cy',
  'r',
  'rx',
  'ry',
  'points',
  'x1',
  'y1',
  'x2',
  'y2',
  // Presentation
  'fill',
  'stroke',
  'stroke-width',
  'opacity',
  'transform',
  'font-size',
  'font-family',
  'text-anchor',
]);

/** Escape text content for safe re-serialization. */
function escapeText(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

/** Escape an attribute value for safe re-serialization (double-quoted). */
function escapeAttr(s: string): string {
  return escapeText(s).replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

/** CSS properties of a `style` attribute that are carried over (as attributes). */
const ALLOWED_STYLE_PROPERTIES: ReadonlySet<string> = new Set([
  'fill',
  'stroke',
  'stroke-width',
  'opacity',
  'font-size',
  'font-family',
  'text-anchor',
]);

/**
 * Read the declarations of an inline `style` attribute and keep only the
 * allowlisted presentation properties with safe values. Authoring tools
 * (Inkscape and the plan generators) put the fill of every shape in `style`,
 * not in a `fill` attribute: dropping it left the hall background with the
 * SVG default fill, black. The result is emitted as plain attributes, so
 * nothing from `style` reaches the page as CSS.
 */
function parseStyleDeclarations(style: string): Array<[string, string]> {
  const out: Array<[string, string]> = [];
  if (!style) return out;
  for (const decl of style.split(';')) {
    const at = decl.indexOf(':');
    if (at < 0) continue;
    const prop = decl.slice(0, at).trim().toLowerCase();
    const value = decl.slice(at + 1).trim();
    if (!ALLOWED_STYLE_PROPERTIES.has(prop) || value === '') continue;
    if (!isSafeAttrValue(value) || /!important/i.test(value)) continue;
    out.push([prop, value]);
  }
  return out;
}

/**
 * Reject attribute values that could trigger resource loading or script.
 * `url(...)` in paint/presentation values can fetch external resources in
 * some engines; decor has no allowlisted paint-server elements anyway.
 */
function isSafeAttrValue(value: string): boolean {
  return !/url\s*\(/i.test(value) && !/javascript\s*:/i.test(value);
}

/** Node type constants (avoid depending on a global `Node`). */
const ELEMENT_NODE = 1;
const TEXT_NODE = 3;
const CDATA_SECTION_NODE = 4;

/**
 * Recursively serialize a node into `out`, keeping only allowlisted content.
 * Disallowed elements are dropped together with their entire subtree.
 */
function serializeNode(node: globalThis.Node, out: string[], inTextContainer: boolean): void {
  if (node.nodeType === TEXT_NODE || node.nodeType === CDATA_SECTION_NODE) {
    if (inTextContainer) {
      out.push(escapeText(node.nodeValue ?? ''));
    }
    return;
  }

  if (node.nodeType !== ELEMENT_NODE) {
    // Comments, processing instructions, etc. — dropped.
    return;
  }

  const el = node as Element;
  const name = el.localName;

  // Namespace + element allowlist: anything else is dropped with its subtree
  // (covers script, foreignObject, use, image, style, a, defs, filters, …).
  if (el.namespaceURI !== SVG_NS || !ALLOWED_ELEMENTS.has(name)) {
    return;
  }

  // Collected by name so a `style` declaration can override the presentation
  // attribute of the same name, as it does in a browser.
  const kept = new Map<string, string>();
  let styleValue = '';
  for (const attr of Array.from(el.attributes)) {
    // Only plain (non-namespaced, non-prefixed) attributes are considered —
    // this drops xlink:href, xmlns declarations, and friends outright.
    if (attr.prefix || attr.namespaceURI) continue;
    const attrName = attr.localName;
    if (attrName === 'style') {
      styleValue = attr.value;
      continue;
    }
    if (!ALLOWED_ATTRIBUTES.has(attrName)) continue; // drops on*, href, id, class, …
    if (!isSafeAttrValue(attr.value)) continue;
    kept.set(attrName, attr.value);
  }
  for (const [prop, value] of parseStyleDeclarations(styleValue)) {
    kept.set(prop, value);
  }
  const attrs: string[] = [];
  for (const [attrName, value] of kept) {
    attrs.push(` ${attrName}="${escapeAttr(value)}"`);
  }

  const children = Array.from(el.childNodes);
  if (children.length === 0) {
    out.push(`<${name}${attrs.join('')}/>`);
    return;
  }

  out.push(`<${name}${attrs.join('')}>`);
  const childInText = TEXT_CONTAINERS.has(name);
  for (const child of children) {
    serializeNode(child, out, childInText);
  }
  out.push(`</${name}>`);
}

/**
 * Sanitize an untrusted decor SVG fragment for safe `{@html}` injection.
 *
 * Returns a re-serialized fragment containing only allowlisted SVG elements,
 * attributes, and text — or an empty string when the input is blank,
 * malformed XML, or `DOMParser` is unavailable (fail closed).
 */
export function sanitizeDecorSvg(decor: string): string {
  if (!decor || decor.trim() === '') return '';
  if (typeof DOMParser === 'undefined') return ''; // non-DOM environment — fail closed

  let doc: Document;
  try {
    doc = new DOMParser().parseFromString(
      `<svg xmlns="${SVG_NS}" ${DECOR_XMLNS}>${decor}</svg>`,
      'application/xml',
    );
  } catch {
    return '';
  }

  // Strict XML parsing surfaces errors as a <parsererror> element.
  if (doc.getElementsByTagName('parsererror').length > 0) return '';

  const root = doc.documentElement;
  if (!root || root.localName !== 'svg' || root.namespaceURI !== SVG_NS) return '';

  const out: string[] = [];
  for (const child of Array.from(root.childNodes)) {
    serializeNode(child, out, false);
  }
  return out.join('');
}
