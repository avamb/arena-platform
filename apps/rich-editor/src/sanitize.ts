// The sanitizer: foreign HTML (a paste from Word, Google Docs or a web page,
// the answer of the formatting API, a stored text) to ART for a profile. It
// is a port of Sanitize in apps/backend/internal/platform/richtext/sanitize.go
// on top of the browser's DOM parser, which never runs scripts or loads
// anything. Limits are not enforced here (the counter shows them, the API is
// the final judge); forbidden characters are dropped instead of refused.

import { CharAction, charPolicy } from './chars'
import { cleanHref } from './href'
import {
  brChunk,
  docHtml,
  finish,
  textChunk,
  visibleLen,
  type Block,
  type Chunk,
  type Doc,
  type Item,
  type List,
} from './model'
import { MAX_DEPTH, MAX_INPUT_BYTES, profileHas, type Profile } from './profiles'

export type RichTextErrorReason = 'too_large' | 'too_deep'

export class RichTextError extends Error {
  readonly reason: RichTextErrorReason
  constructor(reason: RichTextErrorReason) {
    super(reason === 'too_large' ? 'richtext: the input is too large' : 'richtext: the markup is nested too deeply')
    this.name = 'RichTextError'
    this.reason = reason
  }
}

/** Elements removed together with their content. */
const DROP_TAGS = new Set([
  'script', 'style', 'img', 'iframe', 'svg', 'object', 'embed', 'noscript', 'template', 'title', 'head',
  'meta', 'link', 'base', 'audio', 'video', 'canvas', 'math', 'applet', 'frame', 'frameset', 'noframes',
  'noembed', 'param', 'source', 'track', 'map', 'area',
])

/** Block tags that end and start a paragraph; their text is kept. */
const BOUNDARY_TAGS = new Set([
  'div', 'section', 'article', 'aside', 'header', 'footer', 'nav', 'main', 'figure', 'figcaption', 'address',
  'table', 'thead', 'tbody', 'tfoot', 'tr', 'td', 'th', 'caption', 'dl', 'dt', 'dd', 'pre', 'hr', 'fieldset',
  'form', 'details', 'summary', 'center', 'menu', 'dir', 'li', 'ul', 'ol', 'blockquote', 'p', 'h1', 'h2', 'h3',
  'h4', 'h5', 'h6', 'body', 'html', 'legend',
])

type ContainerKind = 'root' | 'quote' | 'list' | 'item'

interface Container {
  kind: ContainerKind
  quote?: { paras: Chunk[][] }
  list?: List
  item?: Item
  /** A flattened list that adds its items to the parent list. */
  alias?: boolean
}

interface Marks {
  strong: boolean
  em: boolean
  u: boolean
  href: string
  linkID: number
}

type BlockKind = 'p' | 'h3' | 'h4'

export interface SanitizeOptions {
  /**
   * For pasted text: read the bold, italic and underline that Word, Google
   * Docs and web pages express as inline style (font-weight, font-style,
   * text-decoration) before the style attributes are dropped, and do not let
   * a <b style="font-weight:normal"> wrapper (Google Docs) make a paste bold.
   * Stored text (setHTML, getHTML) is read exactly as the Go sanitizer does.
   */
  foreignStyles?: boolean
}

/** Where the style attribute text is kept while the HTML is parsed (see renameStyleAttributes). */
const STYLE_ATTR = 'data-arena-style'

/** The declarations of a style attribute, read from the attribute text (no CSSOM: it also works under a strict CSP). */
function styleDeclarations(el: Element): Map<string, string> {
  const out = new Map<string, string>()
  const raw = el.getAttribute(STYLE_ATTR)
  if (!raw) return out
  for (const part of raw.split(';')) {
    const i = part.indexOf(':')
    if (i <= 0) continue
    out.set(part.slice(0, i).trim().toLowerCase(), part.slice(i + 1).replace(/!important/i, '').trim().toLowerCase())
  }
  return out
}

/** true: bold, false: normal, null: the style says nothing. */
function weightOf(v: string | undefined): boolean | null {
  if (v === undefined || v === '') return null
  if (v === 'bold' || v === 'bolder') return true
  if (v === 'normal' || v === 'lighter') return false
  const n = Number(v)
  return Number.isFinite(n) ? n >= 600 : null
}

/** The marks an element's inline style sets or clears. */
function styleMarks(el: Element, m: Marks): Marks {
  const decl = styleDeclarations(el)
  if (decl.size === 0) return m
  let out = m
  const bold = weightOf(decl.get('font-weight'))
  if (bold !== null && bold !== m.strong) out = { ...out, strong: bold }
  const fs = decl.get('font-style')
  if (fs === 'italic' || fs === 'oblique') out = { ...out, em: true }
  else if (fs === 'normal') out = { ...out, em: false }
  const deco = decl.get('text-decoration-line') ?? decl.get('text-decoration')
  // Links are underlined by the page, not by the author: no <u> inside a link.
  if (deco !== undefined && el.localName !== 'a' && out.href === '') {
    if (/\bunderline\b/.test(deco)) out = { ...out, u: true }
    else if (/\bnone\b/.test(deco)) out = { ...out, u: false }
  }
  return out
}

class Converter {
  readonly root: Block[] = []
  readonly stack: Container[] = [{ kind: 'root' }]
  cur: Chunk[] = []
  curKind: BlockKind = 'p'
  nextLink = 0

  constructor(
    private readonly p: Profile,
    private readonly foreign: boolean,
  ) {}

  private top(): Container {
    return this.stack[this.stack.length - 1]
  }

  private quoteDepth(): number {
    return this.stack.filter((k) => k.kind === 'quote').length
  }

  private listDepth(): number {
    return this.stack.filter((k) => k.kind === 'list' && !k.alias).length
  }

  private text(s: string, m: Marks): void {
    if (s === '') return
    const out: string[] = []
    for (const ch of s) {
      const action = charPolicy(ch.codePointAt(0) as number)
      if (action === CharAction.Refuse || action === CharAction.Strip) continue
      out.push(ch)
    }
    this.cur.push(textChunk(out.join(''), m))
  }

  /** Closes the pending run into the current container. */
  flush(): void {
    const chunks = finish(this.cur)
    const kind = this.curKind
    this.cur = []
    this.curKind = 'p'
    if (!chunks || chunks.length === 0) return
    const top = this.top()
    switch (top.kind) {
      case 'root':
        this.root.push({ kind, inl: chunks })
        break
      case 'quote':
        ;(top.quote as { paras: Chunk[][] }).paras.push(chunks)
        break
      case 'list':
        // Text between <ul> and <li>: it becomes an item of its own.
        ;(top.list as List).items.push({ inl: chunks, subs: [] })
        break
      case 'item': {
        const item = top.item as Item
        if (item.inl.length > 0) item.inl.push(brChunk())
        item.inl.push(...chunks)
        break
      }
    }
  }

  private openQuote(): boolean {
    if (!profileHas(this.p, 'blockquote') || this.top().kind !== 'root') return false
    const quote = { kind: 'quote' as const, paras: [] as Chunk[][] }
    this.root.push(quote)
    this.stack.push({ kind: 'quote', quote })
    return true
  }

  private enclosingList(): List {
    for (let i = this.stack.length - 1; i >= 0; i--) {
      if (this.stack[i].kind === 'list') return this.stack[i].list as List
    }
    return { ordered: false, items: [] }
  }

  private openList(ordered: boolean): boolean {
    if (!profileHas(this.p, 'ul') || this.quoteDepth() > 0) return false
    const top = this.top()
    switch (top.kind) {
      case 'root': {
        const l: List = { ordered, items: [] }
        this.root.push({ kind: 'list', list: l })
        this.stack.push({ kind: 'list', list: l })
        break
      }
      case 'item': {
        if (this.listDepth() >= 2) {
          // Too deep: its items join the enclosing list.
          this.stack.push({ kind: 'list', list: this.enclosingList(), alias: true })
          return true
        }
        const l: List = { ordered, items: [] }
        ;(top.item as Item).subs.push(l)
        this.stack.push({ kind: 'list', list: l })
        break
      }
      case 'list': {
        // A list directly in a list belongs to the item before it.
        const tl = top.list as List
        const n = tl.items.length
        if (n > 0 && this.listDepth() < 2) {
          const l: List = { ordered, items: [] }
          tl.items[n - 1].subs.push(l)
          this.stack.push({ kind: 'list', list: l })
          return true
        }
        this.stack.push({ kind: 'list', list: tl, alias: true })
        break
      }
      default:
        return false
    }
    return true
  }

  private styleSays(el: Element, prop: string, bold: boolean): boolean {
    return prop === 'font-weight' && weightOf(styleDeclarations(el).get(prop)) === bold
  }

  private linkTarget(el: Element): string | null {
    const raw = el.getAttribute('href')
    if (raw === null) return null
    return cleanHref(raw, this.p)
  }

  walk(n: Node, m: Marks, depth: number): void {
    if (depth > MAX_DEPTH) throw new RichTextError('too_deep')
    if (n.nodeType === 3) {
      this.text((n as Text).data, m)
      return
    }
    if (n.nodeType !== 1) return // comments, processing instructions
    const el = n as Element
    const tag = el.localName.toLowerCase()
    if (DROP_TAGS.has(tag)) return
    if (this.foreign) m = styleMarks(el, m)
    const children = (mm: Marks) => {
      for (let ch = el.firstChild; ch; ch = ch.nextSibling) this.walk(ch, mm, depth + 1)
    }
    switch (tag) {
      case 'br':
        this.cur.push(brChunk())
        return
      case 'b':
      case 'strong':
        // <b style="font-weight:normal"> is the wrapper Google Docs puts around a paste.
        children(profileHas(this.p, 'strong') && !(this.foreign && this.styleSays(el, 'font-weight', false)) ? { ...m, strong: true } : m)
        return
      case 'i':
      case 'em':
        children(profileHas(this.p, 'em') && !(this.foreign && styleDeclarations(el).get('font-style') === 'normal') ? { ...m, em: true } : m)
        return
      case 'u':
        children(profileHas(this.p, 'u') ? { ...m, u: true } : m)
        return
      case 'a': {
        let mm = m
        if (profileHas(this.p, 'a') && m.href === '') {
          const href = this.linkTarget(el)
          if (href !== null) {
            this.nextLink++
            mm = { ...m, href, linkID: this.nextLink }
          }
        }
        children(mm)
        return
      }
      case 'h1':
      case 'h2':
      case 'h3':
      case 'h4':
      case 'h5':
      case 'h6': {
        this.flush()
        const level: BlockKind = tag === 'h4' || tag === 'h5' || tag === 'h6' ? 'h4' : 'h3'
        let mm = m
        if (profileHas(this.p, 'h3') && this.top().kind === 'root') {
          this.curKind = level
        } else if (profileHas(this.p, 'strong')) {
          mm = { ...m, strong: true } // a heading outside the description is a bold line
        }
        children(mm)
        this.flush()
        return
      }
      case 'blockquote': {
        this.flush()
        const pushed = this.openQuote()
        children(m)
        this.flush()
        if (pushed) this.stack.pop()
        return
      }
      case 'ul':
      case 'ol': {
        this.flush()
        const pushed = this.openList(tag === 'ol')
        children(m)
        this.flush()
        if (pushed) this.stack.pop()
        return
      }
      case 'li': {
        this.flush()
        if (this.top().kind === 'list') {
          const item: Item = { inl: [], subs: [] }
          ;(this.top().list as List).items.push(item)
          this.stack.push({ kind: 'item', item })
          children(m)
          this.flush()
          this.stack.pop()
          return
        }
        // An <li> outside a list is a paragraph.
        children(m)
        this.flush()
        return
      }
      default:
        if (BOUNDARY_TAGS.has(tag)) {
          this.flush()
          children(m)
          this.flush()
          return
        }
        children(m) // any other tag is unwrapped
    }
  }
}

const isTagNameStart = (c: string | undefined): boolean => c !== undefined && /[A-Za-z]/.test(c)
const isAttrSpace = (c: string): boolean => c === ' ' || c === String.fromCharCode(9) || c === String.fromCharCode(10) || c === String.fromCharCode(12) || c === String.fromCharCode(13)

/**
 * Renames every style attribute of a start tag to data-arena-style before the
 * browser parses the string. Under a strict CSP (style-src without
 * unsafe-inline) the parser reports "Applying inline style violates ..." for
 * each style attribute of ANY parsed string, even in an inert template, and
 * that would flood the console on every Word or Google Docs paste. The text of
 * the attribute stays readable for the paste path (styleDeclarations); it
 * never becomes a style. Only start tags are touched: comments, end tags and
 * text are copied as they are.
 */
export function renameStyleAttributes(html: string): string {
  if (!/style/i.test(html)) return html
  const n = html.length
  const out: string[] = []
  let i = 0
  while (i < n) {
    const lt = html.indexOf('<', i)
    if (lt < 0) {
      out.push(html.slice(i))
      break
    }
    out.push(html.slice(i, lt))
    if (html.startsWith('<!--', lt)) {
      const end = html.indexOf('-->', lt + 4)
      const stop = end < 0 ? n : end + 3
      out.push(html.slice(lt, stop))
      i = stop
      continue
    }
    if (!isTagNameStart(html[lt + 1])) {
      out.push('<')
      i = lt + 1
      continue
    }
    // A start tag: its name, then attributes until the closing >.
    let j = lt + 1
    while (j < n && !isAttrSpace(html[j]) && html[j] !== '/' && html[j] !== '>') j++
    out.push(html.slice(lt, j))
    while (j < n && html[j] !== '>') {
      if (isAttrSpace(html[j]) || html[j] === '/') {
        out.push(html[j])
        j++
        continue
      }
      // attribute name
      const nameStart = j
      j++ // a name may start with any character, = included
      while (j < n && !isAttrSpace(html[j]) && html[j] !== '/' && html[j] !== '>' && html[j] !== '=') j++
      const name = html.slice(nameStart, j)
      out.push(name.toLowerCase() === 'style' ? STYLE_ATTR : name)
      // optional = and value
      let k = j
      while (k < n && isAttrSpace(html[k])) k++
      if (html[k] !== '=') continue
      k++
      while (k < n && isAttrSpace(html[k])) k++
      let valueEnd = k
      const q = html[k]
      if (q === '"' || q === "'") {
        const close = html.indexOf(q, k + 1)
        valueEnd = close < 0 ? n : close + 1
      } else {
        while (valueEnd < n && !isAttrSpace(html[valueEnd]) && html[valueEnd] !== '>') valueEnd++
      }
      out.push(html.slice(j, valueEnd))
      j = valueEnd
    }
    i = j
  }
  return out.join('')
}

const NO_MARKS: Marks = { strong: false, em: false, u: false, href: '', linkID: 0 }

/** Reads HTML into the document model for the profile. Throws RichTextError for a hostile input. */
export function parseHtml(input: string, p: Profile, o: SanitizeOptions = {}): Doc {
  if (input.length > MAX_INPUT_BYTES) throw new RichTextError('too_large')
  // A <template> is parsed into an inert fragment: nothing loads and no script runs, and (unlike a
  // DOMParser document, which belongs to the page) it raises no Content-Security-Policy violation for the
  // style attributes of a Word or Google Docs paste. It has no implied <head> either, so a leading
  // <noscript> or <style> cannot leak its text.
  const template = document.createElement('template')
  template.innerHTML = renameStyleAttributes(input)
  const c = new Converter(p, o.foreignStyles === true)
  for (let n = template.content.firstChild; n; n = n.nextSibling) c.walk(n, NO_MARKS, 0)
  c.flush()
  return { blocks: c.root }
}

/** Reduces any HTML to ART for the profile: the canonical form, "" for no text. Idempotent. */
export function sanitize(input: string, p: Profile, o: SanitizeOptions = {}): string {
  return docHtml(parseHtml(input, p, o)).html
}

export interface TextStats {
  /** Canonical ART. */
  html: string
  /** Visible characters, counted the way the API counts them. */
  visible: number
  links: number
  /** Characters of the canonical HTML. */
  htmlLength: number
}

/** Canonical ART and the counters of any HTML. */
export function measure(input: string, p: Profile, o: SanitizeOptions = {}): TextStats {
  const d = parseHtml(input, p, o)
  const { html, links } = docHtml(d)
  return { html, visible: visibleLen(d), links, htmlLength: Array.from(html).length }
}
