// Behaviour demanded by the RT-10 review: scheme words in the strict profile,
// the read-only mode, several sublists in an item, a large paste, the format
// request, the undo history after setHTML and the small fixes.

import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  create,
  LOCALES,
  MESSAGES,
  PROFILES,
  RichTextError,
  sanitize,
  type CreateOptions,
  type FormatRequest,
  type FormatResult,
  type Locale,
  type ProfileName,
  type RichEditor,
} from '../src/index'
import { stripForbiddenChars } from '../src/chars'
import { renameStyleAttributes, sanitizePaste, stripBulk } from '../src/sanitize'

const editors: RichEditor[] = []
afterEach(() => {
  while (editors.length) editors.pop()?.destroy()
  document.body.innerHTML = ''
})

function setup(profile: ProfileName, extra: Partial<CreateOptions> = {}) {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const editor = create(host, { profile, ...extra })
  editors.push(editor)
  const root = host.querySelector('.arena-re') as HTMLElement
  const dom = host.querySelector('.ProseMirror') as HTMLElement
  const button = (action: string) => root.querySelector(`button[data-action="${action}"]`) as HTMLButtonElement | null
  const click = (action: string) => {
    const b = button(action)
    expect(b, action).not.toBeNull()
    b!.click()
  }
  const status = () => (root.querySelector('.arena-re__status') as HTMLElement).textContent
  const counter = () => root.querySelector('.arena-re__counter') as HTMLElement
  return { host, editor, root, dom, button, click, status, counter }
}

const tick = () => new Promise((r) => setTimeout(r, 0))
const result = (html: string, ai_applied = true): FormatResult => ({ html, ai_applied })

describe('Б1: scheme words in the email note', () => {
  it('flags the sentence of the review, counts it as over the limit and says why', () => {
    const { editor, status, counter } = setup('email_note')
    editor.setHTML('<p>старый сайт http://a.ru</p>')
    expect(editor.getHTML()).toBe('<p>старый сайт http://a.ru</p>') // the text stays as it is
    expect(editor.getStats().schemeWord).toBe(true)
    expect(editor.getStats().overLimit).toBe(true)
    expect(status()).toBe(MESSAGES.en.schemeWord)
    expect(counter().classList.contains('arena-re__counter--over')).toBe(true)
  })

  it('shows the message in all five languages', () => {
    for (const l of LOCALES) {
      const { editor, status } = setup('email_note', { locale: l, value: '<p>see data:x</p>' })
      expect(status(), l).toBe(MESSAGES[l as Locale].schemeWord)
      expect(MESSAGES[l as Locale].schemeWord.length, l).toBeGreaterThan(20)
      expect(editor.getStats().schemeWord).toBe(true)
    }
    // Every language has its own wording.
    expect(new Set(LOCALES.map((l) => MESSAGES[l].schemeWord)).size).toBe(LOCALES.length)
  })

  it('follows typing: the message appears with the word and goes away without it', () => {
    const { editor, status, counter } = setup('email_note', { value: '<p>clean</p>' })
    expect(status()).toBe('')
    expect(editor.getStats().schemeWord).toBe(false)
    editor.editor.commands.insertContent(' javascript:alert(1)')
    expect(editor.getStats().overLimit).toBe(true)
    expect(status()).toBe(MESSAGES.en.schemeWord)
    editor.setHTML('<p>clean</p>')
    editor.editor.commands.insertContent(' fine')
    expect(editor.getStats().overLimit).toBe(false)
    expect(status()).toBe('')
    expect(counter().classList.contains('arena-re__counter--over')).toBe(false)
  })

  it('tells the host in onChange', () => {
    const onChange = vi.fn()
    const { editor } = setup('email_note', { onChange })
    editor.editor.commands.insertContent('see http://x.ru')
    const stats = onChange.mock.calls.at(-1)?.[1]
    expect(stats.schemeWord).toBe(true)
    expect(stats.overLimit).toBe(true)
  })

  it('does not flag the profiles without StrictSchemeWords', () => {
    for (const p of ['description', 'session_message'] as const) {
      const { editor, status } = setup(p, { value: '<p>data:foo and http://x</p>' })
      expect(editor.getStats().schemeWord).toBe(false)
      expect(editor.getStats().overLimit).toBe(false)
      expect(status()).toBe('')
    }
  })

  it('a pasted bad link that turns into its text is flagged', () => {
    const { editor } = setup('email_note')
    editor.editor.commands.insertContent('<p><a href="http://old.example/">http://old.example/</a></p>')
    expect(editor.getHTML()).toBe('<p>http://old.example/</p>')
    expect(editor.getStats().schemeWord).toBe(true)
  })

  it('an answer of Format with a scheme word keeps its warning in the status', async () => {
    const { editor, status, click } = setup('email_note', {
      value: '<p>a</p>',
      format: async () => result('<p>go to http://x.ru</p>'),
    })
    click('format')
    await tick()
    expect(editor.getStats().schemeWord).toBe(true)
    expect(status()).toContain(MESSAGES.en.formatDone)
    expect(status()).toContain(MESSAGES.en.schemeWord)
  })

  it('the initial value is checked too', () => {
    const { editor, status } = setup('email_note', { value: '<p>file:///x</p>' })
    expect(editor.getStats().overLimit).toBe(true)
    expect(status()).toBe(MESSAGES.en.schemeWord)
  })
})

describe('В1: the read-only mode protects the text', () => {
  const formatCallback = vi.fn(async () => result('<p>formatted</p>'))

  it('disables the toolbar, Format and the link dialog and marks the area aria-readonly', () => {
    formatCallback.mockClear()
    const { editor, root, dom, button, click } = setup('description', {
      value: '<p>abc</p>',
      disabled: true,
      format: formatCallback,
    })
    const buttons = [...root.querySelectorAll('button[data-action]')] as HTMLButtonElement[]
    expect(buttons.length).toBeGreaterThan(8)
    for (const b of buttons) expect(b.disabled, b.getAttribute('data-action') ?? '').toBe(true)
    expect(dom.getAttribute('aria-readonly')).toBe('true')
    expect(dom.getAttribute('contenteditable')).toBe('false')
    // A click on a disabled button does nothing, selection or not.
    editor.editor.commands.selectAll()
    click('bold')
    click('format')
    click('link')
    expect(editor.getHTML()).toBe('<p>abc</p>')
    expect(formatCallback).not.toHaveBeenCalled()
    expect((root.querySelector('.arena-re__link') as HTMLElement).hidden).toBe(true)
    expect(button('h3')?.disabled).toBe(true)
  })

  it('the commands that bypass the buttons are shut as well: the link shortcut and the dialog', () => {
    const { editor, root } = setup('description', { value: '<p>abc</p>', disabled: true })
    const dialog = root.querySelector('.arena-re__link') as HTMLElement
    editor.editor.view.dom.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', ctrlKey: true, bubbles: true, cancelable: true }))
    expect(dialog.hidden).toBe(true)
    // The same key opens the dialog in an editable text (so the check above is not vacuous).
    const live = setup('description', { value: '<p>abc</p>' })
    live.editor.editor.view.dom.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', ctrlKey: true, bubbles: true, cancelable: true }))
    expect((live.root.querySelector('.arena-re__link') as HTMLElement).hidden).toBe(false)
  })

  it('setDisabled refreshes the toolbar at once and sets and clears aria-readonly', () => {
    const { editor, dom, button } = setup('description', { value: '<p>abc</p>', format: formatCallback })
    expect(button('bold')?.disabled).toBe(false)
    expect(button('format')?.disabled).toBe(false)
    expect(dom.hasAttribute('aria-readonly')).toBe(false)
    editor.setDisabled(true)
    expect(button('bold')?.disabled).toBe(true)
    expect(button('format')?.disabled).toBe(true)
    expect(button('link')?.disabled).toBe(true)
    expect(dom.getAttribute('aria-readonly')).toBe('true')
    expect(editor.editor.isEditable).toBe(false)
    editor.setDisabled(false)
    expect(button('bold')?.disabled).toBe(false)
    expect(button('format')?.disabled).toBe(false)
    expect(dom.hasAttribute('aria-readonly')).toBe(false)
    expect(editor.editor.isEditable).toBe(true)
  })

  it('closes an open link dialog when the text becomes read-only', () => {
    const { editor, root, click } = setup('description', { value: '<p>abc</p>' })
    const dialog = root.querySelector('.arena-re__link') as HTMLElement
    click('link')
    expect(dialog.hidden).toBe(false)
    editor.setDisabled(true)
    expect(dialog.hidden).toBe(true)
    // A submit that still arrives sets no link.
    const form = dialog.querySelector('form') as HTMLFormElement
    ;(dialog.querySelector('input') as HTMLInputElement).value = 'https://example.com'
    form.dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }))
    expect(editor.getHTML()).toBe('<p>abc</p>')
  })

  it('an answer of Format that comes after the text became read-only is not applied', async () => {
    let resolveFormat: (r: FormatResult) => void = () => undefined
    const pending = new Promise<FormatResult>((r) => (resolveFormat = r))
    const { editor, click, status } = setup('description', { value: '<p>keep</p>', format: () => pending })
    click('format')
    editor.setDisabled(true)
    resolveFormat(result('<p>late</p>'))
    await tick()
    expect(editor.getHTML()).toBe('<p>keep</p>')
    expect(status()).toBe('') // no "Formatting..." left behind
    expect(editor.editor.isEditable).toBe(false)
  })

  it('an answer of Format that comes after setHTML is not applied to the new text', async () => {
    let resolveFormat: (r: FormatResult) => void = () => undefined
    const pending = new Promise<FormatResult>((r) => (resolveFormat = r))
    const { editor, click } = setup('description', { value: '<p>old</p>', format: () => pending })
    click('format')
    editor.setHTML('<p>new</p>')
    resolveFormat(result('<p>answer for old</p>'))
    await tick()
    expect(editor.getHTML()).toBe('<p>new</p>')
    expect(editor.editor.isEditable).toBe(true)
  })
})

describe('В2: an item holds several sublists, as in Go (TestItemWithSeveralSublists)', () => {
  const cases = [
    '<ul><li>a<ul><li>b</li></ul><ol><li>c</li></ol></li></ul>',
    '<ol><li>a<ul><li>b</li></ul><ul><li>c</li></ul></li><li>d</li></ol>',
  ]
  for (const art of cases) {
    it(`setHTML then getHTML is the same: ${art}`, () => {
      expect(sanitize(art, PROFILES.description)).toBe(art)
      const { editor } = setup('description')
      editor.setHTML(art)
      expect(editor.getHTML()).toBe(art)
      const again = setup('description', { value: art })
      expect(again.editor.getHTML()).toBe(art)
    })
  }

  it('an answer of Format with two sublists is not reshaped', async () => {
    const { editor, click } = setup('description', { value: '<p>x</p>', format: async () => result(cases[0]) })
    click('format')
    await tick()
    expect(editor.getHTML()).toBe(cases[0])
  })
})

describe('В3: a large paste', () => {
  function paste(dom: HTMLElement, data: { html?: string; text?: string }): Event {
    const ev = new Event('paste', { bubbles: true, cancelable: true })
    Object.defineProperty(ev, 'clipboardData', {
      value: {
        types: [data.html !== undefined ? 'text/html' : '', data.text !== undefined ? 'text/plain' : ''].filter(Boolean),
        files: [],
        items: [],
        getData: (t: string) => (t === 'text/html' ? (data.html ?? '') : t === 'text/plain' || t === 'Text' ? (data.text ?? '') : ''),
      },
    })
    dom.dispatchEvent(ev)
    return ev
  }
  function drop(dom: HTMLElement, data: { html?: string; text?: string }): Event {
    const ev = new Event('drop', { bubbles: true, cancelable: true })
    Object.defineProperty(ev, 'dataTransfer', {
      value: {
        types: [data.html !== undefined ? 'text/html' : '', data.text !== undefined ? 'text/plain' : ''].filter(Boolean),
        files: [],
        items: [],
        getData: (t: string) => (t === 'text/html' ? (data.html ?? '') : t === 'text/plain' || t === 'Text' ? (data.text ?? '') : ''),
      },
    })
    dom.dispatchEvent(ev)
    return ev
  }

  const BASE64 = 'A'.repeat(1_300_000)

  it('a page with a 1.3 MB data: image keeps its text and formatting and needs no fallback', () => {
    const html =
      `<html><head><style>${'p{color:red}'.repeat(1000)}</style></head><body><p>Hello <b>big</b> page</p>` +
      `<img src="data:image/png;base64,${BASE64}" alt="x"><p>second</p></body></html>`
    expect(html.length).toBeGreaterThan(1 << 20)
    const { editor, dom, status } = setup('description')
    paste(dom, { html, text: 'plain version' })
    expect(editor.getHTML()).toBe('<p>Hello <strong>big</strong> page</p><p>second</p>')
    expect(status()).toBe('')
  })

  it('stripBulk removes head, style, script, comments, images and data: URIs only', () => {
    const slim = stripBulk(
      '<!-- c --><head><title>t</title></head><style>a{}</style><script>x()</script><p>keep data:x me</p>' +
        '<img src="a.png" alt=">"><a href="data:text/html;base64,AAAA">l</a>',
    )
    expect(slim).toBe('<p>keep data:x me</p><a href="">l</a>')
    expect(stripBulk('<header>h</header><p>x</p>')).toBe('<header>h</header><p>x</p>')
  })

  it('compares the size in bytes like Go: 600k Cyrillic letters are 1.2 MB', () => {
    const html = '<p>' + 'я'.repeat(600_000) + '</p>'
    expect(html.length).toBeLessThan(1 << 20) // UTF-16 units: under the limit
    expect(() => sanitize(html, PROFILES.description)).toThrowError(RichTextError)
    expect(() => sanitize('<p>' + 'я'.repeat(500_000) + '</p>', PROFILES.description)).not.toThrow()
    expect(() => sanitize('<p>' + 'a'.repeat((1 << 20) - 7) + '</p>', PROFILES.description)).not.toThrow()
    expect(() => sanitize('<p>' + 'a'.repeat((1 << 20) - 6) + '</p>', PROFILES.description)).toThrowError(RichTextError)
  })

  it('a markup still too large falls back to the sanitized plain text and says so', () => {
    const html = '<p>' + 'я'.repeat(600_000) + '</p>'
    const { editor, dom, status } = setup('description')
    const ev = paste(dom, { html, text: 'line one\n\nline <two> ‮\u0007 three' })
    expect(ev.defaultPrevented).toBe(true)
    expect(editor.getHTML()).toBe('<p>line one</p><p>line &lt;two&gt; three</p>')
    expect(status()).toBe(MESSAGES.en.pasteFallback)
  })

  it('a markup nested too deeply falls back as well', () => {
    const { editor, dom, status } = setup('email_note')
    paste(dom, { html: '<div>'.repeat(300) + 'deep' + '</div>'.repeat(300), text: 'only the text' })
    expect(editor.getHTML()).toBe('<p>only the text</p>')
    expect(status()).toBe(MESSAGES.en.pasteFallback)
  })

  it('the fallback message exists in all five languages', () => {
    for (const l of LOCALES) {
      const { dom, status } = setup('description', { locale: l })
      paste(dom, { html: '<p>' + 'я'.repeat(600_000) + '</p>', text: 'x' })
      expect(status(), l).toBe(MESSAGES[l].pasteFallback)
    }
    expect(new Set(LOCALES.map((l) => MESSAGES[l].pasteFallback)).size).toBe(LOCALES.length)
  })

  it('a normal paste still goes through the cleanup, no status', () => {
    const { editor, dom, status } = setup('description')
    paste(dom, { html: '<div style="color:red"><b>a</b> <i>b</i></div>', text: 'a b' })
    expect(editor.getHTML()).toBe('<p><strong>a</strong> <em>b</em></p>')
    expect(status()).toBe('')
  })

  it('a paste into a read-only text is left alone', () => {
    const { editor, dom, status } = setup('description', { value: '<p>keep</p>', disabled: true })
    paste(dom, { html: '<p>' + 'я'.repeat(600_000) + '</p>', text: 'x' })
    expect(editor.getHTML()).toBe('<p>keep</p>')
    expect(status()).toBe('')
  })

  it('a drop of a too large markup falls back to its plain text too', () => {
    const { editor, dom, status } = setup('description', { value: '<p>start</p>' })
    const ev = drop(dom, { html: '<p>' + 'я'.repeat(600_000) + '</p>', text: 'dropped words' })
    expect(ev.defaultPrevented).toBe(true)
    expect(editor.getHTML()).toContain('dropped words')
    expect(status()).toBe(MESSAGES.en.pasteFallback)
  })

  it('a drop of a normal markup is cleaned the usual way', () => {
    const { editor, dom, status } = setup('description')
    drop(dom, { html: '<h1 style="x">Title</h1><p onclick="x()">a <img src=x></p>', text: 'Title a' })
    expect(editor.getHTML()).not.toMatch(/onclick|<img|style/)
    expect(status()).toBe('')
  })

  it('sanitizePaste is the one entry for the cleanup', () => {
    expect(sanitizePaste('<p style="font-weight:700">x</p>', PROFILES.description)).toBe('<p><strong>x</strong></p>')
  })
})

describe('В7: the format request', () => {
  it('sends the profile name as the API kind', async () => {
    for (const name of ['description', 'email_note', 'session_message'] as const) {
      const format = vi.fn(async (_r: FormatRequest) => result('<p>x</p>'))
      const { click } = setup(name, { value: '<p>text</p>', format })
      click('format')
      await tick()
      expect(format).toHaveBeenCalledWith({ text: 'text', profile: name })
    }
  })

  it('warns that links and styling are replaced, then keeps one undo step', async () => {
    let answer: (r: FormatResult) => void = () => undefined
    const pending = new Promise<FormatResult>((r) => (answer = r))
    const original = '<p>our <a href="https://x.example/">site</a> is <strong>open</strong></p>'
    const { editor, status, click, button } = setup('description', { value: original, format: () => pending })
    click('format')
    // The warning is shown before anything is applied.
    expect(status()).toBe(MESSAGES.en.formatting + ' ' + MESSAGES.en.formatLossy)
    expect(editor.getHTML()).toBe(original)
    answer(result('<p>our site is open</p>'))
    await tick()
    expect(editor.getHTML()).toBe('<p>our site is open</p>')
    expect(status()).toBe(MESSAGES.en.formatDone + ' ' + MESSAGES.en.formatLossy)
    // One undo step brings the links and the styling back.
    expect(button('undo')?.disabled).toBe(false)
    click('undo')
    expect(editor.getHTML()).toBe(original)
  })

  it('does not warn when the text has no link and no styling', async () => {
    const { status, click } = setup('description', { value: '<p>plain</p><ul><li>a</li></ul>', format: async () => result('<p>x</p>') })
    click('format')
    expect(status()).toBe(MESSAGES.en.formatting)
    await tick()
    expect(status()).toBe(MESSAGES.en.formatDone)
  })

  it('each of the three marks counts', async () => {
    for (const art of ['<p><em>a</em></p>', '<p><u>a</u></p>', '<p><strong>a</strong></p>']) {
      const { status, click } = setup('description', { value: art, format: async () => result('<p>x</p>') })
      click('format')
      expect(status(), art).toContain(MESSAGES.en.formatLossy)
      await tick()
    }
  })

  it('has the warning in all five languages', () => {
    expect(new Set(LOCALES.map((l) => MESSAGES[l].formatLossy)).size).toBe(LOCALES.length)
    for (const l of LOCALES) expect(MESSAGES[l].formatLossy.length).toBeGreaterThan(20)
  })
})

describe('М2: setHTML starts the undo history empty', () => {
  it('types, then setHTML: undo is disabled and nothing comes back', () => {
    const { editor, button } = setup('description', { value: '<p>start</p>' })
    editor.editor.commands.insertContent(' typed')
    expect(button('undo')?.disabled).toBe(false)
    editor.setHTML('<p>replaced</p>')
    expect(button('undo')?.disabled).toBe(true)
    expect(button('redo')?.disabled).toBe(true)
    expect(editor.editor.can().undo()).toBe(false)
    editor.editor.commands.undo()
    expect(editor.getHTML()).toBe('<p>replaced</p>')
  })

  it('editing after setHTML builds a new history, and onChange is not called by setHTML itself', () => {
    const onChange = vi.fn()
    const { editor, button } = setup('description', { onChange })
    editor.setHTML('<p>one</p>')
    expect(onChange).not.toHaveBeenCalled()
    editor.editor.commands.insertContent(' two')
    expect(onChange).toHaveBeenCalledTimes(1)
    expect(onChange.mock.calls[0][0]).toBe('<p>one two</p>')
    expect(button('undo')?.disabled).toBe(false)
    editor.editor.commands.undo()
    expect(editor.getHTML()).toBe('<p>one</p>')
    expect(editor.editor.can().undo()).toBe(false)
  })

  it('updates the counter and the stats', () => {
    const { editor, counter } = setup('description')
    editor.setHTML('<p>abcd</p>')
    expect(editor.getStats().visible).toBe(4)
    expect(counter().textContent).toContain('4')
    editor.setHTML('')
    expect(editor.getStats().empty).toBe(true)
  })

  it('the editor keeps working after the state was replaced: bold, link, list, Format', async () => {
    const { editor, click, button } = setup('description', { format: async () => result('<p>done</p>') })
    editor.setHTML('<p>word</p>')
    editor.editor.commands.selectAll()
    click('bold')
    expect(editor.getHTML()).toBe('<p><strong>word</strong></p>')
    click('bulletList')
    expect(editor.getHTML()).toBe('<ul><li><strong>word</strong></li></ul>')
    click('format')
    await tick()
    expect(editor.getHTML()).toBe('<p>done</p>')
    expect(button('undo')?.disabled).toBe(false)
  })
})

describe('М3: lang is on the panel and the dialog, not on the text', () => {
  it('leaves the editing area without lang and with the host language', () => {
    const { root, dom } = setup('description', { locale: 'ru' })
    expect(root.hasAttribute('lang')).toBe(false)
    expect(dom.closest('[lang]')).toBeNull()
    expect(root.querySelector('.arena-re__toolbar')?.getAttribute('lang')).toBe('ru')
    expect(root.querySelector('.arena-re__link')?.getAttribute('lang')).toBe('ru')
  })
})

describe('the Czech label (WCAG 2.5.3)', () => {
  it('every language has the visible label of the Format button inside its accessible name', () => {
    for (const l of LOCALES) {
      const m = MESSAGES[l as Locale]
      expect(m.formatHint.toLowerCase(), l).toContain(m.format.toLowerCase())
      const { button } = setup('description', { locale: l, format: async () => result('<p>x</p>') })
      expect(button('format')?.getAttribute('aria-label')?.toLowerCase(), l).toContain(m.format.toLowerCase())
    }
  })
})

describe('М6: the content of textarea, xmp and plaintext is text', () => {
  const quoted = (inner: string) => sanitize(`<p>${inner}</p>`, PROFILES.description)
  it('keeps style= written inside a textarea as text', () => {
    const got = sanitize('<p>a<textarea><b style="color:red">t</b></textarea>b</p>', PROFILES.description)
    expect(got).toBe(quoted('a&lt;b style="color:red"&gt;t&lt;/b&gt;b'))
    expect(got).not.toContain('data-arena-style')
  })
  it('keeps it inside xmp and plaintext', () => {
    expect(sanitize('<p>x</p><xmp><i style="a:b">q</i></xmp>', PROFILES.description)).toBe(
      '<p>x</p><p>' + '&lt;i style="a:b"&gt;q&lt;/i&gt;</p>',
    )
    const plain = sanitize('<p>x</p><plaintext><b style="a:b">q</b>', PROFILES.description)
    expect(plain).not.toContain('data-arena-style')
    expect(plain).toContain('style=')
  })
  it('still renames the style attribute of real tags, also after a textarea', () => {
    const out = renameStyleAttributes('<textarea style="x">a style="y"</textarea><b style="z">q</b>')
    expect(out).toBe('<textarea data-arena-style="x">a style="y"</textarea><b data-arena-style="z">q</b>')
  })
  it('an unclosed textarea runs to the end without hanging', () => {
    expect(renameStyleAttributes('<textarea><b style="x">')).toBe('<textarea><b style="x">')
  })
  it('the end tag may differ in case and the content after it is markup again', () => {
    expect(renameStyleAttributes('<TEXTAREA>a</TextArea ><b style="1">')).toBe('<TEXTAREA>a</TextArea ><b data-arena-style="1">')
  })
})

describe('М7: lone surrogates are removed', () => {
  it('in a text', () => {
    expect(sanitize('<p>a\ud800b\udc00c</p>', PROFILES.description)).toBe('<p>abc</p>')
    expect(sanitize('<p>a\u{1f600}b</p>', PROFILES.description)).toBe('<p>a\u{1f600}b</p>')
  })
  it('in a pasted plain text', () => {
    expect(stripForbiddenChars('x\ud800y\udfffz\u{1f600}')).toBe('xyz\u{1f600}')
  })
  it('in the editor', () => {
    const { editor } = setup('description')
    editor.setHTML('<p>a\ud83db</p>')
    expect(editor.getHTML()).toBe('<p>ab</p>')
    expect(editor.getStats().visible).toBe(2)
  })
})

describe('М9: one parse per keystroke', () => {
  it('getHTML and the stats follow commands that bypass the update event', () => {
    const { editor } = setup('description', { value: '<p>a</p>' })
    editor.editor.commands.setContent('<p>b</p>', { emitUpdate: false })
    expect(editor.getHTML()).toBe('<p>b</p>')
    expect(editor.getStats().visible).toBe(1)
  })

  it('onChange gets the same html that getHTML returns', () => {
    const onChange = vi.fn()
    const { editor } = setup('description', { onChange })
    editor.editor.commands.insertContent('<p>x  <b>y</b></p>')
    expect(onChange.mock.calls.at(-1)?.[0]).toBe(editor.getHTML())
    expect(editor.getHTML()).toBe('<p>x <strong>y</strong></p>')
  })
})
