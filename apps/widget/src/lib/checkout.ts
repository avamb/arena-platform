/**
 * checkout.ts — Pure checkout-flow helpers for the Arena Tickets widget (WID-D).
 *
 * All functions are pure and side-effect-free — no DOM, no fetch, no timers.
 * The UI layer calls these to validate buyer input, detect email typos,
 * drive the checkout state machine, and localize all user-facing copy.
 *
 * Responsibilities:
 *  • Email typo-suggestion engine (gmail/outlook/yandex/seznam/yahoo etc.)
 *  • Buyer form validation driven by buyer_fields flags.
 *  • Checkout i18n copy (en / ru / cs / he).
 *  • Types for start/status/recovery API shapes.
 */

// ─── Types ────────────────────────────────────────────────────────────────────

/** One buyer_fields entry returned by the public feed session. */
export interface BuyerFieldConfig {
  key: 'email' | 'name' | 'phone';
  required: boolean;
  enabled: boolean;
}

/** Values captured in the buyer form. */
export interface BuyerFormValues {
  email: string;
  name: string;
  phone: string;
}

/** Per-field validation errors (undefined = no error). */
export interface BuyerFormErrors {
  email?: string;
  name?: string;
  phone?: string;
}

/** GA item for the checkout/start payload. */
export interface PublicGAItem {
  tier_id: string;
  quantity: number;
}

/** Payload sent to POST /v1/public/feeds/{token}/checkout/start. */
export interface CheckoutStartPayload {
  session_id: string;
  holder_email: string;
  seats?: string[];
  ga_items?: PublicGAItem[];
  buyer?: {
    email: string;
    name?: string | null;
    phone?: string | null;
  };
}

/** Response from POST /v1/public/feeds/{token}/checkout/start. */
export interface CheckoutStartResponse {
  checkout_session: Record<string, unknown>;
  redirect_url: string;
  checkout_token: string;
  expires_at: string;
}

/** A single cart item in the anonymous order-status response. */
export interface CheckoutStatusItem {
  type: 'seat' | 'general_admission';
  seat_key?: string | null;
  sector?: string | null;
  row?: string | null;
  number?: string | null;
  unit_price?: number | null;
  quantity?: number | null;
}

/** A single issued ticket in the order-status response (status=paid). */
export interface CheckoutStatusTicketItem {
  ticket_id: string;
  sector?: string | null;
  row?: string | null;
  number?: string | null;
  human_code?: string | null;
  pdf_url?: string | null;
}

/** Public-facing order status. */
export type CheckoutPublicStatus = 'pending' | 'paid' | 'expired' | 'failed';

/** Response from GET /v1/public/checkout/{token}. */
export interface CheckoutStatusResponse {
  status: CheckoutPublicStatus;
  checkout_token: string;
  checkout_session_id: string;
  expires_at?: string | null;
  subtotal?: number | null;
  discount?: number | null;
  platform_fee?: number | null;
  provider_fee?: number | null;
  tax?: number | null;
  total?: number | null;
  currency?: string | null;
  items: CheckoutStatusItem[];
  tickets: CheckoutStatusTicketItem[];
}

/** Response from POST /v1/public/checkout/{token}/recover. */
export interface CheckoutRecoverResponse {
  checkout_session: Record<string, unknown>;
  checkout_token: string;
  expires_at: string;
}

// ─── Email typo detection ─────────────────────────────────────────────────────

/**
 * Well-known email provider domains used for typo detection.
 * Ordered by approximate global + regional popularity so the closest
 * match is more likely to be picked first in case of ties.
 */
const KNOWN_DOMAINS: ReadonlyArray<string> = [
  'gmail.com',
  'outlook.com',
  'hotmail.com',
  'yahoo.com',
  'icloud.com',
  'live.com',
  'msn.com',
  'proton.me',
  'protonmail.com',
  // Yandex
  'yandex.ru',
  'yandex.com',
  'ya.ru',
  // Czech
  'seznam.cz',
  'email.cz',
  'centrum.cz',
  // Russian
  'mail.ru',
  'bk.ru',
  'inbox.ru',
  'list.ru',
  // Other
  'me.com',
  'aol.com',
  'gmx.com',
  'web.de',
  'zoho.com',
];

/** Maximum edit distance at which a domain is considered a typo. */
const MAX_TYPO_DISTANCE = 2;

/**
 * Compute Levenshtein edit distance between two strings.
 * Uses the classic DP approach, O(mn) time, O(min(m,n)) space.
 */
export function editDistance(a: string, b: string): number {
  if (a === b) return 0;
  if (a.length === 0) return b.length;
  if (b.length === 0) return a.length;

  // Keep only two rows to minimize allocation.
  let rowA = Array.from({ length: b.length + 1 }, (_, i) => i);
  let rowB = new Array<number>(b.length + 1);

  for (let i = 1; i <= a.length; i++) {
    rowB[0] = i;
    for (let j = 1; j <= b.length; j++) {
      const cost = a[i - 1] === b[j - 1] ? 0 : 1;
      rowB[j] = Math.min(
        rowB[j - 1] + 1,        // insertion
        rowA[j] + 1,            // deletion
        rowA[j - 1] + cost,     // substitution
      );
    }
    // Swap rows.
    const tmp = rowA;
    rowA = rowB;
    rowB = tmp;
  }

  return rowA[b.length];
}

/**
 * Parse an email string into its local and domain parts.
 * Returns null when the input has no "@" or the domain is empty.
 */
function splitEmail(email: string): { local: string; domain: string } | null {
  const at = email.lastIndexOf('@');
  if (at < 0) return null;
  const local = email.slice(0, at);
  const domain = email.slice(at + 1).toLowerCase();
  if (!domain) return null;
  return { local, domain };
}

/**
 * Suggest a corrected email address when the domain looks like a typo.
 *
 * Returns the suggested full email string (e.g. "user@gmail.com") when a
 * known domain is within edit distance ≤ 2, or `null` when the email looks
 * correct (domain is already known) or when no close match exists.
 *
 * Examples:
 *   "alice@gmial.com"   → "alice@gmail.com"   (transposition)
 *   "bob@gmal.com"      → "bob@gmail.com"      (deletion)
 *   "eve@outlok.com"    → "eve@outlook.com"    (deletion)
 *   "me@yandex.ru"      → null                 (already correct)
 *   "x@zzz.xyz"         → null                 (unknown, no near match)
 */
export function suggestEmailFix(email: string): string | null {
  const parsed = splitEmail(email.trim());
  if (!parsed) return null;

  const { local, domain } = parsed;

  // Already a known domain — no suggestion needed.
  if (KNOWN_DOMAINS.includes(domain)) return null;

  let bestDomain: string | null = null;
  let bestDist = Infinity;

  for (const known of KNOWN_DOMAINS) {
    const d = editDistance(domain, known);
    if (d < bestDist) {
      bestDist = d;
      bestDomain = known;
    }
  }

  if (bestDist <= MAX_TYPO_DISTANCE && bestDomain !== null) {
    return `${local}@${bestDomain}`;
  }

  return null;
}

// ─── Email format validation ─────────────────────────────────────────────────

/**
 * Lightweight email format check — no DNS lookups.
 *
 * Accepts the vast majority of valid email addresses without being overly
 * strict; the backend performs authoritative validation.
 */
export function isValidEmail(email: string): boolean {
  const trimmed = email.trim();
  if (!trimmed) return false;
  const at = trimmed.lastIndexOf('@');
  if (at < 1) return false;                   // at least one char before @
  const domain = trimmed.slice(at + 1);
  if (domain.length < 3) return false;         // e.g. "a.b"
  if (!domain.includes('.')) return false;     // must have a dot
  return true;
}

// ─── Buyer form validation ───────────────────────────────────────────────────

/**
 * Validate a buyer form against the session's `buyer_fields` config.
 *
 * Each field is validated only if it is both `enabled` AND `required`.
 * Returns an object with per-field error messages in the specified locale,
 * or an empty object when the form is valid.
 */
export function validateBuyerForm(
  values: BuyerFormValues,
  buyerFields: BuyerFieldConfig[],
  locale: CheckoutLocale = 'en',
): BuyerFormErrors {
  const t = CHECKOUT_I18N[locale] ?? CHECKOUT_I18N.en;
  const errors: BuyerFormErrors = {};
  const fieldMap = new Map(buyerFields.map((f) => [f.key, f]));

  // Email is always validated (it is mandatory for every checkout).
  const emailField = fieldMap.get('email');
  const emailEnabled = emailField?.enabled !== false; // treat absent as enabled
  if (emailEnabled) {
    if (!values.email.trim()) {
      errors.email = t.email_required;
    } else if (!isValidEmail(values.email)) {
      errors.email = t.email_invalid;
    }
  }

  // Name — validate only when the field is enabled AND required.
  const nameField = fieldMap.get('name');
  if (nameField?.enabled && nameField.required && !values.name.trim()) {
    errors.name = t.name_required;
  }

  // Phone — validate only when the field is enabled AND required.
  const phoneField = fieldMap.get('phone');
  if (phoneField?.enabled && phoneField.required && !values.phone.trim()) {
    errors.phone = t.phone_required;
  }

  return errors;
}

/**
 * Return true when the form has no validation errors.
 */
export function isBuyerFormValid(errors: BuyerFormErrors): boolean {
  return !errors.email && !errors.name && !errors.phone;
}

// ─── Checkout payload builder ─────────────────────────────────────────────────

/**
 * Build the `POST checkout/start` payload from the form values + cart contents.
 */
export function buildCheckoutPayload(
  sessionId: string,
  values: BuyerFormValues,
  seats: string[],
  gaItems: PublicGAItem[],
  buyerFields: BuyerFieldConfig[],
): CheckoutStartPayload {
  const fieldMap = new Map(buyerFields.map((f) => [f.key, f]));

  const nameField = fieldMap.get('name');
  const phoneField = fieldMap.get('phone');

  const buyer: CheckoutStartPayload['buyer'] = {
    email: values.email.trim(),
    name: nameField?.enabled ? (values.name.trim() || null) : null,
    phone: phoneField?.enabled ? (values.phone.trim() || null) : null,
  };

  const payload: CheckoutStartPayload = {
    session_id: sessionId,
    holder_email: values.email.trim(),
    buyer,
  };

  if (seats.length > 0) payload.seats = seats;
  if (gaItems.length > 0) payload.ga_items = gaItems;

  return payload;
}

// ─── Price formatting ─────────────────────────────────────────────────────────

/**
 * Format an integer amount (smallest currency unit) as a human-readable price.
 *
 * Assumes 2 decimal places for all supported currencies (kopecks, cents, etc.).
 * Falls back to a plain integer string when the currency is unknown/empty.
 */
export function formatPrice(amountSmallest: number, currency: string): string {
  if (!currency) return String(amountSmallest);
  try {
    const amount = amountSmallest / 100;
    return new Intl.NumberFormat(undefined, {
      style: 'currency',
      currency: currency.toUpperCase(),
      minimumFractionDigits: 0,
      maximumFractionDigits: 2,
    }).format(amount);
  } catch {
    return `${(amountSmallest / 100).toFixed(2)} ${currency.toUpperCase()}`;
  }
}

// ─── Order status helpers ─────────────────────────────────────────────────────

/**
 * Determine whether the checkout status can be polled until it resolves.
 *
 * `pending` is a transient state (payment processing); all others are terminal.
 */
export function isCheckoutPending(status: CheckoutPublicStatus): boolean {
  return status === 'pending';
}

/**
 * Return true when the expired checkout can be recovered (WID-0c).
 * Only `expired` sessions may be recovered; `failed`/`paid` are terminal.
 */
export function isCheckoutRecoverable(status: CheckoutPublicStatus): boolean {
  return status === 'expired';
}

// ─── Internationalization ─────────────────────────────────────────────────────

/**
 * Single source of truth for the widget's supported locales.
 *
 * These are exactly the locales with complete translations in
 * `CHECKOUT_I18N` below (spec set: en / ru / cs / he, with `he` RTL).
 * `utils.ts` re-exports this set for host-attribute parsing; do not add a
 * locale here without adding its translation table.
 */
export const SUPPORTED_LOCALES = ['en', 'ru', 'cs', 'he'] as const;
export type SupportedLocale = (typeof SUPPORTED_LOCALES)[number];

export type CheckoutLocale = SupportedLocale;

export interface CheckoutI18nStrings {
  // Buyer form
  email_label: string;
  email_placeholder: string;
  email_required: string;
  email_invalid: string;
  /** Template string; replace {suggestion} with the suggested email. */
  email_suggestion: string;
  name_label: string;
  name_placeholder: string;
  name_required: string;
  phone_label: string;
  phone_placeholder: string;
  phone_required: string;
  submit_label: string;
  // Order status
  status_paid: string;
  status_expired: string;
  status_failed: string;
  status_pending: string;
  order_ref_label: string;
  seats_heading: string;
  ticket_heading: string;
  human_code_label: string;
  send_again: string;
  retry_label: string;
  recover_label: string;
  download_pdf: string;
  // Generic
  loading: string;
  error_generic: string;
}

/** Interpolate {key} placeholders in an i18n string. */
export function interpolate(template: string, vars: Record<string, string>): string {
  return template.replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? `{${k}}`);
}

export const CHECKOUT_I18N: Record<CheckoutLocale, CheckoutI18nStrings> = {
  en: {
    email_label: 'Email',
    email_placeholder: 'your@email.com',
    email_required: 'Email address is required',
    email_invalid: 'Please enter a valid email address',
    email_suggestion: 'Did you mean {suggestion}?',
    name_label: 'Full name',
    name_placeholder: 'Jane Smith',
    name_required: 'Full name is required',
    phone_label: 'Phone',
    phone_placeholder: '+1 555 000 0000',
    phone_required: 'Phone number is required',
    submit_label: 'Continue to payment',
    status_paid: 'Payment successful!',
    status_expired: 'Your reservation has expired',
    status_failed: 'Payment failed',
    status_pending: 'Processing your order…',
    order_ref_label: 'Order',
    seats_heading: 'Your seats',
    ticket_heading: 'Your tickets',
    human_code_label: 'Code',
    send_again: 'Resend tickets',
    retry_label: 'Try again',
    recover_label: 'Reclaim seats',
    download_pdf: 'Download PDF',
    loading: 'Loading…',
    error_generic: 'Something went wrong. Please try again.',
  },
  ru: {
    email_label: 'Email',
    email_placeholder: 'ваш@email.ru',
    email_required: 'Введите адрес электронной почты',
    email_invalid: 'Введите корректный адрес электронной почты',
    email_suggestion: 'Вы имели в виду {suggestion}?',
    name_label: 'Полное имя',
    name_placeholder: 'Иван Иванов',
    name_required: 'Введите полное имя',
    phone_label: 'Телефон',
    phone_placeholder: '+7 999 000 0000',
    phone_required: 'Введите номер телефона',
    submit_label: 'Перейти к оплате',
    status_paid: 'Оплата прошла успешно!',
    status_expired: 'Срок бронирования истёк',
    status_failed: 'Ошибка оплаты',
    status_pending: 'Обрабатываем ваш заказ…',
    order_ref_label: 'Заказ',
    seats_heading: 'Ваши места',
    ticket_heading: 'Ваши билеты',
    human_code_label: 'Код',
    send_again: 'Отправить билеты повторно',
    retry_label: 'Попробовать снова',
    recover_label: 'Восстановить бронирование',
    download_pdf: 'Скачать PDF',
    loading: 'Загрузка…',
    error_generic: 'Произошла ошибка. Пожалуйста, попробуйте ещё раз.',
  },
  cs: {
    email_label: 'E-mail',
    email_placeholder: 'váš@email.cz',
    email_required: 'Zadejte e-mailovou adresu',
    email_invalid: 'Zadejte platnou e-mailovou adresu',
    email_suggestion: 'Mysleli jste {suggestion}?',
    name_label: 'Celé jméno',
    name_placeholder: 'Jan Novák',
    name_required: 'Zadejte celé jméno',
    phone_label: 'Telefon',
    phone_placeholder: '+420 777 000 000',
    phone_required: 'Zadejte telefonní číslo',
    submit_label: 'Pokračovat k platbě',
    status_paid: 'Platba proběhla úspěšně!',
    status_expired: 'Rezervace vypršela',
    status_failed: 'Platba se nezdařila',
    status_pending: 'Zpracováváme vaši objednávku…',
    order_ref_label: 'Objednávka',
    seats_heading: 'Vaše místa',
    ticket_heading: 'Vaše vstupenky',
    human_code_label: 'Kód',
    send_again: 'Odeslat vstupenky znovu',
    retry_label: 'Zkusit znovu',
    recover_label: 'Obnovit rezervaci',
    download_pdf: 'Stáhnout PDF',
    loading: 'Načítání…',
    error_generic: 'Něco se pokazilo. Zkuste to prosím znovu.',
  },
  he: {
    email_label: 'דוא"ל',
    email_placeholder: 'your@email.com',
    email_required: 'נדרשת כתובת דוא"ל',
    email_invalid: 'נא להזין כתובת דוא"ל תקינה',
    email_suggestion: 'האם התכוונת ל-{suggestion}?',
    name_label: 'שם מלא',
    name_placeholder: 'ישראל ישראלי',
    name_required: 'נדרש שם מלא',
    phone_label: 'טלפון',
    phone_placeholder: '050-000-0000',
    phone_required: 'נדרש מספר טלפון',
    submit_label: 'המשך לתשלום',
    status_paid: 'התשלום הצליח!',
    status_expired: 'ההזמנה פגה',
    status_failed: 'התשלום נכשל',
    status_pending: 'מעבד את ההזמנה שלך…',
    order_ref_label: 'הזמנה',
    seats_heading: 'המקומות שלך',
    ticket_heading: 'הכרטיסים שלך',
    human_code_label: 'קוד',
    send_again: 'שלח כרטיסים שוב',
    retry_label: 'נסה שוב',
    recover_label: 'שחזר הזמנה',
    download_pdf: 'הורד PDF',
    loading: 'טוען…',
    error_generic: 'משהו השתבש. אנא נסה שוב.',
  },
};

/**
 * Get the i18n strings for a given locale, falling back to English.
 */
export function getCheckoutI18n(locale: string): CheckoutI18nStrings {
  const key = (locale as CheckoutLocale) in CHECKOUT_I18N
    ? (locale as CheckoutLocale)
    : 'en';
  return CHECKOUT_I18N[key];
}
