/**
 * consentI18n.ts — the words of the checkout consent checkbox (P2).
 *
 * Like the promo and payment-commitment words, this table carries all seven
 * buyer languages (en, ru, cs, he, es, de, fr) and is looked up by language
 * subtag ("es-ES" -> es), so a buyer reads the sale terms sentence in their
 * own language even where the rest of the widget falls back to English.
 *
 * `label` marks the two links with [terms]…[/terms] and [privacy]…[/privacy]
 * and the seller's legal name with {seller}; consentSegments (consent.ts)
 * turns it into text, links and the name. Every locale must carry every key
 * and every marker (consent.test.ts checks it).
 */

export interface ConsentStrings {
  /** The checkbox text: [terms] = the sale terms link, [privacy] = the platform policy link, {seller} = the seller's legal name. */
  label: string;
  /** Shown under the box while it is not ticked and the pay button waits for it. */
  hint: string;
  /** The platform refused the order because the box was not ticked (422 checkout.consent_required). */
  error_required: string;
  /** Screen-reader hint on a link: it opens in a new tab. */
  new_tab: string;
}

export const CONSENT_I18N: Record<string, ConsentStrings> = {
  en: {
    label: 'I accept the [terms]sale terms[/terms] of the organizer {seller} and the [privacy]platform policy[/privacy].',
    hint: 'Tick the box to continue to payment.',
    error_required: 'Please accept the sale terms of the organizer and the platform policy, then try again.',
    new_tab: 'opens in a new tab',
  },
  ru: {
    label: 'Принимаю [terms]условия продажи[/terms] организатора {seller} и [privacy]политику платформы[/privacy].',
    hint: 'Отметьте галочку, чтобы перейти к оплате.',
    error_required: 'Примите условия продажи организатора и политику платформы и попробуйте ещё раз.',
    new_tab: 'откроется в новой вкладке',
  },
  cs: {
    label: 'Souhlasím s [terms]obchodními podmínkami[/terms] pořadatele {seller} a se [privacy]zásadami platformy[/privacy].',
    hint: 'Zaškrtněte políčko a pokračujte k platbě.',
    error_required: 'Přijměte prosím obchodní podmínky pořadatele a zásady platformy a zkuste to znovu.',
    new_tab: 'otevře se na nové kartě',
  },
  he: {
    label: 'אני מאשר/ת את [terms]תנאי המכירה[/terms] של המארגן {seller} ואת [privacy]מדיניות הפלטפורמה[/privacy].',
    hint: 'סמנו את התיבה כדי להמשיך לתשלום.',
    error_required: 'יש לאשר את תנאי המכירה של המארגן ואת מדיניות הפלטפורמה ולנסות שוב.',
    new_tab: 'ייפתח בכרטיסייה חדשה',
  },
  es: {
    label: 'Acepto las [terms]condiciones de venta[/terms] del organizador {seller} y la [privacy]política de la plataforma[/privacy].',
    hint: 'Marca la casilla para continuar al pago.',
    error_required: 'Acepta las condiciones de venta del organizador y la política de la plataforma, y vuelve a intentarlo.',
    new_tab: 'se abre en una pestaña nueva',
  },
  de: {
    label: 'Ich akzeptiere die [terms]Verkaufsbedingungen[/terms] des Veranstalters {seller} und die [privacy]Richtlinie der Plattform[/privacy].',
    hint: 'Setzen Sie das Häkchen, um zur Zahlung zu gelangen.',
    error_required: 'Bitte akzeptieren Sie die Verkaufsbedingungen des Veranstalters und die Richtlinie der Plattform und versuchen Sie es erneut.',
    new_tab: 'öffnet sich in einem neuen Tab',
  },
  fr: {
    label: "J'accepte les [terms]conditions de vente[/terms] de l'organisateur {seller} et la [privacy]politique de la plateforme[/privacy].",
    hint: 'Cochez la case pour passer au paiement.',
    error_required: "Veuillez accepter les conditions de vente de l'organisateur et la politique de la plateforme, puis réessayez.",
    new_tab: "s'ouvre dans un nouvel onglet",
  },
};

/** The consent words for a locale ("es-ES" -> es), English when the language has none. */
export function getConsentStrings(locale: string): ConsentStrings {
  const lang = (locale ?? '').trim().toLowerCase().split(/[-_]/)[0] ?? '';
  return CONSENT_I18N[lang] ?? CONSENT_I18N.en!;
}
