// Paste cleanup: whatever Word, Google Docs or a web page puts on the
// clipboard is cut down to the white list of the profile. Style and class
// attributes, spans, divs, images and tables do not survive.

import { afterEach, describe, expect, it } from 'vitest'

import { create, PROFILES, sanitize, type ProfileName, type RichEditor } from '../src/index'

const editors: RichEditor[] = []
afterEach(() => {
  while (editors.length) editors.pop()?.destroy()
  document.body.innerHTML = ''
})

function setup(profile: ProfileName, value = ''): { editor: RichEditor; dom: HTMLElement } {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const editor = create(host, { profile, value })
  editors.push(editor)
  return { editor, dom: host.querySelector('.ProseMirror') as HTMLElement }
}

/** Fires a paste event the way a browser does: the data is read from clipboardData. */
function paste(dom: HTMLElement, data: { html?: string; text?: string }): Event {
  const ev = new Event('paste', { bubbles: true, cancelable: true })
  const types = [data.html !== undefined ? 'text/html' : '', data.text !== undefined ? 'text/plain' : ''].filter(Boolean)
  Object.defineProperty(ev, 'clipboardData', {
    value: {
      types,
      files: [],
      items: [],
      getData: (t: string) => (t === 'text/html' ? (data.html ?? '') : t === 'text/plain' || t === 'Text' ? (data.text ?? '') : ''),
    },
  })
  dom.dispatchEvent(ev)
  return ev
}

const WORD = `<html xmlns:o="urn:schemas-microsoft-com:office:office" xmlns:w="urn:schemas-microsoft-com:office:word" xmlns="http://www.w3.org/TR/REC-html40">
<head><meta http-equiv=Content-Type content="text/html; charset=utf-8"><meta name=ProgId content=Word.Document>
<style><!-- p.MsoNormal, li.MsoNormal { margin:0cm; font-size:11.0pt; font-family:"Calibri",sans-serif; } --></style></head>
<body lang=EN-US style='tab-interval:36.0pt'><!--StartFragment-->
<p class=MsoNormal><b style='mso-bidi-font-weight:normal'><span style='font-size:14.0pt;font-family:"Calibri",sans-serif'>Summer concert</span></b><o:p></o:p></p>
<p class=MsoNormal>Doors at <i>7 pm</i>, tickets at <a href="https://example.com/tickets" style="color:blue">our site</a>.<o:p>&nbsp;</o:p></p>
<p class=MsoListParagraph style='text-indent:-18.0pt;mso-list:l0 level1 lfo1'><![if !supportLists]><span style='font-family:Symbol'>&middot;<span style='font:7.0pt "Times New Roman"'>&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;</span></span><![endif]>First item<o:p></o:p></p>
<!--EndFragment--></body></html>`

const GOOGLE_DOCS =
  `<meta charset='utf-8'><meta charset="utf-8"><b style="font-weight:normal;" id="docs-internal-guid-1a2b3c4d-7fff-1234-5678-abcdefabcdef">` +
  `<p dir="ltr" style="line-height:1.38;margin-top:0pt;margin-bottom:0pt;"><span style="font-size:11pt;font-family:Arial,sans-serif;color:#000000;background-color:transparent;font-weight:700;font-style:normal;font-variant:normal;text-decoration:none;vertical-align:baseline;white-space:pre;white-space:pre-wrap;">Bold start</span>` +
  `<span style="font-size:11pt;font-family:Arial,sans-serif;font-weight:400;"> and plain, </span>` +
  `<span style="font-size:11pt;font-style:italic;">italic</span>` +
  `<span style="font-size:11pt;font-weight:400;"> and </span>` +
  `<span style="font-size:11pt;text-decoration:underline;-webkit-text-decoration-skip:none;">underlined</span></p>` +
  `<ul style="margin-top:0;margin-bottom:0;padding-inline-start:48px;"><li dir="ltr" style="list-style-type:disc;font-size:11pt;" aria-level="1"><p dir="ltr" style="line-height:1.38;" role="presentation"><span style="font-size:11pt;">One</span></p></li>` +
  `<li dir="ltr" style="list-style-type:disc;" aria-level="1"><p dir="ltr" role="presentation"><span>Two</span></p></li></ul>` +
  `<br class="Apple-interchange-newline"></b>`

const WEB_PAGE =
  `<div class="post" id="main" data-x="1" onclick="alert(1)" style="color:red"><nav><a href="/home">Home</a></nav>` +
  `<h1 class="title" style="font-size:3em">Big title</h1><h2>Smaller</h2><h5>Tiny</h5>` +
  `<p class="lead"><img src="https://example.com/a.png" alt="photo" onerror="alert(1)">Some <span class="hl" style="background:yellow">highlighted</span> text <code>code</code> and ` +
  `<a href="javascript:alert(1)">bad link</a>, <a href="http://example.com/plain">http link</a>, <a href="https://example.com/ok" target="_blank" rel="nofollow" class="x">good link</a>.</p>` +
  `<script>alert('x')</script><style>.a{color:red}</style><iframe src="https://evil.example"></iframe>` +
  `<table class="t" style="width:100%"><thead><tr><th>Name</th><th>Time</th></tr></thead><tbody><tr><td>Anna</td><td>19:00</td></tr><tr><td>Boris</td><td>20:30</td></tr></tbody></table>` +
  `<blockquote class="q"><p>Quoted words</p></blockquote><hr><svg onload="alert(1)"><text>svg text</text></svg></div>`

describe('paste: the pure cleanup', () => {
  it('Word: style, class, spans and o:p go, bold, italic and the link stay', () => {
    expect(sanitize(WORD, PROFILES.description, { foreignStyles: true })).toBe(
      '<p><strong>Summer concert</strong></p>' +
        '<p>Doors at <em>7 pm</em>, tickets at <a href="https://example.com/tickets">our site</a>.</p>' +
        '<p>&middot;'.replace('&middot;', '·') +
        ' First item</p>',
    )
  })

  it('Google Docs: the font-weight:normal wrapper does not make the paste bold, styled spans become marks', () => {
    expect(sanitize(GOOGLE_DOCS, PROFILES.description, { foreignStyles: true })).toBe(
      '<p><strong>Bold start</strong> and plain, <em>italic</em> and <u>underlined</u></p>' + '<ul><li>One</li><li>Two</li></ul>',
    )
  })

  it('Google Docs without the style reading would be bold: the option is the paste path only', () => {
    expect(sanitize(GOOGLE_DOCS, PROFILES.description)).toContain('<strong>')
    expect(sanitize(GOOGLE_DOCS, PROFILES.description, { foreignStyles: true })).not.toContain('plain, </strong>')
  })

  it('a web page in the description profile keeps headings, quote and good links only', () => {
    expect(sanitize(WEB_PAGE, PROFILES.description, { foreignStyles: true })).toBe(
      '<p>Home</p>' +
        '<h3>Big title</h3><h3>Smaller</h3><h4>Tiny</h4>' +
        '<p>Some highlighted text code and bad link, http link, <a href="https://example.com/ok">good link</a>.</p>' +
        '<p>Name</p><p>Time</p><p>Anna</p><p>19:00</p><p>Boris</p><p>20:30</p>' +
        '<blockquote><p>Quoted words</p></blockquote>',
    )
  })

  it('a web page in the letter profiles turns headings into bold lines and the quote into a paragraph', () => {
    for (const p of [PROFILES.email_note, PROFILES.session_message]) {
      const out = sanitize(WEB_PAGE, p, { foreignStyles: true })
      expect(out).toContain('<p><strong>Big title</strong></p><p><strong>Smaller</strong></p><p><strong>Tiny</strong></p>')
      expect(out).toContain('<p>Quoted words</p>')
      expect(out).not.toMatch(/<h[1-6]|<blockquote/)
    }
  })

  it('leaves nothing but the white list', () => {
    for (const html of [WORD, GOOGLE_DOCS, WEB_PAGE]) {
      for (const p of Object.values(PROFILES)) {
        const out = sanitize(html, p, { foreignStyles: true })
        expect(out).not.toMatch(/style=|class=|<span|<div|<img|<table|<td|<tr|<script|<style|<iframe|<svg|onerror|onclick|javascript:|rel=|target=|id=|data-/i)
        expect(sanitize(out, p)).toBe(out)
      }
    }
  })

  it('pasted text with only an image or a script is nothing', () => {
    expect(sanitize('<img src="x"><script>1</script>', PROFILES.description, { foreignStyles: true })).toBe('')
  })
})

describe('paste: through the editor', () => {
  it('Word paste is cut down to the white list', () => {
    const { editor, dom } = setup('description')
    const ev = paste(dom, { html: WORD, text: 'Summer concert\nDoors at 7 pm' })
    expect(ev.defaultPrevented).toBe(true)
    expect(editor.getHTML()).toBe(
      '<p><strong>Summer concert</strong></p>' +
        '<p>Doors at <em>7 pm</em>, tickets at <a href="https://example.com/tickets">our site</a>.</p>' +
        '<p>· First item</p>',
    )
  })

  it('Google Docs paste keeps real bold only', () => {
    const { editor, dom } = setup('email_note')
    paste(dom, { html: GOOGLE_DOCS, text: 'x' })
    expect(editor.getHTML()).toBe(
      '<p><strong>Bold start</strong> and plain, <em>italic</em> and <u>underlined</u></p><ul><li>One</li><li>Two</li></ul>',
    )
  })

  it('a web page paste loses attributes, spans, divs, images and tables, and the DOM holds none of them', () => {
    const { editor, dom } = setup('description')
    paste(dom, { html: WEB_PAGE, text: 'x' })
    const html = editor.getHTML()
    expect(html).not.toMatch(/style=|class=|<span|<div|<img|<table|<td|<script|<iframe|onerror|javascript:/i)
    expect(html).toContain('<h3>Big title</h3>')
    expect(html).toContain('<a href="https://example.com/ok">good link</a>')
    // nothing in the live editor DOM carries a style attribute either
    expect(dom.querySelector('[style]')).toBeNull()
    expect(dom.querySelector('img, table, span, div, script, iframe, svg')).toBeNull()
  })

  it('a heading pasted into a letter text becomes a bold paragraph', () => {
    const { editor, dom } = setup('session_message')
    paste(dom, { html: '<h2>Moved</h2><p>to <b>8 pm</b></p><blockquote>sorry</blockquote>', text: 'x' })
    expect(editor.getHTML()).toBe('<p><strong>Moved</strong></p><p>to <strong>8 pm</strong></p><p>sorry</p>')
  })

  it('pasted inline text lands inside the paragraph', () => {
    const { editor, dom } = setup('description', '<p>Hello world</p>')
    editor.focus()
    paste(dom, { html: '<span style="color:red">big </span>', text: 'big ' })
    expect(editor.getHTML()).toContain('Hello world')
    expect(editor.getHTML()).not.toContain('span')
  })

  it('plain text paste drops forbidden characters and makes paragraphs', () => {
    const { editor, dom } = setup('description')
    paste(dom, { text: 'one' + String.fromCharCode(1, 0x202e) + '\ntwo' })
    expect(editor.getHTML()).toBe('<p>one</p><p>two</p>')
  })

  it('a pasted address is text, not a link', () => {
    const { editor, dom } = setup('description')
    paste(dom, { text: 'https://example.com' })
    expect(editor.getHTML()).toBe('<p>https://example.com</p>')
  })

  it('a hostile paste with a javascript link and an event handler is harmless', () => {
    const { editor, dom } = setup('description')
    paste(dom, { html: '<a href="javascript:alert(1)" onclick="alert(2)"><img src=x onerror=alert(3)>click</a>', text: 'click' })
    expect(editor.getHTML()).toBe('<p>click</p>')
  })
})
