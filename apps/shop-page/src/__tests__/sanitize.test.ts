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

  it('keeps forms but removes where they post', () => {
    const out = sanitizeHtml('<form action="https://evil.test" method="post" id="ContactForm"><input name="x"></form>');
    expect(out).toContain('id="ContactForm"');
    expect(out).not.toContain('evil.test');
  });
});
