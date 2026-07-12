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
  isRtlLocale,
  THEME_CSS_VARS,
  SUPPORTED_LOCALES,
  RTL_LOCALES,
  type SupportedLocale,
  type ThemeCssVar,
  type RtlLocale,
} from './utils.js';

// Strict allowlist sanitizer for untrusted decor SVG fragments.
export { sanitizeDecorSvg } from './lib/svg-sanitize.js';

// WID-E: CSS design token system documentation.
export {
  TOKEN_DEFAULTS,
  TYPOGRAPHY_TOKENS,
  COLOR_TOKENS,
  SHAPE_TOKENS,
  ALL_TOKENS,
  arenaThemeIndigo,
  arenaThemeRose,
  arenaThemeNeutral,
  ARENA_THEMES,
  type DesignToken,
  type ArenaThemeName,
} from './lib/tokens.js';

// WID-C: selection, cart, and hold-timer utilities.
export {
  toggleSeatSelection,
  clearSelection,
  bestAvailableSeats,
  detectSingleSeatGaps,
  clampGaQuantity,
  incrementGaQuantity,
  decrementGaQuantity,
  GA_MIN_QUANTITY,
  GA_MAX_QUANTITY,
  type SelectedKeys,
} from './lib/selection.js';

export {
  emptyCart,
  removeCartLine,
  applyHoldResponse,
  buildSeatedLines,
  buildGaLine,
  cartTotal,
  cartItemCount,
  countdownSeconds,
  isTwoMinWarning,
  formatCountdown,
  type CartLineItem,
  type CartState,
} from './lib/cart.js';

// WID-D: checkout handoff + result + recovery utilities.
export {
  editDistance,
  suggestEmailFix,
  isValidEmail,
  validateBuyerForm,
  isBuyerFormValid,
  buildCheckoutPayload,
  formatPrice,
  isCheckoutPending,
  isCheckoutRecoverable,
  interpolate,
  getCheckoutI18n,
  CHECKOUT_I18N,
  type BuyerFieldConfig,
  type BuyerFormValues,
  type BuyerFormErrors,
  type PublicGAItem,
  type CheckoutStartPayload,
  type CheckoutStartResponse,
  type CheckoutStatusItem,
  type CheckoutStatusTicketItem,
  type CheckoutPublicStatus,
  type CheckoutStatusResponse,
  type CheckoutRecoverResponse,
  type CheckoutLocale,
  type CheckoutI18nStrings,
  // WID-R2: conflict parsing and cart filtering
  parseConflictsFromApiError,
  filterCartWithoutConflicts,
  conflictKeySet,
  type ConflictDetail,
} from './lib/checkout.js';

// WID-R2: seat conflict highlight (DOM mutation, no re-render).
export {
  applyConflictHighlight,
  clearConflictHighlight,
  CONFLICT_COLOR,
} from './lib/seatmap-render.js';

export {
  postCheckoutStart,
  getCheckoutStatus,
  postCheckoutRecover,
  ApiError,
} from './api.js';
