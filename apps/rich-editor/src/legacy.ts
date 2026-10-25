// FromLegacy: the old plain-text form of a text ("**bold**", "_italic_", "- " lists, a blank line between
// paragraphs) turned into ART. A line-for-line port of Go richtext/legacy.go: the corpus
// richtext/testdata/richtext_legacy.json (its `legacy` and `art` fields) is checked by tests/legacy.test.ts,
// so the web wizard shows exactly the text the server would store for the same plain text. It needs no DOM.

import { CharAction, charPolicy, isLoneSurrogate, isUnicodeSpace, isWS } from './chars'
import { cleanHref } from './href'
import { brChunk, docHtml, finish, textChunk, type Block, type Chunk, type Doc, type Item } from './model'
import type { Profile } from './profiles'

/** Cut off the end of an auto-linked address: it belongs to the sentence (Go: trailingPunct). */
const TRAILING_PUNCT = '.,;:!?)]}»”’'
const ITALIC_OPEN_AFTER = '([{"“‘\'«„'
const ITALIC_CLOSE_BEFORE = '.,;:!?)]}"”’\'»…'

/** Go's Profile.AutoLink: only the letter note turns bare https:// addresses into links. */
function autoLinks(p: Profile): boolean {
  return p.name === 'email_note' && p.tags.includes('a')
}

/** The text with the character policy applied the way Sanitize applies it (Go: legacyChars). */
function legacyChars(input: string): string {
  let out = ''
  for (const ch of input) {
    const cp = ch.codePointAt(0) as number
    if (isLoneSurrogate(cp)) continue // Go: strings.ToValidUTF8(text, "")
    switch (charPolicy(cp)) {
      case CharAction.Strip:
        break
      case CharAction.LineSep:
        out += '\n'
        break
      case CharAction.ParaSep:
        out += '\n\n'
        break
      default:
        out += ch
    }
  }
  return out
}

function trimWS(s: string): string {
  let start = 0
  let end = s.length
  while (start < end && isWS(s.codePointAt(start) as number)) start++
  while (end > start && isWS(s.charCodeAt(end - 1))) end--
  return s.slice(start, end)
}

function trimLeftWS(s: string): string {
  let start = 0
  while (start < s.length && isWS(s.codePointAt(start) as number)) start++
  return s.slice(start)
}

function startsWithWS(s: string): boolean {
  return s.length > 0 && isWS(s.codePointAt(0) as number)
}

type LineKind = 'text' | 'bullet' | 'number'

interface Classified {
  kind: LineKind
  body: string
  num: number
}

function classify(line: string): Classified {
  const first = line.codePointAt(0) as number
  const size = first > 0xffff ? 2 : 1
  if ((first === 0x2d || first === 0x2a || first === 0x2022) && size < line.length) {
    const rest = line.slice(size)
    if (startsWithWS(rest)) {
      const body = trimLeftWS(rest)
      if (body !== '') return { kind: 'bullet', body, num: 0 }
    }
  }
  let n = 0
  // A number with a leading zero ("01.") is text: the list would show "1.".
  while (n < line.length && n < 3 && line[0] !== '0' && line[n] >= '0' && line[n] <= '9') n++
  if (n > 0 && n + 1 < line.length && line[n] === '.' && startsWithWS(line.slice(n + 1))) {
    const body = trimLeftWS(line.slice(n + 1))
    if (body !== '') return { kind: 'number', body, num: Number(line.slice(0, n)) }
  }
  return { kind: 'text', body: line, num: 0 }
}

class Converter {
  budget = 0
  nextLink = 0

  constructor(private readonly p: Profile) {
    if (autoLinks(p)) this.budget = p.maxLinks
  }

  block(d: Doc, lines: string[]): void {
    const cls = lines.map(classify)
    let para: Chunk[] = []
    const flushPara = () => {
      const fin = finish(para)
      if (fin !== null && fin.length > 0) d.blocks.push({ kind: 'p', inl: fin } as Block)
      para = []
    }
    const addText = (s: string) => {
      if (para.length > 0) para.push(brChunk())
      para.push(...this.inline(s))
    }
    for (let i = 0; i < lines.length; ) {
      const k = cls[i].kind
      if (k === 'bullet' || (k === 'number' && cls[i].num === 1)) {
        // A numbered list needs the numbers 1, 2, 3 ...: the list shows its own numbers.
        let j = i + 1
        while (j < lines.length && cls[j].kind === k && (k !== 'number' || cls[j].num === 1 + (j - i))) j++
        if (j - i >= 2) {
          flushPara()
          const items: Item[] = []
          for (let x = i; x < j; x++) items.push({ inl: finish(this.inline(cls[x].body)) ?? [], subs: [] })
          d.blocks.push({ kind: 'list', list: { ordered: k === 'number', items } })
          i = j
          continue
        }
      }
      addText(lines[i])
      i++
    }
    flushPara()
  }

  /** One line: **bold** first, then _italic_ and links inside each part. */
  inline(s: string): Chunk[] {
    const out: Chunk[] = []
    let last = 0
    const closers = s.includes('**') ? boldClosers(s) : []
    const next = { i: 0 }
    for (let i = 0; i < s.length; i++) {
      if (s[i] !== '*' || i + 1 >= s.length || s[i + 1] !== '*') continue
      const j = boldEnd(s, i, closers, next)
      if (j < 0) continue
      out.push(...this.italics(s.slice(last, i), false))
      out.push(...this.italics(s.slice(i + 2, j), true))
      i = j + 1
      last = j + 2
    }
    out.push(...this.italics(s.slice(last), false))
    return out
  }

  /** _italic_ at word borders: "snake_case" and "https://x/a_b_c" stay intact. */
  private italics(s: string, strong: boolean): Chunk[] {
    const out: Chunk[] = []
    let last = 0
    for (let i = 0; i < s.length; i++) {
      if (s[i] !== '_' || !italicOpensAt(s, i)) continue
      const j = s.indexOf('_', i + 1)
      if (j < 0) continue
      const body = s.slice(i + 1, j)
      if (body === '' || !italicBody(body) || !italicClosesAt(s, j)) continue
      out.push(...this.links(s.slice(last, i), strong, false))
      out.push(...this.links(body, strong, true))
      i = j
      last = j + 1
    }
    out.push(...this.links(s.slice(last), strong, false))
    return out
  }

  /** Splits s into text and, when the profile auto-links, https links. */
  private links(s: string, strong: boolean, em: boolean): Chunk[] {
    if (s === '') return []
    const marks = { strong, em, u: false, href: '', linkID: 0 }
    const plain = (t: string): Chunk => textChunk(t, marks)
    if (this.budget <= 0) return [plain(s)]
    const out: Chunk[] = []
    let last = 0
    for (let i = 0; i + 8 <= s.length && this.budget > 0; ) {
      if (s.slice(i, i + 8).toLowerCase() !== 'https://') {
        i++
        continue
      }
      let end = i + 8
      while (end < s.length) {
        const cp = s.codePointAt(end) as number
        if (isUnicodeSpace(cp) || isWS(cp) || '<>"\'`'.includes(String.fromCodePoint(cp))) break
        end += cp > 0xffff ? 2 : 1
      }
      const scanEnd = end
      while (end > i && TRAILING_PUNCT.includes(s[end - 1])) end--
      const href = cleanHref(s.slice(i, end), this.p)
      if (href === null) {
        i = scanEnd // the whole run is not a link: do not scan it again
        continue
      }
      if (i > last) out.push(plain(s.slice(last, i)))
      this.nextLink++
      this.budget--
      out.push(textChunk(s.slice(i, end), { strong, em, u: false, href, linkID: this.nextLink }))
      i = end
      last = end
    }
    if (last < s.length) out.push(plain(s.slice(last)))
    return out
  }
}

/** Positions of every "**" that follows a non-space: where a bold part may end. */
function boldClosers(s: string): number[] {
  const out: number[] = []
  for (let j = 1; j + 1 < s.length; j++) {
    if (s[j] === '*' && s[j + 1] === '*' && !isUnicodeSpace(s.charCodeAt(j - 1))) out.push(j)
  }
  return out
}

/** The "**" that closes the one opened at s[i..i+2], or -1. `next` carries the search position of the line. */
function boldEnd(s: string, i: number, closers: number[], next: { i: number }): number {
  const c = i + 2
  if (c >= s.length) return -1
  if (isUnicodeSpace(s.charCodeAt(c))) return -1
  while (next.i < closers.length && closers[next.i] < c + 1) next.i++
  return next.i < closers.length ? closers[next.i] : -1
}

function italicOpensAt(s: string, i: number): boolean {
  if (i === 0) return true
  const r = s[i - 1]
  return isUnicodeSpace(s.charCodeAt(i - 1)) || ITALIC_OPEN_AFTER.includes(r)
}

function italicClosesAt(s: string, j: number): boolean {
  if (j + 1 >= s.length) return true
  return isUnicodeSpace(s.charCodeAt(j + 1)) || ITALIC_CLOSE_BEFORE.includes(s[j + 1])
}

function italicBody(b: string): boolean {
  return !isUnicodeSpace(b.charCodeAt(0)) && !isUnicodeSpace(b.charCodeAt(b.length - 1))
}

/**
 * Turns the old plain-text form into canonical ART for the profile: a blank line is a paragraph, a single line
 * break is <br>, **x** is strong, _x_ at word borders is em, a block of two or more "- ", "* " or "• " lines is a
 * list, and so is a numbered block 1., 2., ... Anything else is text, HTML-escaped. The letter note also links
 * bare https:// addresses (up to its link limit). Empty or blank input gives "".
 */
export function fromLegacy(text: string, p: Profile): string {
  const cleaned = legacyChars(text).replace(/\r\n/g, '\n').replace(/\r/g, '\n')
  const conv = new Converter(p)
  const d: Doc = { blocks: [] }
  let block: string[] = []
  const flush = () => {
    if (block.length > 0) {
      conv.block(d, block)
      block = []
    }
  }
  for (const raw of cleaned.split('\n')) {
    const line = trimWS(raw)
    if (line === '') {
      flush()
      continue
    }
    block.push(line)
  }
  flush()
  return docHtml(d).html
}
