// @vitest-environment jsdom
/**
 * svg-sanitize.test.ts — XSS fixtures and allowlist behavior for the
 * DOMParser-based decor sanitizer.
 *
 * Runs under jsdom (the sanitizer requires DOMParser; without it, it fails
 * closed — see the "fail closed" suite below).
 */

import { describe, it, expect, vi, afterEach } from 'vitest';
import { sanitizeDecorSvg } from './svg-sanitize.js';

describe('sanitizeDecorSvg — pass-through of legitimate decor', () => {
  it('keeps allowlisted shapes with geometry attributes', () => {
    const out = sanitizeDecorSvg('<rect x="0" y="0" width="100" height="50"/>');
    expect(out).toBe('<rect x="0" y="0" width="100" height="50"/>');
  });

  it('keeps nested groups with transform and presentation attributes', () => {
    const input =
      '<g transform="translate(10,20)" opacity="0.5">' +
      '<circle cx="5" cy="5" r="3" fill="#ff0000" stroke="#000000" stroke-width="2"/>' +
      '<path d="M0 0L10 10"/>' +
      '</g>';
    const out = sanitizeDecorSvg(input);
    expect(out).toContain('<g transform="translate(10,20)" opacity="0.5">');
    expect(out).toContain('<circle cx="5" cy="5" r="3" fill="#ff0000" stroke="#000000" stroke-width="2"/>');
    expect(out).toContain('<path d="M0 0L10 10"/>');
  });

  it('keeps ellipse, line, polyline, and polygon', () => {
    const input =
      '<ellipse cx="1" cy="2" rx="3" ry="4"/>' +
      '<line x1="0" y1="0" x2="9" y2="9"/>' +
      '<polyline points="0,0 1,1"/>' +
      '<polygon points="0,0 1,0 1,1"/>';
    const out = sanitizeDecorSvg(input);
    expect(out).toContain('<ellipse cx="1" cy="2" rx="3" ry="4"/>');
    expect(out).toContain('<line x1="0" y1="0" x2="9" y2="9"/>');
    expect(out).toContain('<polyline points="0,0 1,1"/>');
    expect(out).toContain('<polygon points="0,0 1,0 1,1"/>');
  });

  it('keeps text and tspan with their (escaped) text content', () => {
    const out = sanitizeDecorSvg(
      '<text x="10" y="10" font-size="12" font-family="Arial" text-anchor="middle">STAGE <tspan y="20">A &amp; B</tspan></text>',
    );
    expect(out).toContain('font-size="12"');
    expect(out).toContain('STAGE ');
    expect(out).toContain('<tspan y="20">A &amp; B</tspan>');
  });

  it('re-escapes text content so it cannot break out of the fragment', () => {
    // "&lt;img onerror=…&gt;" as decoded text must stay escaped on output.
    const out = sanitizeDecorSvg('<text x="1" y="1">&lt;img src=x onerror=alert(1)&gt;</text>');
    expect(out).toContain('&lt;img src=x onerror=alert(1)&gt;');
    expect(out).not.toContain('<img');
  });
});

describe('sanitizeDecorSvg — XSS fixtures', () => {
  it('strips onload event handler attributes', () => {
    const out = sanitizeDecorSvg('<rect x="0" y="0" width="10" height="10" onload="alert(1)"/>');
    expect(out).toBe('<rect x="0" y="0" width="10" height="10"/>');
  });

  it('strips onclick / onmouseover handlers', () => {
    const out = sanitizeDecorSvg('<circle cx="1" cy="1" r="1" onclick="evil()" onmouseover="evil()"/>');
    expect(out).toBe('<circle cx="1" cy="1" r="1"/>');
  });

  it('drops a script child element and its content', () => {
    const out = sanitizeDecorSvg('<g><script>alert(document.cookie)</script><rect x="1" y="1" width="2" height="2"/></g>');
    expect(out).toBe('<g><rect x="1" y="1" width="2" height="2"/></g>');
    expect(out).not.toContain('script');
    expect(out).not.toContain('alert');
  });

  it('drops an anchor with javascript: href, keeping nothing of the subtree', () => {
    const out = sanitizeDecorSvg('<a href="javascript:alert(1)"><rect x="1" y="1" width="2" height="2"/></a>');
    expect(out).toBe('');
  });

  it('strips javascript: values even on otherwise-allowed attributes', () => {
    const out = sanitizeDecorSvg('<rect x="1" y="1" width="2" height="2" fill="javascript:alert(1)"/>');
    expect(out).not.toContain('javascript');
  });

  it('drops foreignObject subtrees (HTML injection vector)', () => {
    const out = sanitizeDecorSvg(
      '<foreignObject width="100" height="100"><div xmlns="http://www.w3.org/1999/xhtml">' +
        '<img src="x" onerror="alert(1)"/></div></foreignObject><rect x="0" y="0" width="1" height="1"/>',
    );
    expect(out).toBe('<rect x="0" y="0" width="1" height="1"/>');
  });

  it('drops use elements (external/internal reference vector)', () => {
    const out = sanitizeDecorSvg('<use href="#evil"/><use xlink:href="data:image/svg+xml,x" xmlns:xlink="http://www.w3.org/1999/xlink"/>');
    expect(out).toBe('');
  });

  it('drops image elements (resource loading vector)', () => {
    const out = sanitizeDecorSvg('<image href="https://evil.example/x.svg" width="10" height="10"/>');
    expect(out).toBe('');
  });

  it('drops style elements and strips style attributes', () => {
    const out = sanitizeDecorSvg(
      '<style>rect { fill: url("https://evil.example/x") }</style>' +
        '<rect x="1" y="1" width="2" height="2" style="background:url(https://evil.example)"/>',
    );
    expect(out).toBe('<rect x="1" y="1" width="2" height="2"/>');
  });

  it('strips href and xlink:href attributes on allowed elements', () => {
    const out = sanitizeDecorSvg(
      '<text x="1" y="1" href="javascript:alert(1)" xlink:href="javascript:alert(2)" xmlns:xlink="http://www.w3.org/1999/xlink">hi</text>',
    );
    expect(out).toBe('<text x="1" y="1">hi</text>');
  });

  it('strips url(...) values that could load external resources', () => {
    const out = sanitizeDecorSvg('<rect x="1" y="1" width="2" height="2" fill="url(https://evil.example/paint.svg#p)"/>');
    expect(out).toBe('<rect x="1" y="1" width="2" height="2"/>');
  });

  it('strips id/class and other non-allowlisted attributes', () => {
    const out = sanitizeDecorSvg('<rect id="x" class="y" data-evil="z" x="1" y="1" width="2" height="2"/>');
    expect(out).toBe('<rect x="1" y="1" width="2" height="2"/>');
  });

  it('drops elements smuggled through a foreign namespace', () => {
    const out = sanitizeDecorSvg(
      '<h:script xmlns:h="http://www.w3.org/1999/xhtml">alert(1)</h:script><rect x="1" y="1" width="2" height="2"/>',
    );
    expect(out).toBe('<rect x="1" y="1" width="2" height="2"/>');
  });

  it('drops comments and processing instructions', () => {
    const out = sanitizeDecorSvg('<!-- evil --><?evil pi?><rect x="1" y="1" width="2" height="2"/>');
    expect(out).toBe('<rect x="1" y="1" width="2" height="2"/>');
  });
});

describe('sanitizeDecorSvg — fail closed', () => {
  it('returns empty string for empty / whitespace input', () => {
    expect(sanitizeDecorSvg('')).toBe('');
    expect(sanitizeDecorSvg('   ')).toBe('');
  });

  it('returns empty string for malformed XML', () => {
    expect(sanitizeDecorSvg('<rect x="0"')).toBe('');
    expect(sanitizeDecorSvg('<g><rect/></p>')).toBe('');
  });

  it('returns empty string for input that closes the wrapper element', () => {
    // Attempting to escape the sanitizer's <svg> wrapper must not parse.
    expect(sanitizeDecorSvg('</svg><script>alert(1)</script><svg>')).toBe('');
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('returns empty string when DOMParser is unavailable (non-DOM env)', () => {
    vi.stubGlobal('DOMParser', undefined);
    expect(sanitizeDecorSvg('<rect x="0" y="0" width="1" height="1"/>')).toBe('');
  });
});
