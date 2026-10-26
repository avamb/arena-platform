/**
 * errorI18n.ts — what the buyer reads when the platform refuses a request.
 *
 * Two rules live here:
 *  1. The payment-start refusals (`checkout.payment_not_configured`,
 *     `checkout.payment_provider_unsupported`, `checkout.payment_start_failed`)
 *     get a warm sentence in the buyer's language.
 *  2. No error path shows a raw "<call> HTTP <status>: <server text>" message.
 *     Whatever the call threw, the buyer gets the widget's own sentence; the
 *     thrown message is never printed.
 *
 * Looked up by language subtag like cartLimits / promoI18n, so es, de and fr
 * read in their own language where the rest of the UI is English.
 */

import { ApiError } from '../api.js';
import { SUPPORTED_LOCALES } from './checkout.js';

export interface ErrorStrings {
  /** Online payment is not open for this event (or the provider is not supported / failed to start). */
  payment_unavailable: string;
  /** Anything unexpected. */
  generic: string;
}

export const ERROR_I18N: Record<string, ErrorStrings> = {
  en: {
    payment_unavailable:
      'Online payment for these tickets is not open yet. Free tickets can still be booked. Please try again later or write to the organizer.',
    generic: 'Something went wrong. Please try again.',
  },
  ru: {
    payment_unavailable:
      'Онлайн-оплата билетов на это мероприятие пока не открыта. Бесплатные билеты забронировать можно. Попробуйте позже или напишите организатору.',
    generic: 'Произошла ошибка. Пожалуйста, попробуйте ещё раз.',
  },
  cs: {
    payment_unavailable:
      'Online platba za tyto vstupenky zatím není otevřená. Vstupenky zdarma si rezervovat můžete. Zkuste to prosím později nebo napište pořadateli.',
    generic: 'Něco se pokazilo. Zkuste to prosím znovu.',
  },
  he: {
    payment_unavailable:
      'התשלום המקוון עבור הכרטיסים האלה עדיין לא נפתח. אפשר להזמין כרטיסים חינם. נסו שוב מאוחר יותר או כתבו למארגן.',
    generic: 'משהו השתבש. אנא נסה שוב.',
  },
  es: {
    payment_unavailable:
      'El pago en línea de estas entradas aún no está abierto. Las entradas gratuitas sí se pueden reservar. Inténtalo más tarde o escribe al organizador.',
    generic: 'Algo ha salido mal. Inténtalo de nuevo.',
  },
  de: {
    payment_unavailable:
      'Die Online-Zahlung für diese Tickets ist noch nicht geöffnet. Kostenlose Tickets können weiterhin gebucht werden. Bitte versuchen Sie es später erneut oder schreiben Sie dem Veranstalter.',
    generic: 'Etwas ist schiefgelaufen. Bitte versuchen Sie es erneut.',
  },
  fr: {
    payment_unavailable:
      "Le paiement en ligne pour ces billets n'est pas encore ouvert. Les billets gratuits peuvent toujours être réservés. Réessayez plus tard ou écrivez à l'organisateur.",
    generic: "Une erreur s'est produite. Veuillez réessayer.",
  },
};

function langOf(locale: string): string {
  return (locale ?? '').trim().toLowerCase().split(/[-_]/)[0] ?? '';
}

/** The error words for a locale ("es-ES" -> es), English when the language has none. */
export function getErrorStrings(locale: string): ErrorStrings {
  return ERROR_I18N[langOf(locale)] ?? ERROR_I18N.en!;
}

const PAYMENT_CODES = new Set([
  'checkout.payment_not_configured',
  'checkout.payment_provider_unsupported',
  'checkout.payment_start_failed',
]);

/** True for the codes the payment pre-flight / hosted-page start answers. */
export function isPaymentErrorCode(code: string | undefined): boolean {
  return code !== undefined && PAYMENT_CODES.has(code);
}

/** The buyer text for a payment-start refusal, or null when `code` is not one. */
export function paymentErrorMessage(code: string | undefined, locale: string): string | null {
  return isPaymentErrorCode(code) ? getErrorStrings(locale).payment_unavailable : null;
}

/**
 * The text to show a buyer for a failed call. `specific` is the widget's own
 * sentence for the call ("Failed to load event.") in the buyer's locale; it is
 * used when the locale has a full widget translation, otherwise the generic
 * sentence in the buyer's language. The thrown message is never shown: it is
 * "<call> HTTP <status>: <server detail>" or a browser transport message.
 */
export function buyerErrorText(err: unknown, locale: string, specific?: string): string {
  if (err instanceof ApiError) {
    const payment = paymentErrorMessage(err.code, locale);
    if (payment !== null) return payment;
  }
  if (specific && (SUPPORTED_LOCALES as readonly string[]).includes(langOf(locale))) {
    return specific;
  }
  return getErrorStrings(locale).generic;
}
