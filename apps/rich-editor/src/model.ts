// The internal model of a text: blocks of inline chunks, and the canonical
// serialisation (Go: doc.go). The sanitizer builds it from foreign HTML and
// the editor reads its HTML and counters from it, so the output is exactly
// the ART the Go sanitizer accepts unchanged.

import { isWS, runeCount } from './chars'

/** A run of text with its marks, or a line break. */
export interface Chunk {
  text: string
  br: boolean
  strong: boolean
  em: boolean
  u: boolean
  /** Canonical link target, "" for no link. */
  href: string
  /** Identifies the <a> element a chunk came from. */
  linkID: number
}

export interface Item {
  inl: Chunk[]
  subs: List[]
}

export interface List {
  ordered: boolean
  items: Item[]
}

export type Block =
  | { kind: 'p' | 'h3' | 'h4'; inl: Chunk[] }
  | { kind: 'quote'; paras: Chunk[][] }
  | { kind: 'list'; list: List }

export interface Doc {
  blocks: Block[]
}

export function textChunk(text: string, m: { strong: boolean; em: boolean; u: boolean; href: string; linkID: number }): Chunk {
  return { text, br: false, strong: m.strong, em: m.em, u: m.u, href: m.href, linkID: m.linkID }
}

export function brChunk(): Chunk {
  return { text: '', br: true, strong: false, em: false, u: false, href: '', linkID: 0 }
}

function isSpaceText(s: string): boolean {
  for (const ch of s) {
    if (!isWS(ch.codePointAt(0) as number)) return false
  }
  return true
}

function sameMarks(a: Chunk, b: Chunk): boolean {
  return a.strong === b.strong && a.em === b.em && a.u === b.u && a.href === b.href
}

function trimRightSpaces(s: string): string {
  let end = s.length
  while (end > 0 && s.charCodeAt(end - 1) === 0x20) end--
  return end === s.length ? s : s.slice(0, end)
}

/**
 * Normalises the chunks of one paragraph, heading or item: a link that holds
 * only spaces is unwrapped, runs of spaces collapse into one, spaces at the
 * borders and around line breaks go, and neighbours with the same marks merge.
 * A result with no text and no break is null.
 */
export function finish(input: Chunk[]): Chunk[] | null {
  const linkText = new Set<number>()
  for (const c of input) {
    if (c.href !== '' && !c.br && !isSpaceText(c.text)) linkText.add(c.linkID)
  }
  const out: Chunk[] = []
  const bufs: string[] = []
  let lastSpace = true // at the start and after a break a space is dropped
  const trimTail = () => {
    while (out.length > 0) {
      const n = out.length - 1
      if (out[n].br) return
      bufs[n] = trimRightSpaces(bufs[n])
      if (bufs[n].length > 0) return
      out.pop()
      bufs.pop()
    }
  }
  for (const src of input) {
    if (src.br) {
      trimTail()
      if (out.length > 0) {
        // a break at the start is dropped
        out.push(brChunk())
        bufs.push('')
      }
      lastSpace = true
      continue
    }
    let c = src
    if (c.href !== '' && !linkText.has(c.linkID)) c = { ...c, href: '', linkID: 0 }
    const n = out.length
    const merge = n > 0 && !out[n - 1].br && sameMarks(out[n - 1], c)
    let text = merge ? bufs[n - 1] : ''
    const before = text.length
    const parts: string[] = []
    for (const ch of c.text) {
      if (isWS(ch.codePointAt(0) as number)) {
        if (lastSpace) continue
        parts.push(' ')
        lastSpace = true
        continue
      }
      parts.push(ch)
      lastSpace = false
    }
    text += parts.join('')
    if (text.length === before) continue
    if (merge) {
      bufs[n - 1] = text
      continue
    }
    out.push({ ...c, text: '' })
    bufs.push(text)
  }
  trimTail()
  while (out.length > 0 && out[out.length - 1].br) {
    out.pop()
    bufs.pop()
  }
  if (out.length === 0) return null
  for (let i = 0; i < out.length; i++) {
    if (!out[i].br) out[i].text = bufs[i]
  }
  return out
}

// ---- serialisation ----

export function escapeText(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

function escapeAttr(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
}

interface Mark {
  kind: 'a' | 's' | 'e' | 'u'
  href: string
}

function marksOf(c: Chunk): Mark[] {
  const m: Mark[] = []
  if (c.href !== '') m.push({ kind: 'a', href: c.href })
  if (c.strong) m.push({ kind: 's', href: '' })
  if (c.em) m.push({ kind: 'e', href: '' })
  if (c.u) m.push({ kind: 'u', href: '' })
  return m
}

/** Writes chunks with the canonical nesting: link, strong, em, underline, outermost first. */
function writeInline(out: string[], chunks: Chunk[], links: { n: number }): void {
  const open: Mark[] = []
  const closeTo = (n: number) => {
    while (open.length > n) {
      const m = open.pop() as Mark
      out.push(m.kind === 'a' ? '</a>' : m.kind === 's' ? '</strong>' : m.kind === 'e' ? '</em>' : '</u>')
    }
  }
  for (const c of chunks) {
    if (c.br) {
      closeTo(0)
      out.push('<br>')
      continue
    }
    const want = marksOf(c)
    let n = 0
    while (n < open.length && n < want.length && open[n].kind === want[n].kind && open[n].href === want[n].href) n++
    closeTo(n)
    for (const m of want.slice(n)) {
      if (m.kind === 'a') {
        out.push('<a href="' + escapeAttr(m.href) + '">')
        links.n++
      } else {
        out.push(m.kind === 's' ? '<strong>' : m.kind === 'e' ? '<em>' : '<u>')
      }
      open.push(m)
    }
    out.push(escapeText(c.text))
  }
  closeTo(0)
}

export function itemEmpty(it: Item): boolean {
  if (it.inl.length > 0) return false
  return it.subs.every(listEmpty)
}

export function listEmpty(l: List): boolean {
  return l.items.every(itemEmpty)
}

function writeList(out: string[], l: List, links: { n: number }): void {
  if (listEmpty(l)) return
  const tag = l.ordered ? 'ol' : 'ul'
  out.push('<' + tag + '>')
  for (const it of l.items) {
    if (itemEmpty(it)) continue
    out.push('<li>')
    writeInline(out, it.inl, links)
    for (const s of it.subs) writeList(out, s, links)
    out.push('</li>')
  }
  out.push('</' + tag + '>')
}

/** Serialises the document and counts its links. */
export function docHtml(d: Doc): { html: string; links: number } {
  const out: string[] = []
  const links = { n: 0 }
  for (const b of d.blocks) {
    switch (b.kind) {
      case 'p':
        if (b.inl.length === 0) continue
        out.push('<p>')
        writeInline(out, b.inl, links)
        out.push('</p>')
        break
      case 'h3':
      case 'h4':
        if (b.inl.length === 0) continue
        out.push('<' + b.kind + '>')
        writeInline(out, b.inl, links)
        out.push('</' + b.kind + '>')
        break
      case 'quote':
        if (b.paras.length === 0) continue
        out.push('<blockquote>')
        for (const p of b.paras) {
          out.push('<p>')
          writeInline(out, p, links)
          out.push('</p>')
        }
        out.push('</blockquote>')
        break
      case 'list':
        writeList(out, b.list, links)
        break
    }
  }
  return { html: out.join(''), links: links.n }
}

/** Calls fn for every line-like unit in reading order: paragraphs, headings, quote paragraphs and list items. */
export function units(d: Doc, fn: (chunks: Chunk[]) => void): void {
  const walkList = (l: List) => {
    for (const it of l.items) {
      if (itemEmpty(it)) continue
      if (it.inl.length > 0) fn(it.inl)
      for (const s of it.subs) walkList(s)
    }
  }
  for (const b of d.blocks) {
    switch (b.kind) {
      case 'p':
      case 'h3':
      case 'h4':
        if (b.inl.length > 0) fn(b.inl)
        break
      case 'quote':
        for (const p of b.paras) fn(p)
        break
      case 'list':
        walkList(b.list)
        break
    }
  }
}

function chunksText(chunks: Chunk[]): string {
  return chunks.map((c) => (c.br ? '\n' : c.text)).join('')
}

/** The visible characters: the text of every unit and one character for every break between units or lines. */
export function visibleLen(d: Doc): number {
  let n = 0
  let count = 0
  units(d, (chunks) => {
    count++
    n += runeCount(chunksText(chunks))
  })
  if (count > 1) n += count - 1
  return n
}

/** The text a reader sees: units separated by a blank line, list items by a line break. */
export function plainText(d: Doc): string {
  const parts: string[] = []
  const walkList = (l: List, lines: string[], depth: number) => {
    let n = 0
    for (const it of l.items) {
      if (itemEmpty(it)) continue
      if (it.inl.length > 0) {
        n++
        const marker = l.ordered ? n + '. ' : '- '
        lines.push('  '.repeat(depth) + marker + chunksText(it.inl).replace(/\n/g, ' '))
      }
      for (const s of it.subs) walkList(s, lines, depth + 1)
    }
  }
  for (const b of d.blocks) {
    switch (b.kind) {
      case 'p':
      case 'h3':
      case 'h4':
        if (b.inl.length > 0) parts.push(chunksText(b.inl))
        break
      case 'quote':
        for (const p of b.paras) parts.push(chunksText(p))
        break
      case 'list': {
        const lines: string[] = []
        walkList(b.list, lines, 0)
        if (lines.length > 0) parts.push(lines.join('\n'))
        break
      }
    }
  }
  return parts.join('\n\n')
}
