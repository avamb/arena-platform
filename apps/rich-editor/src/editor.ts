// create(element, options): the one editor of every interface. It builds the
// toolbar, the editing area and the counter inside `element`, on the Tiptap
// core with injectCSS off (the host loads rich-editor.css), no NodeViews and
// no inline styles, so it runs under a strict CSP.

import { Editor, createDocument } from '@tiptap/core'
import { closeHistory } from '@tiptap/pm/history'
import { EditorState, Selection } from '@tiptap/pm/state'

import { buildExtensions } from './extensions'
import { directionOf, format, MESSAGES, resolveLocale, type Locale } from './i18n'
import { createLinkDialog } from './link-dialog'
import { parseHtml, measure, sanitize, type TextStats } from './sanitize'
import { hasFormatting, plainText } from './model'
import { PROFILES, profileByName, type Profile, type ProfileName } from './profiles'
import { createToolbar } from './toolbar'

/** What the host's format callback returns: the answer of POST /v1/organizations/{org_id}/texts/format. */
export interface FormatResult {
  html: string
  ai_applied: boolean
  reason?: string
  prompt_version?: string
}

/** What the host's format callback gets: the plain text and the profile name, which is the `kind` of the API. */
export interface FormatRequest {
  text: string
  profile: ProfileName
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
  /** True when the profile is strict about scheme words and the text holds one (the API: scheme_not_allowed). */
  schemeWord: boolean
  /** True when a limit is exceeded or a scheme word is in the text; the API stays the final judge. */
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
   * Adds the "Format" button. The host sends the plain text to the API
   * (POST /v1/organizations/{org_id}/texts/format with `kind` = request.profile)
   * and returns its answer. Links and styling of the text are not sent: the
   * status warns about it. The result goes in as one step that undo reverses.
   */
  format?: (request: FormatRequest) => Promise<FormatResult>
  /** Text direction. Default: rtl for he, ltr for the rest. */
  dir?: 'ltr' | 'rtl'
  /** aria-label of the editing area. Default: the localized "Text editor". */
  label?: string
  /** id of an element that labels the editing area (takes precedence over label). */
  labelledBy?: string
  /** Makes the text read-only: no typing, no toolbar, no Format, no link dialog. */
  disabled?: boolean
}

export interface RichEditor {
  /** The Tiptap editor, for hosts that need more than this API. Read the text with getHTML() of this object, never editor.getHTML(). */
  readonly editor: Editor
  /** The canonical ART: what the Go sanitizer accepts unchanged. "" for no text. */
  getHTML: () => string
  /** Replaces the text (cut down to the profile) and clears the undo history. Makes no onChange call. */
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
    schemeWord: s.schemeWord,
    visible: s.visible,
    maxVisible: p.maxVisible,
    links: s.links,
    maxLinks: p.maxLinks,
    htmlLength: s.htmlLength,
    maxHtml: p.maxHtml,
    empty: s.html === '',
    overLimit: s.visible > p.maxVisible || s.links > p.maxLinks || s.htmlLength > p.maxHtml || s.schemeWord,
  }
}

/** What the status line warns about: a counted limit first, then a scheme word. */
function problemOf(s: EditorStats): 'limit' | 'scheme' | null {
  if (s.visible > s.maxVisible || s.links > s.maxLinks || s.htmlLength > s.maxHtml) return 'limit'
  return s.schemeWord ? 'scheme' : null
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
  root.setAttribute('data-profile', profile.name)

  const body = document.createElement('div')
  body.className = 'arena-re__body'

  const footer = document.createElement('div')
  footer.className = 'arena-re__footer'
  footer.setAttribute('lang', locale) // the counter and the status speak the interface language
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
  let lastProblem: 'limit' | 'scheme' | null = null
  let html = ''
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
    extensions: buildExtensions(profile, {
      onLink: () => dialog.open(),
      onPasteFallback: () => setStatus(t.pasteFallback),
    }),
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

  /** One parse of the text gives the canonical HTML and the counters; the result is kept for this document. */
  let statsDoc: unknown = null
  const readStats = (): EditorStats => {
    const m = measure(editor.getHTML(), profile)
    html = m.html
    stats = toStats(m, profile)
    statsDoc = editor.state.doc
    return stats
  }
  /** The counters of the document as it is now (a command run on editor.commands changes it too). */
  const currentStats = (): EditorStats => (statsDoc === editor.state.doc ? stats : readStats())

  const renderCounter = () => {
    const parts = [format(t.counter, { n: stats.visible, max: stats.maxVisible })]
    if (stats.links > 0) parts.push(format(t.linksCounter, { n: stats.links, max: stats.maxLinks }))
    counter.textContent = parts.join(' · ')
    counter.classList.toggle('arena-re__counter--over', stats.overLimit)
  }

  const getHTML = () => {
    currentStats()
    return html
  }

  /** The status line text for a problem of the text. */
  const problemMessage = (problem: 'limit' | 'scheme' | null): string =>
    problem === 'limit'
      ? format(t.overLimit, { n: Math.max(stats.visible - stats.maxVisible, 1) })
      : problem === 'scheme'
        ? t.schemeWord
        : ''

  const dialog = createLinkDialog({
    editor,
    profile,
    messages: t,
    idPrefix: id,
    locale,
    linkCount: () => stats.links,
    onClose: () => toolbar.refresh(),
  })

  const toolbar = createToolbar({
    editor,
    profile,
    messages: t,
    dir,
    locale,
    openLink: () => dialog.open(),
    runFormat: options.format ? () => void runFormat() : undefined,
    isBusy: () => busy,
  })

  root.append(toolbar.el, dialog.el, body, footer)
  element.appendChild(root)

  /** aria-readonly on the editing area while the host made the text read-only. */
  const applyReadOnly = () => {
    if (options.disabled) editor.view.dom.setAttribute('aria-readonly', 'true')
    else editor.view.dom.removeAttribute('aria-readonly')
  }

  // Bumped by setHTML: an answer of Format that comes after it is not applied to the new text.
  let epoch = 0

  async function runFormat(): Promise<void> {
    const callback = options.format
    if (!callback || busy || destroyed || !editor.isEditable) return
    const doc = parseHtml(editor.getHTML(), profile)
    const text = plainText(doc)
    if (text.trim() === '') {
      setStatus(t.formatEmpty)
      return
    }
    // Only the plain text goes to the API: a link or styling in the text would be replaced.
    const lossy = hasFormatting(doc)
    const withWarning = (message: string) => (lossy ? message + ' ' + t.formatLossy : message)
    const started = epoch
    busy = true
    root.setAttribute('aria-busy', 'true')
    editor.setEditable(false, false)
    setStatus(withWarning(t.formatting))
    toolbar.refresh()
    try {
      const result = await callback({ text, profile: profile.name })
      if (destroyed) return
      if (epoch !== started) return // setHTML replaced the text meanwhile (and cleared the status)
      if (options.disabled) {
        setStatus('') // locked meanwhile: nothing is applied
        return
      }
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
        .setContent(clean, { emitUpdate: true })
        .run()
      editor.chain().command(({ tr }) => {
        closeHistory(tr)
        return true
      }).run()
      const problem = problemOf(currentStats())
      const done = withWarning(result.ai_applied ? t.formatDone : t.formatPlain)
      setStatus(problem === null ? done : done + ' ' + problemMessage(problem), 'format')
    } catch {
      if (!destroyed && epoch === started) setStatus(t.formatFailed)
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
    const problem = problemOf(stats)
    if (problem !== lastProblem) {
      lastProblem = problem
      if (problem !== null) setStatus(problemMessage(problem), 'limit')
      else if (statusKind === 'limit') setStatus('')
    }
    options.onChange?.(html, stats)
  }

  editor.on('update', onUpdate)
  editor.on('transaction', () => toolbar.refresh())
  readStats()
  renderCounter()
  lastProblem = problemOf(stats)
  if (lastProblem !== null) setStatus(problemMessage(lastProblem), 'limit')
  applyReadOnly()
  toolbar.refresh()

  return {
    editor,
    getHTML,
    setHTML(value: string) {
      if (destroyed) return
      epoch++
      const clean = cleanOrEmpty(value, profile)
      // A new state, not a transaction: only that starts the undo history empty again.
      const doc = createDocument(clean, editor.schema)
      editor.view.updateState(
        EditorState.create({ schema: editor.schema, doc, plugins: editor.state.plugins, selection: Selection.atEnd(doc) }),
      )
      readStats()
      renderCounter()
      lastProblem = problemOf(stats)
      setStatus(problemMessage(lastProblem), 'limit')
      toolbar.refresh()
    },
    getText: () => plainText(parseHtml(editor.getHTML(), profile)),
    getStats: () => currentStats(),
    focus: () => {
      editor.commands.focus()
    },
    setDisabled(disabled: boolean) {
      if (destroyed) return
      options.disabled = disabled
      editor.setEditable(!disabled && !busy, false)
      if (disabled) dialog.close(false)
      applyReadOnly()
      toolbar.refresh()
    },
    destroy() {
      if (destroyed) return
      destroyed = true
      editor.destroy()
      root.remove()
    },
  }
}

