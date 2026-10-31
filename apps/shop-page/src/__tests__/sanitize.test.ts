import { describe, expect, it } from 'vitest';
import { sanitizeHtml } from '../lib/sanitize';

describe('sanitizeHtml', () => {
  it('drops scripts, frames and inline handlers but keeps presentation', () => {
    const out = sanitizeHtml(
      '<p onclick="x()">a <b>b</b></p><script>alert(1)</script><iframe src="https://e.test"></iframe><style>.a{color:red}</style><img src="https://x.test/a.jpg" onerror="y()">',
    );
    expect(out).toContain('<p>a <b>b</b></p>');
    expect(out).toContain('<style>.a{color:red}</style>');
    expect(out).toContain('<img src="https://x.test/a.jpg">');
    expect(out).not.toMatch(/script|iframe|onclick|onerror/);
  });

  it('removes javascript: and non-image data: URLs', () => {
    const out = sanitizeHtml('<a href="javascript:alert(1)">x</a><a href=" JaVa\nscript:alert(1)">y</a><a href="data:text/html;base64,AAAA">z</a><a href="/pages/contact">ok</a>');
    expect(out).not.toMatch(/javascript|data:text/i);
    expect(out).toContain('href="/pages/contact"');
  });

  it('drops svg, math, noscript and template with everything inside', () => {
    const out = sanitizeHtml(
      '<p>a</p><svg><animate attributeName="href" values="javascript:alert(1)"/><a xlink:href="javascript:1"><text>x</text></a></svg><math><mi xlink:href="javascript:1">m</mi></math><noscript><img src=x onerror=alert(1)></noscript><template><img src=x></template><p>b</p>',
    );
    expect(out).toBe('<p>a</p><p>b</p>');
  });

  it('drops other forms and formaction, keeps the contact form without an action', () => {
    const out = sanitizeHtml(
      '<form action="https://evil.test"><input name="pw"></form><button formaction="https://evil.test">go</button><form id="ContactForm" action="/contact"><button formaction="https://evil.test">send</button></form>',
    );
    expect(out).not.toContain('evil.test');
    expect(out).not.toContain('name="pw"');
    expect(out).toContain('id="ContactForm"');
    expect(out).toContain('send');
  });

  it('removes the animation attributes values, to and from wherever they appear', () => {
    const out = sanitizeHtml('<div values="javascript:1" to="javascript:1" from="x" class="k">t</div>');
    expect(out).toBe('<div class="k">t</div>');
  });

  it('keeps forms but removes where they post', () => {
    const out = sanitizeHtml('<form action="https://evil.test" method="post" id="ContactForm"><input name="x"></form>');
    expect(out).toContain('id="ContactForm"');
    expect(out).not.toContain('evil.test');
  });
});
