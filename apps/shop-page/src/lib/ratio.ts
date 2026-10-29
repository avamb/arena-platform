/**
 * ratio.ts - Dawn's image_ratio = "adapt": a product picture decides the shape of
 * its own box. The public API's image objects carry no pixel size, so the card
 * and the product media are first drawn with a neutral ratio and fitted to the
 * picture as it loads (naturalWidth / naturalHeight). The ratio is remembered per
 * picture for the life of the tab, so the next render (navigation re-renders the
 * page) draws the right box at once.
 *
 * The key is the URL without its query: picture URLs are signed and short-lived,
 * the path stays the same. Nothing here is persisted.
 */

const learned = new Map<string, number>();

export const imageKey = (url: string): string => url.split('?')[0];

/** Width / height learned from a loaded picture, if this picture was seen already. */
export const learnedRatio = (url: string): number | undefined => learned.get(imageKey(url));

export function learnRatio(url: string, width: number, height: number): void {
  if (width > 0 && height > 0) learned.set(imageKey(url), width / height);
}

/** Fits the box that follows the picture: a standard card, or a product media container. */
export function applyNaturalRatio(img: HTMLImageElement): void {
  if (!img.naturalWidth || !img.naturalHeight) return;
  const src = img.getAttribute('src');
  if (!src) return;
  learnRatio(src, img.naturalWidth, img.naturalHeight);
  const ratio = img.naturalWidth / img.naturalHeight;

  // Only the first picture of a card decides (the hover picture is the second one),
  // and a card forced square (related products) stays square.
  const card = img.closest<HTMLElement>('.card');
  if (card && img.closest('.card__media') && !card.hasAttribute('data-square') && img.matches('.media > img:first-of-type')) {
    const pct = (1 / ratio) * 100 + '%';
    card.style.setProperty('--ratio-percent', pct);
    card.querySelector<HTMLElement>('.card__inner')?.style.setProperty('--ratio-percent', pct);
    return;
  }
  const media = img.closest<HTMLElement>('.product-media-container');
  if (media) {
    media.style.setProperty('--ratio', String(ratio));
    media.style.setProperty('--preview-ratio', String(ratio));
  }
}

/** Pictures that finished loading before the page was painted. */
export function fitLoadedImages(scope: ParentNode = document): void {
  scope.querySelectorAll<HTMLImageElement>('.card__media img, .product-media-container img').forEach((img) => {
    if (img.complete) applyNaturalRatio(img);
  });
}

/** `load` does not bubble: one capturing listener covers every picture drawn later. */
export function watchImageRatios(): void {
  document.addEventListener(
    'load',
    (e) => {
      if (e.target instanceof HTMLImageElement) applyNaturalRatio(e.target);
    },
    true,
  );
}
