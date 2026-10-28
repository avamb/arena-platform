/**
 * catalog.ts - the home page (image banner + featured collection) and the
 * /collections/all page. Markup and ids follow what the live storefront
 * emits for templates/index.json and templates/collection.json.
 */
import type { ShopProduct } from '../lib/api';
import { sized } from '../lib/cdn';
import { esc } from '../lib/html';
import { icon, productCard, type Ctx } from './common';
import { T } from '../lib/i18n';

const BANNER_WIDTHS = [375, 550, 750, 1100, 1500, 1780, 2000, 3000, 3840];

function sectionPadding(cls: string, top: number, bottom: number): string {
  const mobTop = Math.round(top * 0.75);
  const mobBottom = Math.round(bottom * 0.75);
  return `<style data-shopify>.${cls} {
    padding-top: ${mobTop}px;
    padding-bottom: ${mobBottom}px;
  }

  @media screen and (min-width: 750px) {
    .${cls} {
      padding-top: ${top}px;
      padding-bottom: ${bottom}px;
    }
  }</style>`;
}

/** image-banner section, "adapt" height, content box empty (index.json). */
export function imageBanner(ctx: Ctx, id: string, mobileBottom = false): string {
  const b = ctx.theme.banner;
  const pct = (b.height / b.width) * 100;
  const ss = BANNER_WIDTHS.map((w) => `${sized(b.url, w)} ${w}w`).join(', ');
  const bid = `Banner-${id}`;
  return `<section id="shopify-section-${id}" class="shopify-section section"><style data-shopify>@media screen and (max-width: 749px) {
      #${bid}::before,
      #${bid} .banner__media::before,
      #${bid}:not(.banner--mobile-bottom) .banner__content::before {
        padding-bottom: ${pct}%;
        content: '';
        display: block;
      }
    }

    @media screen and (min-width: 750px) {
      #${bid}::before,
      #${bid} .banner__media::before {
        padding-bottom: ${pct}%;
        content: '';
        display: block;
      }
    }</style><style data-shopify>#${bid}::after {
    opacity: 0.0;
  }</style><div
  id="${bid}"
  class="banner banner--content-align-center banner--content-align-mobile-center banner--adapt banner--adapt${mobileBottom ? ' banner--mobile-bottom' : ''} banner--desktop-transparent"
><div class="banner__media media"><img src="${esc(sized(b.url, 3840))}" alt="" srcset="${esc(ss)}" width="${b.width}" height="${b.height}" loading="lazy" sizes="100vw">
    </div><div class="banner__content banner__content--middle-center page-width">
    <div class="banner__box content-container content-container--full-width-mobile color-background-2 gradient"></div>
  </div>
</div>
</section>`;
}

function sliderButtons(sliderId: string, total: number): string {
  return `<div class="slider-buttons no-js-hidden">
          <button
            type="button"
            class="slider-button slider-button--prev"
            name="previous"
            aria-label="${T.sliderPrev}"
            aria-controls="${sliderId}"
          >
            ${icon('caret')}

          </button>
          <div class="slider-counter caption">
            <span class="slider-counter--current">1</span>
            <span aria-hidden="true"> / </span>
            <span class="visually-hidden">${T.sliderOf}</span>
            <span class="slider-counter--total">${total}</span>
          </div>
          <button
            type="button"
            class="slider-button slider-button--next"
            name="next"
            aria-label="${T.sliderNext}"
            aria-controls="${sliderId}"
          >
            ${icon('caret')}

          </button>
        </div>`;
}

function featuredCollection(ctx: Ctx, products: ShopProduct[]): string {
  const sid = 'home__featured_collection';
  const sliderId = `Slider-${sid}`;
  const items = products
    .map(
      (p, i) => `<li
            id="Slide-${sid}-${i + 1}"
            class="grid__item slider__slide"

          >
            ${productCard(ctx, p, { sectionId: sid, showSecondary: true, quickAdd: false })}
          </li>`,
    )
    .join('');
  return `<section id="shopify-section-${sid}" class="shopify-section section">${sectionPadding(`section-${sid}-padding`, 44, 44)}<div class="color-background-2 isolate gradient">
  <div class="collection section-${sid}-padding collection--full-width">
    <div class="collection__title title-wrapper title-wrapper--no-top-margin page-width title-wrapper--self-padded-tablet-down"></div>

    <slider-component class="slider-mobile-gutter slider-component-full-width">
      <ul
        id="${sliderId}"
        class="grid product-grid contains-card contains-card--product contains-card--standard grid--4-col-desktop grid--2-col-tablet-down slider slider--tablet grid--peek"
        role="list"
        aria-label="${T.slider}"
      >${items}</ul>${sliderButtons(sliderId, products.length)}</slider-component></div>
</div>
</section>`;
}

export function homeView(ctx: Ctx): string {
  return imageBanner(ctx, 'home__image_banner') + featuredCollection(ctx, ctx.shop.products);
}

export function collectionView(ctx: Ctx): string {
  const sid = 'collection__grid';
  const items = ctx.shop.products
    .map(
      (p, i) => `<li
            id="Slide-${sid}-${i + 1}"
            class="grid__item"
          >

${productCard(ctx, p, { sectionId: sid, showSecondary: true, quickAdd: true })}
          </li>`,
    )
    .join('');
  return `<div id="shopify-section-collection__banner" class="shopify-section section">

<style data-shopify>@media screen and (max-width: 749px) {
    .collection-hero--with-image .collection-hero__inner {
      padding-bottom: calc(4px + 2rem);
    }
  }</style><div class="collection-hero color-background-1 gradient">
  <div class="collection-hero__inner page-width ">
    <div class="collection-hero__text-wrapper">
      <h1 class="collection-hero__title">
        <span class="visually-hidden">${T.collectionLabel}</span>${T.collectionProducts}</h1><div class="collection-hero__description rte"></div></div></div>
</div>
</div><section id="shopify-section-${sid}" class="shopify-section section">${sectionPadding(`section-${sid}-padding`, 36, 36)}<div class="color-background-1 isolate gradient">
  <div class="collection section-${sid}-padding">
    <div class="collection__title title-wrapper title-wrapper--no-top-margin page-width"><h2 class="title inline-richtext h1">
          ${T.allProducts}
        </h2></div>
    <slider-component class="slider-mobile-gutter page-width page-width-desktop">
      <ul
        id="Slider-${sid}"
        class="grid product-grid contains-card contains-card--product contains-card--standard grid--4-col-desktop grid--2-col-tablet-down"
        role="list"
        aria-label="${T.slider}"
      >${items}</ul></slider-component></div>
</div>
</section>`;
}
