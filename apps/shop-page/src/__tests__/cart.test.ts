import { describe, expect, it } from 'vitest';
import type { ShopProduct } from '../lib/api';
import {
  addItem,
  capCart,
  needsShipping,
  allAvailable,
  buildCheckoutRequest,
  clampField,
  itemCount,
  MAX_QTY,
  overLineLimit,
  parseStored,
  paymentUnavailableMessage,
  qtyLimitMessage,
  removeItem,
  resolveLines,
  setQty,
  shippingMinor,
  subtotalMinor,
  totalMinor,
  validateCheckoutForm,
  wantedQty,
  type CheckoutForm,
} from '../lib/cart';

const products: ShopProduct[] = [
  {
    id: 'p1',
    slug: 'a',
    title: 'A',
    body_html: '',
    kind: 'physical',
    option_names: [],
    images: [],
    variants: [{ id: 'v1', title: 'Default Title', price_minor: 25000, available: true }],
  },
  {
    id: 'p2',
    slug: 'b',
    title: 'B',
    body_html: '',
    kind: 'physical',
    option_names: ['Вино'],
    images: [],
    variants: [
      { id: 'v2', title: 'X', price_minor: 29000, available: true },
      { id: 'v3', title: 'Y', price_minor: 29000, available: false },
    ],
  },
];
const settings = { shipping_fee_minor: 5000 };

describe('basket', () => {
  it('adds, merges and clamps', () => {
    let c = addItem([], 'v1');
    c = addItem(c, 'v1', 2);
    expect(c).toEqual([{ variant_id: 'v1', qty: 3 }]);
    c = addItem(c, 'v1', 500);
    expect(c[0].qty).toBe(MAX_QTY);
    expect(addItem([], 'v2', 0)[0].qty).toBe(1);
  });

  it('sets quantity and removes at zero', () => {
    const c = addItem(addItem([], 'v1'), 'v2');
    expect(setQty(c, 'v1', 4)[0].qty).toBe(4);
    expect(setQty(c, 'v1', 0)).toEqual([{ variant_id: 'v2', qty: 1 }]);
    expect(removeItem(c, 'v2')).toEqual([{ variant_id: 'v1', qty: 1 }]);
    expect(itemCount(setQty(c, 'v1', 3))).toBe(4);
  });
});

describe('totals', () => {
  it('resolves lines against the catalog and drops unknown variants', () => {
    const lines = resolveLines(
      [
        { variant_id: 'v1', qty: 2 },
        { variant_id: 'gone', qty: 1 },
        { variant_id: 'v2', qty: 1 },
      ],
      products,
    );
    expect(lines.map((l) => l.variant.id)).toEqual(['v1', 'v2']);
    expect(subtotalMinor(lines)).toBe(2 * 25000 + 29000);
  });

  it('adds the flat delivery only to a non-empty basket', () => {
    const lines = resolveLines([{ variant_id: 'v1', qty: 2 }], products);
    expect(shippingMinor(settings, lines)).toBe(5000);
    expect(totalMinor(settings, lines)).toBe(55000);
    expect(totalMinor(settings, [])).toBe(0);
  });

  it('knows when something is no longer available', () => {
    expect(allAvailable(resolveLines([{ variant_id: 'v1', qty: 1 }], products))).toBe(true);
    expect(allAvailable(resolveLines([{ variant_id: 'v3', qty: 1 }], products))).toBe(false);
  });
});

describe('storage', () => {
  it('survives garbage', () => {
    expect(parseStored(null)).toEqual([]);
    expect(parseStored('not json')).toEqual([]);
    expect(parseStored('{"a":1}')).toEqual([]);
    expect(parseStored('[{"variant_id":"v1","qty":"3"},{"qty":1},{"variant_id":"","qty":1}]')).toEqual([{ variant_id: 'v1', qty: 3 }]);
  });
});

const good: CheckoutForm = {
  name: 'Ирина Левина',
  email: 'irina@example.com',
  phone: '+972 50 123 4567',
  address1: 'Герцль 1, кв. 5',
  address2: '',
  city: 'Тель-Авив',
  postal: '',
  note: '',
  consent18: true,
};

describe('checkout form', () => {
  it('accepts a complete form', () => {
    expect(validateCheckoutForm(good, 18)).toEqual({});
  });
  it('names every missing field', () => {
    const e = validateCheckoutForm({ ...good, name: '', email: 'x', phone: '12', address1: '', city: '', consent18: false }, 18);
    expect(Object.keys(e).sort()).toEqual(['address1', 'city', 'consent18', 'email', 'name', 'phone']);
  });
  it('does not ask for the age consent when the shop has no age gate', () => {
    expect(validateCheckoutForm({ ...good, consent18: false }, 0)).toEqual({});
  });
  it('builds the request the API expects', () => {
    const req = buildCheckoutRequest([{ variant_id: 'v1', qty: 2 }], { ...good, address2: ' подъезд 2 ' }, 'https://x.test/e/checkout/result');
    expect(req).toEqual({
      items: [{ variant_id: 'v1', qty: 2 }],
      buyer: { name: 'Ирина Левина', email: 'irina@example.com', phone: '+972 50 123 4567' },
      ship: { address1: 'Герцль 1, кв. 5', address2: 'подъезд 2', city: 'Тель-Авив', country: 'IL' },
      locale: 'ru',
      return_url: 'https://x.test/e/checkout/result',
      age_confirmed: good.consent18,
    });
  });
});

describe('quantity cap (max_line_qty)', () => {
  it('stops a line at the shop cap', () => {
    let items = addItem([], 'v1', 8, 10);
    expect(items[0].qty).toBe(8);
    items = addItem(items, 'v1', 5, 10);
    expect(items[0].qty).toBe(10);
    expect(setQty(items, 'v1', 40, 10)[0].qty).toBe(10);
  });

  it('brings a stored basket under a lowered cap', () => {
    expect(capCart([{ variant_id: 'v1', qty: 30 }, { variant_id: 'v2', qty: 2 }], 10)).toEqual([
      { variant_id: 'v1', qty: 10 },
      { variant_id: 'v2', qty: 2 },
    ]);
  });
});

describe('line limit announcement', () => {
  it('names the limit in Russian', () => {
    expect(qtyLimitMessage(24)).toBe('В одном заказе не больше 24 шт. этого товара');
  });
  it('clamps a typed quantity and says whether it was cut', () => {
    expect(clampField('30', 24)).toEqual({ value: 24, limited: true });
    expect(clampField('24', 24)).toEqual({ value: 24, limited: false });
    expect(clampField('5', 24)).toEqual({ value: 5, limited: false });
    expect(clampField('', 24)).toEqual({ value: 1, limited: false });
    expect(clampField('abc', 24)).toEqual({ value: 1, limited: false });
    expect(clampField('0', 24)).toEqual({ value: 1, limited: false });
  });
  it('reads a typed quantity as a whole number of at least 1', () => {
    expect(wantedQty('7.9')).toBe(7);
    expect(wantedQty(undefined)).toBe(1);
    expect(wantedQty('-3')).toBe(1);
  });
  it('counts what is already in the cart against the limit', () => {
    expect(overLineLimit(0, 24, 24)).toBe(false);
    expect(overLineLimit(0, 30, 24)).toBe(true);
    expect(overLineLimit(20, 5, 24)).toBe(true);
    expect(overLineLimit(20, 4, 24)).toBe(false);
  });
});

describe('checkout messages', () => {
  it('asks differently for an empty and for a filled but invalid e-mail', () => {
    expect(validateCheckoutForm({ ...good, email: '' }, 18).email).toBe('Укажите адрес электронной почты');
    expect(validateCheckoutForm({ ...good, email: 'hermes@' }, 18).email).toBe('Проверьте адрес: в нём должна быть часть после @');
  });
  it('asks differently for an empty and for a short phone', () => {
    expect(validateCheckoutForm({ ...good, phone: '  ' }, 18).phone).toBe('Укажите номер телефона');
    expect(validateCheckoutForm({ ...good, phone: '123' }, 18).phone).toBe('Проверьте номер: нужно не меньше 9 цифр');
    expect(validateCheckoutForm({ ...good, phone: '050-123-456' }, 18).phone).toBeUndefined();
  });
  it('sends the order comment as delivery_note', () => {
    const req = buildCheckoutRequest([{ variant_id: 'v1', qty: 1 }], { ...good, note: ' позвонить за час ' }, 'https://x.test/c');
    expect(req.delivery_note).toBe('позвонить за час');
    expect(buildCheckoutRequest([{ variant_id: 'v1', qty: 1 }], good, 'https://x.test/c').delivery_note).toBeUndefined();
  });
  it('says the cart is kept and gives the shop address when payment cannot open', () => {
    expect(paymentUnavailableMessage('sales@einatwinery.com')).toBe(
      'Не удалось открыть страницу оплаты. Корзина сохранена, попробуйте через минуту или напишите нам: sales@einatwinery.com',
    );
    expect(paymentUnavailableMessage('')).toBe('Не удалось открыть страницу оплаты. Корзина сохранена, попробуйте через минуту.');
    expect(paymentUnavailableMessage(null)).not.toContain('напишите');
  });
});

describe('carts by product kind', () => {
  const digital: ShopProduct = { ...products[0], id: 'd', slug: 'd', kind: 'digital', variants: [{ id: 'vd', title: 'Default Title', price_minor: 3000, available: true }] };
  const catalog = [...products, digital];

  it('a cart with no physical line takes no shipping and no address', () => {
    const lines = resolveLines([{ variant_id: 'vd', qty: 1 }], catalog);
    expect(needsShipping(lines)).toBe(false);
    expect(shippingMinor({ shipping_fee_minor: 5000 }, lines)).toBe(0);
    expect(totalMinor({ shipping_fee_minor: 5000 }, lines)).toBe(3000);
    const form = { name: 'Ирина Левина', email: 'irina@example.com', phone: '+972501234567', address1: '', address2: '', city: '', postal: '', note: '', consent18: true };
    expect(validateCheckoutForm(form, 18, false)).toEqual({});
    expect(buildCheckoutRequest([{ variant_id: 'vd', qty: 1 }], form, 'https://x.test/c', 'ru', 'IL', false).ship).toBeUndefined();
  });

  it('one physical line brings the address and the fee back', () => {
    const lines = resolveLines([{ variant_id: 'vd', qty: 1 }, { variant_id: 'v1', qty: 1 }], catalog);
    expect(needsShipping(lines)).toBe(true);
    expect(shippingMinor({ shipping_fee_minor: 5000 }, lines)).toBe(5000);
    expect(Object.keys(validateCheckoutForm({ name: 'Ирина', email: 'i@e.co', phone: '+972501234567', address1: '', address2: '', city: '', postal: '', note: '', consent18: true }, 18, true))).toEqual(['address1', 'city']);
  });
});
