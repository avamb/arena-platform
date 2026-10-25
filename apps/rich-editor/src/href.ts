// The link check (Go: cleanHref and cleanHost in href.go). A link target is
// accepted when its scheme is in the profile, an https link has a host and no
// user-info, port, IP address or localhost, a mailto link is just an address,
// and no whitespace, control, bidi or invisible character occurs anywhere.

import { CharAction, charPolicy, isUnicodeSpace, isWS } from './chars'
import { MAX_LINK_LENGTH, profileAllowsScheme, type Profile } from './profiles'

/** Length in UTF-8 bytes, like Go's len(string). */
function byteLength(s: string): number {
  let n = 0
  for (const ch of s) {
    const cp = ch.codePointAt(0) as number
    n += cp < 0x80 ? 1 : cp < 0x800 ? 2 : cp < 0x10000 ? 3 : 4
  }
  return n
}

function isSpaceChar(c: string): boolean {
  const cp = c.codePointAt(0) as number
  return isWS(cp) || isUnicodeSpace(cp)
}

function trimSpaces(s: string): string {
  const cps = Array.from(s)
  let a = 0
  let b = cps.length
  while (a < b && isSpaceChar(cps[a])) a++
  while (b > a && isSpaceChar(cps[b - 1])) b--
  return cps.slice(a, b).join('')
}

const BACKSLASH = String.fromCharCode(92)

/** Returns the canonical link target for the profile, or null when the link must be unwrapped. */
export function cleanHref(raw: string, p: Profile): string | null {
  const s = trimSpaces(raw)
  if (s === '' || byteLength(s) > MAX_LINK_LENGTH) return null
  for (const ch of s) {
    const cp = ch.codePointAt(0) as number
    if (
      isUnicodeSpace(cp) ||
      charPolicy(cp) !== CharAction.Plain ||
      ch === BACKSLASH ||
      ch === '"' ||
      ch === '<' ||
      ch === '>'
    ) {
      return null
    }
  }
  const colon = s.indexOf(':')
  if (colon <= 0) return null // relative, protocol-relative ("//host") or fragment
  const scheme = s.slice(0, colon).toLowerCase()
  if (!profileAllowsScheme(p, scheme)) return null
  const rest = s.slice(colon + 1)
  if (scheme === 'https') {
    if (!rest.startsWith('//')) return null
    const after = rest.slice(2)
    const m = /[/?#]/.exec(after)
    const end = m ? m.index : -1
    const authority = end >= 0 ? after.slice(0, end) : after
    const tail = end >= 0 ? after.slice(end) : ''
    const host = cleanHost(authority)
    if (host === null) return null
    return 'https://' + host + tail
  }
  if (scheme === 'mailto') {
    // Only the address survives: "?bcc=...&body=..." would let a link write the mail.
    let addr = rest
    const q = addr.indexOf('?')
    if (q >= 0) addr = addr.slice(0, q)
    const at = addr.indexOf('@')
    if (at < 0) return null
    const local = addr.slice(0, at)
    const domain = addr.slice(at + 1)
    if (!validLocalPart(local)) return null
    const d = cleanHost(domain)
    if (d === null) return null
    return 'mailto:' + local + '@' + d
  }
  return null
}

// RFC 5322 atext specials. The percent sign is left out: in a mailto: URI it starts an escape.
const LOCAL_SPECIALS = "!#$&'*+-/=?^_`{|}~"

function validLocalPart(s: string): boolean {
  if (s === '' || s.length > 64 || s[0] === '.' || s[s.length - 1] === '.' || s.includes('..')) return false
  for (let i = 0; i < s.length; i++) {
    const c = s[i]
    if ((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c === '.') continue
    if (LOCAL_SPECIALS.includes(c)) continue
    return false
  }
  return true
}

/** IDNA (UTS 46) to ASCII, the way a browser reads a host. Null when it fails. */
function toASCII(authority: string): string | null {
  try {
    const u = new URL('https://' + authority)
    // A host with a path, port, user-info or the like would not round trip.
    if (u.username !== '' || u.password !== '' || u.port !== '' || u.pathname !== '/' || u.search !== '' || u.hash !== '') {
      return null
    }
    return u.hostname
  } catch {
    return null
  }
}

function cleanHost(authority: string): string | null {
  if (authority === '' || byteLength(authority) > MAX_LINK_LENGTH || /[@:[\]%]/.test(authority)) return null
  const ascii = toASCII(authority)
  if (ascii === null) return null
  let host = ascii.toLowerCase()
  if (host.length > 253 || host.startsWith('.') || host.includes('..')) return null
  if (host.endsWith('.')) host = host.slice(0, -1) // the root label: "example.com." is "example.com"
  if (host === '') return null
  for (let i = 0; i < host.length; i++) {
    const c = host[i]
    if ((c < 'a' || c > 'z') && (c < '0' || c > '9') && c !== '-' && c !== '.') return null
  }
  for (const label of host.split('.')) {
    // IDNA label rules (CheckHyphens, VerifyDNSLength): the Go side refuses what a browser would still open.
    if (label.length < 1 || label.length > 63 || label.startsWith('-') || label.endsWith('-')) return null
    if (label.slice(2, 4) === '--' && !label.startsWith('xn--')) return null
  }
  if (host === 'localhost' || host.endsWith('.localhost')) return null
  const last = host.slice(host.lastIndexOf('.') + 1)
  if (isNumericLabel(last)) return null // 127.1, 2130706433, 0x7f.1: read as an IPv4 address
  return host
}

function isNumericLabel(s: string): boolean {
  if (s === '') return false
  if (s.startsWith('0x')) return /^[0-9a-f]*$/.test(s.slice(2))
  return /^[0-9]+$/.test(s)
}

/**
 * Reads the address a person typed into the link dialog: "example.com" gets
 * https:// (and "a@b.com" gets mailto: where the profile allows it), then the
 * result is checked like cleanHref does. Null when the link is not allowed.
 */
export function parseLinkInput(raw: string, p: Profile): string | null {
  let s = trimSpaces(raw)
  if (s !== '' && !/^[a-z][a-z0-9+.-]*:/i.test(s) && !s.startsWith('//')) {
    if (profileAllowsScheme(p, 'mailto') && /^[^\s/@?#]+@[^\s/@?#]+$/.test(s)) s = 'mailto:' + s
    else s = 'https://' + s
  }
  return cleanHref(s, p)
}
