/**
 * theme/index.ts - the stylesheet stack of a page.
 *
 * Dawn loads its CSS per section, in document order, and a few rules fight
 * (for example `.card-information > *` against `.price` for line-height:
 * whichever sheet comes later wins). To look the same to the pixel this app
 * concatenates the very same files in the very same order the live storefront
 * links them on each kind of page, including the second appearance of
 * component-price after component-card that the product and home pages have.
 */
import base from './base.css?inline';
import predictiveSearch from './component-predictive-search.css?inline';
import slideshow from './component-slideshow.css?inline';
import slider from './component-slider.css?inline';
import listSocial from './component-list-social.css?inline';
import listMenu from './component-list-menu.css?inline';
import search from './component-search.css?inline';
import menuDrawer from './component-menu-drawer.css?inline';
import cartNotification from './component-cart-notification.css?inline';
import cartItems from './component-cart-items.css?inline';
import price from './component-price.css?inline';
import loadingOverlay from './component-loading-overlay.css?inline';
import imageBanner from './section-image-banner.css?inline';
import card from './component-card.css?inline';
import templateCollection from './template-collection.css?inline';
import rating from './component-rating.css?inline';
import volumePricing from './component-volume-pricing.css?inline';
import sectionFooter from './section-footer.css?inline';
import newsletter from './component-newsletter.css?inline';
import listPayment from './component-list-payment.css?inline';
import richText from './section-rich-text.css?inline';
import mainProduct from './section-main-product.css?inline';
import accordion from './component-accordion.css?inline';
import deferredMedia from './component-deferred-media.css?inline';
import pickupAvailability from './component-pickup-availability.css?inline';
import relatedProducts from './section-related-products.css?inline';
import cart from './component-cart.css?inline';
import totals from './component-totals.css?inline';
import discounts from './component-discounts.css?inline';
import quantityPopover from './quantity-popover.css?inline';
import mainPage from './section-main-page.css?inline';
import contactForm from './section-contact-form.css?inline';
import quickAdd from './quick-add.css?inline';
import collectionHero from './component-collection-hero.css?inline';
import articleCard from './component-article-card.css?inline';
import featuredBlog from './section-featured-blog.css?inline';
import imageWithText from './component-image-with-text.css?inline';
import facets from './component-facets.css?inline';
import arenaShop from './arena-shop.css?inline';
import arenaHeader from './arena-header.css?inline';
import arenaCustom from './arena-custom.css?inline';
import shopCss from './shop.css?inline';
import ageGate from './age-gate.css?inline';

import type { Route } from '../lib/route';

/** Everything the live <head> links before the page's own sections. */
const HEAD = [arenaShop, base, predictiveSearch, ageGate, slideshow, slider, listSocial, listMenu, search, menuDrawer, cartNotification, cartItems, price, loadingOverlay, arenaHeader];

/** The footer section's sheets come after the page body. */
const FOOT = [sectionFooter, newsletter, listMenu, listPayment, listSocial];

/** A CMS page is built from sections; this is the union, in the order of the richest live page (events). */
const PAGE_SECTIONS = [imageBanner, slider, card, articleCard, featuredBlog, slider, card, articleCard, featuredBlog, richText, imageWithText, contactForm, mainPage];

const BODY: Record<Route['kind'], string[]> = {
  home: [imageBanner, card, price, slider, templateCollection, rating, volumePricing],
  collection: [collectionHero, card, price, slider, templateCollection, quickAdd, rating, volumePricing],
  product: [mainProduct, accordion, price, slider, rating, loadingOverlay, deferredMedia, pickupAvailability, card, price, relatedProducts],
  cart: [cart, cartItems, totals, price, discounts, loadingOverlay, quantityPopover, cart, totals, price, discounts],
  checkout: [cart, cartItems, totals, price, discounts, quantityPopover, mainPage],
  checkout_result: [],
  status: [mainPage],
  page: PAGE_SECTIONS,
  policy: [mainPage],
  search: [templateCollection, card, price, search, facets, rating, volumePricing],
  not_found: [mainPage],
};

const cache = new Map<string, string>();

export function themeCss(kind: Route['kind']): string {
  let css = cache.get(kind);
  if (css === undefined) {
    css = [...HEAD, ...(BODY[kind] ?? []), ...FOOT, arenaCustom, shopCss].join('\n');
    cache.set(kind, css);
  }
  return css;
}

let applied: string | null = null;

/** Puts the stack for a page kind into <head> (one <style>, replaced on change of kind). */
export function applyTheme(kind: Route['kind']): void {
  if (applied === kind) return;
  let el = document.getElementById('shop-theme') as HTMLStyleElement | null;
  if (!el) {
    el = document.createElement('style');
    el.id = 'shop-theme';
    document.head.appendChild(el);
  }
  el.textContent = themeCss(kind);
  applied = kind;
}
