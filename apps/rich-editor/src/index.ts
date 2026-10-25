// @arena/rich-editor: one editor library for every interface (spec 44, RT-10).
// ESM for apps/event-center, IIFE (global ArenaRichEditor) for WordPress.

export { create } from './editor'
export type { CreateOptions, EditorStats, FormatRequest, FormatResult, RichEditor } from './editor'

export { PROFILES, PROFILE_NAMES, profileByName } from './profiles'
export type { LinkScheme, Profile, ProfileName } from './profiles'

export { LOCALES, MESSAGES, resolveLocale, directionOf } from './i18n'
export type { Locale, Messages } from './i18n'

export { cleanHref, parseLinkInput } from './href'
export { sanitize, measure, RichTextError } from './sanitize'
export type { TextStats } from './sanitize'

export { fromLegacy } from './legacy'
export { artToPlainText } from './plain'
