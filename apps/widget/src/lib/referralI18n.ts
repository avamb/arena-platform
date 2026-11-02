/**
 * referralI18n.ts — the words of the "Invite a friend" block on a paid order.
 *
 * Like the promo and consent words, this table carries all seven buyer
 * languages (en, ru, cs, he, es, de, fr) and is looked up by language subtag
 * ("es-ES" -> es), so the offer reads in the buyer's own language even where
 * the rest of the widget falls back to English. Every locale must carry every
 * key (`ReferralStrings` is a closed interface; widget_referral.test.ts checks
 * the placeholders).
 */

export interface ReferralStrings {
  /** Heading of the block. */
  title: string;
  /** The offer. {discount} = percent off for the friend, {cashback} = the most ("up to") that comes back to the buyer, in percent. */
  text: string;
  /** Label above the big code. */
  code_label: string;
  /** Button that copies the link to share. */
  copy_link: string;
  /** Button that copies the code (used when the page has no address to share). */
  copy_code: string;
  /** Status line after a successful copy. */
  copied: string;
  /** Status line when the browser refused to copy. */
  copy_failed: string;
  /** Button that opens the device's share sheet. */
  share: string;
  /** The message handed to the share sheet. {discount} and {code}. */
  share_text: string;
}

export const REFERRAL_I18N: Record<string, ReferralStrings> = {
  en: {
    title: 'Invite a friend',
    text: 'Your friend gets {discount}% off with your code, and up to {cashback}% of their purchase comes back to your card.',
    code_label: 'Your code',
    copy_link: 'Copy link',
    copy_code: 'Copy code',
    copied: 'Copied. Send it to a friend.',
    copy_failed: 'Could not copy. Select the code and copy it by hand.',
    share: 'Share',
    share_text: 'Get {discount}% off tickets with my code {code}.',
  },
  ru: {
    title: 'Пригласите друга',
    text: 'Друг получит скидку {discount}% по вашему коду, а вам вернётся до {cashback}% от его покупки на карту.',
    code_label: 'Ваш код',
    copy_link: 'Скопировать ссылку',
    copy_code: 'Скопировать код',
    copied: 'Скопировано. Отправьте другу.',
    copy_failed: 'Не удалось скопировать. Выделите код и скопируйте его вручную.',
    share: 'Поделиться',
    share_text: 'Скидка {discount}% на билеты по моему коду {code}.',
  },
  cs: {
    title: 'Pozvěte přítele',
    text: 'Váš přítel získá slevu {discount} % s vaším kódem a až {cashback} % z jeho nákupu se vám vrátí na kartu.',
    code_label: 'Váš kód',
    copy_link: 'Kopírovat odkaz',
    copy_code: 'Kopírovat kód',
    copied: 'Zkopírováno. Pošlete to příteli.',
    copy_failed: 'Kopírování se nezdařilo. Označte kód a zkopírujte ho ručně.',
    share: 'Sdílet',
    share_text: 'Sleva {discount} % na vstupenky s mým kódem {code}.',
  },
  he: {
    title: 'הזמינו חבר',
    text: 'החבר שלכם מקבל {discount}% הנחה עם הקוד שלכם, ועד {cashback}% מהרכישה שלו יחזרו לכרטיס שלכם.',
    code_label: 'הקוד שלכם',
    copy_link: 'העתקת קישור',
    copy_code: 'העתקת קוד',
    copied: 'הועתק. שלחו אותו לחבר.',
    copy_failed: 'ההעתקה לא הצליחה. סמנו את הקוד והעתיקו אותו ידנית.',
    share: 'שיתוף',
    share_text: '{discount}% הנחה על כרטיסים עם הקוד שלי {code}.',
  },
  es: {
    title: 'Invita a un amigo',
    text: 'Tu amigo recibe un {discount}% de descuento con tu código y hasta el {cashback}% de su compra vuelve a tu tarjeta.',
    code_label: 'Tu código',
    copy_link: 'Copiar enlace',
    copy_code: 'Copiar código',
    copied: 'Copiado. Envíaselo a un amigo.',
    copy_failed: 'No se pudo copiar. Selecciona el código y cópialo a mano.',
    share: 'Compartir',
    share_text: '{discount}% de descuento en entradas con mi código {code}.',
  },
  de: {
    title: 'Freund einladen',
    text: 'Ihr Freund erhält {discount}% Rabatt mit Ihrem Code, und bis zu {cashback}% seines Einkaufs gehen auf Ihre Karte zurück.',
    code_label: 'Ihr Code',
    copy_link: 'Link kopieren',
    copy_code: 'Code kopieren',
    copied: 'Kopiert. Schicken Sie es einem Freund.',
    copy_failed: 'Kopieren hat nicht geklappt. Markieren Sie den Code und kopieren Sie ihn von Hand.',
    share: 'Teilen',
    share_text: '{discount}% Rabatt auf Tickets mit meinem Code {code}.',
  },
  fr: {
    title: 'Invitez un ami',
    text: 'Votre ami obtient {discount} % de réduction avec votre code, et jusqu\'à {cashback} % de son achat reviennent sur votre carte.',
    code_label: 'Votre code',
    copy_link: 'Copier le lien',
    copy_code: 'Copier le code',
    copied: 'Copié. Envoyez-le à un ami.',
    copy_failed: 'La copie a échoué. Sélectionnez le code et copiez-le à la main.',
    share: 'Partager',
    share_text: '{discount} % de réduction sur les billets avec mon code {code}.',
  },
};

/** The words for a locale ("es-ES" -> es), English when the language has none. */
export function getReferralStrings(locale: string): ReferralStrings {
  const lang = (locale ?? '').trim().toLowerCase().split(/[-_]/)[0] ?? '';
  return REFERRAL_I18N[lang] ?? REFERRAL_I18N.en!;
}
