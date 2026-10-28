import { describe, expect, it } from 'vitest';
import type { ShopProduct } from '../lib/api';
import {
  addItem,
  allAvailable,
  buildCheckoutRequest,
  itemCount,
  MAX_QTY,
  parseStored,
  removeItem,
  resolveLines,
  setQty,
  shippingMinor,
  subtotalMinor,
  totalMinor,
  validateCheckoutForm,
  type CheckoutForm,
} from '../lib/cart';

const products: ShopProduct[] = [
  {
    id: 'p1',
    slug: 'a',
    title: 'A',
    body_html: '',
    images: [],
    variants: [{ id: 'v1', title: 'Default Title', price_minor: 25000, available: true }],
  },
  {
    id: 'p2',
    slug: 'b',
    title: 'B',
    body_html: '',
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
