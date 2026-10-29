/**
 * static.ts - /pages/{slug}, /policies/{slug}, the search results page and
 * the 404. Pages that were built from theme sections on the old shop (banners,
 * rich text, event lists) arrive from the API as that section markup and are
 * shown as-is; a plain page body is wrapped in Dawn's main-page template.
 */
import type { ShopPage, ShopProduct } from '../lib/api';
import { esc } from '../lib/html';
import { resultsLabel, T } from '../lib/i18n';
import { sanitizeHtml } from '../lib/sanitize';
import { contactLine, productCard, url, type Ctx } from './common';

export const isSectionBody = (html: string): boolean => /class="shopify-section/.test(html);

export function pageView(ctx: Ctx, page: ShopPage): string {
  const body = sanitizeHtml(page.body_html);
  // The "Контакты" page names the shop's address under the imported text: the text itself
  // carries no way to reach the seller.
  const contact = page.slug === 'contact' ? contactLine(ctx.shop.settings.contact_email, 'shop-page-contact') : '';
  if (isSectionBody(page.body_html)) {
    return contact ? `${body}<div class="page-width page-width--narrow">${contact}</div>` : body;
  }
  return `<section id="shopify-section-page__main" class="shopify-section section"><style data-shopify>.section-page-main-padding {
    padding-top: 21px;
    padding-bottom: 21px;
  }
  @media screen and (min-width: 750px) {
    .section-page-main-padding {
      padding-top: 28px;
      padding-bottom: 28px;
    }
  }</style><div class="page-width page-width--narrow section-page-main-padding">
  <h1 class="main-page-title page-title h0">
    ${esc(page.title)}
  </h1>
  <div class="rte">
    ${body}
  </div>${contact}
</div>
</section>`;
}

export function policyView(page: ShopPage): string {
  return `<div class="shopify-policy__container">
  <div class="shopify-policy__title">
    <h1>${esc(page.title)}</h1>
  </div>
  <div class="shopify-policy__body">
    <div class="rte">
        ${sanitizeHtml(page.body_html)}
    </div>
  </div>
</div>`;
}

/** `ctx` null: no shop is known (unknown organization), so there is nowhere to send the visitor. */
export function notFoundView(ctx: Ctx | null): string {
  return `<style type="text/css">
  .template-404 .title + * {
    margin-top: 1rem;
  }

  @media screen and (min-width: 750px) {
    .template-404 .title + * {
      margin-top: 2rem;
    }
  }
</style>

<div class="template-404 page-width page-margin center">
  <p>
    404
  </p>
  <h1 class="title">
    ${T.notFoundTitle}
  </h1>
  ${
    ctx
      ? `<a href="${esc(url(ctx, '/collections/all'))}" class="button">
    ${T.notFoundButton}
  </a>`
      : ''
  }
</div>`;
}

/** The bare platform address (no organization in the path): a short placeholder, not a shop. */
export function platformHomeView(): string {
  return `<div class="template-404 page-width page-margin center shop-platform">
  <h1 class="title">Arena Sold Out</h1>
  <p>${T.platformPlaceholder}</p>
</div>`;
}

/** Case-insensitive match over title, vendor, variant titles and the description text. */
export function searchProducts(products: ShopProduct[], query: string): ShopProduct[] {
  const terms = query
    .toLowerCase()
    .split(/\s+/)
    .filter(Boolean);
  if (terms.length === 0) return [];
  return products.filter((p) => {
    const hay = [p.title, p.vendor ?? '', ...p.variants.map((v) => v.title), p.body_html.replace(/<[^>]*>/g, ' ')]
      .join(' ')
      .toLowerCase();
    return terms.every((t) => hay.includes(t));
  });
}

export function searchView(ctx: Ctx, query: string): string {
  const results = searchProducts(ctx.shop.products, query);
  const performed = query.trim() !== '';
  const sid = 'search__results';
  const items = results
    .map(
      (p) => `<li class="grid__item">
            ${productCard(ctx, p, { sectionId: sid, showSecondary: true, quickAdd: false })}
          </li>`,
    )
    .join('');
  return `<style>
  .template-search__header {
    margin-bottom: 3rem;
  }
  .template-search__search {
    margin: 0 auto 3.5rem;
    max-width: 74.1rem;
  }
  .template-search__search .search {
    margin-top: 3rem;
  }
  .template-search--empty {
    padding-bottom: 18rem;
  }
  @media screen and (min-width: 750px) {
    .template-search__header {
      margin-bottom: 5rem;
    }
  }
  .search__button .icon {
    height: 1.8rem;
  }
</style><div class="template-search${performed && results.length > 0 ? '' : ' template-search--empty'} section-search-padding">
    <style data-shopify>.section-search-padding { padding-top: 27px; padding-bottom: 27px; } @media screen and (min-width: 750px) { .section-search-padding { padding-top: 36px; padding-bottom: 36px; } }</style>
    <div class="template-search__header page-width">
      <h1 class="h2 center">${performed ? T.searchResults : T.search}</h1>
      <div class="template-search__search">
        <predictive-search>
            <form action="${esc(url(ctx, '/search'))}" method="get" role="search" class="search" data-shop-search>
                <div class="field">
                  <input
                    class="search__input field__input"
                    id="Search-In-Template"
                    type="search"
                    name="q"
                    value="${esc(query)}"
                    placeholder="${T.search}"
                  >
                  <label class="field__label" for="Search-In-Template">${T.search}</label>
                  <button type="reset" class="reset__button field__button${performed ? '' : ' hidden'}" aria-label="${T.searchReset}">
                    <svg class="icon icon-close" aria-hidden="true" focusable="false">
                      <use xlink:href="#icon-reset">
                    </svg>
                  </button>
                  <button type="submit" class="search__button field__button" aria-label="${T.search}">
                    <svg class="icon icon-search" aria-hidden="true" focusable="false">
                      <use xlink:href="#icon-search">
                    </svg>
                  </button>
                </div>
              </form>
        </predictive-search>
      </div>${performed && results.length > 0 ? `
      <p role="status">${esc(resultsLabel(results.length, query))}</p>` : ''}
    </div>${
      performed && results.length === 0
        ? `<div class="page-width"><p class="center">По запросу «${esc(query)}» ничего не найдено. Проверьте правописание или выберите другие слова либо фразу.</p></div>`
        : ''
    }${
      results.length > 0
        ? `<div class="page-width"><ul class="grid product-grid grid--4-col-desktop grid--2-col-tablet-down" role="list">${items}</ul></div>`
        : ''
    }
  </div>`;
}
