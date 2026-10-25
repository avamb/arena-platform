// The one character policy (Go: charPolicy in href.go). It is used by the
// sanitizer, by the serializer and by the link check.

export enum CharAction {
  /** An ordinary character (the tab and line break family included): kept. */
  Plain,
  /** A direction mark or joiner that real texts need: kept. */
  Keep,
  /** An invisible character that only hides things: removed silently. */
  Strip,
  /** A character that must never be stored: refused by the API, dropped here. */
  Refuse,
  /** U+2028: a line break in plain text, a space in ART. */
  LineSep,
  /** U+2029: a paragraph break in plain text, a space in ART. */
  ParaSep,
}

export function charPolicy(cp: number): CharAction {
  switch (cp) {
    case 0x09:
    case 0x0a:
    case 0x0d:
    case 0x0c:
      return CharAction.Plain
    case 0x200e:
    case 0x200f:
    case 0x061c:
    case 0x200c:
    case 0x200d:
    case 0x00ad:
      return CharAction.Keep
    case 0x2066:
    case 0x2067:
    case 0x2068:
    case 0x2069:
    case 0xfeff:
    case 0x200b:
    case 0x2060:
      return CharAction.Strip
    case 0x2028:
      return CharAction.LineSep
    case 0x2029:
      return CharAction.ParaSep
    case 0x180e:
      return CharAction.Refuse
  }
  if (cp <= 0x1f || (cp >= 0x7f && cp <= 0x9f)) return CharAction.Refuse // C0, DEL, C1
  if (cp >= 0x202a && cp <= 0x202e) return CharAction.Refuse // embeddings and overrides
  if (cp >= 0x2061 && cp <= 0x2064) return CharAction.Refuse // invisible operators
  if (cp >= 0xfff9 && cp <= 0xfffb) return CharAction.Refuse // interlinear annotation
  if (cp >= 0xe0000 && cp <= 0xe007f) return CharAction.Refuse // tag characters
  return CharAction.Plain
}

/** The sanitizer's plain space: HTML whitespace, no-break spaces, line and paragraph separators. */
export function isWS(cp: number): boolean {
  switch (cp) {
    case 0x20:
    case 0x09:
    case 0x0a:
    case 0x0d:
    case 0x0c:
    case 0xa0:
    case 0x2007:
    case 0x202f:
    case 0x2028:
    case 0x2029:
      return true
  }
  return false
}

/** Go's unicode.IsSpace. */
export function isUnicodeSpace(cp: number): boolean {
  if (cp <= 0xff) {
    return cp === 0x20 || (cp >= 0x09 && cp <= 0x0d) || cp === 0x85 || cp === 0xa0
  }
  return (
    cp === 0x1680 ||
    (cp >= 0x2000 && cp <= 0x200a) ||
    cp === 0x2028 ||
    cp === 0x2029 ||
    cp === 0x202f ||
    cp === 0x205f ||
    cp === 0x3000
  )
}

/** Length in characters (code points), like Go's utf8.RuneCountInString. */
export function runeCount(s: string): number {
  let n = 0
  for (const _ of s) {
    void _
    n++
  }
  return n
}

/** A UTF-16 surrogate code unit on its own (a half of a pair). Go reads it as U+FFFD after JSON. */
export function isLoneSurrogate(cp: number): boolean {
  return cp >= 0xd800 && cp <= 0xdfff
}

/**
 * Removes the characters that never reach a stored text: the ones the policy
 * refuses or strips, and lone surrogates (JSON would turn them into U+FFFD, so
 * the text saved would differ from the text shown). Line breaks and tabs stay.
 * `for...of` yields a valid pair as one code point, so only lone halves are cut.
 */
export function stripForbiddenChars(text: string): string {
  let out = ''
  for (const ch of text) {
    const cp = ch.codePointAt(0) as number
    if (isLoneSurrogate(cp)) continue
    const action = charPolicy(cp)
    if (action === CharAction.Refuse || action === CharAction.Strip) continue
    out += ch
  }
  return out
}

/** Length in UTF-8 bytes, like Go's len(string). A lone surrogate counts as U+FFFD (3 bytes). */
export function byteLength(s: string): number {
  let n = 0
  for (const ch of s) {
    const cp = ch.codePointAt(0) as number
    n += cp < 0x80 ? 1 : cp < 0x800 ? 2 : cp < 0x10000 ? 3 : 4
  }
  return n
}
