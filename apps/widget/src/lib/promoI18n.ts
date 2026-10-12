/**
 * promoI18n.ts — the words of the promo code in the cart sheet.
 *
 * Kept apart from CHECKOUT_I18N for the reason PAYMENT_COMMIT_I18N is: that
 * table is the widget's complete-or-nothing UI language set (en/ru/cs/he), but
 * money wording has to read in the buyer's own language even where the rest of
 * the widget still falls back to English. So this table also carries es, de and
 * fr, and it is looked up by language subtag ("es-ES" -> es).
 *
 * Every locale must carry every key (`PromoStrings` is a closed interface and
 * `TestEveryLocale…` in promo_i18n.test.ts checks the placeholders match).
 */

export interface PromoStrings {
  /** Label of the promo code field. */
  label: string;
  /** Placeholder inside the field. */
  placeholder: string;
  /** Button that checks the typed code. */
  apply: string;
  /** Button that takes an applied code off. */
  remove: string;
  /** Status line after a code is applied. {code} is the code as typed. */
  applied: string;
  /** Status line after a code is taken off. */
  removed: string;
  /** Status line while the code is being checked. */
  checking: string;
  /** Row label for the discount. */
  discount_label: string;
  /** Row label for the price of the tickets before the discount. */
  subtotal_label: string;
  /** The code does not fit this order (unknown, expired, other event, other currency). */
  error_not_applicable: string;
  /** The order is below the minimum amount of the code. */
  error_minimum: string;
  /** The code has no uses left. */
  error_exhausted: string;
  /** This buyer used the code the maximum number of times. */
  error_per_customer: string;
  /** The check itself failed (network, timeout, server). */
  error_generic: string;
  /** Apply was pressed with an empty field. */
  error_empty: string;
  /** Too many attempts in a short time. */
  error_rate_limited: string;
  /** The code is valid but takes nothing off this order. */
  error_no_discount: string;
}

export const PROMO_I18N: Record<string, PromoStrings> = {
  en: {
    label: 'Promo code',
    placeholder: 'Enter code',
    apply: 'Apply',
    remove: 'Remove',
    applied: 'Code {code} applied.',
    removed: 'Code removed.',
    checking: 'Checking the code…',
    discount_label: 'Discount',
    subtotal_label: 'Tickets',
    error_not_applicable: 'This code does not work for this order.',
    error_minimum: 'The order is too small for this code.',
    error_exhausted: 'This code has no uses left.',
    error_per_customer: 'You already used this code.',
    error_generic: 'Cannot check the code now. Try again.',
    error_empty: 'Type a code first.',
    error_rate_limited: 'Too many attempts. Wait a minute and try again.',
    error_no_discount: 'This code gives no discount on this order.',
  },
  ru: {
    label: 'Промокод',
    placeholder: 'Введите код',
    apply: 'Применить',
    remove: 'Убрать',
    applied: 'Код {code} применён.',
    removed: 'Код убран.',
    checking: 'Проверяем код…',
    discount_label: 'Скидка',
    subtotal_label: 'Билеты',
    error_not_applicable: 'Этот код не подходит для заказа.',
    error_minimum: 'Сумма заказа мала для этого кода.',
    error_exhausted: 'Этот код больше нельзя использовать.',
    error_per_customer: 'Вы уже использовали этот код.',
    error_generic: 'Сейчас нельзя проверить код. Попробуйте ещё раз.',
    error_empty: 'Сначала введите код.',
    error_rate_limited: 'Слишком много попыток. Подождите минуту и попробуйте снова.',
    error_no_discount: 'Этот код не даёт скидки на этот заказ.',
  },
  cs: {
    label: 'Slevový kód',
    placeholder: 'Zadejte kód',
    apply: 'Použít',
    remove: 'Odebrat',
    applied: 'Kód {code} byl použit.',
    removed: 'Kód byl odebrán.',
    checking: 'Kontrolujeme kód…',
    discount_label: 'Sleva',
    subtotal_label: 'Vstupenky',
    error_not_applicable: 'Tento kód pro objednávku neplatí.',
    error_minimum: 'Objednávka je pro tento kód příliš malá.',
    error_exhausted: 'Tento kód už nelze použít.',
    error_per_customer: 'Tento kód jste už použili.',
    error_generic: 'Kód teď nelze zkontrolovat. Zkuste to znovu.',
    error_empty: 'Nejdřív zadejte kód.',
    error_rate_limited: 'Příliš mnoho pokusů. Počkejte minutu a zkuste to znovu.',
    error_no_discount: 'Tento kód na tuto objednávku nedává žádnou slevu.',
  },
  he: {
    label: 'קוד קופון',
    placeholder: 'הזינו קוד',
    apply: 'החל',
    remove: 'הסר',
    applied: 'הקוד {code} הוחל.',
    removed: 'הקוד הוסר.',
    checking: 'בודקים את הקוד…',
    discount_label: 'הנחה',
    subtotal_label: 'כרטיסים',
    error_not_applicable: 'הקוד הזה לא מתאים להזמנה.',
    error_minimum: 'ההזמנה קטנה מדי לקוד הזה.',
    error_exhausted: 'לקוד הזה לא נותרו שימושים.',
    error_per_customer: 'כבר השתמשתם בקוד הזה.',
    error_generic: 'אי אפשר לבדוק את הקוד כרגע. נסו שוב.',
    error_empty: 'הזינו קוד קודם.',
    error_rate_limited: 'יותר מדי ניסיונות. המתינו דקה ונסו שוב.',
    error_no_discount: 'הקוד הזה לא נותן הנחה על ההזמנה הזו.',
  },
  es: {
    label: 'Código promocional',
    placeholder: 'Introduce el código',
    apply: 'Aplicar',
    remove: 'Quitar',
    applied: 'Código {code} aplicado.',
    removed: 'Código quitado.',
    checking: 'Comprobando el código…',
    discount_label: 'Descuento',
    subtotal_label: 'Entradas',
    error_not_applicable: 'Este código no sirve para este pedido.',
    error_minimum: 'El pedido es demasiado pequeño para este código.',
    error_exhausted: 'Este código ya no tiene usos disponibles.',
    error_per_customer: 'Ya has usado este código.',
    error_generic: 'No se puede comprobar el código ahora. Inténtalo de nuevo.',
    error_empty: 'Escribe primero un código.',
    error_rate_limited: 'Demasiados intentos. Espera un minuto e inténtalo de nuevo.',
    error_no_discount: 'Este código no aplica ningún descuento a este pedido.',
  },
  de: {
    label: 'Gutscheincode',
    placeholder: 'Code eingeben',
    apply: 'Einlösen',
    remove: 'Entfernen',
    applied: 'Code {code} eingelöst.',
    removed: 'Code entfernt.',
    checking: 'Code wird geprüft…',
    discount_label: 'Rabatt',
    subtotal_label: 'Tickets',
    error_not_applicable: 'Dieser Code gilt nicht für diese Bestellung.',
    error_minimum: 'Die Bestellung ist für diesen Code zu klein.',
    error_exhausted: 'Dieser Code kann nicht mehr verwendet werden.',
    error_per_customer: 'Sie haben diesen Code bereits verwendet.',
    error_generic: 'Der Code kann gerade nicht geprüft werden. Bitte versuchen Sie es erneut.',
    error_empty: 'Bitte geben Sie zuerst einen Code ein.',
    error_rate_limited: 'Zu viele Versuche. Warten Sie eine Minute und versuchen Sie es erneut.',
    error_no_discount: 'Dieser Code gewährt für diese Bestellung keinen Rabatt.',
  },
  fr: {
    label: 'Code promo',
    placeholder: 'Saisissez le code',
    apply: 'Appliquer',
    remove: 'Retirer',
    applied: 'Code {code} appliqué.',
    removed: 'Code retiré.',
    checking: 'Vérification du code…',
    discount_label: 'Réduction',
    subtotal_label: 'Billets',
    error_not_applicable: "Ce code ne s'applique pas à cette commande.",
    error_minimum: 'La commande est trop petite pour ce code.',
    error_exhausted: "Ce code n'a plus d'utilisations disponibles.",
    error_per_customer: 'Vous avez déjà utilisé ce code.',
    error_generic: 'Impossible de vérifier le code pour le moment. Réessayez.',
    error_empty: "Saisissez d'abord un code.",
    error_rate_limited: 'Trop de tentatives. Attendez une minute et réessayez.',
    error_no_discount: "Ce code n'accorde aucune réduction sur cette commande.",
  },
};

/** The promo words for a locale ("es-ES" -> es), English when the language has none. */
export function getPromoStrings(locale: string): PromoStrings {
  const lang = (locale ?? '').trim().toLowerCase().split(/[-_]/)[0] ?? '';
  return PROMO_I18N[lang] ?? PROMO_I18N.en!;
}
