// The Tiptap extension set for a profile. StarterKit with code, code block,
// horizontal rule (and strike, drop cursor, gap cursor: none of them is in the
// white list) disabled, plus Underline and Link. Headings (h3, h4) and the
// quote exist only where the profile has them. No NodeViews anywhere.

import { Extension, type AnyExtension } from '@tiptap/core'
import Blockquote from '@tiptap/extension-blockquote'
import Link from '@tiptap/extension-link'
import ListItem from '@tiptap/extension-list-item'
import Underline from '@tiptap/extension-underline'
import { Plugin, PluginKey } from '@tiptap/pm/state'
import StarterKit from '@tiptap/starter-kit'

import { CharAction, charPolicy } from './chars'
import { cleanHref } from './href'
import { profileHas, type Profile } from './profiles'
import { sanitize } from './sanitize'

/** Removes the characters the character policy never lets into a text (line breaks and tabs stay). */
export function stripForbiddenChars(text: string): string {
  let out = ''
  for (const ch of text) {
    const action = charPolicy(ch.codePointAt(0) as number)
    if (action === CharAction.Refuse || action === CharAction.Strip) continue
    out += ch
  }
  return out
}

/**
 * Everything pasted or dropped is cut down to the white list of the profile
 * before ProseMirror reads it: Word, Google Docs and web pages lose their
 * style and class attributes, spans, divs, images and tables.
 */
const PasteCleanup = Extension.create<{ profile: Profile }>({
  name: 'arenaPasteCleanup',
  addOptions() {
    return { profile: undefined as unknown as Profile }
  },
  addProseMirrorPlugins() {
    const profile = this.options.profile
    return [
      new Plugin({
        key: new PluginKey('arenaPasteCleanup'),
        props: {
          transformPastedHTML: (html) => {
            try {
              return sanitize(html, profile, { foreignStyles: true })
            } catch {
              return '' // too large or nested too deeply: nothing is pasted
            }
          },
          transformPastedText: (text) => stripForbiddenChars(text),
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

/** A list item holds text and a nested list, and lists nest two levels at most (the server flattens deeper ones). */
const ArenaListItem = ListItem.extend({
  content: 'paragraph (bulletList | orderedList)?',
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

/** A link is made in the dialog only: no linkified paste, no autolink (a bare address stays text, as in the description profile). */
const ArenaLink = Link.extend({
  addPasteRules() {
    return []
  },
})

export interface ExtensionHooks {
  onLink: () => void
}

export function buildExtensions(profile: Profile, hooks: ExtensionHooks): AnyExtension[] {
  const exts: AnyExtension[] = [
    StarterKit.configure({
      blockquote: false,
      listItem: false,
      heading: profileHas(profile, 'h3') ? { levels: [3, 4] } : false,
      code: false,
      codeBlock: false,
      horizontalRule: false,
      strike: false,
      dropcursor: false,
      gapcursor: false,
    }),
    ArenaListItem,
    Underline,
    ArenaLink.configure({
      openOnClick: false,
      autolink: false,
      linkOnPaste: false,
      // The canonical ART link has an href and nothing else.
      HTMLAttributes: { target: null, rel: null, class: null },
      isAllowedUri: (url) => cleanHref(url, profile) !== null,
    }),
    PasteCleanup.configure({ profile }),
    LinkShortcut.configure({ onLink: hooks.onLink }),
  ]
  if (profileHas(profile, 'blockquote')) exts.push(ArenaBlockquote)
  return exts
}
