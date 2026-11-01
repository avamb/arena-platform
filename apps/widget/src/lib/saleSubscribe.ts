/**
 * saleSubscribe.ts — a session that is not on sale yet ("soon") or has nothing
 * left ("sold_out"), and the "Notify me when sales open" form of the first.
 *
 * The public feed puts `sales_state` on every session: open | soon | sold_out |
 * ended | cancelled. While a session is soon or sold out no category is
 * sellable (every one reports available 0), so the widget says so ONCE, on the
 * date, instead of printing a grid of zero-availability categories. A missing
 * or unknown value (an older API) changes nothing.
 *
 * Like the promo and consent words, the table carries all seven buyer languages
 * (en, ru, cs, he, es, de, fr) and is looked up by language subtag
 * ("es-ES" -> es), so a buyer reads the consent they give in their own language
 * even where the rest of the widget falls back to English. Every locale must
 * carry every key (`SaleStateStrings` is a closed interface and
 * widget_sale_state.test.ts checks the placeholders).
 */

import { ApiError } from '../api.js';
import type { FeedSession } from '../types.js';

/**
 * The version of the consent text below. It travels with the request so the
 * platform can refuse a page that still shows an older wording
 * (400 `subscription.consent_outdated`). Change it whenever `consent` changes
 * meaning in any language.
 */
export const SALE_NOTIFY_CONSENT_VERSION = 'sale-notify-2026-11-01';

// ─── Sales state ─────────────────────────────────────────────────────────────

export type SessionSalesState = 'open' | 'soon' | 'sold_out' | 'ended' | 'cancelled';

/** The two states in which the widget replaces the categories with a notice. */
export type PausedSalesState = 'soon' | 'sold_out';

/** The session's paused state, or null for open, ended, cancelled, missing and unknown values. */
export function pausedSalesState(s: Pick<FeedSession, 'sales_state'> | null | undefined): PausedSalesState | null {
  const state = s?.sales_state;
  return state === 'soon' || state === 'sold_out' ? state : null;
}

/** True when "Notify me when sales open" is offered: only a session that is "soon". */
export function offersSaleSubscription(s: Pick<FeedSession, 'sales_state'> | null | undefined): boolean {
  return pausedSalesState(s) === 'soon';
}

// ─── Words ───────────────────────────────────────────────────────────────────

export interface SaleStateStrings {
  /** Chip label of a session whose sales have not started. */
  soon_label: string;
  /** Chip label of a session with nothing left. */
  sold_out_label: string;
  /** Notice under the chips for a session that is "soon". */
  soon_notice: string;
  /** Notice under the chips for a session that is sold out. */
  sold_out_notice: string;
  /** Heading of the form. */
  form_title: string;
  /** Label of the e-mail field. */
  email_label: string;
  /** The consent checkbox; {org} is the organizer's name. */
  consent: string;
  /** The same consent for a widget that does not know the organizer's name. */
  consent_no_org: string;
  /** Submit button. */
  submit: string;
  /** Submit button while the request is out. */
  sending: string;
  /** Note under the button: the confirmation letter and what Unsubscribe does. */
  note: string;
  /** Replaces the form after a 202. */
  done: string;
  /** subscription.invalid_email, or an address that fails the check before sending. */
  error_invalid_email: string;
  /** subscription.consent_required, or an unticked box. */
  error_consent_required: string;
  /** subscription.consent_outdated. */
  error_consent_outdated: string;
  /** subscription.sales_open. */
  error_sales_open: string;
  /** subscription.rate_limited and feed.rate_limited. */
  error_rate_limited: string;
  /** Anything else, including a network failure. */
  error_generic: string;
}

export const SALE_STATE_I18N: Record<string, SaleStateStrings> = {
  en: {
    soon_label: 'Coming soon',
    sold_out_label: 'Sold out',
    soon_notice: 'Ticket sales for this date have not started yet.',
    sold_out_notice: 'All tickets for this date are sold out.',
    form_title: 'Notify me when sales open',
    email_label: 'E-mail',
    consent:
      'I agree that {org} may e-mail me when sales open for this session and invite me to its future events. I can withdraw this consent at any time.',
    consent_no_org:
      'I agree that the organizer may e-mail me when sales open for this session and invite me to its future events. I can withdraw this consent at any time.',
    submit: 'Notify me',
    sending: 'Sending…',
    note: 'We will send you a letter to confirm your address. If you later click «Unsubscribe» in any of our letters, letters from us will never reach this address again, tickets included.',
    done: 'Almost done. We sent you a letter: open it and confirm your address.',
    error_invalid_email: 'Please check the e-mail address.',
    error_consent_required: 'Please tick the box to agree.',
    error_consent_outdated: 'This page is out of date. Please reload it and try again.',
    error_sales_open: 'Sales have just opened. Reload the page to buy tickets.',
    error_rate_limited: 'Too many attempts. Please try again later.',
    error_generic: 'We could not send it. Try again.',
  },
  ru: {
    soon_label: 'Скоро в продаже',
    sold_out_label: 'Продано',
    soon_notice: 'Продажа билетов на этот сеанс ещё не началась.',
    sold_out_notice: 'Все билеты на этот сеанс проданы.',
    form_title: 'Сообщить, когда начнутся продажи',
    email_label: 'Электронная почта',
    consent:
      'Даю согласие, что {org} напишет мне, когда начнутся продажи на этот сеанс, и будет приглашать меня на будущие мероприятия. Согласие можно отозвать в любой момент.',
    consent_no_org:
      'Даю согласие, что организатор напишет мне, когда начнутся продажи на этот сеанс, и будет приглашать меня на будущие мероприятия. Согласие можно отозвать в любой момент.',
    submit: 'Сообщить мне',
    sending: 'Отправляем…',
    note: 'Мы отправим письмо, чтобы подтвердить адрес. Если позже вы нажмёте «Отписаться» в любом нашем письме, письма от нас на этот адрес больше никогда не придут, в том числе билеты.',
    done: 'Почти готово. Мы отправили вам письмо: откройте его и подтвердите адрес.',
    error_invalid_email: 'Проверьте адрес электронной почты.',
    error_consent_required: 'Отметьте галочку, чтобы дать согласие.',
    error_consent_outdated: 'Страница устарела. Обновите её и попробуйте ещё раз.',
    error_sales_open: 'Продажи только что открылись. Обновите страницу, чтобы купить билеты.',
    error_rate_limited: 'Слишком много попыток. Попробуйте позже.',
    error_generic: 'Не удалось отправить. Попробуйте ещё раз.',
  },
  cs: {
    soon_label: 'Brzy v prodeji',
    sold_out_label: 'Vyprodáno',
    soon_notice: 'Prodej vstupenek na tento termín ještě nezačal.',
    sold_out_notice: 'Všechny vstupenky na tento termín jsou vyprodané.',
    form_title: 'Upozornit mě na začátek prodeje',
    email_label: 'E-mail',
    consent:
      'Souhlasím s tím, že mi {org} může napsat e-mail, až začne prodej na tento termín, a zvát mě na své budoucí akce. Souhlas mohu kdykoli odvolat.',
    consent_no_org:
      'Souhlasím s tím, že mi pořadatel může napsat e-mail, až začne prodej na tento termín, a zvát mě na své budoucí akce. Souhlas mohu kdykoli odvolat.',
    submit: 'Upozornit mě',
    sending: 'Odesílám…',
    note: 'Pošleme vám e-mail pro potvrzení adresy. Pokud později v kterémkoli našem e-mailu kliknete na «Odhlásit», e-maily od nás na tuto adresu již nikdy nedorazí, včetně vstupenek.',
    done: 'Už to skoro máte. Poslali jsme vám e-mail: otevřete ho a potvrďte svou adresu.',
    error_invalid_email: 'Zkontrolujte prosím e-mailovou adresu.',
    error_consent_required: 'Zaškrtněte prosím políčko se souhlasem.',
    error_consent_outdated: 'Stránka je zastaralá. Načtěte ji prosím znovu a zkuste to znovu.',
    error_sales_open: 'Prodej právě začal. Načtěte stránku znovu a koupíte vstupenky.',
    error_rate_limited: 'Příliš mnoho pokusů. Zkuste to prosím později.',
    error_generic: 'Odeslání se nepodařilo. Zkuste to znovu.',
  },
  he: {
    soon_label: 'בקרוב',
    sold_out_label: 'אזל',
    soon_notice: 'מכירת הכרטיסים למועד הזה עדיין לא התחילה.',
    sold_out_notice: 'כל הכרטיסים למועד הזה אזלו.',
    form_title: 'עדכנו אותי כשהמכירה תתחיל',
    email_label: 'אימייל',
    consent:
      'אני מסכים/ה ש-{org} רשאי לשלוח לי אימייל כשהמכירה למועד הזה תתחיל, ולהזמין אותי לאירועים עתידיים שלו. אפשר לבטל את ההסכמה בכל עת.',
    consent_no_org:
      'אני מסכים/ה שהמארגן רשאי לשלוח לי אימייל כשהמכירה למועד הזה תתחיל, ולהזמין אותי לאירועים עתידיים שלו. אפשר לבטל את ההסכמה בכל עת.',
    submit: 'עדכנו אותי',
    sending: 'שולחים…',
    note: 'נשלח אליכם מייל לאישור הכתובת. אם בהמשך תלחצו על «ביטול הרשמה» באחד המיילים שלנו, מיילים מאיתנו לא יגיעו יותר לכתובת הזו, כולל כרטיסים.',
    done: 'כמעט סיימנו. שלחנו לכם מייל: פתחו אותו ואשרו את הכתובת.',
    error_invalid_email: 'אנא בדקו את כתובת האימייל.',
    error_consent_required: 'סמנו את התיבה כדי להסכים.',
    error_consent_outdated: 'הדף אינו עדכני. רעננו אותו ונסו שוב.',
    error_sales_open: 'המכירה בדיוק התחילה. רעננו את הדף כדי לרכוש כרטיסים.',
    error_rate_limited: 'יותר מדי ניסיונות. נסו שוב מאוחר יותר.',
    error_generic: 'לא הצלחנו לשלוח. נסו שוב.',
  },
  es: {
    soon_label: 'Próximamente',
    sold_out_label: 'Agotado',
    soon_notice: 'La venta de entradas para esta sesión aún no ha comenzado.',
    sold_out_notice: 'Todas las entradas para esta sesión están agotadas.',
    form_title: 'Avísame cuando empiece la venta',
    email_label: 'Correo electrónico',
    consent:
      'Acepto que {org} me escriba por correo electrónico cuando empiece la venta de esta sesión y me invite a sus próximos eventos. Puedo retirar este consentimiento en cualquier momento.',
    consent_no_org:
      'Acepto que el organizador me escriba por correo electrónico cuando empiece la venta de esta sesión y me invite a sus próximos eventos. Puedo retirar este consentimiento en cualquier momento.',
    submit: 'Avisarme',
    sending: 'Enviando…',
    note: 'Te enviaremos un correo para confirmar tu dirección. Si más adelante pulsas «Darse de baja» en cualquiera de nuestros correos, ya no recibirás nunca más correos nuestros en esta dirección, entradas incluidas.',
    done: 'Casi está. Te hemos enviado un correo: ábrelo y confirma tu dirección.',
    error_invalid_email: 'Revisa la dirección de correo electrónico.',
    error_consent_required: 'Marca la casilla para dar tu consentimiento.',
    error_consent_outdated: 'Esta página está desactualizada. Recárgala e inténtalo de nuevo.',
    error_sales_open: 'La venta acaba de abrirse. Recarga la página para comprar entradas.',
    error_rate_limited: 'Demasiados intentos. Inténtalo más tarde.',
    error_generic: 'No hemos podido enviarlo. Inténtalo de nuevo.',
  },
  de: {
    soon_label: 'Bald erhältlich',
    sold_out_label: 'Ausverkauft',
    soon_notice: 'Der Ticketverkauf für diesen Termin hat noch nicht begonnen.',
    sold_out_notice: 'Alle Tickets für diesen Termin sind ausverkauft.',
    form_title: 'Benachrichtigen Sie mich, wenn der Verkauf startet',
    email_label: 'E-Mail',
    consent:
      'Ich bin damit einverstanden, dass {org} mir per E-Mail Bescheid gibt, wenn der Verkauf für diesen Termin startet, und mich zu künftigen Veranstaltungen einlädt. Ich kann diese Einwilligung jederzeit widerrufen.',
    consent_no_org:
      'Ich bin damit einverstanden, dass der Veranstalter mir per E-Mail Bescheid gibt, wenn der Verkauf für diesen Termin startet, und mich zu künftigen Veranstaltungen einlädt. Ich kann diese Einwilligung jederzeit widerrufen.',
    submit: 'Benachrichtigen',
    sending: 'Wird gesendet…',
    note: 'Wir senden Ihnen eine E-Mail, um Ihre Adresse zu bestätigen. Wenn Sie später in einer unserer E-Mails auf «Abmelden» klicken, erreichen Sie von uns nie wieder E-Mails an diese Adresse, auch keine Tickets.',
    done: 'Fast geschafft. Wir haben Ihnen eine E-Mail geschickt: Öffnen Sie sie und bestätigen Sie Ihre Adresse.',
    error_invalid_email: 'Bitte prüfen Sie die E-Mail-Adresse.',
    error_consent_required: 'Bitte setzen Sie das Häkchen, um zuzustimmen.',
    error_consent_outdated: 'Diese Seite ist veraltet. Bitte laden Sie sie neu und versuchen Sie es erneut.',
    error_sales_open: 'Der Verkauf hat gerade begonnen. Laden Sie die Seite neu, um Tickets zu kaufen.',
    error_rate_limited: 'Zu viele Versuche. Bitte versuchen Sie es später erneut.',
    error_generic: 'Das Absenden hat nicht geklappt. Bitte versuchen Sie es erneut.',
  },
  fr: {
    soon_label: 'Bientôt disponible',
    sold_out_label: 'Complet',
    soon_notice: "La vente des billets pour cette séance n'a pas encore commencé.",
    sold_out_notice: 'Tous les billets pour cette séance sont épuisés.',
    form_title: "Prévenez-moi à l'ouverture de la vente",
    email_label: 'E-mail',
    consent:
      "J'accepte que {org} m'écrive par e-mail à l'ouverture de la vente pour cette séance et m'invite à ses prochains événements. Je peux retirer ce consentement à tout moment.",
    consent_no_org:
      "J'accepte que l'organisateur m'écrive par e-mail à l'ouverture de la vente pour cette séance et m'invite à ses prochains événements. Je peux retirer ce consentement à tout moment.",
    submit: 'Me prévenir',
    sending: 'Envoi…',
    note: "Nous vous enverrons un e-mail pour confirmer votre adresse. Si vous cliquez plus tard sur «Se désabonner» dans l'un de nos e-mails, plus aucun e-mail de notre part n'arrivera à cette adresse, billets compris.",
    done: 'Presque terminé. Nous vous avons envoyé un e-mail : ouvrez-le et confirmez votre adresse.',
    error_invalid_email: "Vérifiez l'adresse e-mail.",
    error_consent_required: 'Cochez la case pour donner votre accord.',
    error_consent_outdated: 'Cette page est obsolète. Rechargez-la et réessayez.',
    error_sales_open: 'La vente vient d\'ouvrir. Rechargez la page pour acheter des billets.',
    error_rate_limited: 'Trop de tentatives. Réessayez plus tard.',
    error_generic: "L'envoi a échoué. Réessayez.",
  },
};

/** The language subtag of a locale tag: "es-ES" -> "es". */
function langOf(locale: string): string {
  return (locale ?? '').trim().toLowerCase().split(/[-_]/)[0] ?? '';
}

/** The sales-state words for a locale ("es-ES" -> es), English when the language has none. */
export function getSaleStateStrings(locale: string): SaleStateStrings {
  return SALE_STATE_I18N[langOf(locale)] ?? SALE_STATE_I18N.en!;
}

/** The consent sentence with the organizer's name filled in, or the nameless wording when none is known. */
export function saleConsentText(locale: string, orgName: string | null | undefined): string {
  const s = getSaleStateStrings(locale);
  const name = (orgName ?? '').trim();
  return name ? s.consent.replace('{org}', name) : s.consent_no_org;
}

// ─── The request ─────────────────────────────────────────────────────────────

/** Body of POST /v1/public/feeds/{token}/sessions/{id}/sale-subscriptions. */
export interface SaleSubscriptionPayload {
  email: string;
  /** The widget's language subtag ("en", "ru", "es"): the language of the letters. */
  locale: string;
  consent: true;
  consent_version: string;
  /** The page the buyer is on: the letters link back to it when its origin is allowed. */
  page_url: string;
}

/** Response of a 202: the buyer must open the letter. */
export interface SaleSubscriptionResponse {
  status: 'check_email';
}

/** Build the request body. The e-mail is trimmed; consent is always true (an unticked box never reaches here). */
export function buildSaleSubscriptionPayload(email: string, locale: string, pageUrl: string): SaleSubscriptionPayload {
  return {
    email: email.trim(),
    locale: langOf(locale) || 'en',
    consent: true,
    consent_version: SALE_NOTIFY_CONSENT_VERSION,
    page_url: pageUrl,
  };
}

/** A shape check only (one @, a dot in the domain, no spaces): the platform has the last word. */
export function isPlausibleEmail(email: string): boolean {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim());
}

// ─── Errors ──────────────────────────────────────────────────────────────────

/** The buyer text for an error code of the endpoint, in their language. */
export function saleSubscriptionErrorText(code: string | undefined, locale: string): string {
  const s = getSaleStateStrings(locale);
  switch (code) {
    case 'subscription.invalid_email':
      return s.error_invalid_email;
    case 'subscription.consent_required':
      return s.error_consent_required;
    case 'subscription.consent_outdated':
      return s.error_consent_outdated;
    case 'subscription.sales_open':
      return s.error_sales_open;
    case 'subscription.rate_limited':
    case 'feed.rate_limited':
      return s.error_rate_limited;
    default:
      return s.error_generic;
  }
}

/** The buyer text for whatever a failed call threw; a network failure or an unknown code is the generic sentence. */
export function saleSubscriptionFailureText(err: unknown, locale: string): string {
  return saleSubscriptionErrorText(err instanceof ApiError ? err.code : undefined, locale);
}

// ─── The submit flow ─────────────────────────────────────────────────────────

export interface SaleSubscriptionForm {
  email: string;
  consent: boolean;
  locale: string;
  pageUrl: string;
}

export type SaleSubscriptionOutcome =
  | { ok: true }
  | { ok: false; field: 'email' | 'consent' | null; message: string };

/**
 * Validate the form, then send it through `post`. Nothing is sent for a bad
 * address or an unticked box. Never throws: the outcome carries the sentence
 * to show, and `field` names the input to focus (null = a general failure).
 */
export async function submitSaleSubscription(
  form: SaleSubscriptionForm,
  post: (payload: SaleSubscriptionPayload) => Promise<SaleSubscriptionResponse>,
): Promise<SaleSubscriptionOutcome> {
  const s = getSaleStateStrings(form.locale);
  if (!isPlausibleEmail(form.email)) return { ok: false, field: 'email', message: s.error_invalid_email };
  if (!form.consent) return { ok: false, field: 'consent', message: s.error_consent_required };
  try {
    await post(buildSaleSubscriptionPayload(form.email, form.locale, form.pageUrl));
    return { ok: true };
  } catch (err) {
    const code = err instanceof ApiError ? err.code : undefined;
    const field = code === 'subscription.invalid_email' ? 'email' : code === 'subscription.consent_required' ? 'consent' : null;
    return { ok: false, field, message: saleSubscriptionFailureText(err, form.locale) };
  }
}
