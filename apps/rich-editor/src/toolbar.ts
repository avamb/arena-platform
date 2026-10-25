// The toolbar, written without a framework. Plain DOM, CSS classes only (no
// style attributes: the editor works under a strict CSP). The buttons carry
// aria-label, aria-pressed for toggles and aria-keyshortcuts; the toolbar is
// one tab stop with arrow-key navigation, mirrored in right-to-left locales.

import type { Editor } from '@tiptap/core'

import type { Messages } from './i18n'
import { profileHas, type Profile } from './profiles'

const SVG_NS = 'http://www.w3.org/2000/svg'

const ICONS: Record<string, string[]> = {
  bold: ['M6 4h8a4 4 0 0 1 0 8H6z', 'M6 12h9a4 4 0 0 1 0 8H6z'],
  italic: ['M19 4h-9', 'M14 20H5', 'M15 4L9 20'],
  underline: ['M6 3v7a6 6 0 0 0 12 0V3', 'M4 21h16'],
  link: [
    'M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71',
    'M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71',
  ],
  unlink: [
    'M18.84 12.25l1.72-1.71a5 5 0 0 0-7.07-7.07l-1.72 1.71',
    'M5.17 11.75l-1.71 1.71a5 5 0 0 0 7.07 7.07l1.71-1.71',
    'M8 2v3',
    'M2 8h3',
    'M16 22v-3',
    'M22 16h-3',
  ],
  bulletList: ['M8 6h13', 'M8 12h13', 'M8 18h13', 'M3 6h.01', 'M3 12h.01', 'M3 18h.01'],
  orderedList: ['M10 6h11', 'M10 12h11', 'M10 18h11', 'M4 6h1v4', 'M4 10h2', 'M6 18H4c0-1 2-2 2-3s-1-1.5-2-1'],
  quote: [
    'M3 21c3 0 7-1 7-8V5c0-1.25-.756-2.017-2-2H4c-1.25 0-2 .75-2 1.972V11c0 1.25.75 2 2 2 1 0 1 0 1 1v1c0 1-1 2-2 2s-1 .008-1 1.031V20c0 1 0 1 1 1z',
    'M15 21c3 0 7-1 7-8V5c0-1.25-.757-2.017-2-2h-4c-1.25 0-2 .75-2 1.972V11c0 1.25.75 2 2 2h.75c0 2.25.25 4-2.75 4v3c0 1 0 1 1 1z',
  ],
  undo: ['M3 7v6h6', 'M21 17a9 9 0 0 0-9-9 9 9 0 0 0-6 2.3L3 13'],
  redo: ['M21 7v6h-6', 'M3 17a9 9 0 0 1 9-9 9 9 0 0 1 6 2.3L21 13'],
  format: ['M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9z', 'M19 15l.7 1.8L21.5 17.5l-1.8.7L19 20l-.7-1.8-1.8-.7 1.8-.7z'],
}

function icon(name: string): SVGElement {
  const svg = document.createElementNS(SVG_NS, 'svg')
  svg.setAttribute('viewBox', '0 0 24 24')
  svg.setAttribute('width', '18')
  svg.setAttribute('height', '18')
  svg.setAttribute('fill', 'none')
  svg.setAttribute('stroke', 'currentColor')
  svg.setAttribute('stroke-width', '2')
  svg.setAttribute('stroke-linecap', 'round')
  svg.setAttribute('stroke-linejoin', 'round')
  svg.setAttribute('aria-hidden', 'true')
  svg.setAttribute('focusable', 'false')
  svg.setAttribute('class', 'arena-re__icon')
  for (const d of ICONS[name] ?? []) {
    const path = document.createElementNS(SVG_NS, 'path')
    path.setAttribute('d', d)
    svg.appendChild(path)
  }
  return svg
}

function isMac(): boolean {
  const nav = typeof navigator === 'undefined' ? undefined : navigator
  return !!nav && /Mac|iPhone|iPad/.test(nav.platform || nav.userAgent || '')
}

/** "Mod-Shift-b" for the tooltip: "Ctrl+Shift+B", or the Mac symbols. */
function shortcutLabel(keys: string): string {
  const mac = isMac()
  return keys
    .split('-')
    .map((k) => {
      if (k === 'Mod') return mac ? '⌘' : 'Ctrl'
      if (k === 'Shift') return mac ? '⇧' : 'Shift'
      if (k === 'Alt') return mac ? '⌥' : 'Alt'
      return k.length === 1 ? k.toUpperCase() : k
    })
    .join(mac ? '' : '+')
}

/** aria-keyshortcuts uses Control and Meta, not Mod. */
function ariaShortcuts(keys: string): string {
  const parts = keys.split('-').map((k) => (k.length === 1 ? k.toUpperCase() : k))
  const withMod = (mod: string) => parts.map((k) => (k === 'Mod' ? mod : k)).join('+')
  return keys.includes('Mod') ? withMod('Control') + ' ' + withMod('Meta') : withMod('Control')
}

interface ButtonDef {
  id: string
  label: string
  /** Icon name, or a short text glyph when `glyph` is set. */
  icon?: string
  glyph?: string
  /** A visible text next to the icon. */
  text?: string
  hint?: string
  shortcut?: string
  /** A toggle shows aria-pressed. */
  active?: (e: Editor) => boolean
  enabled?: (e: Editor) => boolean
  run: (e: Editor) => void
}

export interface ToolbarOptions {
  editor: Editor
  profile: Profile
  messages: Messages
  dir: 'ltr' | 'rtl'
  openLink: () => void
  /** Present when the host passed a format callback. */
  runFormat?: () => void
  isBusy: () => boolean
}

export interface Toolbar {
  el: HTMLElement
  /** Re-reads the editor state: pressed and disabled buttons. */
  refresh: () => void
  /** Moves the focus to the first button. */
  focus: () => void
}

export function createToolbar(o: ToolbarOptions): Toolbar {
  const { editor, profile, messages: t } = o
  const defs: ButtonDef[] = [
    {
      id: 'bold',
      label: t.bold,
      icon: 'bold',
      shortcut: 'Mod-b',
      active: (e) => e.isActive('bold'),
      run: (e) => e.chain().focus().toggleBold().run(),
    },
    {
      id: 'italic',
      label: t.italic,
      icon: 'italic',
      shortcut: 'Mod-i',
      active: (e) => e.isActive('italic'),
      run: (e) => e.chain().focus().toggleItalic().run(),
    },
    {
      id: 'underline',
      label: t.underline,
      icon: 'underline',
      shortcut: 'Mod-u',
      active: (e) => e.isActive('underline'),
      run: (e) => e.chain().focus().toggleUnderline().run(),
    },
    {
      id: 'link',
      label: t.link,
      icon: 'link',
      shortcut: 'Mod-k',
      active: (e) => e.isActive('link'),
      run: () => o.openLink(),
    },
    {
      id: 'unlink',
      label: t.unlink,
      icon: 'unlink',
      enabled: (e) => e.isActive('link'),
      run: (e) => e.chain().focus().extendMarkRange('link').unsetLink().run(),
    },
    {
      id: 'bulletList',
      label: t.bulletList,
      icon: 'bulletList',
      shortcut: 'Mod-Shift-8',
      active: (e) => e.isActive('bulletList'),
      run: (e) => e.chain().focus().toggleBulletList().run(),
    },
    {
      id: 'orderedList',
      label: t.orderedList,
      icon: 'orderedList',
      shortcut: 'Mod-Shift-7',
      active: (e) => e.isActive('orderedList'),
      run: (e) => e.chain().focus().toggleOrderedList().run(),
    },
  ]
  if (profileHas(profile, 'h3')) {
    defs.push(
      {
        id: 'h3',
        label: t.h3,
        glyph: 'H3',
        shortcut: 'Mod-Alt-3',
        active: (e) => e.isActive('heading', { level: 3 }),
        run: (e) => e.chain().focus().toggleHeading({ level: 3 }).run(),
      },
      {
        id: 'h4',
        label: t.h4,
        glyph: 'H4',
        shortcut: 'Mod-Alt-4',
        active: (e) => e.isActive('heading', { level: 4 }),
        run: (e) => e.chain().focus().toggleHeading({ level: 4 }).run(),
      },
    )
  }
  if (profileHas(profile, 'blockquote')) {
    defs.push({
      id: 'quote',
      label: t.quote,
      icon: 'quote',
      shortcut: 'Mod-Shift-b',
      active: (e) => e.isActive('blockquote'),
      run: (e) => e.chain().focus().toggleBlockquote().run(),
    })
  }
  defs.push(
    {
      id: 'undo',
      label: t.undo,
      icon: 'undo',
      shortcut: 'Mod-z',
      enabled: (e) => e.can().undo(),
      run: (e) => e.chain().focus().undo().run(),
    },
    {
      id: 'redo',
      label: t.redo,
      icon: 'redo',
      shortcut: 'Mod-Shift-z',
      enabled: (e) => e.can().redo(),
      run: (e) => e.chain().focus().redo().run(),
    },
  )
  const runFormat = o.runFormat
  if (runFormat) {
    defs.push({
      id: 'format',
      label: t.format,
      hint: t.formatHint,
      icon: 'format',
      text: t.format,
      enabled: (e) => !e.isEmpty,
      run: () => runFormat(),
    })
  }

  const el = document.createElement('div')
  el.className = 'arena-re__toolbar'
  el.setAttribute('role', 'toolbar')
  el.setAttribute('aria-label', t.toolbarLabel)
  el.setAttribute('aria-orientation', 'horizontal')

  interface Built {
    def: ButtonDef
    btn: HTMLButtonElement
  }
  const built: Built[] = []
  let lastGroup = ''
  const groupOf = (id: string) =>
    ['bold', 'italic', 'underline'].includes(id)
      ? 'text'
      : ['link', 'unlink'].includes(id)
        ? 'link'
        : ['bulletList', 'orderedList'].includes(id)
          ? 'list'
          : ['h3', 'h4', 'quote'].includes(id)
            ? 'block'
            : ['undo', 'redo'].includes(id)
              ? 'history'
              : 'format'
  for (const def of defs) {
    const group = groupOf(def.id)
    if (group !== lastGroup) {
      if (lastGroup !== '') {
        const sep = document.createElement('span')
        sep.className = 'arena-re__sep'
        sep.setAttribute('role', 'separator')
        sep.setAttribute('aria-orientation', 'vertical')
        el.appendChild(sep)
      }
      lastGroup = group
    }
    const btn = document.createElement('button')
    btn.type = 'button'
    btn.className = 'arena-re__btn arena-re__btn--' + def.id
    btn.setAttribute('data-action', def.id)
    btn.setAttribute('aria-label', def.hint ?? def.label)
    const tip = def.shortcut ? `${def.hint ?? def.label} (${shortcutLabel(def.shortcut)})` : (def.hint ?? def.label)
    btn.title = tip
    if (def.shortcut) btn.setAttribute('aria-keyshortcuts', ariaShortcuts(def.shortcut))
    if (def.active) btn.setAttribute('aria-pressed', 'false')
    if (def.icon) btn.appendChild(icon(def.icon))
    if (def.glyph) {
      const g = document.createElement('span')
      g.className = 'arena-re__glyph'
      g.setAttribute('aria-hidden', 'true')
      g.textContent = def.glyph
      btn.appendChild(g)
    }
    if (def.text) {
      const s = document.createElement('span')
      s.className = 'arena-re__btn-text'
      s.textContent = def.text
      btn.appendChild(s)
    }
    // Keep the selection in the editor: a click on a button must not blur it.
    btn.addEventListener('mousedown', (ev) => ev.preventDefault())
    btn.addEventListener('click', () => {
      if (btn.disabled || o.isBusy()) return
      def.run(editor)
      refresh()
    })
    el.appendChild(btn)
    built.push({ def, btn })
  }

  // One tab stop: the button that was used last (roving tabindex).
  let current = 0
  const enabledIndexes = () => built.map((b, i) => (b.btn.disabled ? -1 : i)).filter((i) => i >= 0)
  const applyTabindex = () => {
    const enabled = enabledIndexes()
    if (!enabled.includes(current)) current = enabled[0] ?? 0
    built.forEach((b, i) => b.btn.setAttribute('tabindex', i === current ? '0' : '-1'))
  }
  const focusIndex = (i: number) => {
    current = i
    applyTabindex()
    built[i].btn.focus()
  }
  el.addEventListener('focusin', (ev) => {
    const i = built.findIndex((b) => b.btn === ev.target)
    if (i >= 0 && i !== current) {
      current = i
      applyTabindex()
    }
  })
  el.addEventListener('keydown', (ev) => {
    const i = built.findIndex((b) => b.btn === ev.target)
    if (i < 0) return
    const enabled = enabledIndexes()
    const pos = enabled.indexOf(i)
    if (pos < 0) return
    const forward = o.dir === 'rtl' ? 'ArrowLeft' : 'ArrowRight'
    const backward = o.dir === 'rtl' ? 'ArrowRight' : 'ArrowLeft'
    let next = -1
    if (ev.key === forward) next = enabled[(pos + 1) % enabled.length]
    else if (ev.key === backward) next = enabled[(pos - 1 + enabled.length) % enabled.length]
    else if (ev.key === 'Home') next = enabled[0]
    else if (ev.key === 'End') next = enabled[enabled.length - 1]
    else if (ev.key === 'Escape') {
      ev.preventDefault()
      editor.commands.focus()
      return
    }
    if (next >= 0) {
      ev.preventDefault()
      focusIndex(next)
    }
  })

  function refresh(): void {
    const busy = o.isBusy()
    for (const { def, btn } of built) {
      if (def.active) btn.setAttribute('aria-pressed', def.active(editor) ? 'true' : 'false')
      let enabled = def.enabled ? def.enabled(editor) : true
      if (busy) enabled = false
      btn.disabled = !enabled
      if (def.id === 'format') btn.setAttribute('aria-busy', busy ? 'true' : 'false')
    }
    applyTabindex()
  }
  refresh()

  return {
    el,
    refresh,
    focus: () => {
      const enabled = enabledIndexes()
      if (enabled.length > 0) focusIndex(enabled[0])
    },
  }
}
