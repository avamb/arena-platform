import { describe, expect, it } from 'vitest';
import { CARD_WIDTHS, CART_WIDTHS, GALLERY_WIDTHS, MEDIA_WIDTHS, THUMB_WIDTHS, isMediaFile, isResizable, sized, srcset } from '../lib/cdn';

const shopify = 'https://cdn.shopify.com/s/files/1/0836/1574/4285/files/GN3A8431.jpg?v=1711115096';
const media = 'https://api.arenasoldout.com/v1/media-files/0a1b2c?exp=1790000000&sig=AbC-_9';
const bare = 'https://api.arenasoldout.com/v1/media-files/0a1b2c';

describe('cdn', () => {
  it('resizes Shopify CDN urls and leaves others alone', () => {
    expect(isResizable(shopify)).toBe(true);
    expect(isResizable('https://www.einatwinery.com/cdn/shop/files/a.webp?v=1')).toBe(true);
    expect(sized(shopify, 533)).toBe(shopify + '&width=533');
    expect(sized('https://api.arenasoldout.com/m/1', 533)).toBe('https://api.arenasoldout.com/m/1');
    expect(isMediaFile('https://api.arenasoldout.com/m/1')).toBe(false);
  });

  it('appends w= to a media url with a query string, keeping the signature untouched', () => {
    expect(isMediaFile(media)).toBe(true);
    expect(sized(media, 360)).toBe(media + '&w=360');
  });

  it('starts a query string when the media url has none', () => {
    expect(sized(bare, 360)).toBe(bare + '?w=360');
  });

  it('replaces an existing w= instead of adding a second one', () => {
    expect(sized(media + '&w=165', 720)).toBe(media + '&w=720');
    expect(sized(bare + '?w=165', 720)).toBe(bare + '?w=720');
  });

  it('keeps a fragment at the end', () => {
    expect(sized(media + '#x', 240)).toBe(media + '&w=240#x');
  });

  it('only emits widths the server allows', () => {
    for (const asked of [1, 165, 200, 500, 800, 1200, 3840]) {
      const w = Number(new URL(sized(media, asked)).searchParams.get('w'));
      expect(MEDIA_WIDTHS).toContain(w);
    }
    expect(sized(media, 500)).toBe(media + '&w=533');
    expect(sized(media, 3840)).toBe(media + '&w=1200');
  });

  it('every surface list is made of allowed widths', () => {
    for (const list of [CARD_WIDTHS, GALLERY_WIDTHS, THUMB_WIDTHS, CART_WIDTHS]) {
      for (const w of list) expect(MEDIA_WIDTHS).toContain(w);
    }
  });

  it('builds a media srcset from the whole list when the size is unknown', () => {
    expect(srcset(media, undefined, CARD_WIDTHS)).toBe(CARD_WIDTHS.map((w) => `${media}&w=${w} ${w}w`).join(', '));
  });

  it('uses the whole list for a picture wider than every candidate', () => {
    expect(srcset(media, 3648, GALLERY_WIDTHS)).toBe(GALLERY_WIDTHS.map((w) => `${media}&w=${w} ${w}w`).join(', '));
  });

  it('caps a media srcset at the picture own width', () => {
    // 500 wide: 165 and 360 are smaller, 533 reaches the original (served unscaled) and is labelled 500w.
    expect(srcset(media, 500, CARD_WIDTHS)).toBe(`${media}&w=165 165w, ${media}&w=360 360w, ${media}&w=533 500w`);
    expect(srcset(media, 100, THUMB_WIDTHS)).toBe(`${media}&w=165 100w`);
    expect(srcset(media, 240, THUMB_WIDTHS)).toBe(`${media}&w=165 165w, ${media}&w=240 240w`);
  });

  it('builds a Shopify srcset up to the natural width, then the original', () => {
    const s = srcset(shopify, 800, [165, 360, 533, 720, 940, 1066]);
    const expected = [165, 360, 533, 720].map((w) => shopify + '&width=' + w + ' ' + w + 'w').join(',') + ',' + shopify + ' 800w';
    expect(s).toBe(expected);
  });

  it('gives no srcset for an image that cannot be resized', () => {
    expect(srcset('https://api.arenasoldout.com/m/1', 800, [165])).toBe('');
  });
});
