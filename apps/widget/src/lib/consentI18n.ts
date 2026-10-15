/**
 * consentI18n.ts — the words of the checkout consent checkbox (P2).
 *
 * Until task P3 the terms link leads to the PLATFORM terms page, so the text
 * names the seller in brackets and never claims terms written by the
 * organizer: "I accept the sale terms (seller: <legal name>) and the privacy
 * policy".
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
  /** The checkbox text: [terms] = the sale terms link, [privacy] = the privacy policy link, {seller} = the seller's legal name (inside brackets). */
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
    label: 'I accept the [terms]sale terms[/terms] (seller: {seller}) and the [privacy]privacy policy[/privacy].',
    hint: 'Tick the box to continue to payment.',
    error_required: 'Please accept the sale terms and the privacy policy, then try again.',
    new_tab: 'opens in a new tab',
  },
  ru: {
    label: 'Принимаю [terms]условия продажи[/terms] (продавец: {seller}) и [privacy]политику конфиденциальности[/privacy].',
    hint: 'Отметьте галочку, чтобы перейти к оплате.',
    error_required: 'Примите условия продажи и политику конфиденциальности и попробуйте ещё раз.',
    new_tab: 'откроется в новой вкладке',
  },
  cs: {
    label: 'Souhlasím s [terms]obchodními podmínkami[/terms] (prodávající: {seller}) a se [privacy]zásadami ochrany osobních údajů[/privacy].',
    hint: 'Zaškrtněte políčko a pokračujte k platbě.',
    error_required: 'Přijměte prosím obchodní podmínky a zásady ochrany osobních údajů a zkuste to znovu.',
    new_tab: 'otevře se na nové kartě',
  },
  he: {
    label: 'אני מאשר/ת את [terms]תנאי המכירה[/terms] (המוכר: {seller}) ואת [privacy]מדיניות הפרטיות[/privacy].',
    hint: 'סמנו את התיבה כדי להמשיך לתשלום.',
    error_required: 'יש לאשר את תנאי המכירה ואת מדיניות הפרטיות ולנסות שוב.',
    new_tab: 'ייפתח בכרטיסייה חדשה',
  },
  es: {
    label: 'Acepto las [terms]condiciones de venta[/terms] (vendedor: {seller}) y la [privacy]política de privacidad[/privacy].',
    hint: 'Marca la casilla para continuar al pago.',
    error_required: 'Acepta las condiciones de venta y la política de privacidad, y vuelve a intentarlo.',
    new_tab: 'se abre en una pestaña nueva',
  },
  de: {
    label: 'Ich akzeptiere die [terms]Verkaufsbedingungen[/terms] (Verkäufer: {seller}) und die [privacy]Datenschutzerklärung[/privacy].',
    hint: 'Setzen Sie das Häkchen, um zur Zahlung zu gelangen.',
    error_required: 'Bitte akzeptieren Sie die Verkaufsbedingungen und die Datenschutzerklärung und versuchen Sie es erneut.',
    new_tab: 'öffnet sich in einem neuen Tab',
  },
  fr: {
    label: "J'accepte les [terms]conditions de vente[/terms] (vendeur : {seller}) et la [privacy]politique de confidentialité[/privacy].",
    hint: 'Cochez la case pour passer au paiement.',
    error_required: 'Veuillez accepter les conditions de vente et la politique de confidentialité, puis réessayez.',
    new_tab: "s'ouvre dans un nouvel onglet",
  },
};

/** The consent words for a locale ("es-ES" -> es), English when the language has none. */
export function getConsentStrings(locale: string): ConsentStrings {
  const lang = (locale ?? '').trim().toLowerCase().split(/[-_]/)[0] ?? '';
  return CONSENT_I18N[lang] ?? CONSENT_I18N.en!;
}
