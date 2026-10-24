import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it, vi } from 'vitest';
import {
  descriptionPlain,
  hasDescription,
  renderArtHtml,
  renderDescription,
  renderRichText,
  resetMissingDescriptionHtmlReport,
  safeHref,
} from '../lib/richtext.ts';
import { applyDocumentChrome, renderEvent } from '../lib/render.ts';
import type { HostedPageResponse } from '../lib/api.ts';

// The golden corpus shared with the Go package (richtext_test.go reads the same
// file): old text -> ART. The page must show the old text and its ART alike.
const CORPUS_PATH = resolve(
  dirname(fileURLToPath(import.meta.url)),
  '../../../backend/internal/platform/richtext/testdata/richtext_legacy.json',
);

interface CorpusCase {
  name: string;
  profile: string;
  legacy: string;
  art: string;
}

const corpus: CorpusCase[] = JSON.parse(readFileSync(CORPUS_PATH, 'utf8'));

/** Where the old page renderer and the server's converter differ on purpose.
 * The old renderer makes a list of ONE "- " line, has no ordered lists and keeps
 * the line and paragraph separators U+2028/U+2029 as plain spaces; the server
 * (FromLegacy) needs two lines for a list, knows "1. 2. 3." runs and breaks
 * lines at U+2028. The ART is the truth, so for these the new renderer is only
 * held to showing the ART as it is. */
const OLD_RENDERER_DIFFERS = new Map<string, string>([
  ["single dash line is not a list", "old renderer: one '- ' line is already a list"],
  ["single star line is not a list", "old renderer: one '* ' line is already a list"],
  ["numbered list", "old renderer: no ordered lists"],
  ["bullet list then numbered list", "old renderer: no ordered lists"],
  ["stray bullet inside text", "old renderer: a lone leading '- ' line is a list"],
  ["numbered list stops at the first gap", "old renderer: no ordered lists"],
  ["numbered list stops at a repeated number", "old renderer: no ordered lists"],
  ["two numbered lists", "old renderer: no ordered lists"],
  ["line and paragraph separators", "old renderer: U+2028/U+2029 are not line breaks"],
]);

/** The visible structure of a fragment: elements without attributes, text with
 * runs of whitespace collapsed and invisible characters removed. */
function structure(node: Node): string {
  let out = '';
  node.childNodes.forEach((c) => {
    if (c.nodeType === 3) {
      out += (c.nodeValue ?? '')
        .replace(/[\u200b\u2060\ufeff\u2066-\u2069]/g, '')
        .replace(/[\s\u00a0]+/g, ' ');
    } else if (c.nodeType === 1) {
      const tag = (c as Element).localName;
      out += tag === 'br' ? '<br>' : `<${tag}>${structure(c)}</${tag}>`;
    }
  });
  return out;
}

function html(src: string): string {
  const box = document.createElement('div');
  box.appendChild(renderArtHtml(document, src));
  return box.innerHTML;
}

/** The tree a plain DOMParser reads from the ART, as the same structure. */
function parsedStructure(src: string): string {
  const parsed = new DOMParser().parseFromString(`<!doctype html><body>${src}`, 'text/html');
  return structure(parsed.body);
}

describe('golden corpus: the old renderer and the ART renderer agree', () => {
  it('reads the shared corpus', () => {
    expect(corpus.length).toBeGreaterThan(60);
    expect(corpus.filter((c) => c.profile === 'description').length).toBeGreaterThan(50);
  });

  for (const c of corpus) {
    if (c.profile !== 'description') continue;
    const reason = OLD_RENDERER_DIFFERS.get(c.name);
    if (reason !== undefined) continue;
    it(c.name, () => {
      const legacy = renderRichText(document, c.legacy);
      const art = renderArtHtml(document, c.art);
      expect(structure(art)).toBe(structure(legacy));
    });
  }

  it('the known differences are cases of the corpus', () => {
    const names = new Set(corpus.map((c) => c.name));
    for (const name of OLD_RENDERER_DIFFERS.keys()) expect(names.has(name)).toBe(true);
  });
});

describe('renderArtHtml keeps every tag and text of the corpus ART', () => {
  for (const c of corpus) {
    it(`${c.profile}: ${c.name}`, () => {
      const out = document.createElement('div');
      out.appendChild(renderArtHtml(document, c.art));
      // links of the email_note profile carry rel/target added by the page; the
      // structure ignores attributes
      expect(structure(out)).toBe(parsedStructure(c.art));
    });
  }
});

describe('renderArtHtml: the safe subset', () => {
  it('rebuilds the whitelisted tags', () => {
    const src =
      '<h3>T</h3><h4>S</h4><p>A <strong>b</strong> <em>i</em> <u>u</u><br>x</p><blockquote><p>q</p></blockquote>' +
      '<ul><li>a</li></ul><ol><li>b</li></ol>';
    expect(html(src)).toBe(src);
  });

  it('gives links rel and target and keeps https and mailto only', () => {
    expect(html('<p><a href="https://example.com/a?x=1&amp;y=2">go</a></p>')).toBe(
      '<p><a href="https://example.com/a?x=1&amp;y=2" rel="noopener noreferrer" target="_blank">go</a></p>',
    );
    expect(html('<p><a href="mailto:a@example.com">mail</a></p>')).toContain('href="mailto:a@example.com"');
    // the server\'s own rel and target are replaced by the page\'s
    expect(html('<a href="https://e.com" rel="opener" target="_self">x</a>')).toBe(
      '<a href="https://e.com" rel="noopener noreferrer" target="_blank">x</a>',
    );
  });

  it.each([
    'javascript:alert(1)',
    'JaVaScRiPt:alert(1)',
    ' javascript:alert(1)',
    'java\tscript:alert(1)',
    'jav&#x61;script:alert(1)',
    'data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==',
    'vbscript:msgbox(1)',
    '//evil.example/x',
    '/relative/path',
    'relative.html',
    'http://example.com',
    'ftp://example.com',
    'https://exa mple.com',
    'https://example.com/\u200b',
    '',
  ])('drops the link but keeps the text for href %j', (href) => {
    const out = html(`<p><a href="${href}">label</a></p>`);
    expect(out).toBe('<p>label</p>');
  });

  it('shows no script, event handler, style or unknown element', () => {
    const hostile =
      '<p onclick="alert(1)" style="x:y">a</p><script>alert(1)</script><img src=x onerror=alert(1)>' +
      '<iframe src="https://evil.example"></iframe><svg><script>alert(1)</script></svg>' +
      '<math><mi xlink:href="javascript:alert(1)">m</mi></math><style>p{display:none}</style>' +
      '<form action="https://evil.example"><input name=x><button>go</button></form>' +
      '<a href="https://e.com" onclick="alert(1)" style="color:red" id="x" class="c">l</a>' +
      '<div class="x"><span style="position:fixed">text of div</span></div>';
    const box = document.createElement('div');
    box.appendChild(renderArtHtml(document, hostile));
    const out = box.innerHTML;
    expect(box.querySelector('script, img, iframe, svg, math, style, form, input, button, div, span')).toBeNull();
    expect(out).not.toMatch(/onclick|onerror|style=|alert\(1\)|class=|id=|evil\.example/);
    expect(out).toContain('text of div');
    expect(out).toContain('<a href="https://e.com" rel="noopener noreferrer" target="_blank">l</a>');
    // an unknown element is shown as its text
    expect(html('<div><span>kept</span></div>')).toBe('kept');
  });

  it('never gives the server markup to innerHTML', () => {
    const proto = Element.prototype;
    const desc = Object.getOwnPropertyDescriptor(proto, 'innerHTML');
    expect(desc).toBeDefined();
    const calls: string[] = [];
    Object.defineProperty(proto, 'innerHTML', {
      configurable: true,
      get: desc?.get,
      set(v: string) {
        calls.push(v);
        desc?.set?.call(this, v);
      },
    });
    try {
      renderArtHtml(document, '<p>a <strong>b</strong><a href="https://e.com">c</a></p><script>x</script>');
      renderDescription(document, { description_html: '<p>x</p>' });
    } finally {
      Object.defineProperty(proto, 'innerHTML', desc as PropertyDescriptor);
    }
    expect(calls).toEqual([]);
  });

  it('shows text, not markup, for escaped tags', () => {
    expect(html('<p>&lt;script&gt;alert(1)&lt;/script&gt; &amp; &lt;b&gt;</p>')).toBe(
      '<p>&lt;script&gt;alert(1)&lt;/script&gt; &amp; &lt;b&gt;</p>',
    );
  });

  it('flattens a very deep tree to text instead of recursing forever', () => {
    const deep = '<blockquote>'.repeat(200) + 'bottom' + '</blockquote>'.repeat(200);
    const box = document.createElement('div');
    box.appendChild(renderArtHtml(document, deep));
    expect(box.textContent).toBe('bottom');
    expect(box.querySelectorAll('blockquote').length).toBeLessThanOrEqual(33);
  });

  it('past the depth limit skips dropped elements with their content', () => {
    const open = '<blockquote>'.repeat(40);
    const close = '</blockquote>'.repeat(40);
    const deep =
      open +
      'visible<script>alert(1)</script><style>p{display:none}</style><textarea>area text</textarea>' +
      '<noscript>no script text</noscript><template><p>tpl</p></template> tail<a href="javascript:alert(2)">link</a>' +
      close;
    const box = document.createElement('div');
    box.appendChild(renderArtHtml(document, deep));
    expect(box.textContent).toBe('visible taillink');
    expect(box.innerHTML).not.toMatch(/alert|display|area text|no script|tpl|<a /);
  });

  it('drops <template>, <noscript>, <textarea> and <title> with their content', () => {
    expect(html('<p>a</p><template><p>x</p><script>alert(1)</script></template><p>b</p>')).toBe('<p>a</p><p>b</p>');
    expect(html('<p>a</p><noscript><p>no</p><img src=x onerror=alert(1)></noscript><p>b</p>')).toBe('<p>a</p><p>b</p>');
    expect(html('<p>a</p><textarea>secret</textarea><title>tt</title><p>b</p>')).toBe('<p>a</p><p>b</p>');
  });

  it('keeps no link from a MathML or SVG <a href="javascript:">', () => {
    const out = html(
      '<p>m</p><math><a href="javascript:alert(1)">math link</a></math>' +
        '<svg><a href="javascript:alert(2)" xlink:href="javascript:alert(3)"><text>svg link</text></a></svg>' +
        '<math><mi xlink:href="javascript:alert(4)">mi</mi></math>',
    );
    expect(out).not.toMatch(/javascript|alert|<a |href|<svg|<math/);
    expect(out).toContain('math link');
  });

  it('renders nothing for an empty string and for a comment', () => {
    expect(html('')).toBe('');
    expect(html('<!-- x -->')).toBe('');
  });
});

describe('safeHref', () => {
  it('accepts absolute https and mailto and nothing else', () => {
    expect(safeHref('https://example.com/a')).toBe('https://example.com/a');
    expect(safeHref('HTTPS://Example.com')).toBe('HTTPS://Example.com');
    expect(safeHref('mailto:a@b.co')).toBe('mailto:a@b.co');
    expect(safeHref(null)).toBeNull();
    expect(safeHref('javascript:alert(1)')).toBeNull();
    expect(safeHref('//x.y')).toBeNull();
  });
});

describe('the page falls back to the old fields', () => {
  it('uses description_html when present, else the old description with the legacy renderer', () => {
    const box = (ev: Parameters<typeof renderDescription>[1]): string => {
      const d = document.createElement('div');
      d.appendChild(renderDescription(document, ev));
      return d.innerHTML;
    };
    expect(box({ description: '<p>raw</p>', description_html: '<p>safe <strong>html</strong></p>' })).toBe(
      '<p>safe <strong>html</strong></p>',
    );
    expect(box({ description: 'A **bold** word' })).toBe('<p>A <strong>bold</strong> word</p>');
    expect(box({ description: 'A **bold** word', description_html: null })).toBe('<p>A <strong>bold</strong> word</p>');
  });

  it('reports a description that came without description_html as an error, once', () => {
    resetMissingDescriptionHtmlReport();
    const spy = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    try {
      renderDescription(document, { description: '<p>raw art</p>', description_html: null });
      renderDescription(document, { description: 'again', description_html: null });
      expect(spy).toHaveBeenCalledTimes(1);
      // with description_html, or with no description at all, nothing is logged
      resetMissingDescriptionHtmlReport();
      renderDescription(document, { description: 'x', description_html: '<p>x</p>' });
      renderDescription(document, { description: null, description_html: null });
      expect(spy).toHaveBeenCalledTimes(1);
    } finally {
      spy.mockRestore();
    }
  });

  it('hasDescription sees either field', () => {
    expect(hasDescription({ description: null, description_html: null })).toBe(false);
    expect(hasDescription({ description: 'x' })).toBe(true);
    expect(hasDescription({ description: null, description_html: '<p>x</p>' })).toBe(true);
  });

  it('descriptionPlain prefers description_text, else the old plainText', () => {
    expect(descriptionPlain({ description: '<p>raw</p>', description_text: 'Tonight\n\n• one\n• two' })).toBe(
      'Tonight • one • two',
    );
    expect(descriptionPlain({ description: 'A **bold** _tale_\n\n- one\n- two' })).toBe('A bold tale one two');
    expect(descriptionPlain({ description: null })).toBe('');
  });
});

describe('the sales page uses description_html and description_text', () => {
  const data: HostedPageResponse = {
    org: { slug: 'arenasoldout', name: 'Arena Sold Out', logo_url: null },
    event: {
      id: '01929d0e-0e47-7000-8000-000000000301',
      slug: 'summer-festival',
      title: 'Summer Festival 2026',
      description: '<p>raw <strong>art</strong></p>',
      description_html:
        '<h3>Head</h3><p>Hello <strong>there</strong> <a href="https://example.com/a" rel="noopener noreferrer" target="_blank">site</a><img src=x onerror=alert(1)></p><script>alert(1)</script>',
      description_text: 'Head\n\nHello there site',
      short_description: null,
      image_url: null,
      poster_url: null,
      age_rating: null,
      venue_names: [],
      first_session_at: null,
      last_session_at: null,
      first_session_timezone: null,
    },
    feed_token: 'ft',
    default_locale: 'en',
  };

  it('shows description_html through the safe renderer, never the raw description', () => {
    const box = document.createElement('div');
    renderEvent(box, data, 'en', { apiBase: '', resumingCheckout: false });
    const d = box.querySelector('.asa-hero-description');
    expect(d?.innerHTML).toBe(
      '<h3>Head</h3><p>Hello <strong>there</strong> <a href="https://example.com/a" rel="noopener noreferrer" target="_blank">site</a></p>',
    );
    expect(d?.querySelector('script, img')).toBeNull();
  });

  it('falls back to the old description and the legacy renderer on an older backend', () => {
    const box = document.createElement('div');
    const old = { ...data, event: { ...data.event, description: 'A **bold** word', description_html: undefined, description_text: undefined } };
    renderEvent(box, old, 'en', { apiBase: '', resumingCheckout: false });
    expect(box.querySelector('.asa-hero-description')?.innerHTML).toBe('<p>A <strong>bold</strong> word</p>');
  });

  it('puts description_text into <title> and <meta>, the old plainText when it is missing', () => {
    document.head.innerHTML = '';
    applyDocumentChrome('en', { title: 'T', descriptionPlain: descriptionPlain(data.event) });
    expect(document.title).toBe('T — Arena Sold Out');
    expect(document.querySelector('meta[name="description"]')?.getAttribute('content')).toBe('Head Hello there site');

    applyDocumentChrome('en', { title: 'T2', description: descriptionPlain({ description: 'A **bold** _tale_' }) });
    expect(document.querySelector('meta[name="description"]')?.getAttribute('content')).toBe('A bold tale');
  });
});
