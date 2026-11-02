/**
 * marketingI18n.ts — the words of the mailing tick and the reminder line
 * (spec 51-1), in all seven buyer languages (en, ru, cs, he, es, de, fr),
 * looked up by language subtag like consentI18n.ts.
 *
 * `label` carries {org} (the organizer's name), `discount` carries
 * {discount} ("10 %"). Every locale must carry every key and every marker
 * (marketing.test.ts checks it).
 */

export interface MarketingStrings {
  /** The tick: "Send me news and offers from {org}." */
  label: string;
  /** The tick when the organizer has no name on file. */
  label_no_org: string;
  /** Added to the tick when it earns a discount on this order. */
  discount: string;
  /** Small print under the tick: double opt-in and the way out. */
  note: string;
  /** Under the e-mail field when the organizer sends the one reminder. */
  reminder: string;
  /** 422 marketing.discount_used: the reward is not given twice. */
  discount_used: string;
  /** 422 marketing.discount_unavailable: the reward's uses are all taken. */
  discount_unavailable: string;
  /** 422 marketing.consent_outdated: the page shows an old text. */
  outdated: string;
}

export const MARKETING_I18N: Record<string, MarketingStrings> = {
  en: {
    label: 'Send me news and offers from {org}.',
    label_no_org: 'Send me news and offers from the organizer.',
    discount: 'You get {discount} off this order.',
    note: 'We will ask you to confirm by e-mail. You can stop the mailings at any time.',
    reminder: 'If the payment is not finished, we will remind you once by e-mail.',
    discount_used: 'This e-mail address has already received the subscriber discount. Press the pay button again to pay the regular price.',
    discount_unavailable: 'The subscriber discount has run out. Press the pay button again to pay the regular price.',
    outdated: 'The page is out of date. Please reload it and try again.',
  },
  ru: {
    label: 'Присылайте мне новости и предложения от {org}.',
    label_no_org: 'Присылайте мне новости и предложения организатора.',
    discount: 'Скидка {discount} на этот заказ.',
    note: 'Мы попросим подтвердить адрес по e-mail. От рассылки можно отказаться в любой момент.',
    reminder: 'Если оплата не завершится, мы один раз напомним о заказе по e-mail.',
    discount_used: 'Этот адрес уже получал скидку для подписчиков. Нажмите кнопку оплаты ещё раз, чтобы оплатить по обычной цене.',
    discount_unavailable: 'Скидка для подписчиков закончилась. Нажмите кнопку оплаты ещё раз, чтобы оплатить по обычной цене.',
    outdated: 'Страница устарела. Обновите её и попробуйте ещё раз.',
  },
  cs: {
    label: 'Posílejte mi novinky a nabídky od {org}.',
    label_no_org: 'Posílejte mi novinky a nabídky pořadatele.',
    discount: 'Na tuto objednávku získáte slevu {discount}.',
    note: 'Adresu vás požádáme potvrdit e-mailem. Zasílání můžete kdykoli ukončit.',
    reminder: 'Pokud platbu nedokončíte, jednou vám ji připomeneme e-mailem.',
    discount_used: 'Tato e-mailová adresa už slevu pro odběratele získala. Stiskněte tlačítko platby znovu a zaplaťte běžnou cenu.',
    discount_unavailable: 'Sleva pro odběratele je vyčerpaná. Stiskněte tlačítko platby znovu a zaplaťte běžnou cenu.',
    outdated: 'Stránka je zastaralá. Načtěte ji prosím znovu a zkuste to ještě jednou.',
  },
  he: {
    label: 'שלחו לי עדכונים והצעות מ־{org}.',
    label_no_org: 'שלחו לי עדכונים והצעות מהמארגן.',
    discount: 'הנחה של {discount} על ההזמנה הזו.',
    note: 'נבקש לאשר את הכתובת במייל. אפשר להפסיק את הדיוור בכל עת.',
    reminder: 'אם התשלום לא יושלם, נזכיר לכם פעם אחת במייל.',
    discount_used: 'כתובת המייל הזו כבר קיבלה את הנחת המנויים. לחצו שוב על כפתור התשלום כדי לשלם במחיר הרגיל.',
    discount_unavailable: 'הנחת המנויים אזלה. לחצו שוב על כפתור התשלום כדי לשלם במחיר הרגיל.',
    outdated: 'הדף אינו מעודכן. רעננו אותו ונסו שוב.',
  },
  es: {
    label: 'Envíame novedades y ofertas de {org}.',
    label_no_org: 'Envíame novedades y ofertas del organizador.',
    discount: 'Obtienes un {discount} de descuento en este pedido.',
    note: 'Te pediremos que confirmes tu correo. Puedes dejar de recibir los envíos cuando quieras.',
    reminder: 'Si el pago no se completa, te lo recordaremos una vez por correo.',
    discount_used: 'Este correo ya recibió el descuento para suscriptores. Pulsa de nuevo el botón de pago para pagar el precio normal.',
    discount_unavailable: 'El descuento para suscriptores se ha agotado. Pulsa de nuevo el botón de pago para pagar el precio normal.',
    outdated: 'La página está desactualizada. Vuelve a cargarla e inténtalo de nuevo.',
  },
  de: {
    label: 'Senden Sie mir Neuigkeiten und Angebote von {org}.',
    label_no_org: 'Senden Sie mir Neuigkeiten und Angebote des Veranstalters.',
    discount: 'Sie erhalten {discount} Rabatt auf diese Bestellung.',
    note: 'Wir bitten Sie, Ihre Adresse per E-Mail zu bestätigen. Sie können die Mailings jederzeit beenden.',
    reminder: 'Wenn die Zahlung nicht abgeschlossen wird, erinnern wir Sie einmal per E-Mail.',
    discount_used: 'Diese E-Mail-Adresse hat den Abonnentenrabatt bereits erhalten. Drücken Sie erneut auf Bezahlen, um den regulären Preis zu zahlen.',
    discount_unavailable: 'Der Abonnentenrabatt ist aufgebraucht. Drücken Sie erneut auf Bezahlen, um den regulären Preis zu zahlen.',
    outdated: 'Die Seite ist veraltet. Bitte laden Sie sie neu und versuchen Sie es noch einmal.',
  },
  fr: {
    label: 'Envoyez-moi les nouvelles et les offres de {org}.',
    label_no_org: "Envoyez-moi les nouvelles et les offres de l'organisateur.",
    discount: 'Vous obtenez {discount} de réduction sur cette commande.',
    note: 'Nous vous demanderons de confirmer votre adresse par e-mail. Vous pouvez arrêter les envois à tout moment.',
    reminder: "Si le paiement n'est pas terminé, nous vous le rappellerons une fois par e-mail.",
    discount_used: 'Cette adresse a déjà reçu la réduction abonnés. Appuyez de nouveau sur le bouton de paiement pour payer le prix normal.',
    discount_unavailable: 'La réduction abonnés est épuisée. Appuyez de nouveau sur le bouton de paiement pour payer le prix normal.',
    outdated: "La page n'est plus à jour. Rechargez-la et réessayez.",
  },
};

/** The words for a locale ("es-ES" -> es), English when the language has none. */
export function getMarketingStrings(locale: string): MarketingStrings {
  const lang = (locale ?? '').trim().toLowerCase().split(/[-_]/)[0] ?? '';
  return MARKETING_I18N[lang] ?? MARKETING_I18N.en!;
}
