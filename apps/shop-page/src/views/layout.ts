/**
 * layout.ts - everything around <main>: skip link, announcement bar, header
 * (sticky, menu drawer, search modal, cart icon), the cart notification, the
 * footer and the age gate. Markup is the live storefront's, byte for byte
 * where the data allows.
 */
import { sized } from '../lib/cdn';
import { esc } from '../lib/html';
import { cartCountLabel, T } from '../lib/i18n';
import { itemCount } from '../lib/cart';
import type { MenuItem } from '../lib/api';
import { icon, url, type Ctx } from './common';

/** Shopify's handleize: lowercase, runs of non-letters become one dash. */
export function handleize(s: string): string {
  return s
    .toLowerCase()
    .replace(/[^\p{L}\p{N}]+/gu, '-')
    .replace(/^-+|-+$/g, '');
}

function announcementBar(ctx: Ctx): string {
  const text = ctx.shop.settings.announcement_text;
  if (!text) return '';
  const link = ctx.theme.announcementUrl;
  const message = `<p class="announcement-bar__message h5">
            <span>${esc(text)}</span>${link ? icon('arrow') : ''}
</p>`;
  const inner = link
    ? `<a
              href="${esc(url(ctx, link))}"
              class="announcement-bar__link link link--text focus-inset animate-arrow"
            >${message}</a>`
    : message;
  return `<div id="shopify-section-announcement-bar" class="shopify-section shopify-section-group-header-group announcement-bar-section">
<div
  class="utility-bar color-background-1 gradient utility-bar--bottom-border header-localization"
>
  <div class="page-width utility-bar__grid utility-bar__grid--3-col"><div
        class="announcement-bar"
        role="region"
        aria-label="${T.announcement}"
      >${inner}</div><div class="localization-wrapper">
</div>
  </div>
</div>
</div>`;
}

function sprite(): string {
  return `<svg xmlns="http://www.w3.org/2000/svg" class="hidden">
  <symbol id="icon-search" viewbox="0 0 18 19" fill="none">
    <path fill-rule="evenodd" clip-rule="evenodd" d="M11.03 11.68A5.784 5.784 0 112.85 3.5a5.784 5.784 0 018.18 8.18zm.26 1.12a6.78 6.78 0 11.72-.7l5.4 5.4a.5.5 0 11-.71.7l-5.41-5.4z" fill="currentColor"/>
  </symbol>

  <symbol id="icon-reset" class="icon icon-close"  fill="none" viewBox="0 0 18 18" stroke="currentColor">
    <circle r="8.5" cy="9" cx="9" stroke-opacity="0.2"/>
    <path d="M6.82972 6.82915L1.17193 1.17097" stroke-linecap="round" stroke-linejoin="round" transform="translate(5 5)"/>
    <path d="M1.22896 6.88502L6.77288 1.11523" stroke-linecap="round" stroke-linejoin="round" transform="translate(5 5)"/>
  </symbol>

  <symbol id="icon-close" class="icon icon-close" fill="none" viewBox="0 0 18 17">
    <path d="M.865 15.978a.5.5 0 00.707.707l7.433-7.431 7.579 7.282a.501.501 0 00.846-.37.5.5 0 00-.153-.351L9.712 8.546l7.417-7.416a.5.5 0 10-.707-.708L8.991 7.853 1.413.573a.5.5 0 10-.693.72l7.563 7.268-7.418 7.417z" fill="currentColor">
  </symbol>
</svg>`;
}

function socialList(ctx: Ctx, cls: string, linkCls: string): string {
  const { facebook, instagram } = ctx.theme.social;
  const item = (href: string, ic: string, label: string): string =>
    href
      ? `<li class="list-social__item">
      <a href="${esc(href)}" class="${linkCls}">${icon(ic).replace('<svg\n', '<svg\n')}
<span class="visually-hidden">${label}</span>
      </a>
    </li>`
      : '';
  return `<ul class="${cls}" role="list">${item(facebook, 'facebook', 'Facebook')}${item(instagram, 'instagram', 'Instagram')}</ul>`;
}

export function cartBubble(count: number): string {
  const body =
    count === 0
      ? icon('cart-empty')
      : icon('cart');
  return `${body}<span class="visually-hidden">${T.cart}</span>${
    count > 0
      ? `<div class="cart-count-bubble">${count < 100 ? `<span aria-hidden="true">${count}</span>` : ''}<span class="visually-hidden">${cartCountLabel(count)}</span></div>`
      : ''
  }`;
}

function header(ctx: Ctx, home: boolean): string {
  const menu: MenuItem[] = ctx.shop.menus.main;
  const logo = ctx.theme.logo;
  const name = ctx.shop.settings.shop_name || ctx.theme.shopName;
  const accountHref = esc(url(ctx, '/pages/contact'));
  const drawerItems = menu
    .map(
      (m) => `<li><a
                      id="HeaderDrawer-${esc(handleize(m.title))}"
                      href="${esc(url(ctx, m.url))}"
                      class="menu-drawer__menu-item list-menu__item link link--text focus-inset"

                    >
                      ${esc(m.title)}
                    </a></li>`,
    )
    .join('');
  const inlineItems = menu
    .map(
      (m) => `<li><a
            id="HeaderMenu-${esc(handleize(m.title))}"
            href="${esc(url(ctx, m.url))}"
            class="header__menu-item list-menu__item link link--text focus-inset"

          >
            <span
            >${esc(m.title)}</span>
          </a></li>`,
    )
    .join('');
  const count = itemCount(ctx.cart);
  return `<div id="shopify-section-header" class="shopify-section shopify-section-group-header-group section-header">${sprite()}<sticky-header data-sticky-type="reduce-logo-size" class="header-wrapper color-background-1 gradient header-wrapper--border-bottom"><header class="header header--middle-left header--mobile-center page-width header--has-menu header--has-social header--has-account">

<header-drawer data-breakpoint="tablet">
  <details id="Details-menu-drawer-container" class="menu-drawer-container">
    <summary
      class="header__icon header__icon--menu header__icon--summary link focus-inset"
      aria-label="${T.menu}"
    >
      <span>
        ${icon('hamburger')}

        ${icon('close')}

      </span>
    </summary>
    <div id="menu-drawer" class="gradient menu-drawer motion-reduce color-background-1">
      <div class="menu-drawer__inner-container">
        <div class="menu-drawer__navigation-container">
          <nav class="menu-drawer__navigation">
            <ul class="menu-drawer__menu has-submenu list-menu" role="list">${drawerItems}</ul>
          </nav>
          <div class="menu-drawer__utility-links"><a
                href="${accountHref}"
                class="menu-drawer__account link focus-inset h5 medium-hide large-up-hide"
              >
                ${icon('account')}

${T.login}</a><div class="menu-drawer__localization header-localization">
</div>${socialList(ctx, 'list list-social list-unstyled', 'list-social__link link')}
          </div>
        </div>
      </div>
    </div>
  </details>
</header-drawer>
${home ? '<h1 class="header__heading">' : ''}<a href="${esc(url(ctx, '/'))}" class="header__heading-link link link--text focus-inset"><div class="header__heading-logo-wrapper">

                <img src="${esc(sized(logo.url, 600))}" alt="${esc(name)}" srcset="${esc(sized(logo.url, 110))} 110w, ${esc(sized(logo.url, 165))} 165w, ${esc(sized(logo.url, 220))} 220w" width="${logo.width}" height="${logo.height}" loading="eager" class="header__heading-logo motion-reduce" sizes="(max-width: 220px) 50vw, ${logo.displayWidth}px">
              </div></a>${home ? '</h1>' : ''}

<nav class="header__inline-menu">
  <ul class="list-menu list-menu--inline" role="list">${inlineItems}</ul>
</nav>

<div class="header__icons header__icons--localization header-localization">
      <div class="desktop-localization-wrapper">
</div>


<details-modal class="header__search">
  <details>
    <summary class="header__icon header__icon--search header__icon--summary link focus-inset modal__toggle" aria-haspopup="dialog" aria-label="${T.search}">
      <span>
        <svg class="modal__toggle-open icon icon-search" aria-hidden="true" focusable="false">
          <use href="#icon-search">
        </svg>
        <svg class="modal__toggle-close icon icon-close" aria-hidden="true" focusable="false">
          <use href="#icon-close">
        </svg>
      </span>
    </summary>
    <div class="search-modal modal__content gradient" role="dialog" aria-modal="true" aria-label="${T.search}">
      <div class="modal-overlay"></div>
      <div class="search-modal__content search-modal__content-bottom" tabindex="-1"><predictive-search class="search-modal__form"><form action="${esc(url(ctx, '/search'))}" method="get" role="search" class="search search-modal__form" data-shop-search>
              <div class="field">
                <input class="search__input field__input"
                  id="Search-In-Modal"
                  type="search"
                  name="q"
                  value=""
                  placeholder="${T.search}"
                    autocorrect="off"
                    autocomplete="off"
                    autocapitalize="off"
                    spellcheck="false">
                <label class="field__label" for="Search-In-Modal">${T.search}</label>
                <button type="reset" class="reset__button field__button hidden" aria-label="${T.searchReset}">
                  <svg class="icon icon-close" aria-hidden="true" focusable="false">
                    <use xlink:href="#icon-reset">
                  </svg>
                </button>
                <button class="search__button field__button" aria-label="${T.search}">
                  <svg class="icon icon-search" aria-hidden="true" focusable="false">
                    <use href="#icon-search">
                  </svg>
                </button>
              </div></form></predictive-search><button type="button" class="search-modal__close-button modal__close-button link link--text focus-inset" aria-label="${T.close}">
          <svg class="icon icon-close" aria-hidden="true" focusable="false">
            <use href="#icon-close">
          </svg>
        </button>
      </div>
    </div>
  </details>
</details-modal>

<a href="${accountHref}" class="header__icon header__icon--account link focus-inset small-hide">
          ${icon('account')}

          <span class="visually-hidden">${T.login}</span>
        </a><a href="${esc(url(ctx, '/cart'))}" class="header__icon header__icon--cart link focus-inset" id="cart-icon-bubble">${cartBubble(count)}</a>
    </div>
  </header>
</sticky-header>

${cartNotification(ctx)}
</div>`;
}

function cartNotification(ctx: Ctx): string {
  return `<cart-notification>
  <div class="cart-notification-wrapper page-width">
    <div
      id="cart-notification"
      class="cart-notification focus-inset color-background-1 gradient"
      aria-modal="true"
      aria-label="${T.itemAdded}"
      role="dialog"
      tabindex="-1"
    >
      <div class="cart-notification__header">
        <h2 class="cart-notification__heading caption-large text-body">${icon('checkmark')}
${T.itemAdded}
        </h2>
        <button
          type="button"
          class="cart-notification__close modal__close-button link link--text focus-inset"
          aria-label="${T.close}"
        >
          <svg class="icon icon-close" aria-hidden="true" focusable="false">
            <use href="#icon-close">
          </svg>
        </button>
      </div>
      <div id="cart-notification-product" class="cart-notification-product"></div>
      <div class="cart-notification__links">
        <a
          href="${esc(url(ctx, '/cart'))}"
          id="cart-notification-button"
          class="button button--secondary button--full-width"
        >${T.viewCart}</a>
        <a href="${esc(url(ctx, '/checkout'))}" class="button button--primary button--full-width" id="cart-notification-checkout">
          ${T.checkout}
        </a>
        <button type="button" class="link button-label">${T.continueShopping}</button>
      </div>
    </div>
  </div>
</cart-notification>
<style data-shopify>
  .cart-notification {
    display: none;
  }
</style>`;
}

function footer(ctx: Ctx): string {
  const t = ctx.theme;
  const rules = ctx.shop.menus.footer_rules;
  const info = ctx.shop.menus.footer_info;
  const menuBlock = (heading: string, items: MenuItem[]): string =>
    items.length === 0
      ? ''
      : `<div
                class="footer-block grid__item footer-block--menu"


              ><h2 class="footer-block__heading inline-richtext">${esc(heading)}</h2><ul class="footer-block__details-content list-unstyled">${items
          .map(
            (m) => `<li>
                            <a
                              href="${esc(url(ctx, m.url))}"
                              class="link link--text list-menu__item list-menu__item--link"
                            >
                              ${esc(m.title)}
                            </a>
                          </li>`,
          )
          .join('')}</ul></div>`;
  const fl = t.footerLogo;
  const year = new Date().getFullYear();
  const name = ctx.shop.settings.shop_name || t.shopName;
  return `<div id="shopify-section-footer" class="shopify-section shopify-section-group-footer-group">
<footer class="footer color-inverse gradient section-footer-padding"><div class="footer__content-top page-width"><div
            class="footer__blocks-wrapper grid grid--1-col grid--2-col grid--4-col-tablet "

          >${menuBlock('Информация для Вас', info)}${menuBlock(t.footerRulesHeading, rules)}<div
                class="footer-block grid__item"


              ><div class="footer-block__details-content footer-block-image center"><div
                          class="footer-block__image-wrapper"
                          style="max-width: min(100%, ${fl.maxWidth}px);"
                        >
                          <img
                            srcset="${esc(sized(fl.url, fl.maxWidth))}, ${esc(sized(fl.url, fl.maxWidth * 2))} 2x"
                            src="${esc(sized(fl.url, 760))}"
                            alt=""
                            loading="lazy"
                            width="${fl.width}"
                            height="${fl.height}"
                          >
                        </div></div></div><div
                class="footer-block grid__item"


              ><div class="footer-block__brand-info"><h2 class="footer-block__heading rte">${esc(t.brandHeadline)}</h2><div class="rte">${t.brandHtml}</div>

${socialList(ctx, 'list-unstyled list-social footer__list-social', 'link list-social__link')}
</div></div></div><div
          class="footer-block--newsletter"

        ></div>
      </div><div
    class="footer__content-bottom"

  >
    <div class="footer__content-bottom-wrapper page-width">
      <div class="footer__column footer__localization isolate"></div>
      <div class="footer__column footer__column--info"><div class="footer__payment">
            <span class="visually-hidden">${T.paymentMethods}</span>
            <ul class="list list-payment" role="list"></ul>
          </div></div>
    </div>
    <div class="footer__content-bottom-wrapper page-width">
      <div class="footer__copyright caption">
        <small class="copyright__content"
          >&copy; ${year}, <a href="${esc(url(ctx, '/'))}" title="">${esc(name)}</a></small>
        <small class="copyright__content">${T.poweredBy}</small></div>
    </div>
  </div>
</footer>

<style data-shopify>.footer {
    margin-top: 0px;
  }

  .section-footer-padding {
    padding-top: 27px;
    padding-bottom: 27px;
  }

  @media screen and (min-width: 750px) {
    .footer {
      margin-top: 0px;
    }

    .section-footer-padding {
      padding-top: 36px;
      padding-bottom: 36px;
    }
  }</style>
</div>`;
}

function ageGate(ctx: Ctx): string {
  const minAge = ctx.shop.settings.age_gate_min_age ?? 0;
  if (minAge <= 0) return '';
  const g = ctx.theme.ageGate;
  return `<div id="popup" data-exit-url="${esc(g.exitUrl)}">
        <div class="popup-content">
            <div class="popup-contents">
            <div class="section-heading">${esc(g.heading)}</div>
            <div class="section-text">+${minAge}</div>
            <div class="buttons">
                <button id="allowButton">${esc(g.allow)}</button>
                <button id="denyButton">${esc(g.deny)}</button>
            </div>
        </div>
        </div>
    </div>`;
}

export function layout(ctx: Ctx, mainHtml: string, home = false): string {
  return `<a class="skip-to-content-link button visually-hidden" href="#MainContent">
      ${T.skipToContent}
    </a>${announcementBar(ctx)}${header(ctx, home)}
    <main id="MainContent" class="content-for-layout focus-none" role="main" tabindex="-1">${mainHtml}</main>
    ${footer(ctx)}
    <ul hidden>
      <li id="a11y-refresh-page-message">Выбор выделенного элемента приводит к полному обновлению страницы.</li>
      <li id="a11y-new-window-message">Открывается в новом окне.</li>
    </ul>${ageGate(ctx)}`;
}
