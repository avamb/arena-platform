// The profiles table. It mirrors apps/backend/internal/platform/richtext
// (richtext.go: ProfileDescription, ProfileEmailNote, ProfileSessionMessage)
// field by field, so keep the two in step. Headings (h3, h4) and the quote
// exist only in the description profile (spec 44, owner decision 9.1).

export type ProfileName = 'description' | 'email_note' | 'session_message'

export type LinkScheme = 'https' | 'mailto'

export interface Profile {
  /** Stable name, the same as the Go profile name and the API `kind`. */
  readonly name: ProfileName
  /** White list of output tags. b and i are input aliases of strong and em. */
  readonly tags: readonly string[]
  /** Link schemes: https, and mailto for the description only. */
  readonly schemes: readonly LinkScheme[]
  /** The longest visible text, in characters. */
  readonly maxVisible: number
  /** The longest stored HTML, in characters (the API checks it, the counter does not). */
  readonly maxHtml: number
  /** How many links the text may carry. */
  readonly maxLinks: number
  /**
   * Refuses a visible text that holds a "javascript:", "data:", "http:" and the like as a word
   * (Go: StrictSchemeWords, the strictness of the old email note).
   */
  readonly strictSchemeWords: boolean
}

/** The longest link target, in bytes (Go: MaxLinkLength). */
export const MAX_LINK_LENGTH = 500

/** Raw input larger than this is refused before parsing (Go: maxInputBytes). */
export const MAX_INPUT_BYTES = 1 << 20

/** Element nesting the sanitizer walks (Go: maxDepth). */
export const MAX_DEPTH = 200

const BASE_TAGS = ['p', 'br', 'strong', 'em', 'u', 'a', 'ul', 'ol', 'li'] as const

/** The one exported profile table. */
export const PROFILES: Readonly<Record<ProfileName, Profile>> = Object.freeze({
  description: Object.freeze({
    name: 'description',
    tags: Object.freeze([...BASE_TAGS, 'h3', 'h4', 'blockquote']),
    schemes: Object.freeze(['https', 'mailto'] as LinkScheme[]),
    maxVisible: 4000,
    maxHtml: 16000,
    maxLinks: 20,
    strictSchemeWords: false,
  }),
  email_note: Object.freeze({
    name: 'email_note',
    tags: Object.freeze([...BASE_TAGS]),
    schemes: Object.freeze(['https'] as LinkScheme[]),
    maxVisible: 2000,
    maxHtml: 8000,
    maxLinks: 5,
    strictSchemeWords: true,
  }),
  session_message: Object.freeze({
    name: 'session_message',
    tags: Object.freeze([...BASE_TAGS]),
    schemes: Object.freeze(['https'] as LinkScheme[]),
    maxVisible: 1000,
    maxHtml: 4000,
    maxLinks: 3,
    strictSchemeWords: false,
  }),
})

export const PROFILE_NAMES = Object.freeze(Object.keys(PROFILES) as ProfileName[])

export function profileByName(name: string): Profile | undefined {
  return Object.prototype.hasOwnProperty.call(PROFILES, name) ? PROFILES[name as ProfileName] : undefined
}

export function profileHas(p: Profile, tag: string): boolean {
  return p.tags.includes(tag)
}

export function profileAllowsScheme(p: Profile, scheme: string): boolean {
  return (p.schemes as readonly string[]).includes(scheme)
}
