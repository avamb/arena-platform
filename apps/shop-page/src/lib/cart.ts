/**
 * cart.ts - the buyer's basket. Pure functions over a list of
 * { variant_id, qty } plus the catalog the page already loaded; the only
 * side effects are loadCart / saveCart, which talk to localStorage and
 * swallow every storage failure (private windows, blocked storage) so the
 * basket then simply lives for the page's lifetime.
 *
 * Everything is in minor units; the shipping fee is added at checkout, never
 * in the basket (the live Shopify cart says "shipping is calculated at
 * checkout" too).
 */
import type { CheckoutRequest, ShopProduct, ShopSettings, ShopVariant } from './api';

/** The cap when the shop settings do not name one (the API's own default). */
export const MAX_QTY = 99;

export interface CartItem {
  variant_id: string;
  qty: number;
}

export interface CartLine {
  item: CartItem;
  product: ShopProduct;
  variant: ShopVariant;
  qty: number;
  /** qty * variant price, minor units. */
  lineMinor: number;
}

const clamp = (qty: number, max = MAX_QTY): number => {
  const n = Math.floor(Number(qty));
  if (!Number.isFinite(n)) return 1;
  return Math.max(1, Math.min(Math.max(1, max), n));
};

/** What the buyer is told when an order would hold more than the shop's per-line limit. */
export const qtyLimitMessage = (max: number): string => `В одном заказе не больше ${max} шт. этого товара`;

/** A typed quantity as a whole number of at least 1 (empty or junk reads as 1). */
export function wantedQty(raw: string | number | null | undefined): number {
  const n = Math.floor(Number(raw));
  return Number.isFinite(n) && n >= 1 ? n : 1;
}

/** Quantity field value: `value` is what the field should show, `limited` that the buyer asked for more than a line may hold. */
export function clampField(raw: string | number | null | undefined, max: number): { value: number; limited: boolean } {
  const wanted = wantedQty(raw);
  const cap = Math.max(1, max);
  return { value: Math.min(wanted, cap), limited: wanted > cap };
}

/** Adding `requested` to a line that already holds `inCart` would pass the line limit. */
export const overLineLimit = (inCart: number, requested: number, max: number): boolean => inCart + requested > Math.max(1, max);

/** `max` is the shop's max_line_qty: one line never holds more. */
export function addItem(items: CartItem[], variantId: string, qty = 1, max = MAX_QTY): CartItem[] {
  const existing = items.find((i) => i.variant_id === variantId);
  if (existing) {
    return items.map((i) => (i.variant_id === variantId ? { ...i, qty: clamp(i.qty + qty, max) } : i));
  }
  return [...items, { variant_id: variantId, qty: clamp(qty, max) }];
}

/** qty <= 0 removes the line. */
export function setQty(items: CartItem[], variantId: string, qty: number, max = MAX_QTY): CartItem[] {
  if (!(qty > 0)) return removeItem(items, variantId);
  return items.map((i) => (i.variant_id === variantId ? { ...i, qty: clamp(qty, max) } : i));
}

/** Brings a stored basket under the shop's current cap (the cap may have been lowered since). */
export function capCart(items: CartItem[], max: number): CartItem[] {
  return items.map((i) => (i.qty > max ? { ...i, qty: clamp(i.qty, max) } : i));
}

export function removeItem(items: CartItem[], variantId: string): CartItem[] {
  return items.filter((i) => i.variant_id !== variantId);
}

export function itemCount(items: CartItem[]): number {
  return items.reduce((n, i) => n + i.qty, 0);
}

/** Joins basket items with the catalog. A variant the catalog no longer has is dropped. */
export function resolveLines(items: CartItem[], products: ShopProduct[]): CartLine[] {
  const byVariant = new Map<string, { product: ShopProduct; variant: ShopVariant }>();
  for (const product of products) {
    for (const variant of product.variants) byVariant.set(variant.id, { product, variant });
  }
  const lines: CartLine[] = [];
  for (const item of items) {
    const hit = byVariant.get(item.variant_id);
    if (!hit) continue;
    lines.push({
      item,
      product: hit.product,
      variant: hit.variant,
      qty: item.qty,
      lineMinor: hit.variant.price_minor * item.qty,
    });
  }
  return lines;
}

export function subtotalMinor(lines: CartLine[]): number {
  return lines.reduce((sum, l) => sum + l.lineMinor, 0);
}

/** True when every line can still be bought. */
export function allAvailable(lines: CartLine[]): boolean {
  return lines.every((l) => l.variant.available);
}

/** Only a physical product ships: a cart of digital products and services takes no address and no delivery fee. */
export function needsShipping(lines: CartLine[]): boolean {
  return lines.some((l) => l.product.kind === 'physical');
}

export function shippingMinor(settings: Pick<ShopSettings, 'shipping_fee_minor'>, lines: CartLine[]): number {
  return needsShipping(lines) ? Math.max(0, settings.shipping_fee_minor || 0) : 0;
}

export function totalMinor(settings: Pick<ShopSettings, 'shipping_fee_minor'>, lines: CartLine[]): number {
  return subtotalMinor(lines) + shippingMinor(settings, lines);
}

// ---- storage ---------------------------------------------------------------

const key = (org: string): string => 'arena_shop_cart:' + org;

export function parseStored(raw: string | null): CartItem[] {
  if (!raw) return [];
  try {
    const data: unknown = JSON.parse(raw);
    if (!Array.isArray(data)) return [];
    const out: CartItem[] = [];
    for (const row of data) {
      const r = row as { variant_id?: unknown; qty?: unknown };
      if (typeof r?.variant_id !== 'string' || r.variant_id === '') continue;
      out.push({ variant_id: r.variant_id, qty: clamp(Number(r.qty)) });
    }
    return out;
  } catch {
    return [];
  }
}

export function loadCart(org: string): CartItem[] {
  try {
    return parseStored(localStorage.getItem(key(org)));
  } catch {
    return [];
  }
}

export function saveCart(org: string, items: CartItem[]): void {
  try {
    localStorage.setItem(key(org), JSON.stringify(items));
  } catch {
    /* basket lives in memory only */
  }
}

// ---- checkout form ---------------------------------------------------------

export interface CheckoutForm {
  name: string;
  email: string;
  phone: string;
  address1: string;
  address2: string;
  city: string;
  postal: string;
  /** Free-form comment for the seller (sent as delivery_note). */
  note: string;
  consent18: boolean;
}

export type FormErrors = Partial<Record<keyof CheckoutForm, string>>;

/** The API takes up to 2000 characters of delivery_note. */
export const MAX_NOTE_LEN = 2000;

const MIN_PHONE_DIGITS = 9;

const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/;

/** The payment page could not be opened: say the cart is kept and, when the shop names one, where to write. */
export function paymentUnavailableMessage(contactEmail: string | null | undefined): string {
  const email = (contactEmail ?? '').trim();
  const base = 'Не удалось открыть страницу оплаты. Корзина сохранена, попробуйте через минуту';
  return email ? `${base} или напишите нам: ${email}` : `${base}.`;
}

/** `shipping` false: a cart without a physical product takes no address. */
export function validateCheckoutForm(f: CheckoutForm, minAge: number | null | undefined, shipping = true): FormErrors {
  const e: FormErrors = {};
  if (f.name.trim().length < 2) e.name = 'Укажите имя и фамилию';
  const email = f.email.trim();
  if (email === '') e.email = 'Укажите адрес электронной почты';
  else if (!EMAIL.test(email)) e.email = 'Проверьте адрес: в нём должна быть часть после @';
  const phone = f.phone.trim();
  if (phone === '') e.phone = 'Укажите номер телефона';
  else if (phone.replace(/\D/g, '').length < MIN_PHONE_DIGITS) e.phone = `Проверьте номер: нужно не меньше ${MIN_PHONE_DIGITS} цифр`;
  if (shipping && f.address1.trim().length < 3) e.address1 = 'Укажите адрес доставки';
  if (shipping && f.city.trim().length < 2) e.city = 'Укажите город';
  if ((minAge ?? 0) > 0 && !f.consent18) e.consent18 = 'Необходимо подтвердить возраст';
  return e;
}

export function buildCheckoutRequest(
  items: CartItem[],
  f: CheckoutForm,
  returnUrl: string,
  locale = 'ru',
  country = 'IL',
  shipping = true,
): CheckoutRequest {
  const req: CheckoutRequest = {
    items: items.map((i) => ({ variant_id: i.variant_id, qty: i.qty })),
    buyer: { name: f.name.trim(), email: f.email.trim(), phone: f.phone.trim() },
    locale,
    return_url: returnUrl,
    age_confirmed: f.consent18,
  };
  const note = (f.note ?? '').trim();
  if (note) req.delivery_note = note.slice(0, MAX_NOTE_LEN);
  if (shipping) {
    const ship: NonNullable<CheckoutRequest['ship']> = { address1: f.address1.trim(), city: f.city.trim(), country };
    if (f.address2.trim()) ship.address2 = f.address2.trim();
    if (f.postal.trim()) ship.postal = f.postal.trim();
    req.ship = ship;
  }
  return req;
}
