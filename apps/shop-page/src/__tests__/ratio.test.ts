import { describe, expect, it } from 'vitest';
import { applyNaturalRatio, learnedRatio } from '../lib/ratio';
import { productCard, ratioOf } from '../views/common';
import type { ShopProduct } from '../lib/api';
import { EINAT_THEME } from '../lib/theme';

const product: ShopProduct = {
  id: 'p',
  slug: 'p',
  title: 'P',
  body_html: '',
  kind: 'physical',
  option_names: [],
  images: [{ url: 'https://api.test/media/a.jpg?sig=1', alt: '' }],
  variants: [{ id: 'v', title: 'Default Title', price_minor: 100, available: true }],
};

const ctx = { org: 'o', base: '', shop: { settings: { currency: 'ILS', shipping_fee_minor: 0, max_line_qty: 99 }, products: [product], pages: [], menus: { main: [], footer_rules: [], footer_info: [] } }, theme: EINAT_THEME, cart: [] };

function mount(square = false): { img: HTMLImageElement; card: HTMLElement } {
  document.body.innerHTML = productCard(ctx, product, { sectionId: 's', showSecondary: false, quickAdd: false, square });
  const img = document.querySelector('.card__media img') as HTMLImageElement;
  Object.defineProperty(img, 'naturalWidth', { value: 223 });
  Object.defineProperty(img, 'naturalHeight', { value: 335 });
  return { img, card: document.querySelector('.card') as HTMLElement };
}

describe('adaptive card ratio', () => {
  it('starts neutral when the API gives no size, then follows the picture', () => {
    const { img, card } = mount();
    expect(card.style.getPropertyValue('--ratio-percent')).toBe('100%');
    applyNaturalRatio(img);
    expect(parseFloat(card.style.getPropertyValue('--ratio-percent'))).toBeCloseTo((335 / 223) * 100, 6);
    expect(parseFloat(card.querySelector<HTMLElement>('.card__inner')?.style.getPropertyValue('--ratio-percent') ?? '')).toBeCloseTo((335 / 223) * 100, 6);
  });

  it('remembers the ratio per picture, ignoring the signed query', () => {
    mount();
    expect(learnedRatio('https://api.test/media/a.jpg?sig=other')).toBeCloseTo(223 / 335);
    expect(ratioOf({ url: 'https://api.test/media/a.jpg?sig=again' })).toBeCloseTo(223 / 335);
    // the next render draws the right box at once
    document.body.innerHTML = productCard(ctx, product, { sectionId: 's', showSecondary: false, quickAdd: false });
    expect(parseFloat(((document.querySelector('.card') as HTMLElement).getAttribute('style') ?? '').split(':')[1])).toBeCloseTo((335 / 223) * 100, 6);
  });

  it('keeps a square card square', () => {
    const { img, card } = mount(true);
    applyNaturalRatio(img);
    expect(card.style.getPropertyValue('--ratio-percent')).toBe('100%');
  });
});
