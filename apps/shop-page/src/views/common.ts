/**
 * common.ts - building blocks shared by every view: the page context, the
 * price block, the product card. Each function returns Dawn 11's markup (the
 * HTML the live storefront emits for the same snippet), so the unmodified
 * Dawn CSS styles it.
 */
import type { ShopData, ShopImage, ShopProduct, ShopVariant } from '../lib/api';
import { learnedRatio } from '../lib/ratio';
import type { CartItem } from '../lib/cart';
import { CARD_WIDTHS, CART_WIDTHS, sized, srcset } from '../lib/cdn';
import { esc } from '../lib/html';
import { T } from '../lib/i18n';
import { ICONS } from '../lib/icons';
import { formatMoney } from '../lib/money';
import { href } from '../lib/route';
import type { ThemeConfig } from '../lib/theme';

export interface Ctx {
  org: string;
  /** '' on a custom domain, '/{org_slug}' otherwise. */
  base: string;
  shop: ShopData;
  theme: ThemeConfig;
  cart: CartItem[];
}

export const icon = (name: string): string => ICONS[name] ?? '';

/** Shop-relative path -> link on this deployment. */
export const url = (ctx: Pick<Ctx, 'base'>, path: string): string => href(ctx.base, path);

export const money = (ctx: Ctx, minor: number): string => formatMoney(minor, ctx.shop.settings.currency);

export const productPath = (p: ShopProduct): string => '/products/' + p.slug;

export function spinner(): string {
  return `<svg aria-hidden="true" focusable="false" class="spinner" viewBox="0 0 66 66" xmlns="http://www.w3.org/2000/svg"><circle class="path" fill="none" stroke-width="6" cx="33" cy="33" r="30"></circle></svg>`;
}

/**
 * The (option name, value) pairs of a variant for the cart and checkout lines:
 * one pair per option of a multi-option product, the single option's name with the
 * variant title otherwise. A product with one variant has none.
 */
export function optionPairs(p: ShopProduct, v: ShopVariant): [string, string][] {
  if (p.variants.length < 2) return [];
  if (p.option_names.length > 1) {
    const values = [v.option1, v.option2, v.option3];
    return p.option_names.map((name, i): [string, string] => [name, values[i] ?? '']).filter(([, val]) => val !== '');
  }
  return [[p.option_names[0] || p.option_name || '', v.title]];
}

// ---- derived product facts (Liquid: product.price, product.available, ...) -----

export function priceMin(p: ShopProduct): number {
  return Math.min(...p.variants.map((v) => v.price_minor));
}
export function priceMax(p: ShopProduct): number {
  return Math.max(...p.variants.map((v) => v.price_minor));
}
export function priceVaries(p: ShopProduct): boolean {
  return priceMin(p) !== priceMax(p);
}
export function productAvailable(p: ShopProduct): boolean {
  return p.variants.some((v) => v.available);
}
export function compareAtOf(p: ShopProduct): number {
  const first = p.variants.find((v) => v.available) ?? p.variants[0];
  return first?.compare_at_minor ?? 0;
}
export function firstAvailableVariant(p: ShopProduct): ShopVariant {
  return p.variants.find((v) => v.available) ?? p.variants[0];
}

/**
 * Aspect ratio (width / height) of an image: the size the API gave, else what was
 * learned when the picture loaded earlier in this tab, else 1 (fitted on load, see lib/ratio.ts).
 */
export function ratioOf(img: ShopImage | undefined): number {
  if (img && img.width && img.height) return img.width / img.height;
  return (img && learnedRatio(img.url)) || 1;
}

// ---- price (Dawn snippets/price.liquid) ---------------------------------------

export interface PriceOpts {
  /** A variant to price (product page) instead of the whole product (cards). */
  variant?: ShopVariant;
  showBadges?: boolean;
  large?: boolean;
}

export function priceBlock(ctx: Ctx, p: ShopProduct, opts: PriceOpts = {}): string {
  const v = opts.variant;
  const price = v ? v.price_minor : priceMin(p);
  const compareAt = v ? (v.compare_at_minor ?? 0) : compareAtOf(p);
  const available = v ? v.available : productAvailable(p);
  const varies = !v && priceVaries(p);
  const moneyPrice = varies ? `${T.fromPrice} ${money(ctx, price)}` : money(ctx, price);
  const onSale = compareAt > price;
  const cls = [
    'price',
    opts.large ? 'price--large' : '',
    !available ? 'price--sold-out' : '',
    onSale ? 'price--on-sale' : '',
    opts.showBadges ? 'price--show-badge' : '',
  ]
    .filter(Boolean)
    .join(' ');
  const compareText = compareAt > 0 ? money(ctx, compareAt) : '';
  return `<div class="${cls}">
  <div class="price__container"><div class="price__regular"><span class="visually-hidden visually-hidden--inline">${T.regularPrice}</span>
        <span class="price-item price-item--regular">
          ${esc(moneyPrice)}
        </span></div>
    <div class="price__sale">
        <span class="visually-hidden visually-hidden--inline">${T.regularPrice}</span>
        <span>
          <s class="price-item price-item--regular">
            ${esc(compareText)}
          </s>
        </span><span class="visually-hidden visually-hidden--inline">${T.salePrice}</span>
      <span class="price-item price-item--sale price-item--last">
        ${esc(moneyPrice)}
      </span>
    </div>
    <small class="unit-price caption hidden">
      <span class="visually-hidden">${T.unitPrice}</span>
      <span class="price-item price-item--last">
        <span></span>
        <span aria-hidden="true">/</span>
        <span class="visually-hidden">&nbsp;${T.unitPriceSeparator}&nbsp;</span>
        <span>
        </span>
      </span>
    </small>
  </div>${
    opts.showBadges
      ? `<span class="badge price__badge-sale color-accent-2">
      ${T.onSale}
    </span>
    <span class="badge price__badge-sold-out color-inverse">
      ${T.soldOut}
    </span>`
      : ''
  }</div>`;
}

/** ` srcset=... sizes=...` for a cart / checkout thumbnail drawn `cssWidth` wide, or '' when it cannot be resized. */
export function cartSrcset(img: ShopImage, cssWidth: string): string {
  const ss = srcset(img.url, img.width, CART_WIDTHS);
  return ss ? `
            srcset="${esc(ss)}" sizes="${cssWidth}"` : '';
}

// ---- product card (Dawn snippets/card-product.liquid) -------------------------

export interface CardOpts {
  sectionId: string;
  showSecondary: boolean;
  quickAdd: boolean;
  /** Force a square image box (the related-products block uses image_ratio=square). */
  square?: boolean;
  /** The home page's first row loads eagerly in Dawn; the live markup lazy-loads. */
  lazy?: boolean;
}

function cardImage(img: ShopImage, alt: string, lazy: boolean, sectionPageWidth: number): string {
  const sizes = `(min-width: ${sectionPageWidth}px) ${Math.floor((sectionPageWidth - 130) / 4)}px, (min-width: 990px) calc((100vw - 130px) / 4), (min-width: 750px) calc((100vw - 120px) / 3), calc((100vw - 35px) / 2)`;
  const ss = srcset(img.url, img.width, CARD_WIDTHS);
  return `<img${ss ? `
                srcset="${esc(ss)}"` : ''}
                src="${esc(sized(img.url, 533))}"${ss ? `
                sizes="${esc(sizes)}"` : ''}
                alt="${esc(alt)}"
                class="motion-reduce"${lazy ? `
                loading="lazy"` : ''}${img.width ? `
                width="${img.width}"
                height="${img.height ?? ''}"` : ''}
              >`;
}

export function productCard(ctx: Ctx, p: ShopProduct, o: CardOpts): string {
  const first = p.images[0];
  const second = p.images[1];
  const ratio = o.square ? 1 : ratioOf(first);
  const pct = (1 / ratio) * 100;
  const sid = o.sectionId;
  const pid = p.id;
  const available = productAvailable(p);
  const onSale = available && compareAtOf(p) > priceMin(p);
  const badge = (idPrefix: string): string =>
    !available
      ? `<span
                id="${idPrefix}-${sid}-${pid}"
                class="badge badge--bottom-left color-inverse"
              >${T.soldOut}</span>`
      : onSale
        ? `<span
                id="${idPrefix}-${sid}-${pid}"
                class="badge badge--bottom-left color-accent-2"
              >${T.onSale}</span>`
        : '';
  const link = esc(url(ctx, productPath(p)));
  const title = esc(p.title);
  const lazy = o.lazy !== false;
  const pageWidth = ctx.theme.pageWidthPx;

  const media = first
    ? `<div class="card__media">
            <div class="media media--transparent media--hover-effect">
              ${cardImage(first, first.alt || p.title, lazy, pageWidth)}
              ${o.showSecondary && second ? cardImage(second, '', true, pageWidth) : ''}</div>
          </div>`
    : '';

  return `<div class="card-wrapper product-card-wrapper underline-links-hover">
    <div
      class="card card--standard ${first ? 'card--media' : 'card--text'}"${o.square ? `
      data-square` : ''}
      style="--ratio-percent: ${pct}%;"
    >
      <div
        class="card__inner color-background-2 gradient ratio"
        style="--ratio-percent: ${pct}%;"
      >${media}<div class="card__content">
          <div class="card__information">
            <h3
              class="card__heading"
            >
              <a
                href="${link}"
                id="StandardCardNoMediaLink-${sid}-${pid}"
                class="full-unstyled-link"
                aria-labelledby="StandardCardNoMediaLink-${sid}-${pid} NoMediaStandardBadge-${sid}-${pid}"
              >
                ${title}
              </a>
            </h3>
          </div>
          <div class="card__badge bottom left">${badge('NoMediaStandardBadge')}</div>
        </div>
      </div>
      <div class="card__content">
        <div class="card__information">
          <h3
            class="card__heading h5"
              id="title-${sid}-${pid}"
          >
            <a
              href="${link}"
              id="CardLink-${sid}-${pid}"
              class="full-unstyled-link"
              aria-labelledby="CardLink-${sid}-${pid} Badge-${sid}-${pid}"
            >
              ${title}
            </a>
          </h3>
          <div class="card-information"><span class="caption-large light"></span>
${priceBlock(ctx, p)}
</div>
        </div>${o.quickAdd ? quickAdd(ctx, p, sid) : ''}<div class="card__badge bottom left">${badge('Badge')}</div>
      </div>
    </div>
  </div>`;
}

function quickAdd(ctx: Ctx, p: ShopProduct, sid: string): string {
  const formId = `quick-add-${sid}${p.id}`;
  const labelled = `${formId}-submit title-${sid}-${p.id}`;
  if (p.variants.length > 1) {
    return `<div class="quick-add no-js-hidden"><span class="quick-add__opener">
                <button
                  id="${formId}-submit"
                  type="submit"
                  name="add"
                  class="quick-add__submit button button--full-width button--secondary"
                  aria-haspopup="dialog"
                  aria-labelledby="${labelled}"
                  data-product-url="${esc(url(ctx, productPath(p)))}"
                >
                  ${T.chooseOptions}
<div class="loading-overlay__spinner hidden">
                    ${spinner()}
                  </div>
                </button>
              </span></div>`;
  }
  const v = firstAvailableVariant(p);
  return `<div class="quick-add no-js-hidden"><product-form><form method="post" action="/cart/add" id="${formId}" accept-charset="UTF-8" class="form" novalidate="novalidate" data-type="add-to-cart-form" data-variant-id="${esc(v.id)}">
                  <button
                    id="${formId}-submit"
                    type="submit"
                    name="add"
                    class="quick-add__submit button button--full-width button--secondary"
                    aria-haspopup="dialog"
                    aria-labelledby="${labelled}"
                    aria-live="polite"
                    data-sold-out-message="true"${v.available ? '' : `
                    disabled`}
                  >
                    <span>${v.available ? T.addToCart : T.soldOut}
</span>
                    <span class="sold-out-message hidden">
                      ${T.soldOut}
                    </span><div class="loading-overlay__spinner hidden">
                      ${spinner()}
                    </div>
                  </button></form></product-form></div>`;
}

/** "Электронная почта: <address>" from the shop settings; nothing when the shop names no address. */
export function contactLine(email: string | null | undefined, cls: string): string {
  const e = (email ?? '').trim();
  if (e === '') return '';
  return `<p class="${cls}">${T.contactEmailLabel} <a href="mailto:${esc(e)}" class="link">${esc(e)}</a></p>`;
}
