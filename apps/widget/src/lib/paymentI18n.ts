/**
 * paymentI18n.ts — words of the embedded payment screen (spec 65 §4, §6.1)
 * in every widget locale (en / ru / cs / he / es), and the translation of a
 * Stripe decline code into one of five groups.
 *
 * Kept apart from CHECKOUT_I18N so the payment screen stays one reviewable
 * piece; `paymentI18n.test.ts` fails when a locale misses a key or leaves an
 * English text in place.
 */

import type { DeclineGroup } from './paymentSteps.js';

export interface PaymentStrings {
  title: string;
  loading: string;
  /** "{time}" is a clock time in the buyer's zone. */
  held_until: string;
  /** Same line in the last five minutes; shown prominently. */
  held_until_warn: string;
  /** Hold ran out: the form is disabled. */
  time_up: string;
  start_again: string;
  /** After a decline, under the group text. "{time}" as above. */
  try_another: string;
  /** Pay button while confirm() runs. */
  paying: string;
  /** Waiting for the platform to confirm the payment. */
  confirming: string;
  /** Stripe accepted the payment; the platform has not confirmed it yet. */
  payment_received: string;
  /** Returned from the bank's own page; checking. */
  checking_return: string;
  /** Stripe.js did not load or start; the platform page is being opened. */
  opening_hosted: string;
  /** Hosted fallback refused: the order cannot be paid here any more. */
  cannot_open: string;
  /** Confirmed too slowly (the poll gave up) while the money may still arrive. */
  still_confirming: string;
  /** Link "Back" to the contact screen. */
  back: string;
  organizer: string;
  express_label: string;
  or_pay_by_card: string;
  /** Decline groups. */
  decline_no_funds: string;
  decline_bank_declined: string;
  decline_input_error: string;
  decline_auth_failed: string;
  decline_other: string;
}

export const PAYMENT_I18N: Record<string, PaymentStrings> = {
  en: {
    title: 'Payment',
    loading: 'Loading the payment form…',
    held_until: 'Your seats are held until {time}',
    held_until_warn: 'Hurry: your seats are held only until {time}',
    time_up: 'Time is up: your seats are no longer held.',
    start_again: 'Start again',
    try_another: 'Your order is held until {time}. Try another card.',
    paying: 'Paying…',
    confirming: 'Confirming your payment…',
    payment_received: 'Payment received. Waiting for confirmation…',
    checking_return: 'Checking your payment…',
    opening_hosted: 'Opening the secure payment page…',
    cannot_open: 'The payment form could not be opened. Please start again.',
    still_confirming: 'Your payment is still being confirmed. Your tickets will be e-mailed to you; you can close this page.',
    back: 'Back',
    organizer: 'Organizer: {name}',
    express_label: 'Pay quickly',
    or_pay_by_card: 'Or pay by card',
    decline_no_funds: 'Not enough funds on the card.',
    decline_bank_declined: 'Your bank declined the payment.',
    decline_input_error: 'Check the card details: a number, date or code looks wrong.',
    decline_auth_failed: 'The bank could not verify the payment.',
    decline_other: 'The payment did not go through.',
  },
  ru: {
    title: 'Оплата',
    loading: 'Загружаем форму оплаты…',
    held_until: 'Места за вами до {time}',
    held_until_warn: 'Поторопитесь: места за вами только до {time}',
    time_up: 'Время вышло: места больше не удерживаются.',
    start_again: 'Начать заново',
    try_another: 'Заказ держится до {time}. Попробуйте другую карту.',
    paying: 'Оплачиваем…',
    confirming: 'Подтверждаем оплату…',
    payment_received: 'Платёж получен. Ждём подтверждения…',
    checking_return: 'Проверяем оплату…',
    opening_hosted: 'Открываем защищённую страницу оплаты…',
    cannot_open: 'Не удалось открыть форму оплаты. Пожалуйста, начните заново.',
    still_confirming: 'Оплата ещё подтверждается. Билеты придут на вашу почту, эту страницу можно закрыть.',
    back: 'Назад',
    organizer: 'Организатор: {name}',
    express_label: 'Быстрая оплата',
    or_pay_by_card: 'Или оплатите картой',
    decline_no_funds: 'На карте недостаточно средств.',
    decline_bank_declined: 'Банк отклонил платёж.',
    decline_input_error: 'Проверьте данные карты: номер, срок или код введены неверно.',
    decline_auth_failed: 'Не прошла проверка банка.',
    decline_other: 'Платёж не прошёл.',
  },
  cs: {
    title: 'Platba',
    loading: 'Načítáme platební formulář…',
    held_until: 'Místa jsou pro vás držena do {time}',
    held_until_warn: 'Pospěšte si: místa jsou držena jen do {time}',
    time_up: 'Čas vypršel: místa již nejsou držena.',
    start_again: 'Začít znovu',
    try_another: 'Objednávka je držena do {time}. Zkuste jinou kartu.',
    paying: 'Platíme…',
    confirming: 'Potvrzujeme platbu…',
    payment_received: 'Platba přijata. Čekáme na potvrzení…',
    checking_return: 'Kontrolujeme platbu…',
    opening_hosted: 'Otevíráme zabezpečenou platební stránku…',
    cannot_open: 'Platební formulář se nepodařilo otevřít. Začněte prosím znovu.',
    still_confirming: 'Platba se stále potvrzuje. Vstupenky vám dorazí e-mailem, tuto stránku můžete zavřít.',
    back: 'Zpět',
    organizer: 'Pořadatel: {name}',
    express_label: 'Rychlá platba',
    or_pay_by_card: 'Nebo zaplaťte kartou',
    decline_no_funds: 'Na kartě není dostatek prostředků.',
    decline_bank_declined: 'Banka platbu zamítla.',
    decline_input_error: 'Zkontrolujte údaje karty: číslo, platnost nebo kód jsou chybné.',
    decline_auth_failed: 'Banka platbu neověřila.',
    decline_other: 'Platba neproběhla.',
  },
  he: {
    title: 'תשלום',
    loading: 'טוען את טופס התשלום…',
    held_until: 'המקומות שמורים עבורך עד {time}',
    held_until_warn: 'כדאי למהר: המקומות שמורים עבורך רק עד {time}',
    time_up: 'הזמן עבר: המקומות כבר לא שמורים.',
    start_again: 'להתחיל מחדש',
    try_another: 'ההזמנה שמורה עד {time}. נסו כרטיס אחר.',
    paying: 'מבצע תשלום…',
    confirming: 'מאשר את התשלום…',
    payment_received: 'התשלום התקבל. ממתינים לאישור…',
    checking_return: 'בודק את התשלום…',
    opening_hosted: 'פותח את דף התשלום המאובטח…',
    cannot_open: 'לא ניתן היה לפתוח את טופס התשלום. נא להתחיל מחדש.',
    still_confirming: 'התשלום עדיין מאושר. הכרטיסים יישלחו אליכם בדוא"ל, אפשר לסגור את הדף.',
    back: 'חזרה',
    organizer: 'מארגן: {name}',
    express_label: 'תשלום מהיר',
    or_pay_by_card: 'או שלמו בכרטיס',
    decline_no_funds: 'אין מספיק כסף בכרטיס.',
    decline_bank_declined: 'הבנק דחה את התשלום.',
    decline_input_error: 'בדקו את פרטי הכרטיס: מספר, תוקף או קוד שגויים.',
    decline_auth_failed: 'הבנק לא אימת את התשלום.',
    decline_other: 'התשלום לא הושלם.',
  },
  es: {
    title: 'Pago',
    loading: 'Cargando el formulario de pago…',
    held_until: 'Tus asientos están reservados hasta las {time}',
    held_until_warn: 'Date prisa: tus asientos están reservados solo hasta las {time}',
    time_up: 'Se acabó el tiempo: los asientos ya no están reservados.',
    start_again: 'Empezar de nuevo',
    try_another: 'Tu pedido está reservado hasta las {time}. Prueba con otra tarjeta.',
    paying: 'Pagando…',
    confirming: 'Confirmando tu pago…',
    payment_received: 'Pago recibido. Esperando la confirmación…',
    checking_return: 'Comprobando tu pago…',
    opening_hosted: 'Abriendo la página de pago segura…',
    cannot_open: 'No se ha podido abrir el formulario de pago. Empieza de nuevo.',
    still_confirming: 'Tu pago aún se está confirmando. Recibirás las entradas por correo, puedes cerrar esta página.',
    back: 'Atrás',
    organizer: 'Organizador: {name}',
    express_label: 'Pago rápido',
    or_pay_by_card: 'O paga con tarjeta',
    decline_no_funds: 'No hay fondos suficientes en la tarjeta.',
    decline_bank_declined: 'Tu banco ha rechazado el pago.',
    decline_input_error: 'Revisa los datos de la tarjeta: el número, la fecha o el código parecen incorrectos.',
    decline_auth_failed: 'El banco no ha podido verificar el pago.',
    decline_other: 'El pago no se ha completado.',
  },
};

/** Words of the payment screen for a locale ("he-IL" -> he), English otherwise. */
export function getPaymentStrings(locale: string): PaymentStrings {
  const lang = (locale ?? '').trim().toLowerCase().split(/[-_]/)[0] ?? '';
  return PAYMENT_I18N[lang] ?? PAYMENT_I18N.en!;
}

// ─── Decline groups (§6.1) ────────────────────────────────────────────────────

const NO_FUNDS = new Set(['insufficient_funds']);
const BANK_DECLINED = new Set([
  'card_declined',
  'do_not_honor',
  'fraudulent',
  'lost_card',
  'stolen_card',
]);
const INPUT_ERROR = new Set([
  'expired_card',
  'incorrect_cvc',
  'invalid_cvc',
  'incorrect_number',
  'invalid_number',
  'invalid_expiry_month',
  'invalid_expiry_year',
  'invalid_expiry_month_past',
  'invalid_expiry_year_past',
  'incomplete_number',
  'incomplete_cvc',
  'incomplete_expiry',
]);
const AUTH_FAILED = new Set([
  'authentication_required',
  'authentication_failed',
  'payment_intent_authentication_failure',
]);

function groupOfOne(code: string): DeclineGroup | null {
  const c = code.trim().toLowerCase();
  if (!c) return null;
  if (NO_FUNDS.has(c)) return 'no_funds';
  if (BANK_DECLINED.has(c)) return 'bank_declined';
  if (INPUT_ERROR.has(c) || c.startsWith('invalid_expiry_')) return 'input_error';
  if (AUTH_FAILED.has(c)) return 'auth_failed';
  return null;
}

/**
 * Translates a Stripe error into a decline group. Stripe puts the reason in
 * `code` (`card_declined`) and, for a generic decline, the finer reason in
 * `decline_code` (`insufficient_funds`); the finer one wins when it is known.
 */
export function declineGroup(err: {
  code?: string | null;
  decline_code?: string | null;
  declineCode?: string | null;
}): DeclineGroup {
  return (
    groupOfOne(err.decline_code ?? err.declineCode ?? '') ??
    groupOfOne(err.code ?? '') ??
    'other'
  );
}

/** The buyer-facing sentence for a decline group. */
export function declineText(group: DeclineGroup, s: PaymentStrings): string {
  switch (group) {
    case 'no_funds':
      return s.decline_no_funds;
    case 'bank_declined':
      return s.decline_bank_declined;
    case 'input_error':
      return s.decline_input_error;
    case 'auth_failed':
      return s.decline_auth_failed;
    default:
      return s.decline_other;
  }
}

/** Fills `{key}` placeholders. */
export function fill(template: string, vars: Record<string, string>): string {
  return template.replace(/\{(\w+)\}/g, (_, k: string) => vars[k] ?? `{${k}}`);
}

/**
 * The clock time of the hold's end in the buyer's zone, as the screen prints
 * it ("21:45"). Empty for an unparsable date.
 */
export function formatHoldTime(iso: string | null | undefined, locale: string): string {
  if (!iso) return '';
  const ms = Date.parse(iso);
  if (!Number.isFinite(ms)) return '';
  try {
    return new Intl.DateTimeFormat(locale === 'en' ? 'en-GB' : locale, {
      hour: '2-digit',
      minute: '2-digit',
      hourCycle: 'h23',
    }).format(new Date(ms));
  } catch {
    return '';
  }
}
