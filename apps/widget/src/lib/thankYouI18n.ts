/**
 * thankYouI18n.ts - the words of the blocks under a paid order. Seven buyer
 * languages looked up by language subtag ("es-ES" -> es). Stored as one array
 * per language in the order of KEYS (smaller than seven keyed objects). The
 * copy button and its notes reuse the referral words.
 */

export const KEYS = [
  'share_title',
  'follow_title',
  'news_title',
  'news_consent',
  'news_button',
  'news_pending',
  'news_error',
  'related_title',
] as const;

export type ThankYouStrings = Record<(typeof KEYS)[number], string>;

const TABLE: Record<string, string[]> = {
  en: [
    'Tell your friends',
    'Follow us',
    'Stay in the loop',
    "I agree to receive news and offers from {org} at this order's e-mail.",
    'Subscribe',
    'Check your mail and confirm the subscription.',
    'Could not subscribe.',
    'You may also like',
  ],
  ru: [
    'Расскажите друзьям',
    'Подписывайтесь на нас',
    'Будьте в курсе',
    'Получать новости и предложения от {org} на e-mail заказа',
    'Подписаться',
    'Проверьте почту и подтвердите подписку.',
    'Не удалось подписаться.',
    'Вам может понравиться',
  ],
  cs: [
    'Řekněte to přátelům',
    'Sledujte nás',
    'Buďte v obraze',
    'Souhlasím se zasíláním novinek a nabídek od {org} na e-mail objednávky.',
    'Odebírat',
    'Zkontrolujte poštu a potvrďte odběr.',
    'Odběr se nepodařil.',
    'Mohlo by se vám líbit',
  ],
  he: [
    'ספרו לחברים',
    'עקבו אחרינו',
    'הישארו מעודכנים',
    'אני מסכים/ה לקבל חדשות והצעות מ-{org} לאימייל של ההזמנה.',
    'הרשמה',
    'בדקו את תיבת הדואר ואשרו את ההרשמה.',
    'ההרשמה נכשלה.',
    'אולי יעניין אתכם גם',
  ],
  es: [
    'Cuéntaselo a tus amigos',
    'Síguenos',
    'Mantente al día',
    'Acepto recibir novedades y ofertas de {org} en el correo del pedido.',
    'Suscribirme',
    'Revisa tu correo y confirma la suscripción.',
    'No se pudo suscribir.',
    'También te puede gustar',
  ],
  de: [
    'Erzählen Sie es Ihren Freunden',
    'Folgen Sie uns',
    'Bleiben Sie auf dem Laufenden',
    'Ich möchte Neuigkeiten und Angebote von {org} an die E-Mail der Bestellung erhalten.',
    'Abonnieren',
    'Prüfen Sie Ihr Postfach und bestätigen Sie das Abonnement.',
    'Abonnieren nicht möglich.',
    'Das könnte Ihnen auch gefallen',
  ],
  fr: [
    'Parlez-en à vos amis',
    'Suivez-nous',
    'Restez informé',
    "J'accepte de recevoir des nouvelles et offres de {org} à l'e-mail de la commande.",
    "S'abonner",
    "Consultez votre messagerie et confirmez l'abonnement.",
    'Abonnement impossible.',
    'Vous pourriez aussi aimer',
  ],
};

export const THANK_YOU_LANGS = Object.keys(TABLE);

export function getThankYouStrings(locale: string): ThankYouStrings {
  const lang = (locale ?? '').trim().toLowerCase().split(/[-_]/)[0] ?? '';
  const row = TABLE[lang] ?? TABLE.en!;
  const out = {} as ThankYouStrings;
  KEYS.forEach((k, i) => {
    out[k] = row[i]!;
  });
  return out;
}

/** Row length per language (the parity test). */
export function rowLengths(): number[] {
  return Object.values(TABLE).map((r) => r.length);
}
