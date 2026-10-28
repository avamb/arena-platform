import { describe, expect, it } from 'vitest';
import { isResizable, sized, srcset } from '../lib/cdn';

const shopify = 'https://cdn.shopify.com/s/files/1/0836/1574/4285/files/GN3A8431.jpg?v=1711115096';

describe('cdn', () => {
  it('resizes Shopify CDN urls and leaves others alone', () => {
    expect(isResizable(shopify)).toBe(true);
    expect(isResizable('https://www.einatwinery.com/cdn/shop/files/a.webp?v=1')).toBe(true);
    expect(isResizable('https://api.arenasoldout.com/v1/media-files/abc')).toBe(false);
    expect(sized(shopify, 533)).toBe(shopify + '&width=533');
    expect(sized('https://api.arenasoldout.com/m/1', 533)).toBe('https://api.arenasoldout.com/m/1');
  });

  it('builds a srcset up to the natural width, then the original', () => {
    const s = srcset(shopify, 800, [165, 360, 533, 720, 940, 1066]);
    const expected = [165, 360, 533, 720].map((w) => shopify + '&width=' + w + ' ' + w + 'w').join(',') + ',' + shopify + ' 800w';
    expect(s).toBe(expected);
  });

  it('gives no srcset for an image that cannot be resized', () => {
    expect(srcset('https://api.arenasoldout.com/m/1', 800, [165])).toBe('');
  });
});
