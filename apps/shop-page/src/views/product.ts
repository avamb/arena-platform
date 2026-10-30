/**
 * product.ts - /products/{slug}: stacked gallery, info column (vendor, title,
 * price, variant pills, quantity, add to cart, description, share) and the
 * "You may also like" block. Markup is Dawn 11's main-product section as the
 * live storefront renders it (templates/product.json: medium media, stacked
 * gallery, thumbnails hidden on mobile, no zoom, dynamic checkout off here).
 */
import type { ShopProduct, ShopVariant } from '../lib/api';
import { GALLERY_WIDTHS, sized, srcset } from '../lib/cdn';
import { esc } from '../lib/html';
import { limitMessage, variantLimit } from '../lib/cart';
import { sanitizeHtml } from '../lib/sanitize';
import { T } from '../lib/i18n';
import { icon, idToken, priceBlock, productCard, ratioOf, spinner, url, firstAvailableVariant, type Ctx } from './common';

const SID = 'product__main';
const MEDIA_SIZES =
  '(min-width: 1600px) 825px, (min-width: 990px) calc(55.0vw - 10rem), (min-width: 750px) calc((100vw - 11.5rem) / 2), calc(100vw / 1 - 4rem)';

function sectionPadding(cls: string, top: number, bottom: number): string {
  return `<style data-shopify>.${cls} {
      padding-top: ${Math.round(top * 0.75)}px;
      padding-bottom: ${Math.round(bottom * 0.75)}px;
    }
    @media screen and (min-width: 750px) {
      .${cls} {
        padding-top: ${top}px;
        padding-bottom: ${bottom}px;
      }
    }</style>`;
}

/** A stable numeric-looking id for a gallery image, as Shopify's media ids are. */
const mediaId = (p: ShopProduct, i: number): string => `${idToken(p.id)}${String(i).padStart(2, '0')}`;

function gallery(p: ShopProduct): string {
  const n = p.images.length;
  const items = p.images
    .map((img, i) => {
      const ratio = ratioOf(img);
      const h = Math.floor(1946 / ratio);
      const mid = mediaId(p, i);
      const ss = srcset(img.url, img.width, GALLERY_WIDTHS);
      const tag = `<img src="${esc(sized(img.url, 720))}" alt="${esc(img.alt ?? '')}"${ss ? ` srcset="${esc(ss)}"` : ''} width="1946" height="${h}"${i > 0 ? ' loading="lazy"' : ''} class="image-magnify-none" sizes="${MEDIA_SIZES}">`;
      return `<li
            id="Slide-${SID}-${mid}"
            class="product__media-item grid__item slider__slide${i === 0 ? ' is-active' : ''}"
            data-media-id="${SID}-${mid}"
          >
<div
  class="product-media-container media-type-image media-fit-cover global-media-settings gradient constrain-height"
  style="--ratio: ${ratio}; --preview-ratio: ${ratio};"
>
  <modal-opener class="product__modal-opener product__modal-opener--image no-js-hidden" data-modal="#ProductModal-${SID}">
    <span class="product__media-icon motion-reduce quick-add-hidden product__media-icon--none" aria-hidden="true">${icon('zoom')}
</span>
    <div class="loading-overlay__spinner hidden">
      ${spinner()}
    </div>
    <div class="product__media media media--transparent">
      ${tag}
    </div>
    <button class="product__media-toggle quick-add-hidden product__media-zoom-none" type="button" aria-haspopup="dialog" data-media-id="${mid}">
      <span class="visually-hidden">
        Открыть медиа-файлы ${i + 1} в модальном окне
      </span>
    </button>
  </modal-opener></div>
          </li>`;
    })
    .join('');
  const buttons = `<div class="slider-buttons no-js-hidden quick-add-hidden${n > 1 ? '' : ' small-hide'}">
        <button
          type="button"
          class="slider-button slider-button--prev"
          name="previous"
          aria-label="${T.sliderPrev}"
        >
          ${icon('caret')}
        </button>
        <div class="slider-counter caption">
          <span class="slider-counter--current">1</span>
          <span aria-hidden="true"> / </span>
          <span class="visually-hidden">${T.sliderOf}</span>
          <span class="slider-counter--total">${n}</span>
        </div>
        <button
          type="button"
          class="slider-button slider-button--next"
          name="next"
          aria-label="${T.sliderNext}"
        >
          ${icon('caret')}
        </button>
      </div>`;
  return `<media-gallery
  id="MediaGallery-${SID}"
  role="region"
    class="product__column-sticky"
  aria-label="${T.galleryViewer}"
  data-desktop-layout="stacked"
>
  <div id="GalleryStatus-${SID}" class="visually-hidden" role="status"></div>
  <slider-component id="GalleryViewer-${SID}" class="slider-mobile-gutter"><a class="skip-to-content-link button visually-hidden quick-add-hidden" href="#ProductInfo-${SID}">
        ${T.skipToProductInfo}
      </a><ul
      id="Slider-Gallery-${SID}"
      class="product__media-list contains-media grid grid--peek list-unstyled slider slider--mobile"
      role="list"
    >${items}</ul>${buttons}</slider-component></media-gallery>`;
}

function modal(p: ShopProduct): string {
  const imgs = p.images
    .map((img, i) => {
      const ratio = ratioOf(img);
      const ss = srcset(img.url, img.width, GALLERY_WIDTHS);
      return `<img
    class="global-media-settings global-media-settings--no-shadow"
${ss ? `    srcset="${esc(ss)}"
    sizes="(min-width: 750px) calc(100vw - 22rem), 1100px"
` : ''}    src="${esc(sized(img.url, 990))}"
    alt="${esc(p.title)}"
    loading="lazy"
    width="1100"
    height="${Math.floor(1100 / ratio)}"
    data-media-id="${mediaId(p, i)}"
  >`;
    })
    .join('');
  return `<product-modal id="ProductModal-${SID}" class="product-media-modal media-modal">
  <div
    class="product-media-modal__dialog"
    role="dialog"
    aria-label="${T.galleryModal}"
    aria-modal="true"
    tabindex="-1"
  >
    <button
      id="ModalClose-${SID}"
      type="button"
      class="product-media-modal__toggle"
      aria-label="${T.close}"
    >
      ${icon('close')}
    </button>
    <div
      class="product-media-modal__content color-background-1 gradient"
      role="document"
      aria-label="${T.galleryModal}"
      tabindex="0"
    >${imgs}</div>
  </div>
</product-modal>`;
}

/** Several options (up to three): one group of pills per option, labelled with the option's name. */
function multiOptionPicker(p: ShopProduct, selected: ShopVariant): string {
  const chosen = [selected.option1, selected.option2, selected.option3];
  const groups = p.option_names.map((name, k) => {
    const values: string[] = [];
    for (const v of p.variants) {
      const val = [v.option1, v.option2, v.option3][k];
      if (val && !values.includes(val)) values.push(val);
    }
    const radios = values
      .map((val, i) => {
        // sold out when no variant with this value can be bought
        const sold = !p.variants.some((v) => [v.option1, v.option2, v.option3][k] === val && v.available);
        return `<input
      type="radio"
      id="${SID}-${k + 1}-${i}"
      name="${esc(name)}"
      value="${esc(val)}"
      form="product-form-${SID}"
      data-option-index="${k}"${chosen[k] === val ? `
        checked` : ''}${sold ? `
        class="disabled"` : ''}
    >
    <label for="${SID}-${k + 1}-${i}">
      ${esc(val)}<span class="visually-hidden">${T.variantSoldOut}</span>
    </label>`;
      })
      .join('');
    return `<fieldset class="js product-form__input">
          <legend class="form__label">${esc(name)}</legend>${radios}
        </fieldset>`;
  });
  return `<div
      id="variant-radios-${SID}"
      class="variant-radios no-js-hidden"
      data-section="${SID}"
    >${groups.join('')}</div>`;
}

function variantPicker(p: ShopProduct, selected: ShopVariant): string {
  if (p.variants.length < 2) return '';
  if (p.option_names.length > 1) return multiOptionPicker(p, selected);
  const name = p.option_names[0] || p.option_name || '';
  const radios = p.variants
    .map(
      (v, i) => `<input
      type="radio"
      id="${SID}-1-${i}"
      name="${esc(name)}"
      value="${esc(v.title)}"
      form="product-form-${SID}"
      data-variant-id="${esc(v.id)}"${v.id === selected.id ? `
        checked` : ''}${v.available ? '' : `
        class="disabled"`}
    >
    <label for="${SID}-1-${i}">
      ${esc(v.title)}<span class="visually-hidden">${T.variantSoldOut}</span>
    </label>`,
    )
    .join('');
  return `<div
      id="variant-radios-${SID}"
      class="variant-radios no-js-hidden"
      data-section="${SID}"
    ><fieldset class="js product-form__input">
          <legend class="form__label">${esc(name)}</legend>${radios}
        </fieldset></div>`;
}

/** The two parts that change when a variant is picked. */
export function buyButtonHtml(v: ShopVariant): string {
  return `<span>${v.available ? T.addToCart : T.soldOut}
</span>
            <div class="loading-overlay__spinner hidden">
              ${spinner()}
            </div>`;
}

/** The live shop's accelerated-checkout "Купить" button (shown only while the variant can be bought). */
export function buyNowButton(v: ShopVariant): string {
  return `<div data-shopify="payment-button" class="shopify-payment-button"${v.available ? '' : ' hidden'}> <button type="button" class="shopify-payment-button__button shopify-payment-button__button--unbranded" data-buy-now>Купить</button> </div>`;
}

function related(ctx: Ctx, p: ShopProduct): string {
  // The API may name the related products; otherwise the four that follow this one in the catalog, wrapping around.
  const all = ctx.shop.products;
  const named = (p.related_slugs ?? []).map((slug) => all.find((x) => x.slug === slug)).filter((x): x is ShopProduct => !!x && x.id !== p.id);
  const at = all.findIndex((x) => x.id === p.id);
  const cyclic = [...all.slice(at + 1), ...all.slice(0, Math.max(at, 0))].filter((x) => x.id !== p.id);
  const others = (named.length > 0 ? named : cyclic).slice(0, 4);
  if (others.length === 0) return '';
  const sid = 'product__related';
  const items = others
    .map(
      (o) => `<li class="grid__item">
            ${productCard(ctx, o, { sectionId: sid, showSecondary: true, quickAdd: false, square: true })}
          </li>`,
    )
    .join('');
  return `<section id="shopify-section-${sid}" class="shopify-section section">${sectionPadding(`section-${sid}-padding`, 36, 28)}<div class="color-background-1 gradient no-js-hidden">
  <div class="related-products page-width section-${sid}-padding isolate">
      <h2 class="related-products__heading inline-richtext h2">
        ${T.youMayAlsoLike}
      </h2>
      <ul
        class="grid product-grid grid--4-col-desktop grid--2-col-tablet-down"
        role="list"
      >${items}</ul>
  </div>
  </div>
</section>`;
}

export function productView(ctx: Ctx, p: ShopProduct, selectedId?: string): string {
  const selected = p.variants.find((v) => v.id === selectedId) ?? firstAvailableVariant(p);
  const vendor = p.vendor || ctx.shop.settings.shop_name || ctx.theme.shopName;
  const link = esc(url(ctx, '/products/' + p.slug));
  const title = esc(p.title);
  const shareUrl = typeof location !== 'undefined' ? location.origin + url(ctx, '/products/' + p.slug) : '';
  const description = sanitizeHtml(p.body_html);
  const inCart = ctx.cart.find((i) => i.variant_id === selected.id)?.qty ?? 0;
  const lineMax = ctx.shop.settings.max_line_qty;
  const limit = variantLimit(selected, lineMax);
  return `<section id="shopify-section-${SID}" class="shopify-section section"><section
  id="MainProduct-${SID}"
  class="page-width section-${SID}-padding"
  data-section="${SID}"
>
  ${sectionPadding(`section-${SID}-padding`, 36, 12)}
  <div class="product product--medium product--left product--stacked product--mobile-hide grid grid--1-col grid--2-col-tablet">
    <div class="grid__item product__media-wrapper">
${gallery(p)}
    </div>
    <div class="product__info-wrapper grid__item">
      <product-info
        id="ProductInfo-${SID}"
        data-section="${SID}"
        data-url="${link}"
        class="product__info-container product__column-sticky"
        data-product-slug="${esc(p.slug)}"
      ><p
                class="product__text inline-richtext caption-with-letter-spacing"
              >${esc(vendor)}</p><div class="product__title" >
                <h1>${title}</h1>
                <a href="${link}" class="product__title">
                  <h2 class="h1">
                    ${title}
                  </h2>
                </a>
              </div><div class="no-js-hidden" id="price-${SID}" role="status" >
${priceBlock(ctx, p, { variant: selected, showBadges: true, large: true })}</div><div class="product__tax caption rte">${T.taxesIncluded}
</div>
              ${variantPicker(p, selected)}<div
                id="Quantity-Form-${SID}"
                class="product-form__input product-form__quantity"
              >
                <label class="quantity__label form__label" for="Quantity-${SID}">
                  ${T.quantity}
                  <span class="quantity__rules-cart no-js-hidden${inCart > 0 ? '' : ' hidden'}">
                    <span class="loading-overlay hidden">
                      <span class="loading-overlay__spinner">
                        ${spinner()}
                      </span>
                    </span>
                    <span>(В корзине: <span class="quantity-cart">${inCart}</span>)</span>
                  </span>
                </label>
                <div class="price-per-item__container">
                  <quantity-input class="quantity" data-url="${link}" data-section="${SID}">
                    <button class="quantity__button no-js-hidden" name="minus" type="button">
                      <span class="visually-hidden">Уменьшить количество ${title}</span>
                      ${icon('minus')}
                    </button>
                    <input
                      class="quantity__input"
                      type="number"
                      name="quantity"
                      id="Quantity-${SID}"
                      data-cart-quantity="${inCart}"
                      data-min="1"
                      min="1"
                      max="${limit}"
                      step="1"
                      value="1"
                      form="product-form-${SID}"
                    />
                    <button class="quantity__button no-js-hidden" name="plus" type="button"${limit <= 1 ? ` aria-disabled="true" title="${esc(limitMessage(limit, lineMax))}"` : ''}>
                      <span class="visually-hidden">Увеличить количество ${title}</span>
                      ${icon('plus')}
                    </button>
                  </quantity-input></div>
                <div class="quantity__rules caption no-js-hidden"></div></div>
<div ><product-form
      class="product-form"
      data-hide-errors="false"
      data-section-id="${SID}"
    >
      <div class="product-form__error-message-wrapper" role="alert" hidden>
        ${icon('error')}
        <span class="product-form__error-message"></span>
      </div><form method="post" action="/cart/add" id="product-form-${SID}" accept-charset="UTF-8" class="form" novalidate="novalidate" data-type="add-to-cart-form" data-variant-id="${esc(selected.id)}"><input
          type="hidden"
          name="id"
          value="${esc(selected.id)}"
          disabled
          class="product-variant-id"
        ><div class="product-form__buttons"><button
            id="ProductSubmitButton-${SID}"
            type="submit"
            name="add"
            class="product-form__submit button button--full-width button--secondary"${selected.available ? '' : `
            disabled`}
          >
            ${buyButtonHtml(selected)}
          </button>${buyNowButton(selected)}</div></form></product-form></div>
<div class="product__description rte quick-add-hidden" >
                  ${description}
                </div>
<share-button id="Share-${SID}" class="share-button quick-add-hidden" >
  <button class="share-button__button hidden">
    ${icon('share')}
    ${T.share}
  </button>
  <details id="Details-share-${SID}">
    <summary class="share-button__button">
      ${icon('share')}
      ${T.share}
    </summary>
    <div class="share-button__fallback motion-reduce">
      <div class="field">
        <span id="ShareMessage-${SID}" class="share-button__message hidden" role="status"> </span>
        <input
          type="text"
          class="field__input"
          id="ShareUrl-${SID}"
          value="${esc(shareUrl)}"
          placeholder="${T.shareUrl}"
          readonly
        >
        <label class="field__label" for="ShareUrl-${SID}">${T.shareUrl}</label>
      </div>
      <button class="share-button__close hidden no-js-hidden">
        ${icon('close')}
        <span class="visually-hidden">${T.shareClose}</span>
      </button>
      <button class="share-button__copy no-js-hidden">
        ${icon('clipboard')}
        <span class="visually-hidden">${T.shareCopy}</span>
      </button>
    </div>
  </details>
</share-button>
<a href="${link}" class="link product__view-details animate-arrow">
          ${T.viewFullDetails}
          ${icon('arrow')}
        </a>
      </product-info>
    </div></div>
${modal(p)}
</section>
</section>${related(ctx, p)}`;
}
