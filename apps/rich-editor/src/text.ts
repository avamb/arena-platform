// @arena/rich-editor/text: the pure text functions of the library, without the editor and without Tiptap.
// A host that only has to count, compare or convert a text (the wizard draft, the poster hints, the
// session message) imports this entry, so the editor itself can be loaded later, on the screen that shows it.
// `sanitize` and `measure` read HTML through a template element, so they need a DOM (a browser or jsdom).

export { PROFILES, PROFILE_NAMES, profileByName } from './profiles'
export type { LinkScheme, Profile, ProfileName } from './profiles'

export { cleanHref, parseLinkInput } from './href'
export { sanitize, measure, RichTextError } from './sanitize'
export type { TextStats } from './sanitize'
export { fromLegacy } from './legacy'
export { artToPlainText } from './plain'
