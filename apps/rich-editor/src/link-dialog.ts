// The link dialog: an inline panel (no modal, no inline style) that checks the
// address against the profile the way href.go does before the link is set.

import type { Editor } from '@tiptap/core'

import { format, type Messages } from './i18n'
import { parseLinkInput } from './href'
import { profileAllowsScheme, type Profile } from './profiles'

export interface LinkDialogOptions {
  editor: Editor
  profile: Profile
  messages: Messages
  idPrefix: string
  /** The interface language, set on the dialog (the editing text has its own). */
  locale: string
  /** How many links the text has now. */
  linkCount: () => number
  /** Called after the dialog closed, so the host can refresh the toolbar. */
  onClose: () => void
}

export interface LinkDialog {
  el: HTMLElement
  open: () => void
  close: (restoreFocus?: boolean) => void
  isOpen: () => boolean
}

export function createLinkDialog(o: LinkDialogOptions): LinkDialog {
  const { editor, profile, messages: t } = o
  const mailto = profileAllowsScheme(profile, 'mailto')
  const el = document.createElement('div')
  el.className = 'arena-re__link'
  el.setAttribute('lang', o.locale)
  el.setAttribute('role', 'dialog')
  el.setAttribute('aria-labelledby', o.idPrefix + '-link-title')
  el.hidden = true

  const form = document.createElement('form')
  form.className = 'arena-re__link-form'
  form.noValidate = true

  const title = document.createElement('div')
  title.className = 'arena-re__link-title'
  title.id = o.idPrefix + '-link-title'
  title.textContent = t.linkDialogTitle

  const field = (id: string, labelText: string, hint?: string) => {
    const row = document.createElement('div')
    row.className = 'arena-re__field'
    const label = document.createElement('label')
    label.htmlFor = id
    label.textContent = labelText
    const input = document.createElement('input')
    input.type = 'text'
    input.id = id
    input.autocomplete = 'off'
    input.spellcheck = false
    input.setAttribute('autocapitalize', 'off')
    input.setAttribute('dir', 'ltr')
    row.append(label, input)
    if (hint) {
      const h = document.createElement('div')
      h.className = 'arena-re__hint'
      h.id = id + '-hint'
      h.textContent = hint
      row.appendChild(h)
      input.setAttribute('aria-describedby', h.id)
    }
    return { row, input }
  }

  const address = field(o.idPrefix + '-link-href', t.linkAddress, mailto ? t.linkHintMailto : t.linkHintHttps)
  address.input.inputMode = 'url'
  address.input.placeholder = 'https://'
  const text = field(o.idPrefix + '-link-text', t.linkText)

  const error = document.createElement('div')
  error.className = 'arena-re__error'
  error.id = o.idPrefix + '-link-error'
  error.setAttribute('role', 'alert')

  const actions = document.createElement('div')
  actions.className = 'arena-re__link-actions'
  const apply = document.createElement('button')
  apply.type = 'submit'
  apply.className = 'arena-re__btn arena-re__btn--primary'
  apply.textContent = t.linkApply
  const cancel = document.createElement('button')
  cancel.type = 'button'
  cancel.className = 'arena-re__btn'
  cancel.textContent = t.linkCancel
  actions.append(apply, cancel)

  form.append(title, address.row, text.row, error, actions)
  el.appendChild(form)

  let editing = false

  const showError = (message: string) => {
    error.textContent = message
    address.input.setAttribute('aria-invalid', 'true')
    address.input.setAttribute('aria-describedby', address.input.id + '-hint ' + error.id)
    address.input.focus()
  }
  const clearError = () => {
    error.textContent = ''
    address.input.removeAttribute('aria-invalid')
    address.input.setAttribute('aria-describedby', address.input.id + '-hint')
  }

  function close(restoreFocus = true): void {
    if (el.hidden) return
    el.hidden = true
    clearError()
    if (restoreFocus) editor.commands.focus()
    o.onClose()
  }

  function open(): void {
    if (!editor.isEditable) return // a read-only text takes no link
    const existing = editor.isActive('link') ? (editor.getAttributes('link').href as string | undefined) : undefined
    editing = !!existing
    const empty = editor.state.selection.empty
    address.input.value = existing ?? ''
    text.input.value = ''
    // A cursor with no text selected needs the words of the link.
    text.row.hidden = !(empty && !editing)
    clearError()
    el.hidden = false
    address.input.focus()
    address.input.select()
  }

  form.addEventListener('submit', (ev) => {
    ev.preventDefault()
    if (!editor.isEditable) {
      close(false)
      return
    }
    const href = parseLinkInput(address.input.value, profile)
    if (href === null) {
      showError(mailto ? t.linkInvalidMailto : t.linkInvalidHttps)
      return
    }
    if (!editing && o.linkCount() >= profile.maxLinks) {
      showError(format(t.linkTooMany, { max: profile.maxLinks }))
      return
    }
    const chain = editor.chain().focus()
    if (editing) {
      chain.extendMarkRange('link').setLink({ href }).run()
    } else if (!editor.state.selection.empty) {
      chain.setLink({ href }).run()
    } else {
      const words = text.input.value.trim() || href.replace(/^(https:\/\/|mailto:)/, '')
      chain.insertContent({ type: 'text', text: words, marks: [{ type: 'link', attrs: { href } }] }).run()
    }
    close(false)
    editor.commands.focus()
  })
  address.input.addEventListener('input', clearError)
  cancel.addEventListener('click', () => close(true))
  el.addEventListener('keydown', (ev) => {
    if (ev.key === 'Escape') {
      ev.preventDefault()
      ev.stopPropagation()
      close(true)
    }
  })

  return { el, open, close, isOpen: () => !el.hidden }
}
