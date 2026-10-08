import { describe, expect, it } from 'vitest';
import { plainText, renderRichText } from '../lib/richtext.ts';

function html(src: string): string {
  const box = document.createElement('div');
  box.appendChild(renderRichText(document, src));
  return box.innerHTML;
}

describe('renderRichText', () => {
  it('turns a blank line into a new paragraph and a single break into <br>', () => {
    expect(html('First line\nsecond line\n\nNext paragraph')).toBe('<p>First line<br>second line</p><p>Next paragraph</p>');
  });

  it('formats **bold** and _italic_, italic inside bold included', () => {
    expect(html('A **bold** and _italic_ word')).toBe('<p>A <strong>bold</strong> and <em>italic</em> word</p>');
    expect(html('**bold and _both_ here**')).toBe('<p><strong>bold and <em>both</em> here</strong></p>');
  });

  it('leaves unmatched markers and snake_case words as typed', () => {
    expect(html('2 ** 3 and a ** b')).toBe('<p>2 ** 3 and a ** b</p>');
    expect(html('open file_name_here now')).toBe('<p>open file_name_here now</p>');
    expect(html('**not closed')).toBe('<p>**not closed</p>');
    expect(html('** spaced **')).toBe('<p>** spaced **</p>');
  });

  it('builds bulleted lists from "- ", "• " and "* " lines', () => {
    expect(html('Line-up:\n- Anna — **piano**\n• Boris\n* Clara')).toBe(
      '<p>Line-up:</p><ul><li>Anna — <strong>piano</strong></li><li>Boris</li><li>Clara</li></ul>',
    );
  });

  it('does not take **bold** at the start of a line for a bullet', () => {
    expect(html('**Tonight** only')).toBe('<p><strong>Tonight</strong> only</p>');
  });

  it('never interprets HTML: tags are shown as text', () => {
    const out = html('<img src=x onerror=alert(1)> and <b>no</b>');
    expect(out).toBe('<p>&lt;img src=x onerror=alert(1)&gt; and &lt;b&gt;no&lt;/b&gt;</p>');
    const box = document.createElement('div');
    box.appendChild(renderRichText(document, '<script>alert(1)</script>'));
    expect(box.querySelector('script, img, b')).toBeNull();
  });

  it('handles Windows line endings and surrounding whitespace', () => {
    expect(html('  One\r\n\r\nTwo  \r\n')).toBe('<p>One</p><p>Two</p>');
  });

  it('renders nothing for an empty description', () => {
    expect(html('   \n  ')).toBe('');
  });
});

describe('plainText', () => {
  it('drops the markers for the page meta description', () => {
    expect(plainText('A **bold** _tale_\n\n- one\n- two')).toBe('A bold tale one two');
  });
});
