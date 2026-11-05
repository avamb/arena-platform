/**
 * cartLimits.ts — the size limits of one order and the words for them.
 *
 * The platform caps one public cart (checkout.cart_too_large, checkout.invalid_qty).
 * The widget keeps a buyer inside the seat cap before the request is made, and
 * tells them in their own language when the platform refuses a cart anyway.
 * Looked up by language subtag like the promo and payment-commitment words, so
 * es, de and fr read in their own language where the rest of the UI is English.
 */

/** The most assigned seats in one order (the platform's cap). */
export const MAX_CART_SEATS = 100;
/** The most tickets of one general-admission category in one order (the platform's cap). */
export const MAX_LINE_QUANTITY = 50;

export interface CartLimitStrings {
  /** The seat cap was reached. {max} is the cap. */
  too_many_seats: string;
  /** The platform refused the cart as too large. */
  cart_too_large: string;
  /** The platform refused a line over the per-category cap. {max} is the cap. */
  invalid_qty: string;
  /** Beside an add-on category: it is sold only with a main ticket. */
  addon_note: string;
  /** The platform refused a cart of add-on tickets only. */
  addon_only: string;
}

export const CART_LIMIT_I18N: Record<string, CartLimitStrings> = {
  en: {
    too_many_seats: 'You can select at most {max} seats in one order.',
    cart_too_large: 'This order is too large. Split it into smaller orders.',
    invalid_qty: 'You can buy at most {max} tickets of one category in one order.',
    addon_note: 'Only together with a main ticket.',
    addon_only: 'This ticket is sold only together with a main ticket for the same date. Add a main ticket to the order.',
  },
  ru: {
    too_many_seats: 'В одном заказе можно выбрать не больше {max} мест.',
    cart_too_large: 'Заказ слишком большой. Разделите его на несколько заказов.',
    invalid_qty: 'В одном заказе можно взять не больше {max} билетов одной категории.',
    addon_note: 'Только вместе с основным билетом.',
    addon_only: 'Этот билет продаётся только вместе с основным билетом на ту же дату. Добавьте основной билет в заказ.',
  },
  cs: {
    too_many_seats: 'V jedné objednávce můžete vybrat nejvýše {max} míst.',
    cart_too_large: 'Objednávka je příliš velká. Rozdělte ji na menší objednávky.',
    invalid_qty: 'V jedné objednávce můžete koupit nejvýše {max} vstupenek jedné kategorie.',
    addon_note: 'Jen společně s hlavní vstupenkou.',
    addon_only: 'Tato vstupenka se prodává jen společně s hlavní vstupenkou na stejný termín. Přidejte do objednávky hlavní vstupenku.',
  },
  he: {
    too_many_seats: 'אפשר לבחור עד {max} מקומות בהזמנה אחת.',
    cart_too_large: 'ההזמנה גדולה מדי. חלקו אותה להזמנות קטנות יותר.',
    invalid_qty: 'אפשר לרכוש עד {max} כרטיסים מקטגוריה אחת בהזמנה אחת.',
    addon_note: 'רק יחד עם כרטיס ראשי.',
    addon_only: 'כרטיס זה נמכר רק יחד עם כרטיס ראשי לאותו מועד. הוסיפו כרטיס ראשי להזמנה.',
  },
  es: {
    too_many_seats: 'Puedes elegir como máximo {max} asientos en un pedido.',
    cart_too_large: 'El pedido es demasiado grande. Divídelo en pedidos más pequeños.',
    invalid_qty: 'Puedes comprar como máximo {max} entradas de una categoría en un pedido.',
    addon_note: 'Solo junto con una entrada principal.',
    addon_only: 'Esta entrada solo se vende junto con una entrada principal para la misma fecha. Añade una entrada principal al pedido.',
  },
  de: {
    too_many_seats: 'Sie können höchstens {max} Plätze in einer Bestellung wählen.',
    cart_too_large: 'Die Bestellung ist zu groß. Teilen Sie sie in kleinere Bestellungen auf.',
    invalid_qty: 'Sie können höchstens {max} Tickets einer Kategorie in einer Bestellung kaufen.',
    addon_note: 'Nur zusammen mit einem Hauptticket.',
    addon_only: 'Dieses Ticket wird nur zusammen mit einem Hauptticket für denselben Termin verkauft. Fügen Sie der Bestellung ein Hauptticket hinzu.',
  },
  fr: {
    too_many_seats: 'Vous pouvez choisir au maximum {max} places par commande.',
    cart_too_large: 'La commande est trop grande. Répartissez-la en plusieurs commandes.',
    invalid_qty: "Vous pouvez acheter au maximum {max} billets d'une catégorie par commande.",
    addon_note: 'Uniquement avec un billet principal.',
    addon_only: 'Ce billet est vendu uniquement avec un billet principal pour la même date. Ajoutez un billet principal à la commande.',
  },
};

/** The cart-limit words for a locale ("es-ES" -> es), English when the language has none. */
export function getCartLimitStrings(locale: string): CartLimitStrings {
  const lang = (locale ?? '').trim().toLowerCase().split(/[-_]/)[0] ?? '';
  return CART_LIMIT_I18N[lang] ?? CART_LIMIT_I18N.en!;
}

function fill(template: string, max: number): string {
  return template.replace('{max}', String(max));
}

/** The line beside an add-on category: it is sold only with a main ticket. */
export function addonNote(locale: string): string {
  return getCartLimitStrings(locale).addon_note;
}

/** The notice for a seat the buyer tried to add past the cap. */
export function seatCapMessage(locale: string): string {
  return fill(getCartLimitStrings(locale).too_many_seats, MAX_CART_SEATS);
}

/**
 * The buyer text for a platform refusal of the cart's size, or null when `code`
 * is not one of them.
 */
export function cartLimitMessage(code: string | undefined, locale: string): string | null {
  const s = getCartLimitStrings(locale);
  switch (code) {
    case 'checkout.cart_too_large':
      return s.cart_too_large;
    case 'checkout.invalid_qty':
      return fill(s.invalid_qty, MAX_LINE_QUANTITY);
    case 'tier.addon_requires_main':
      return s.addon_only;
    default:
      return null;
  }
}
