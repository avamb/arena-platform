/**
 * Arena Tickets Widget — entry point.
 *
 * Importing this module registers the <arena-tickets> custom element in the
 * browser's custom element registry.  The component uses an open Shadow DOM,
 * so host page styles do not bleed in, and the widget's internal styles do not
 * bleed out.
 *
 * Usage:
 *   <script type="module" src="arena-tickets.js"></script>
 *   <arena-tickets feed-token="…" session-id="…" locale="en"></arena-tickets>
 */

// Side-effect import: registers the <arena-tickets> custom element.
export { default as ArenaTickets } from './ArenaTickets.svelte';

// Public utility API (importable by callers who consume the widget as an npm package).
export {
  parseLocale,
  parseFeedToken,
  parseSessionId,
  buildThemeStyle,
  THEME_CSS_VARS,
  SUPPORTED_LOCALES,
  type SupportedLocale,
  type ThemeCssVar,
} from './utils.js';
