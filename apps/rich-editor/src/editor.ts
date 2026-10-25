// create(element, options): the one editor of every interface. It builds the
// toolbar, the editing area and the counter inside `element`, on the Tiptap
// core with injectCSS off (the host loads rich-editor.css), no NodeViews and
// no inline styles, so it runs under a strict CSP.

import { Editor } from '@tiptap/core'
import { closeHistory } from '@tiptap/pm/history'

import { buildExtensions } from './extensions'
import { directionOf, format, MESSAGES, resolveLocale, type Locale } from './i18n'
import { createLinkDialog } from './link-dialog'
import { parseHtml, measure, sanitize, type TextStats } from './sanitize'
import { plainText } from './model'
import { PROFILES, profileByName, type Profile, type ProfileName } from './profiles'
import { createToolbar } from './toolbar'

/** What the host's format callback returns: the answer of POST /v1/organizations/{org_id}/texts/format. */
export interface FormatResult {
  html: string
  ai_applied: boolean
  reason?: string
  prompt_version?: string
}

export interface EditorStats {
  /** Visible characters, counted the way the API counts them. */
  visible: number
  maxVisible: number
  links: number
  maxLinks: number
  /** Characters of the canonical HTML and the profile's stored limit. */
  htmlLength: number
  maxHtml: number
  /** True when the text has no visible content. */
  empty: boolean
  /** True when a limit is exceeded; the API stays the final judge. */
  overLimit: boolean
}

export interface CreateOptions {
  /** description, email_note or session_message. */
  profile: ProfileName
  /** en, ru, cs, he or es (a tag like "ru-RU" is read as ru). Default en. */
  locale?: string
  /** The initial text: ART, or any HTML (it is cut down to the profile). */
  value?: string
  /** Called after every change made by the person with the canonical HTML. */
  onChange?: (html: string, stats: EditorStats) => void
  /**
   * Adds the "Format" button. The host sends the text to the API
   * (POST /v1/organizations/{org_id}/texts/format) and returns its answer.
   * The result goes in as one step that undo reverses.
   */
  format?: (text: string) => Promise<FormatResult>
  /** Text direction. Default: rtl for he, ltr for the rest. */
  dir?: 'ltr' | 'rtl'
  /** aria-label of the editing area. Default: the localized "Text editor". */
  label?: string
  /** id of an element that labels the editing area (takes precedence over label). */
  labelledBy?: string
  /** Makes the text read-only. */
  disabled?: boolean
}

export interface RichEditor {
  /** The Tiptap editor, for hosts that need more than this API. Read the text with getHTML() of this object, never editor.getHTML(). */
  readonly editor: Editor
  /** The canonical ART: what the Go sanitizer accepts unchanged. "" for no text. */
  getHTML: () => string
  /** Replaces the text (cut down to the profile) and clears the undo history. */
  setHTML: (html: string) => void
  /** The plain text of the editor, paragraphs separated by a blank line. */
  getText: () => string
  getStats: () => EditorStats
  focus: () => void
  setDisabled: (disabled: boolean) => void
  /** Removes the editor and its listeners from the page. */
  destroy: () => void
}

let instanceCounter = 0

function toStats(s: TextStats, p: Profile): EditorStats {
  return {
    visible: s.visible,
    maxVisible: p.maxVisible,
    links: s.links,
    maxLinks: p.maxLinks,
    htmlLength: s.htmlLength,
    maxHtml: p.maxHtml,
    empty: s.html === '',
    overLimit: s.visible > p.maxVisible || s.links > p.maxLinks || s.htmlLength > p.maxHtml,
  }
}

/** Canonical ART of a text for the profile, or "" when it cannot be read. */
function cleanOrEmpty(html: string, p: Profile): string {
  try {
    return sanitize(html, p)
  } catch {
    return ''
  }
}

export function create(element: HTMLElement, options: CreateOptions): RichEditor {
  const found = profileByName(options.profile)
  if (!found) {
    throw new Error(`rich-editor: unknown profile "${String(options.profile)}"; use ${Object.keys(PROFILES).join(', ')}`)
  }
  const profile: Profile = found
  const locale: Locale = resolveLocale(options.locale)
  const t = MESSAGES[locale]
  const dir = options.dir ?? directionOf(locale)
  const id = `arena-re-${++instanceCounter}`

  const root = document.createElement('div')
  root.className = 'arena-re'
  root.setAttribute('dir', dir)
  root.setAttribute('lang', locale)
  root.setAttribute('data-profile', profile.name)

  const body = document.createElement('div')
  body.className = 'arena-re__body'

  const footer = document.createElement('div')
  footer.className = 'arena-re__footer'
  const counter = document.createElement('span')
  counter.className = 'arena-re__counter'
  counter.id = id + '-counter'
  const status = document.createElement('span')
  status.className = 'arena-re__status'
  status.setAttribute('role', 'status')
  status.setAttribute('aria-live', 'polite')
  footer.append(counter, status)

  let destroyed = false
  let busy = false
  let lastOver = false
  let stats: EditorStats = toStats(measure('', profile), profile)

  // What the status line says: a result of Format goes away at the next change of the text, the
  // limit warning when the text is back under the limit.
  let statusKind: 'format' | 'limit' | 'other' | null = null
  const setStatus = (message: string, kind: 'format' | 'limit' | 'other' | null = 'other') => {
    if (status.textContent !== message) status.textContent = message
    statusKind = message === '' ? null : kind
  }

  const label: Record<string, string> = options.labelledBy
    ? { 'aria-labelledby': options.labelledBy }
    : { 'aria-label': options.label ?? t.editorLabel }

  const editor = new Editor({
    element: body,
    extensions: buildExtensions(profile, { onLink: () => dialog.open() }),
    content: cleanOrEmpty(options.value ?? '', profile),
    injectCSS: false,
    editable: !options.disabled,
    editorProps: {
      attributes: {
        class: 'arena-re__content',
        role: 'textbox',
        'aria-multiline': 'true',
        'aria-describedby': counter.id,
        ...label,
      },
    },
  })

  const readStats = (): EditorStats => {
    stats = toStats(measure(editor.getHTML(), profile), profile)
    return stats
  }

  const renderCounter = () => {
    const parts = [format(t.counter, { n: stats.visible, max: stats.maxVisible })]
    if (stats.links > 0) parts.push(format(t.linksCounter, { n: stats.links, max: stats.maxLinks }))
    counter.textContent = parts.join(' · ')
    counter.classList.toggle('arena-re__counter--over', stats.overLimit)
  }

  const getHTML = () => cleanOrEmpty(editor.getHTML(), profile)

  const dialog = createLinkDialog({
    editor,
    profile,
    messages: t,
    idPrefix: id,
    linkCount: () => stats.links,
    onClose: () => toolbar.refresh(),
  })

  const toolbar = createToolbar({
    editor,
    profile,
    messages: t,
    dir,
    openLink: () => dialog.open(),
    runFormat: options.format ? () => void runFormat() : undefined,
    isBusy: () => busy,
  })

  root.append(toolbar.el, dialog.el, body, footer)
  element.appendChild(root)

  async function runFormat(): Promise<void> {
    const callback = options.format
    if (!callback || busy || destroyed) return
    const text = plainText(parseHtml(editor.getHTML(), profile))
    if (text.trim() === '') {
      setStatus(t.formatEmpty)
      return
    }
    busy = true
    root.setAttribute('aria-busy', 'true')
    editor.setEditable(false, false)
    setStatus(t.formatting)
    toolbar.refresh()
    try {
      const result = await callback(text)
      if (destroyed) return
      const clean = cleanOrEmpty(result.html, profile)
      if (clean === '') {
        setStatus(t.formatFailed)
        return
      }
      // One transaction, its own undo step: undo brings the old text back.
      editor
        .chain()
        .command(({ tr }) => {
          closeHistory(tr)
          return true
        })
        .setContent(clean, true)
        .run()
      editor.chain().command(({ tr }) => {
        closeHistory(tr)
        return true
      }).run()
      setStatus(result.ai_applied ? t.formatDone : t.formatPlain, 'format')
    } catch {
      if (!destroyed) setStatus(t.formatFailed)
    } finally {
      busy = false
      if (!destroyed) {
        root.removeAttribute('aria-busy')
        editor.setEditable(!options.disabled, false)
        toolbar.refresh()
        if (!options.disabled) editor.commands.focus()
      }
    }
  }

  const onUpdate = () => {
    readStats()
    renderCounter()
    if (statusKind === 'format') setStatus('')
    if (stats.overLimit !== lastOver) {
      lastOver = stats.overLimit
      if (stats.overLimit) {
        setStatus(format(t.overLimit, { n: Math.max(stats.visible - stats.maxVisible, 1) }), 'limit')
      } else if (statusKind === 'limit') {
        setStatus('')
      }
    }
    options.onChange?.(getHTML(), stats)
  }

  editor.on('update', onUpdate)
  editor.on('transaction', () => toolbar.refresh())
  readStats()
  renderCounter()
  lastOver = stats.overLimit
  toolbar.refresh()

  return {
    editor,
    getHTML,
    setHTML(html: string) {
      const clean = cleanOrEmpty(html, profile)
      editor.chain().setMeta('addToHistory', false).setContent(clean, false).run()
      readStats()
      renderCounter()
      lastOver = stats.overLimit
      setStatus('')
      toolbar.refresh()
    },
    getText: () => plainText(parseHtml(editor.getHTML(), profile)),
    getStats: () => stats,
    focus: () => {
      editor.commands.focus()
    },
    setDisabled(disabled: boolean) {
      options.disabled = disabled
      editor.setEditable(!disabled && !busy, false)
    },
    destroy() {
      if (destroyed) return
      destroyed = true
      editor.destroy()
      root.remove()
    },
  }
}

