import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  create,
  LOCALES,
  MESSAGES,
  resolveLocale,
  type CreateOptions,
  type FormatRequest,
  type FormatResult,
  type Locale,
  type ProfileName,
  type RichEditor,
} from '../src/index'

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
  const select = (from: number, to = from) => editor.editor.commands.setTextSelection({ from, to })
  return { host, editor, root, dom, button, click, select }
}

const tick = () => new Promise((r) => setTimeout(r, 0))

describe('the toolbar', () => {
  it('has the same buttons in every profile, plus heading and quote in the description only', () => {
    const base = ['bold', 'italic', 'underline', 'link', 'unlink', 'bulletList', 'orderedList', 'undo', 'redo']
    const names = (p: ProfileName) => [...setup(p).root.querySelectorAll('button[data-action]')].map((b) => b.getAttribute('data-action'))
    expect(names('description')).toEqual(['bold', 'italic', 'underline', 'link', 'unlink', 'bulletList', 'orderedList', 'h3', 'h4', 'quote', 'undo', 'redo'])
    expect(names('email_note')).toEqual(base)
    expect(names('session_message')).toEqual(base)
  })

  it('shows the Format button only when the host passes a format callback', () => {
    expect(setup('description').button('format')).toBeNull()
    const s = setup('description', { format: async () => ({ html: '<p>x</p>', ai_applied: true }) })
    expect(s.button('format')).not.toBeNull()
  })

  it('has an accessible name on every button, aria-pressed on toggles and a toolbar role', () => {
    const { root } = setup('description', { locale: 'en', format: async () => ({ html: '', ai_applied: false }) })
    const toolbar = root.querySelector('[role="toolbar"]') as HTMLElement
    expect(toolbar.getAttribute('aria-label')).toBe(MESSAGES.en.toolbarLabel)
    const toggles = ['bold', 'italic', 'underline', 'link', 'bulletList', 'orderedList', 'h3', 'h4', 'quote']
    for (const b of toolbar.querySelectorAll('button')) {
      expect(b.getAttribute('aria-label') ?? '', b.outerHTML).not.toBe('')
      expect(b.type).toBe('button')
      const action = b.getAttribute('data-action') as string
      expect(b.hasAttribute('aria-pressed'), action).toBe(toggles.includes(action))
    }
    expect(root.querySelector('[data-action="bold"]')?.getAttribute('aria-keyshortcuts')).toBe('Control+B Meta+B')
  })

  it('is one tab stop (roving tabindex) with arrow keys, mirrored in Hebrew', () => {
    const en = setup('email_note', { locale: 'en' })
    const tab = (r: HTMLElement) => [...r.querySelectorAll('button[data-action]')].filter((b) => b.getAttribute('tabindex') === '0')
    expect(tab(en.root)).toHaveLength(1)
    const first = tab(en.root)[0] as HTMLButtonElement
    first.focus()
    first.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }))
    expect(document.activeElement?.getAttribute('data-action')).toBe('italic')
    expect(tab(en.root)).toHaveLength(1)
    const he = setup('email_note', { locale: 'he' })
    const f = tab(he.root)[0] as HTMLButtonElement
    f.focus()
    f.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true }))
    expect(document.activeElement?.getAttribute('data-action')).toBe('italic')
  })

  it('toggles bold, italic, underline and lists, and tracks aria-pressed', () => {
    const { editor, button, click, select } = setup('description', { value: '<p>hello world</p>' })
    select(1, 6)
    click('bold')
    expect(editor.getHTML()).toBe('<p><strong>hello</strong> world</p>')
    expect(button('bold')?.getAttribute('aria-pressed')).toBe('true')
    click('italic')
    click('underline')
    expect(editor.getHTML()).toBe('<p><strong><em><u>hello</u></em></strong> world</p>')
    click('bold')
    expect(button('bold')?.getAttribute('aria-pressed')).toBe('false')
    select(3)
    click('bulletList')
    expect(editor.getHTML()).toBe('<ul><li><em><u>hello</u></em> world</li></ul>')
    expect(button('bulletList')?.getAttribute('aria-pressed')).toBe('true')
    click('orderedList')
    expect(editor.getHTML()).toBe('<ol><li><em><u>hello</u></em> world</li></ol>')
  })

  it('sets heading 3, heading 4 and the quote in the description profile', () => {
    const { editor, click, select } = setup('description', { value: '<p>a</p><p>b</p><p>c</p>' })
    select(1)
    click('h3')
    select(4)
    click('h4')
    select(7)
    click('quote')
    expect(editor.getHTML()).toBe('<h3>a</h3><h4>b</h4><blockquote><p>c</p></blockquote>')
  })

  it('has no heading or quote in the letter profiles, whatever comes in', () => {
    for (const p of ['email_note', 'session_message'] as const) {
      const { editor } = setup(p, { value: '<h3>t</h3><blockquote><p>q</p></blockquote>' })
      expect(editor.getHTML()).toBe('<p><strong>t</strong></p><p>q</p>')
      expect(editor.editor.schema.nodes.heading).toBeUndefined()
      expect(editor.editor.schema.nodes.blockquote).toBeUndefined()
    }
    const d = setup('description')
    expect(d.editor.editor.schema.nodes.heading).toBeDefined()
    expect(d.editor.editor.schema.nodes.blockquote).toBeDefined()
  })

  it('has no code, code block, rule, strike or image in the schema', () => {
    const { editor } = setup('description')
    const names = Object.keys(editor.editor.schema.nodes).sort()
    expect(names).toEqual(['blockquote', 'bulletList', 'doc', 'hardBreak', 'heading', 'listItem', 'orderedList', 'paragraph', 'text'])
    expect(Object.keys(editor.editor.schema.marks).sort()).toEqual(['bold', 'italic', 'link', 'underline'])
  })

  it('undo and redo buttons follow the history', () => {
    const { editor, button, click, select } = setup('description', { value: '<p>x</p>' })
    expect(button('undo')?.disabled).toBe(true)
    select(1, 2)
    click('bold')
    expect(button('undo')?.disabled).toBe(false)
    click('undo')
    expect(editor.getHTML()).toBe('<p>x</p>')
    expect(button('redo')?.disabled).toBe(false)
    click('redo')
    expect(editor.getHTML()).toBe('<p><strong>x</strong></p>')
  })

  it('answers the keyboard shortcuts', () => {
    const { editor, dom, select } = setup('description', { value: '<p>word</p>' })
    select(1, 5)
    const key = (init: KeyboardEventInit) => dom.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }))
    key({ key: 'b', ctrlKey: true })
    expect(editor.getHTML()).toBe('<p><strong>word</strong></p>')
    key({ key: 'i', ctrlKey: true })
    key({ key: 'u', ctrlKey: true })
    expect(editor.getHTML()).toBe('<p><strong><em><u>word</u></em></strong></p>')
    key({ key: 'z', ctrlKey: true })
    expect(editor.getHTML()).toBe('<p><strong><em>word</em></strong></p>')
    key({ key: 'z', ctrlKey: true, shiftKey: true })
    expect(editor.getHTML()).toBe('<p><strong><em><u>word</u></em></strong></p>')
  })
})

describe('the link dialog', () => {
  const dialogOf = (root: HTMLElement) => root.querySelector('[role="dialog"]') as HTMLElement
  const submit = (root: HTMLElement, href: string, text?: string) => {
    const inputs = root.querySelectorAll<HTMLInputElement>('.arena-re__link input')
    inputs[0].value = href
    if (text !== undefined) inputs[1].value = text
    root.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }))
  }
  const error = (root: HTMLElement) => (root.querySelector('.arena-re__error') as HTMLElement).textContent

  it('opens from the button and from Mod-k, and closes on Escape', () => {
    const { root, click, dom } = setup('description', { value: '<p>go</p>' })
    expect(dialogOf(root).hidden).toBe(true)
    click('link')
    expect(dialogOf(root).hidden).toBe(false)
    dialogOf(root).dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    expect(dialogOf(root).hidden).toBe(true)
    dom.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', ctrlKey: true, bubbles: true, cancelable: true }))
    expect(dialogOf(root).hidden).toBe(false)
  })

  it('links the selected text with the checked address, in canonical form', () => {
    const { editor, root, click, select } = setup('email_note', { value: '<p>see the map</p>' })
    select(9, 12)
    click('link')
    submit(root, 'Example.com/Map?x=1')
    expect(dialogOf(root).hidden).toBe(true)
    expect(editor.getHTML()).toBe('<p>see the <a href="https://example.com/Map?x=1">map</a></p>')
  })

  it('inserts the words of the link when nothing is selected', () => {
    const { editor, root, click, select } = setup('email_note', { value: '<p>go</p>' })
    select(3)
    click('link')
    expect((root.querySelectorAll('.arena-re__link .arena-re__field')[1] as HTMLElement).hidden).toBe(false)
    submit(root, 'https://example.com', 'here')
    expect(editor.getHTML()).toBe('<p>go<a href="https://example.com">here</a></p>')
  })

  it('refuses what href.go refuses and says so', () => {
    const { editor, root, click, select } = setup('email_note', { value: '<p>abc</p>' })
    select(1, 4)
    click('link')
    for (const bad of ['http://example.com', 'javascript:alert(1)', 'https://user@example.com', 'https://example.com:81', 'https://10.0.0.1', 'https://localhost', 'mailto:a@b.com']) {
      submit(root, bad)
      expect(dialogOf(root).hidden, bad).toBe(false)
      expect(error(root), bad).toBe(MESSAGES.en.linkInvalidHttps)
      expect((root.querySelector('.arena-re__link input') as HTMLElement).getAttribute('aria-invalid')).toBe('true')
    }
    expect(editor.getHTML()).toBe('<p>abc</p>')
  })

  it('accepts mailto only in the description profile', () => {
    const d = setup('description', { value: '<p>abc</p>' })
    d.select(1, 4)
    d.click('link')
    submit(d.root, 'Team@Example.com')
    expect(d.editor.getHTML()).toBe('<p><a href="mailto:Team@example.com">abc</a></p>')
    const e = setup('email_note', { value: '<p>abc</p>' })
    e.select(1, 4)
    e.click('link')
    submit(e.root, 'team@example.com')
    expect(e.editor.getHTML()).toBe('<p>abc</p>')
  })

  it('stops at the link limit of the profile', () => {
    const links = [1, 2, 3].map((n) => `<a href="https://example.com/${n}">l${n}</a>`).join(' ')
    const { editor, root, click, select } = setup('session_message', { value: `<p>${links} tail</p>` })
    const end = editor.editor.state.doc.content.size - 1
    select(end - 3, end)
    click('link')
    submit(root, 'https://example.com/4')
    expect(error(root)).toBe(MESSAGES.en.linkTooMany.replace('{max}', '3'))
    expect(editor.getHTML()).not.toContain('/4')
  })

  it('edits and removes a link', () => {
    const { editor, root, button, click, select } = setup('description', { value: '<p>a <a href="https://example.com/old">link</a> b</p>' })
    expect(button('unlink')?.disabled).toBe(true)
    select(5)
    expect(button('unlink')?.disabled).toBe(false)
    expect(button('link')?.getAttribute('aria-pressed')).toBe('true')
    click('link')
    expect((root.querySelector('.arena-re__link input') as HTMLInputElement).value).toBe('https://example.com/old')
    submit(root, 'https://example.com/new')
    expect(editor.getHTML()).toBe('<p>a <a href="https://example.com/new">link</a> b</p>')
    click('unlink')
    expect(editor.getHTML()).toBe('<p>a link b</p>')
  })

  it('writes a link with an href and nothing else, also in the live DOM', () => {
    const { dom } = setup('description', { value: '<p><a href="https://example.com">x</a></p>' })
    const a = dom.querySelector('a') as HTMLAnchorElement
    expect(a.getAttributeNames()).toEqual(['href'])
  })
})

describe('the Format button', () => {
  const result = (html: string, ai_applied = true): FormatResult => ({ html, ai_applied })

  it('sends the plain text, applies the answer and undo brings the old text back in one step', async () => {
    const format = vi.fn(async (_request: FormatRequest) => result('<p><strong>Concert</strong></p><ul><li>one</li><li>two</li></ul>'))
    const { editor, root, button, click } = setup('description', { value: '<p>Concert</p><p>one<br>two</p>', format })
    click('format')
    // The API gets the plain text and the profile name (its `kind`).
    expect(format).toHaveBeenCalledWith({ text: 'Concert\n\none\ntwo', profile: 'description' })
    expect(button('format')?.disabled).toBe(true) // busy
    expect(root.getAttribute('aria-busy')).toBe('true')
    expect(editor.editor.isEditable).toBe(false)
    await tick()
    expect(editor.getHTML()).toBe('<p><strong>Concert</strong></p><ul><li>one</li><li>two</li></ul>')
    expect(editor.editor.isEditable).toBe(true)
    expect(root.hasAttribute('aria-busy')).toBe(false)
    expect((root.querySelector('.arena-re__status') as HTMLElement).textContent).toBe(MESSAGES.en.formatDone)
    click('undo')
    expect(editor.getHTML()).toBe('<p>Concert</p><p>one<br>two</p>')
    // the message about the result goes away with the next change of the text
    expect((root.querySelector('.arena-re__status') as HTMLElement).textContent).toBe('')
    click('redo')
    expect(editor.getHTML()).toBe('<p><strong>Concert</strong></p><ul><li>one</li><li>two</li></ul>')
  })

  it('is one undo step even right after typing', async () => {
    const { editor, click } = setup('description', { value: '<p>start</p>', format: async () => result('<p>formatted</p>') })
    editor.editor.commands.insertContentAt(editor.editor.state.doc.content.size - 1, ' typed')
    expect(editor.getHTML()).toBe('<p>start typed</p>')
    click('format')
    await tick()
    expect(editor.getHTML()).toBe('<p>formatted</p>')
    click('undo')
    expect(editor.getHTML()).toBe('<p>start typed</p>')
  })

  it('cuts the answer down to the profile before it goes in', async () => {
    const { editor, click } = setup('session_message', {
      value: '<p>x</p>',
      format: async () => result('<h3>Title</h3><p onclick="x()">a <img src=x> <a href="http://bad.example">b</a></p>'),
    })
    click('format')
    await tick()
    expect(editor.getHTML()).toBe('<p><strong>Title</strong></p><p>a b</p>')
  })

  it('says so when the AI did not apply, and still takes the answer', async () => {
    const { editor, root, click } = setup('description', { value: '<p>**x**</p>', format: async () => result('<p><strong>x</strong></p>', false) })
    click('format')
    await tick()
    expect(editor.getHTML()).toBe('<p><strong>x</strong></p>')
    expect((root.querySelector('.arena-re__status') as HTMLElement).textContent).toBe(MESSAGES.en.formatPlain)
  })

  it('keeps the text and reports a failure when the callback rejects or answers nothing', async () => {
    const failing = setup('description', {
      value: '<p>keep</p>',
      format: async () => {
        throw new Error('network')
      },
    })
    failing.click('format')
    await tick()
    expect(failing.editor.getHTML()).toBe('<p>keep</p>')
    expect((failing.root.querySelector('.arena-re__status') as HTMLElement).textContent).toBe(MESSAGES.en.formatFailed)
    expect(failing.editor.editor.isEditable).toBe(true)
    expect(failing.button('format')?.disabled).toBe(false)

    const empty = setup('description', { value: '<p>keep</p>', format: async () => result('<script>1</script>') })
    empty.click('format')
    await tick()
    expect(empty.editor.getHTML()).toBe('<p>keep</p>')
    expect((empty.root.querySelector('.arena-re__status') as HTMLElement).textContent).toBe(MESSAGES.en.formatFailed)
  })

  it('does nothing for an empty text and does not call the host', () => {
    const format = vi.fn(async () => result('<p>x</p>'))
    const { button } = setup('description', { format })
    expect(button('format')?.disabled).toBe(true)
    button('format')?.click()
    expect(format).not.toHaveBeenCalled()
  })

  it('tells the host about the change once, with the formatted html', async () => {
    const onChange = vi.fn()
    const { click } = setup('description', { value: '<p>a</p>', onChange, format: async () => result('<p><em>a</em></p>') })
    click('format')
    await tick()
    expect(onChange).toHaveBeenCalledTimes(1)
    expect(onChange.mock.calls[0][0]).toBe('<p><em>a</em></p>')
  })
})

describe('the counter and onChange', () => {
  it('shows visible characters against the profile limit', () => {
    const { root } = setup('description', { value: '<p>ab</p><p>cd</p>' })
    expect((root.querySelector('.arena-re__counter') as HTMLElement).textContent).toBe('5 of 4000 characters')
    const s = setup('session_message', { locale: 'ru' })
    expect((s.root.querySelector('.arena-re__counter') as HTMLElement).textContent).toBe('0 из 1000 знаков')
  })

  it('goes red and says so when the limit is exceeded, and updates on typing', () => {
    const onChange = vi.fn()
    const { editor, root } = setup('session_message', { onChange })
    const counter = root.querySelector('.arena-re__counter') as HTMLElement
    editor.editor.commands.insertContent('a'.repeat(1000))
    expect(counter.textContent).toBe('1000 of 1000 characters')
    expect(counter.classList.contains('arena-re__counter--over')).toBe(false)
    editor.editor.commands.insertContent('bbb')
    expect(counter.textContent).toBe('1003 of 1000 characters')
    expect(counter.classList.contains('arena-re__counter--over')).toBe(true)
    expect((root.querySelector('.arena-re__status') as HTMLElement).textContent).toBe('The text is 3 characters over the limit.')
    expect(editor.getStats().overLimit).toBe(true)
    const [html, stats] = onChange.mock.calls[onChange.mock.calls.length - 1]
    expect(html).toBe('<p>' + 'a'.repeat(1000) + 'bbb</p>')
    expect(stats).toMatchObject({ visible: 1003, maxVisible: 1000, overLimit: true, empty: false })
  })

  it('counts links against the limit', () => {
    const { root } = setup('email_note', { value: '<p><a href="https://a.com">a</a></p>' })
    expect((root.querySelector('.arena-re__counter') as HTMLElement).textContent).toBe('1 of 2000 characters · 1 of 5 links')
  })

  it('does not call onChange for setHTML and the initial value', () => {
    const onChange = vi.fn()
    const { editor } = setup('description', { value: '<p>a</p>', onChange })
    editor.setHTML('<p>b</p>')
    expect(onChange).not.toHaveBeenCalled()
    expect(editor.getHTML()).toBe('<p>b</p>')
  })
})

describe('getHTML and setHTML', () => {
  it('returns the empty string for no text, never <p></p>', () => {
    const { editor } = setup('description')
    expect(editor.getHTML()).toBe('')
    editor.setHTML('<p></p><p> </p><p><br></p>')
    expect(editor.getHTML()).toBe('')
    expect(editor.getStats().empty).toBe(true)
  })

  it('collapses spaces, trims, drops empty paragraphs and writes marks in the canonical order', () => {
    const { editor } = setup('description')
    editor.editor.commands.insertContent('<p>  a   b  </p><p></p><p><u><em><strong>x</strong></em></u></p>')
    expect(editor.getHTML()).toBe('<p>a b</p><p><strong><em><u>x</u></em></strong></p>')
  })

  it('reads any HTML into the profile and clears the undo history', () => {
    const { editor, button } = setup('email_note')
    editor.setHTML('<div class="x">a</div><h2>t</h2>')
    expect(editor.getHTML()).toBe('<p>a</p><p><strong>t</strong></p>')
    expect(button('undo')?.disabled).toBe(true)
  })

  it('getText gives the plain text', () => {
    const { editor } = setup('description', { value: '<p>a<br>b</p><ul><li>x</li><li>y</li></ul><ol><li>z</li></ol>' })
    expect(editor.getText()).toBe('a\nb\n\n- x\n- y\n\n1. z')
  })

  it('keeps a text nested three lists deep at two levels', () => {
    const { editor } = setup('description')
    editor.setHTML('<ul><li>a<ul><li>b<ul><li>c</li></ul></li></ul></li></ul>')
    expect(editor.getHTML()).toBe('<ul><li>a<ul><li>b</li><li>c</li></ul></li></ul>')
  })
})

describe('CSP and security hygiene', () => {
  it('injects no <style> and writes no style attribute', () => {
    const { root, dom } = setup('description', {
      value: '<h3>t</h3><p>a <strong>b</strong> <a href="https://example.com">c</a></p><ul><li>x</li></ul><blockquote><p>q</p></blockquote>',
      format: async () => ({ html: '', ai_applied: false }),
    })
    expect(document.head.querySelector('style')).toBeNull()
    expect(document.querySelector('style')).toBeNull()
    expect(root.querySelector('[style]')).toBeNull()
    expect(root.outerHTML).not.toMatch(/\sstyle=/)
    expect(dom.querySelector('script, iframe, img')).toBeNull()
  })

  it('removes itself from the page on destroy', () => {
    const { host, editor } = setup('description')
    expect(host.querySelector('.arena-re')).not.toBeNull()
    editor.destroy()
    expect(host.querySelector('.arena-re')).toBeNull()
    editor.destroy() // twice is fine
  })

  it('refuses an unknown profile', () => {
    expect(() => create(document.createElement('div'), { profile: 'nope' as ProfileName })).toThrowError(/unknown profile/)
  })

  it('can be read-only', () => {
    const { editor, dom } = setup('description', { value: '<p>x</p>', disabled: true })
    expect(editor.editor.isEditable).toBe(false)
    expect(dom.getAttribute('contenteditable')).toBe('false')
    editor.setDisabled(false)
    expect(editor.editor.isEditable).toBe(true)
  })

  it('gives the editing area a role and a label', () => {
    const { dom } = setup('description', { locale: 'cs' })
    expect(dom.getAttribute('role')).toBe('textbox')
    expect(dom.getAttribute('aria-multiline')).toBe('true')
    expect(dom.getAttribute('aria-label')).toBe(MESSAGES.cs.editorLabel)
    const labelled = setup('description', { labelledBy: 'my-label' })
    expect(labelled.dom.getAttribute('aria-labelledby')).toBe('my-label')
    expect(labelled.dom.hasAttribute('aria-label')).toBe(false)
  })
})

describe('languages', () => {
  const keys = Object.keys(MESSAGES.en).sort()

  it('has the five locales with the same keys and no empty text', () => {
    expect([...LOCALES].sort()).toEqual(['cs', 'en', 'es', 'he', 'ru'])
    for (const l of LOCALES) {
      expect(Object.keys(MESSAGES[l]).sort(), l).toEqual(keys)
      for (const [k, v] of Object.entries(MESSAGES[l])) expect(v.trim(), `${l}.${k}`).not.toBe('')
    }
  })

  it('keeps the placeholders of every message', () => {
    for (const l of LOCALES) {
      for (const k of keys) {
        const want = [...(MESSAGES.en as unknown as Record<string, string>)[k].matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort()
        const got = [...(MESSAGES[l] as unknown as Record<string, string>)[k].matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort()
        expect(got, `${l}.${k}`).toEqual(want)
      }
    }
  })

  it('labels the buttons in every language', () => {
    for (const l of LOCALES) {
      const { root } = setup('description', { locale: l, format: async () => ({ html: '', ai_applied: false }) })
      const m = MESSAGES[l as Locale]
      const label = (a: string) => root.querySelector(`[data-action="${a}"]`)?.getAttribute('aria-label')
      expect(label('bold')).toBe(m.bold)
      expect(label('italic')).toBe(m.italic)
      expect(label('underline')).toBe(m.underline)
      expect(label('link')).toBe(m.link)
      expect(label('unlink')).toBe(m.unlink)
      expect(label('bulletList')).toBe(m.bulletList)
      expect(label('orderedList')).toBe(m.orderedList)
      expect(label('h3')).toBe(m.h3)
      expect(label('h4')).toBe(m.h4)
      expect(label('quote')).toBe(m.quote)
      expect(label('undo')).toBe(m.undo)
      expect(label('redo')).toBe(m.redo)
      expect(label('format')).toBe(m.formatHint)
      expect(root.querySelector('.arena-re__btn-text')?.textContent).toBe(m.format)
    }
    expect(MESSAGES.ru.format).toBe('Оформить')
  })

  it('is right-to-left for Hebrew only, and dir can be set by the host', () => {
    for (const l of LOCALES) {
      const { root } = setup('description', { locale: l })
      expect(root.getAttribute('dir')).toBe(l === 'he' ? 'rtl' : 'ltr')
      // The interface language sits on the panel, the dialog and the footer, never on the text itself.
      expect(root.getAttribute('lang')).toBeNull()
      expect(root.querySelector('.ProseMirror')?.closest('[lang]')).toBeNull()
      for (const part of ['.arena-re__toolbar', '.arena-re__link', '.arena-re__footer']) {
        expect(root.querySelector(part)?.getAttribute('lang')).toBe(l)
      }
    }
    expect(setup('description', { locale: 'en', dir: 'rtl' }).root.getAttribute('dir')).toBe('rtl')
  })

  it('reads locale tags', () => {
    expect(resolveLocale('ru-RU')).toBe('ru')
    expect(resolveLocale('he_IL')).toBe('he')
    expect(resolveLocale('iw')).toBe('he')
    expect(resolveLocale('CS')).toBe('cs')
    expect(resolveLocale('de')).toBe('en')
    expect(resolveLocale(undefined)).toBe('en')
  })

  it('writes Hebrew in Hebrew letters', () => {
    expect(MESSAGES.he.bold).toMatch(/[֐-׿]/)
    expect(MESSAGES.cs.bold).toBe('Tučně')
  })
})
