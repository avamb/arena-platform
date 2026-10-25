// The sanitizer is a port of Sanitize in the Go package richtext. The table
// below is taken from the Go tests (richtext_test.go, richtext_review_test.go)
// with the Go expectations, so the two stay in step.

import { describe, expect, it } from 'vitest'

import { PROFILES, RichTextError, cleanHref, measure, parseLinkInput, sanitize } from '../src/index'
import { renameStyleAttributes } from '../src/sanitize'

const descr = PROFILES.description
const email = PROFILES.email_note
const sess = PROFILES.session_message

const ch = (...codes: number[]) => String.fromCodePoint(...codes)
const BACKSLASH = ch(92)
const long = 'https://example.com/' + 'a'.repeat(480) // exactly 500 characters

type Case = [name: string, input: string, profile: typeof descr, want: string]

const cases: Case[] = [
  // basics and aliases
  ['paragraph', '<p>Hi</p>', descr, '<p>Hi</p>'],
  ['bare text', 'Hi there', descr, '<p>Hi there</p>'],
  ['empty', '', descr, ''],
  ['b and i', '<b>x</b><i>y</i>', descr, '<p><strong>x</strong><em>y</em></p>'],
  ['underline', '<u>x</u>', descr, '<p><u>x</u></p>'],
  ['mark order is canonical', '<u><em><strong>x</strong></em></u>', descr, '<p><strong><em><u>x</u></em></strong></p>'],
  ['link outside marks', '<strong><a href="https://e.com">x</a></strong>', descr, '<p><a href="https://e.com"><strong>x</strong></a></p>'],
  ['unclosed tags', '<p>a<strong>b', descr, '<p>a<strong>b</strong></p>'],
  ['hebrew', '<p>שלום <b>עולם</b></p>', descr, '<p>שלום <strong>עולם</strong></p>'],
  ['text entities', '&lt;script&gt;alert(1)&lt;/script&gt;', descr, '<p>&lt;script&gt;alert(1)&lt;/script&gt;</p>'],
  ['numeric entities of tag characters', '<p>&#60;b&#62; 5 &amp; 6</p>', descr, '<p>&lt;b&gt; 5 &amp; 6</p>'],
  ['nbsp and numeric spaces', 'a&nbsp;&nbsp;b&#160;c&#x20;d', descr, '<p>a b c d</p>'],
  ['literal nbsp character', 'a' + ch(0xa0, 0xa0) + 'b', descr, '<p>a b</p>'],
  ['newlines are spaces', '<p>a\nb\n\nc</p>', descr, '<p>a b c</p>'],
  ['newlines between blocks', '<p>a</p>\n<p>b</p>\n', descr, '<p>a</p><p>b</p>'],
  ['br kept, border br dropped', '<br>a<br><br>b<br>', descr, '<p>a<br><br>b</p>'],
  ['spaces around br', '<p>a <br> b</p>', descr, '<p>a<br>b</p>'],
  ['marks do not span a break', '<strong>a<br>b</strong>', descr, '<p><strong>a</strong><br><strong>b</strong></p>'],

  // OWASP style vectors
  ['javascript scheme', '<a href="javascript:alert(1)">x</a>', descr, '<p>x</p>'],
  ['mixed case javascript', '<a href="JaVaScRiPt:alert(1)">x</a>', descr, '<p>x</p>'],
  ['entity encoded javascript', '<a href="&#106;avascript:alert(1)">x</a>', descr, '<p>x</p>'],
  ['hex entity encoded javascript', '<a href="&#x6A;avascript:alert(1)">x</a>', descr, '<p>x</p>'],
  ['newline inside scheme', '<a href="java&#x0A;script:alert(1)">x</a>', descr, '<p>x</p>'],
  ['tab inside scheme', '<a href="java\tscript:alert(1)">x</a>', descr, '<p>x</p>'],
  ['data scheme', '<a href="data:text/html;base64,PHNjcmlwdD4=">x</a>', descr, '<p>x</p>'],
  ['vbscript scheme', '<a href="vbscript:msgbox(1)">x</a>', descr, '<p>x</p>'],
  ['protocol relative', '<a href="//evil.com">x</a>', descr, '<p>x</p>'],
  ['relative path', '<a href="/admin">x</a>', descr, '<p>x</p>'],
  ['fragment', '<a href="#x">x</a>', descr, '<p>x</p>'],
  ['backslash host', '<a href="https:' + BACKSLASH + BACKSLASH + 'evil.com">x</a>', descr, '<p>x</p>'],
  ['https without slashes', '<a href="https:evil.com">x</a>', descr, '<p>x</p>'],
  ['plain http', '<a href="http://example.com">x</a>', descr, '<p>x</p>'],
  ['upper case scheme and host', '<a href="HTTPS://Example.com">x</a>', descr, '<p><a href="https://example.com">x</a></p>'],
  ['leading space in href', '<a href=" https://x">y</a>', descr, '<p><a href="https://x">y</a></p>'],
  ['trailing newline in href', '<a href="https://x.com/p\n">y</a>', descr, '<p><a href="https://x.com/p">y</a></p>'],
  ['space inside href', '<a href="https://exa mple.com">y</a>', descr, '<p>y</p>'],
  ['path and query kept', '<a href="https://Example.com/A/b?x=1&y=2#z">y</a>', descr, '<p><a href="https://example.com/A/b?x=1&amp;y=2#z">y</a></p>'],
  ['href with quote', '<a href="https://example.com/&quot;onmouseover=alert(1)">y</a>', descr, '<p>y</p>'],
  ['event handler attributes', '<p onclick="alert(1)" style="color:red" class="x" id="y">x</p>', descr, '<p>x</p>'],
  ['link keeps only href', '<a href="https://e.com" onclick="x()" target="_self" rel="x" style="a:b">y</a>', descr, '<p><a href="https://e.com">y</a></p>'],
  ['uppercase attribute names', '<A HREF="https://e.com" ONCLICK="x()">y</A>', descr, '<p><a href="https://e.com">y</a></p>'],
  ['svg onload', '<svg onload=alert(1)><circle/></svg>text', descr, '<p>text</p>'],
  ['img onerror', '<img src=x onerror=alert(1)>a', descr, '<p>a</p>'],
  ['iframe', '<iframe src="https://e.com">hello</iframe>x', descr, '<p>x</p>'],
  ['object and embed', '<object data="x">o</object><embed src="x">y', descr, '<p>y</p>'],
  ['script with content', '<script>alert(1)</script>ok', descr, '<p>ok</p>'],
  ['style with content', '<style>p{color:red}</style>ok', descr, '<p>ok</p>'],
  ['noscript and template', '<noscript>n</noscript><template>t</template>ok', descr, '<p>ok</p>'],
  ['comment', '<!-- secret --><p>ok</p>', descr, '<p>ok</p>'],
  ['conditional comment', '<!--[if IE]><script>alert(1)</script><![endif]-->ok', descr, '<p>ok</p>'],
  [
    'nested anchors',
    '<a href="https://a.com">1<a href="https://b.com">2</a></a>',
    descr,
    '<p><a href="https://a.com">1</a><a href="https://b.com">2</a></p>',
  ],
  ['broken script tag', '<scr<script>ipt>alert(1)</scr</script>ipt>', descr, '<p>ipt&gt;alert(1)ipt&gt;</p>'],
  ['double angle script', '<<script>alert(1)//<</script>', descr, '<p>&lt;</p>'],
  ['script inside a link', '<a href="https://e.com"><script>alert(1)</script>t</a>', descr, '<p><a href="https://e.com">t</a></p>'],

  // hosts
  ['userinfo', '<a href="https://user:pw@example.com/">x</a>', descr, '<p>x</p>'],
  ['userinfo trick', '<a href="https://example.com@evil.com/">x</a>', descr, '<p>x</p>'],
  ['port', '<a href="https://example.com:8080/">x</a>', descr, '<p>x</p>'],
  ['empty port', '<a href="https://example.com:/">x</a>', descr, '<p>x</p>'],
  ['ipv4', '<a href="https://127.0.0.1/">x</a>', descr, '<p>x</p>'],
  ['public ipv4', '<a href="https://8.8.8.8/">x</a>', descr, '<p>x</p>'],
  ['ipv6', '<a href="https://[::1]/">x</a>', descr, '<p>x</p>'],
  ['decimal ip', '<a href="https://2130706433/">x</a>', descr, '<p>x</p>'],
  ['hex ip', '<a href="https://0x7f.0.0.1/">x</a>', descr, '<p>x</p>'],
  ['localhost', '<a href="https://localhost/">x</a>', descr, '<p>x</p>'],
  ['LOCALHOST', '<a href="https://LOCALHOST/x">x</a>', descr, '<p>x</p>'],
  ['sub localhost', '<a href="https://app.localhost/">x</a>', descr, '<p>x</p>'],
  ['empty host', '<a href="https:///path">x</a>', descr, '<p>x</p>'],
  ['percent in host', '<a href="https://exa%6Dple.com/">x</a>', descr, '<p>x</p>'],
  ['long href', '<a href="' + long + '">x</a>', descr, '<p><a href="' + long + '">x</a></p>'],
  ['too long href', '<a href="' + long + 'a">x</a>', descr, '<p>x</p>'],
  ['empty href', '<a href="">x</a>', descr, '<p>x</p>'],
  ['anchor without href', '<a name="x">x</a>', descr, '<p>x</p>'],
  [
    'idn host is stored as punycode',
    '<a href="https://пример.рф/путь">x</a>',
    descr,
    '<p><a href="https://xn--e1afmkfd.xn--p1ai/путь">x</a></p>',
  ],

  // schemes per profile
  ['mailto in description drops the query', '<a href="mailto:Org@Example.com?subject=Hi">m</a>', descr, '<p><a href="mailto:Org@example.com">m</a></p>'],
  ['mailto uppercase scheme', '<a href="MAILTO:a@b.com">m</a>', descr, '<p><a href="mailto:a@b.com">m</a></p>'],
  ['mailto in email note', '<a href="mailto:a@b.com">m</a>', email, '<p>m</p>'],
  ['mailto in session message', '<a href="mailto:a@b.com">m</a>', sess, '<p>m</p>'],
  ['mailto without address', '<a href="mailto:">m</a>', descr, '<p>m</p>'],
  ['mailto with two addresses', '<a href="mailto:a@b.com,c@d.com">m</a>', descr, '<p>m</p>'],

  // headings and blockquote per profile
  [
    'headings in description',
    '<h1>A</h1><h2>B</h2><h3>C</h3><h4>D</h4><h5>E</h5><h6>F</h6>',
    descr,
    '<h3>A</h3><h3>B</h3><h3>C</h3><h4>D</h4><h4>E</h4><h4>F</h4>',
  ],
  ['heading keeps marks', '<h1>A <em>b</em></h1>', descr, '<h3>A <em>b</em></h3>'],
  ['heading in email note', '<h1>A</h1><h3>B</h3>', email, '<p><strong>A</strong></p><p><strong>B</strong></p>'],
  ['heading in session message', '<h2>A</h2>text', sess, '<p><strong>A</strong></p><p>text</p>'],
  ['blockquote in description', '<blockquote>q</blockquote>', descr, '<blockquote><p>q</p></blockquote>'],
  [
    'blockquote with paragraphs',
    '<blockquote><p>a</p><p>b <b>c</b></p></blockquote>',
    descr,
    '<blockquote><p>a</p><p>b <strong>c</strong></p></blockquote>',
  ],
  ['nested blockquote is flat', '<blockquote><blockquote>q</blockquote></blockquote>', descr, '<blockquote><p>q</p></blockquote>'],
  ['blockquote in email note', '<blockquote>q</blockquote>', email, '<p>q</p>'],
  ['blockquote in session message', '<blockquote><p>a</p><p>b</p></blockquote>', sess, '<p>a</p><p>b</p>'],
  [
    'list inside blockquote',
    '<blockquote><ul><li>a</li><li>b</li></ul></blockquote>',
    descr,
    '<blockquote><p>a</p><p>b</p></blockquote>',
  ],

  // blocks
  ['divs', '<div>a</div><div>b</div>', descr, '<p>a</p><p>b</p>'],
  ['table', '<table><tr><td>a</td><td>b</td></tr><tr><td>c</td></tr></table>', descr, '<p>a</p><p>b</p><p>c</p>'],
  ['section and article', '<section>a<article>b</article></section>', descr, '<p>a</p><p>b</p>'],
  ['other tags are unwrapped', '<span style="x">a</span> <font color="red">b</font><center>c</center>', descr, '<p>a b</p><p>c</p>'],
  ['empty paragraphs', '<p></p><p> </p><p><br></p>x', descr, '<p>x</p>'],
  ['empty link', '<a href="https://e.com"></a>', descr, ''],
  ['link with only spaces', '<a href="https://e.com"> </a>x', descr, '<p>x</p>'],
  ['link with only a break', 'a<a href="https://e.com"><br></a>b', descr, '<p>a<br>b</p>'],
  ['li outside a list', '<li>a</li><li>b</li>', descr, '<p>a</p><p>b</p>'],
  ['list inside paragraph', '<p>a<ul><li>b</li></ul></p>', descr, '<p>a</p><ul><li>b</li></ul>'],
  ['empty lists', '<ul></ul><ol><li></li><li> </li></ol>', descr, ''],
  ['empty items are dropped', '<ul><li>a</li><li></li><li>b</li></ul>', descr, '<ul><li>a</li><li>b</li></ul>'],
  ['paragraphs in a list item', '<ul><li><p>a</p><p>b</p></li></ul>', descr, '<ul><li>a<br>b</li></ul>'],
  ['text between list tags', '<ul>x<li>a</li></ul>', descr, '<ul><li>x</li><li>a</li></ul>'],
  ['ordered list', '<ol><li>a</li><li>b</li></ol>', descr, '<ol><li>a</li><li>b</li></ol>'],
  ['ol start is not kept', '<ol start="5"><li>a</li></ol>', descr, '<ol><li>a</li></ol>'],
  [
    'nesting depth two is kept',
    '<ul><li>a<ol><li>b</li></ol></li></ul>',
    descr,
    '<ul><li>a<ol><li>b</li></ol></li></ul>',
  ],
  [
    'nesting depth three is flattened',
    '<ul><li>a<ul><li>b<ul><li>c<ul><li>d</li></ul></li></ul></li></ul></li></ul>',
    descr,
    '<ul><li>a<ul><li>b</li><li>c</li><li>d</li></ul></li></ul>',
  ],
  ['list directly in list', '<ul><li>a</li><ul><li>b</li></ul></ul>', descr, '<ul><li>a<ul><li>b</li></ul></li></ul>'],
  ['heading inside list item', '<ul><li><h1>a</h1></li></ul>', descr, '<ul><li><strong>a</strong></li></ul>'],
  [
    'link in list item',
    '<ul><li><a href="https://e.com">a</a> b</li></ul>',
    descr,
    '<ul><li><a href="https://e.com">a</a> b</li></ul>',
  ],
  ['zero-width joiner is allowed', '<p>' + ch(0x1f468, 0x200d, 0x1f469) + '</p>', descr, '<p>' + ch(0x1f468, 0x200d, 0x1f469) + '</p>'],

  // the character policy
  ['isolates and zero-width characters are removed', '<p>a' + ch(0x2066) + 'b' + ch(0x2069, 0x200b, 0xfeff, 0x2060) + 'c</p>', descr, '<p>abc</p>'],
  ['direction marks are kept', '<p>a' + ch(0x200f) + ' b' + ch(0x200e) + '</p>', descr, '<p>a' + ch(0x200f) + ' b' + ch(0x200e) + '</p>'],
  ['bidi override is dropped', '<p>a' + ch(0x202e) + 'b</p>', descr, '<p>ab</p>'],
  ['control characters are dropped', '<p>a' + ch(1, 0x7f, 0x85) + 'b</p>', descr, '<p>ab</p>'],
  ['tag characters are dropped', '<p>a' + ch(0xe0041) + 'b</p>', descr, '<p>ab</p>'],
  ['line separator is a space', '<p>a' + ch(0x2028) + 'b</p>', descr, '<p>a b</p>'],
]

describe('sanitize: the Go table', () => {
  for (const [name, input, profile, want] of cases) {
    it(`${profile.name}: ${name}`, () => {
      const got = sanitize(input, profile)
      expect(got).toBe(want)
      expect(sanitize(got, profile), 'idempotent').toBe(got)
    })
  }

  it('never emits another tag or an attribute but href on a', () => {
    const allowed = new Set(['p', 'br', 'strong', 'em', 'u', 'a', 'ul', 'ol', 'li', 'h3', 'h4', 'blockquote'])
    for (const [name, input, profile] of cases) {
      const out = sanitize(input, profile)
      for (const m of out.matchAll(/<(\/?)([a-zA-Z0-9]+)([^>]*)>/g)) {
        expect(allowed.has(m[2]), `${name}: <${m[2]}>`).toBe(true)
        if (m[2] === 'a') expect(m[3] === '' || /^ href="[^"]*"$/.test(m[3]), `${name}: ${m[0]}`).toBe(true)
        else expect(m[3], `${name}: attributes on <${m[2]}>`).toBe('')
      }
    }
  })

  it('refuses a markup nested too deeply and an input that is too large', () => {
    expect(() => sanitize('<div>'.repeat(300) + 'x', descr)).toThrowError(RichTextError)
    expect(() => sanitize('a'.repeat((1 << 20) + 1), descr)).toThrowError(RichTextError)
  })
})

describe('hosts and mailto addresses (Go: TestHostNormalisation, TestMailto)', () => {
  const host: Array<[string, string, string]> = [
    ['fullwidth ipv4', 'https://１２７.０.０.１/', ''],
    ['fullwidth ipv4 with a path', 'https://１２７.０.０.１/admin', ''],
    ['ideographic full stops', 'https://127。0。0。1/', ''],
    ['fullwidth decimal ip', 'https://２１３０７０６４３３/', ''],
    ['fullwidth localhost', 'https://ｌｏｃａｌｈｏｓｔ/', ''],
    ['fullwidth upper case localhost', 'https://ＬＯＣＡＬＨＯＳＴ/x', ''],
    ['subdomain of fullwidth localhost', 'https://app.ｌｏｃａｌｈｏｓｔ/', ''],
    ['localhost with a final dot', 'https://localhost./', ''],
    ['fullwidth colon', 'https://example.com：8080/', ''],
    ['fullwidth at sign', 'https://user＠example.com/', ''],
    ['fullwidth letters become ascii', 'https://ｅｘａｍｐｌｅ.ｃｏｍ/p', 'https://example.com/p'],
    ['upper case', 'https://EXAMPLE.Com/P', 'https://example.com/P'],
    ['cyrillic', 'https://пример.рф/путь', 'https://xn--e1afmkfd.xn--p1ai/путь'],
    ['umlaut', 'https://münchen.de/x', 'https://xn--mnchen-3ya.de/x'],
    ['hebrew', 'https://יין.קום/', 'https://xn--eebas.xn--9dbq2a/'],
    ['underscore is not a host character', 'https://exa_mple.com/', ''],
    ['leading hyphen', 'https://-bad.com/', ''],
    ['trailing dot is dropped', 'https://example.com./x', 'https://example.com/x'],
    ['empty label', 'https://a..com/', ''],
    ['two final dots', 'https://example.com../', ''],
    ['numeric host with two dots', 'https://0..#', ''],
    ['only a dot', 'https://./', ''],
    ['numeric last label', 'https://example.123/', ''],
    ['ipv4 octal', 'https://0177.0.0.1/', ''],
    ['ascii ipv4 still refused', 'https://127.0.0.1/', ''],
    ['public name with digits', 'https://8x8.com/', 'https://8x8.com/'],
    ['soft hyphen in a host', 'https://exam' + ch(0xad) + 'ple.com/', ''],
    ['too long host', 'https://' + ['a', 'b', 'c', 'd', 'e'].map((c) => c.repeat(60)).join('.') + '.com/', ''],
  ]
  for (const [name, href, want] of host) {
    it(`host: ${name}`, () => {
      expect(cleanHref(href, descr) ?? '').toBe(want)
    })
  }

  const mailto: Array<[string, string, string]> = [
    ['plain', 'mailto:a@b.com', 'mailto:a@b.com'],
    ['query is dropped', 'mailto:a@b.com?subject=Hi', 'mailto:a@b.com'],
    ['bcc is dropped', 'mailto:a@good.com?bcc=x@evil.com&body=hello', 'mailto:a@good.com'],
    ['encoded line break in the query', 'mailto:a@b.com?subject=x%0D%0ABcc:x@evil.com', 'mailto:a@b.com'],
    ['empty query', 'mailto:a@b.com?', 'mailto:a@b.com'],
    ['query only', 'mailto:?to=x@evil.com', ''],
    ['encoded line break after the address', 'mailto:a@b.com%0D%0Abcc:x@evil.com', ''],
    ['percent in the local part', 'mailto:a%40evil.com@b.com', ''],
    ['two addresses with a comma', 'mailto:a@b.com,c@d.com', ''],
    ['two addresses with a semicolon', 'mailto:a@b.com;c@d.com', ''],
    ['quoted local part', 'mailto:"a"@b.com', ''],
    ['space in the local part', 'mailto:a b@c.com', ''],
    ['angle brackets', 'mailto:<a@b.com>', ''],
    ['leading dot', 'mailto:.a@b.com', ''],
    ['double dot', 'mailto:a..b@c.com', ''],
    ['fragment', 'mailto:a@b.com#x', ''],
    ['no at sign', 'mailto:abc', ''],
    ['two at signs', 'mailto:a@b@c.com', ''],
    ['no local part', 'mailto:@b.com', ''],
    ['atext and dots', 'mailto:First.Last+tag_1@Example.COM', 'mailto:First.Last+tag_1@example.com'],
    ['local part too long', 'mailto:' + 'a'.repeat(65) + '@b.com', ''],
    ['loopback host', 'mailto:a@127.0.0.1', ''],
    ['fullwidth loopback host', 'mailto:a@１２７.０.０.１', ''],
    ['localhost', 'mailto:a@localhost', ''],
    ['ipv6 literal', 'mailto:a@[::1]', ''],
    ['idn domain', 'mailto:a@пример.рф', 'mailto:a@xn--e1afmkfd.xn--p1ai'],
  ]
  for (const [name, href, want] of mailto) {
    it(`mailto: ${name}`, () => {
      expect(cleanHref(href, descr) ?? '').toBe(want)
    })
  }

  it('mailto exists only in the description profile', () => {
    expect(cleanHref('mailto:a@b.com', descr)).toBe('mailto:a@b.com')
    expect(cleanHref('mailto:a@b.com', email)).toBeNull()
    expect(cleanHref('mailto:a@b.com', sess)).toBeNull()
  })
})

describe('the link dialog input', () => {
  it('adds https:// to a bare host and mailto: to an address where the profile allows it', () => {
    expect(parseLinkInput('example.com/a', email)).toBe('https://example.com/a')
    expect(parseLinkInput('  Example.com ', descr)).toBe('https://example.com')
    expect(parseLinkInput('a@b.com', descr)).toBe('mailto:a@b.com')
    expect(parseLinkInput('a@b.com', email)).toBeNull()
  })
  it('refuses what href.go refuses', () => {
    for (const bad of [
      'http://example.com',
      'javascript:alert(1)',
      'https://user:pw@example.com',
      'https://example.com:8080',
      'https://127.0.0.1',
      'https://localhost',
      'localhost',
      'example.com:8080',
      '192.168.0.1',
      '',
      '   ',
      '//evil.com',
    ]) {
      expect(parseLinkInput(bad, descr), bad).toBeNull()
    }
  })
})

describe('measure: the counter counts the way the API counts', () => {
  it('counts characters, a break between units and a <br>', () => {
    expect(measure('<p>ab</p><p>cd</p>', descr).visible).toBe(5)
    expect(measure('<p>ab<br>cd</p>', descr).visible).toBe(5)
    expect(measure('<ul><li>a</li><li>b</li></ul>', descr).visible).toBe(3)
    expect(measure('<h3>x</h3><blockquote><p>a</p><p>b</p></blockquote>', descr).visible).toBe(5)
    expect(measure('', descr).visible).toBe(0)
  })
  it('counts characters, not UTF-16 units', () => {
    expect(measure('<p>' + ch(0x1f600) + 'я</p>', descr).visible).toBe(2)
  })
  it('counts links and the HTML length', () => {
    const m = measure('<p><a href="https://a.com">a</a> <a href="https://b.com">b</a></p>', descr)
    expect(m.links).toBe(2)
    expect(m.htmlLength).toBe(m.html.length)
  })
})

describe('style attributes never reach the browser parser (strict CSP)', () => {
  // A strict CSP (style-src without unsafe-inline) makes the browser report a
  // violation for every style attribute of every string it parses, so the
  // sanitizer renames them first and reads the text from the renamed attribute.
  it('renames style attributes of start tags only', () => {
    expect(renameStyleAttributes('<p style="color:red">a</p>')).toBe('<p data-arena-style="color:red">a</p>')
    expect(renameStyleAttributes("<P STYLE='a:b' class=x Style=c:d>")).toBe("<P data-arena-style='a:b' class=x data-arena-style=c:d>")
    expect(renameStyleAttributes('<span\n style = "x" >t</span>')).toBe('<span\n data-arena-style = "x" >t</span>')
    expect(renameStyleAttributes('<br style=x/>')).toBe('<br data-arena-style=x/>')
  })

  it('leaves text, comments, end tags and values that only look like a style attribute', () => {
    for (const same of [
      'no tags, style=red',
      '<p>use style="x" here</p>',
      '<!-- <p style="x"> -->',
      '<p title="a style=b" data-x=\'style="c"\'>t</p>',
      '<a href="https://example.com/?style=1">t</a>',
      '</p style="x">',
      'a < b style=c',
      '<p styles="x" mystyle="y">t</p>',
    ]) {
      expect(renameStyleAttributes(same), same).toBe(same)
    }
  })

  it('handles a > inside a quoted value and an unterminated tag', () => {
    expect(renameStyleAttributes('<p title="a>b" style="c">t</p>')).toBe('<p title="a>b" data-arena-style="c">t</p>')
    expect(renameStyleAttributes('<p style="c')).toBe('<p data-arena-style="c')
    expect(renameStyleAttributes('<p style')).toBe('<p data-arena-style')
    expect(renameStyleAttributes('<p style=')).toBe('<p data-arena-style=')
  })

  it('is what the HTML parser is given, for stored text and for paste', () => {
    const desc = Object.getOwnPropertyDescriptor(Element.prototype, 'innerHTML') as PropertyDescriptor
    const seen: string[] = []
    Object.defineProperty(Element.prototype, 'innerHTML', {
      ...desc,
      set(this: Element, v: string) {
        seen.push(v)
        desc.set?.call(this, v)
      },
    })
    try {
      const html = '<p style="color:red"><span STYLE=\'font-weight:700\'>b</span></p>'
      expect(sanitize(html, descr)).toBe('<p>b</p>')
      expect(sanitize(html, descr, { foreignStyles: true })).toBe('<p><strong>b</strong></p>')
    } finally {
      Object.defineProperty(Element.prototype, 'innerHTML', desc)
    }
    expect(seen.length).toBeGreaterThan(0)
    for (const v of seen) expect(v).not.toMatch(/\sstyle\s*=/i)
  })
})
