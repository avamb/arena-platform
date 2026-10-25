// The Tiptap extension set for a profile, built from individual extensions
// (not StarterKit): document, paragraph, text, bold, italic, underline, hard
// break, undo and redo, lists, a link mark of our own, and where the profile has
// them the headings (h3, h4) and the quote. Code, code block, horizontal rule,
// strike, drop cursor, gap cursor and the linkify library are not in the white
// list and not in the bundle. No NodeViews anywhere.

import { Extension, Mark, mergeAttributes, type AnyExtension } from '@tiptap/core'
import Blockquote from '@tiptap/extension-blockquote'
import Bold from '@tiptap/extension-bold'
import Document from '@tiptap/extension-document'
import HardBreak from '@tiptap/extension-hard-break'
import Heading from '@tiptap/extension-heading'
import Italic from '@tiptap/extension-italic'
import { BulletList } from '@tiptap/extension-list/bullet-list'
import { ListItem } from '@tiptap/extension-list/item'
import { OrderedList } from '@tiptap/extension-list/ordered-list'
import Paragraph from '@tiptap/extension-paragraph'
import Text from '@tiptap/extension-text'
import Underline from '@tiptap/extension-underline'
import { UndoRedo } from '@tiptap/extensions/undo-redo'
import { Plugin, PluginKey, TextSelection } from '@tiptap/pm/state'

import { stripForbiddenChars } from './chars'
import { cleanHref } from './href'
import { profileHas, type Profile } from './profiles'
import { RichTextError, sanitizePaste } from './sanitize'

export { stripForbiddenChars }

/**
 * Everything pasted or dropped is cut down to the white list of the profile
 * before ProseMirror reads it: Word, Google Docs and web pages lose their
 * style and class attributes, spans, divs, images and tables. A markup too
 * large or nested too deeply is not dropped silently: the plain text of the
 * clipboard goes in instead and the host is told (onFallback).
 */
const PasteCleanup = Extension.create<{ profile: Profile; onFallback: () => void }>({
  name: 'arenaPasteCleanup',
  addOptions() {
    return { profile: undefined as unknown as Profile, onFallback: () => undefined }
  },
  addProseMirrorPlugins() {
    const { profile, onFallback } = this.options
    // The DOM handler below cleans the markup first; the transform reuses its answer.
    let memo: { html: string; out: string } | null = null
    const clean = (html: string): string => {
      if (memo !== null && memo.html === html) return memo.out
      const out = sanitizePaste(html, profile)
      memo = { html, out }
      return out
    }
    /** True when the markup of the data cannot be read (RichTextError): the plain text is used. */
    const needsFallback = (html: string): boolean => {
      try {
        clean(html)
        return false
      } catch (e) {
        if (e instanceof RichTextError) return true
        throw e
      }
    }
    return [
      new Plugin({
        key: new PluginKey('arenaPasteCleanup'),
        props: {
          transformPastedHTML: (html) => {
            try {
              return clean(html)
            } catch (e) {
              if (e instanceof RichTextError) return '' // a drop of an unreadable markup
              throw e
            }
          },
          transformPastedText: (text) => stripForbiddenChars(text),
          handleDOMEvents: {
            paste: (view, event) => {
              const data = (event as ClipboardEvent).clipboardData
              if (!view.editable || !data) return false
              const html = data.getData('text/html')
              if (html === '' || !needsFallback(html)) return false
              event.preventDefault()
              view.pasteText(data.getData('text/plain'), event as ClipboardEvent)
              onFallback()
              return true
            },
            drop: (view, event) => {
              const data = (event as DragEvent).dataTransfer
              if (!view.editable || !data || view.dragging) return false // a drag inside the editor is our own markup
              const html = data.getData('text/html')
              if (html === '' || !needsFallback(html)) return false
              event.preventDefault()
              const at = view.posAtCoords({ left: (event as DragEvent).clientX, top: (event as DragEvent).clientY })
              if (at) {
                view.dispatch(view.state.tr.setSelection(TextSelection.near(view.state.doc.resolve(at.pos))))
              }
              view.pasteText(data.getData('text/plain'), event as unknown as ClipboardEvent) // the event only tells ProseMirror where it came from
              onFallback()
              return true
            },
          },
        },
      }),
    ]
  },
})

/** The link shortcut (Mod-k) belongs to the toolbar's dialog, the host passes the callback. */
const LinkShortcut = Extension.create<{ onLink: () => void }>({
  name: 'arenaLinkShortcut',
  addOptions() {
    return { onLink: () => undefined }
  },
  addKeyboardShortcuts() {
    return {
      'Mod-k': () => {
        this.options.onLink()
        return true
      },
    }
  },
})

/** The nesting depth of lists around the selection. */
function listDepth(editor: { state: { selection: { $from: { depth: number; node: (d: number) => { type: { name: string } } } } } }): number {
  const { $from } = editor.state.selection
  let depth = 0
  for (let d = $from.depth; d > 0; d--) {
    const name = $from.node(d).type.name
    if (name === 'bulletList' || name === 'orderedList') depth++
  }
  return depth
}

/**
 * A list item holds a paragraph and any number of nested lists, as in the Go
 * model (item.subs), and lists nest two levels at most (the server flattens
 * deeper ones).
 */
const ArenaListItem = ListItem.extend({
  content: 'paragraph (bulletList | orderedList)*',
  addKeyboardShortcuts() {
    return {
      ...this.parent?.(),
      Tab: () => {
        if (listDepth(this.editor) >= 2) return false // the browser moves the focus on
        return this.editor.commands.sinkListItem(this.name)
      },
    }
  },
})

/** A quote holds paragraphs only. */
const ArenaBlockquote = Blockquote.extend({
  content: 'paragraph+',
})

declare module '@tiptap/core' {
  interface Commands<ReturnType> {
    link: {
      setLink: (attributes: { href: string }) => ReturnType
      unsetLink: () => ReturnType
    }
  }
}

/**
 * The link mark: an href and nothing else (no target, rel, class or title), no
 * autolink, no linkified paste, no click handler. @tiptap/extension-link is not
 * used because it bundles the linkify library for those features. The address
 * is checked with the Go rules (cleanHref) when the HTML is read and when a
 * link is set, so no other address gets into the document.
 */
const ArenaLink = Mark.create<{ isAllowedUri: (url: string) => boolean }>({
  name: 'link',
  priority: 1000,
  keepOnSplit: false,
  exitable: true,
  inclusive: false,
  addOptions() {
    return { isAllowedUri: () => false }
  },
  addAttributes() {
    return {
      href: {
        default: null,
        parseHTML: (element) => element.getAttribute('href'),
      },
    }
  },
  parseHTML() {
    return [
      {
        tag: 'a[href]',
        getAttrs: (dom) => {
          const href = (dom as HTMLElement).getAttribute('href')
          return href && this.options.isAllowedUri(href) ? null : false
        },
      },
    ]
  },
  renderHTML({ HTMLAttributes }) {
    const href = typeof HTMLAttributes.href === 'string' && this.options.isAllowedUri(HTMLAttributes.href) ? HTMLAttributes.href : ''
    return ['a', mergeAttributes({ href }), 0]
  },
  addCommands() {
    return {
      setLink:
        (attributes) =>
        ({ chain }) =>
          this.options.isAllowedUri(attributes.href) ? chain().setMark(this.name, { href: attributes.href }).run() : false,
      unsetLink:
        () =>
        ({ chain }) =>
          chain().unsetMark(this.name, { extendEmptyMarkRange: true }).run(),
    }
  },
})

export interface ExtensionHooks {
  onLink: () => void
  /** A pasted or dropped markup could not be read: its plain text went in instead. */
  onPasteFallback: () => void
}

export function buildExtensions(profile: Profile, hooks: ExtensionHooks): AnyExtension[] {
  const exts: AnyExtension[] = [
    Document,
    Paragraph,
    Text,
    Bold,
    Italic,
    Underline,
    HardBreak,
    UndoRedo,
    BulletList,
    OrderedList,
    ArenaListItem,
    ArenaLink.configure({ isAllowedUri: (url) => cleanHref(url, profile) !== null }),
    PasteCleanup.configure({ profile, onFallback: hooks.onPasteFallback }),
    LinkShortcut.configure({ onLink: hooks.onLink }),
  ]
  if (profileHas(profile, 'h3')) exts.push(Heading.configure({ levels: [3, 4] }))
  if (profileHas(profile, 'blockquote')) exts.push(ArenaBlockquote)
  return exts
}
