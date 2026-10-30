/**
 * cart.ts - the /cart page and the product rows of the "item added" cart
 * notification (the live theme uses cart_type "notification", not a drawer).
 * Markup is Dawn 11's main-cart-items / main-cart-footer /
 * cart-notification-product sections.
 */
import { limitMessage, subtotalMinor, variantLimit, type CartLine } from '../lib/cart';
import { sized } from '../lib/cdn';
import { esc } from '../lib/html';
import { T } from '../lib/i18n';
import { cartSrcset, icon, money, optionPairs, ratioOf, spinner, url, type Ctx } from './common';

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

function optionLines(line: CartLine): string {
  const pairs = optionPairs(line.product, line.variant);
  if (pairs.length === 0) return '';
  return `<dl>${pairs
    .map(
      ([name, value]) => `<div class="product-option">
                              <dt>${esc(name)}:</dt>
                              <dd>${esc(value)}</dd>
                            </div>`,
    )
    .join('')}</dl>

                      <p class="product-option"></p>`;
}

function lineRow(ctx: Ctx, line: CartLine, index: number, limitedVariant: string | null): string {
  const n = index + 1;
  const lineMax = ctx.shop.settings.max_line_qty;
  const max = variantLimit(line.variant, lineMax);
  const atMax = line.qty >= max;
  const limitNote = limitMessage(max, lineMax);
  const img = line.product.images[0];
  const link = esc(url(ctx, '/products/' + line.product.slug));
  const title = esc(line.product.title);
  const lineMoney = money(ctx, line.lineMinor);
  const total = (cls: string): string => `<td class="cart-item__totals right ${cls}">
                    <div class="loading-overlay hidden">
                      <div class="loading-overlay__spinner">
                        ${spinner()}
                      </div>
                    </div>
                    <div class="cart-item__price-wrapper"><span class="price price--end">
                          ${esc(lineMoney)}
                        </span></div>
                  </td>`;
  const image = img
    ? `<a href="${link}" class="cart-item__link" aria-hidden="true" tabindex="-1"> </a>
                      <div class="cart-item__image-container gradient global-media-settings">
                        <img
                          src="${esc(sized(img.url, 240))}"${cartSrcset(img, '100px')}
                          class="cart-item__image"
                          alt="${esc(img.alt ?? '')}"
                          loading="lazy"
                          width="150"
                          height="${Math.ceil(150 / ratioOf(img))}"
                        >
                      </div>`
    : '';
  return `<tr class="cart-item" id="CartItem-${n}" data-variant-id="${esc(line.variant.id)}">
                  <td class="cart-item__media">
                    ${image}
                  </td>

                  <td class="cart-item__details">
                    <a href="${link}" class="cart-item__name h4 break">${title}</a>
                    <div class="product-option">
                        ${esc(money(ctx, line.variant.price_minor))}
                      </div>
                    ${optionLines(line)}
                    <ul class="discounts list-unstyled" role="list" aria-label="Скидка"></ul>
                  </td>

                  ${total('medium-hide large-up-hide')}
                  <td class="cart-item__quantity">
                    <quantity-popover>
                      <div class="cart-item__quantity-wrapper quantity-popover-wrapper">
                        <label class="visually-hidden" for="Quantity-${n}">
                          ${T.quantity}
                        </label>
                        <div class="quantity-popover-container">
                          <quantity-input class="quantity cart-quantity">
                            <button class="quantity__button no-js-hidden" name="minus" type="button">
                              <span class="visually-hidden">Уменьшить количество ${title}</span>
                              ${icon('minus')}
                            </button>
                            <input
                              class="quantity__input"
                              data-quantity-variant-id="${esc(line.variant.id)}"
                              type="number"
                              name="updates[]"
                              value="${line.qty}"
                              data-cart-quantity="${line.qty}"
                              min="1"
                              max="${max}"
                              step="1"
                              aria-label="Количество: ${title}"
                              id="Quantity-${n}"
                              data-index="${n}"
                            >
                            <button class="quantity__button no-js-hidden" name="plus" type="button"${atMax ? ` aria-disabled="true" title="${esc(limitNote)}"` : ''}>
                              <span class="visually-hidden">Увеличить количество ${title}</span>
                              ${icon('plus')}
                            </button>
                          </quantity-input>
                        </div>
                        <cart-remove-button
                          id="Remove-${n}"
                          data-index="${n}"
                        >
                          <a
                            href="${esc(url(ctx, '/cart'))}"
                            class="button button--tertiary"
                            aria-label="Удалить «${title}»"
                            data-remove-variant="${esc(line.variant.id)}"
                          >
                            ${icon('remove')}
                          </a>
                        </cart-remove-button>
                      </div>
                      <div class="cart-item__error" id="Line-item-error-${n}" role="alert">
                        <small class="cart-item__error-text">${limitedVariant === line.variant.id ? esc(limitNote) : ''}</small>
                        ${icon('error')}
                      </div>
                    </quantity-popover>
                  </td>

                  ${total('small-hide')}
                </tr>`;
}

export function cartItemsTable(ctx: Ctx, lines: CartLine[], limitedVariant: string | null = null): string {
  if (lines.length === 0) return '';
  return `<table class="cart-items">
            <caption class="visually-hidden">
              ${T.cart}
            </caption>
            <thead>
              <tr>
                <th class="caption-with-letter-spacing" colspan="2" scope="col">
                  ${T.product}
                </th>
                <th class="medium-hide large-up-hide right caption-with-letter-spacing" colspan="1" scope="col">
                  ${T.total}
                </th>
                <th
                  class="cart-items__heading--wide cart-items__heading--quantity small-hide caption-with-letter-spacing"
                  colspan="1"
                  scope="col"
                >
                  ${T.quantity}
                </th>
                <th class="small-hide right caption-with-letter-spacing" colspan="1" scope="col">
                  ${T.total}
                </th>
              </tr>
            </thead>

            <tbody>${lines.map((l, i) => lineRow(ctx, l, i, limitedVariant)).join('')}</tbody>
          </table>`;
}

/** `limitedVariant`: the line whose typed quantity was cut to the shop's limit (its note is shown once). */
export function cartView(ctx: Ctx, lines: CartLine[], limitedVariant: string | null = null): string {
  const empty = lines.length === 0;
  const all = esc(url(ctx, '/collections/all'));
  const subtotal = money(ctx, subtotalMinor(lines));
  return `<div id="shopify-section-cart__items" class="shopify-section">${sectionPadding('section-cart-items-padding', 36, 36)}
<cart-items class="page-width${empty ? ' is-empty' : ' section-cart-items-padding'}">
  <div class="title-wrapper-with-link">
    <h1 class="title title--primary">${T.cart}</h1>
    <a href="${all}" class="underlined-link">${T.continueShopping}</a>
  </div>
  <div class="cart__warnings">
    <h1 class="cart__empty-text">${T.cartEmpty}</h1>
    <a href="${all}" class="button">
      ${T.continueShopping}
    </a>
  </div>
  <form action="${esc(url(ctx, '/cart'))}" class="cart__contents critical-hidden" method="post" id="cart">
    <div class="cart__items" id="main-cart-items" data-id="cart__items">
      <div class="js-contents">${cartItemsTable(ctx, lines, limitedVariant)}</div>
    </div>
    <p class="visually-hidden" id="cart-live-region-text" aria-live="polite" role="status"></p>
  </form>
</cart-items>
</div><div id="shopify-section-cart__footer" class="shopify-section cart__footer-wrapper">${sectionPadding('section-cart-footer-padding', 20, 40)}
<div class="page-width${empty ? ' is-empty' : ''}" id="main-cart-footer" data-id="cart__footer">
  <div>
    <div class="cart__footer section-cart-footer-padding"><div class="cart__blocks">
<div class="js-contents" >
                <div></div>
                <div class="totals">
                  <h2 class="totals__total">${T.estimatedTotal}</h2>
                  <p class="totals__total-value">${esc(subtotal)}</p>
                </div>
                <small class="tax-note caption-large rte">${T.cartTaxNote}
</small>
              </div>
<div class="cart__ctas" >
                <button
                  type="submit"
                  id="checkout"
                  class="cart__checkout-button button"
                  name="checkout"${empty ? `
                    disabled` : ''}
                  form="cart"
                >
                  ${T.checkout}
                </button>
              </div>
        <div id="cart-errors"></div>
      </div>
    </div>
  </div>
</div>
</div>`;
}

/** Rows of the "item added to the cart" notification (Dawn's cart-notification-product). */
export function notificationProduct(line: CartLine, limitNote?: string): string {
  const img = line.product.images[0];
  const image = img
    ? `<div class="cart-notification-product__image global-media-settings">
          <img
            src="${esc(sized(img.url, 165))}"${cartSrcset(img, '70px')}
            alt="${esc(img.alt ?? '')}"
            width="70"
            height="${Math.ceil(70 / ratioOf(img))}"
            loading="lazy"
          >
        </div>`
    : '';
  const option = optionPairs(line.product, line.variant)
    .map(
      ([name, value]) => `<div class="product-option">
                <dt>${esc(name)}:</dt>
                <dd>${esc(value)}</dd>
              </div>`,
    )
    .join('');
  // Dawn puts the INNER html of #cart-notification-product-{key} into the flex row, so no wrapper here.
  return `${image}
      <div>
        <h3 class="cart-notification-product__name h4">${esc(line.product.title)}</h3>
        <dl>${option}</dl>${limitNote ? `
        <p class="cart-notification-product__limit caption" role="status">${esc(limitNote)}</p>` : ''}
      </div>`;
}
