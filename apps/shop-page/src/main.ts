/**
 * main.ts - boots the storefront: finds the organization (first path segment
 * on a platform host, GET /v1/public/shop/by-host on a custom domain), loads
 * the catalog, renders the route and wires the interactions. The page is
 * re-rendered as a whole on every navigation (cheap: it is a string of
 * markup) so Dawn's own custom elements simply upgrade as they appear.
 */

import {
  ApiError,
  createCheckout,
  getOrderStatus,
  getPage,
  getProduct,
  getShop,
  resolveHost,
  type OrderStatus,
  type ShopData,
  type ShopPage,
} from './lib/api';
import {
  addItem,
  allAvailable,
  buildCheckoutRequest,
  itemCount,
  loadCart,
  removeItem,
  resolveLines,
  saveCart,
  setQty,
  validateCheckoutForm,
  type CartItem,
  type CheckoutForm,
} from './lib/cart';
import { T } from './lib/i18n';
import { applyTheme } from './theme';
import { href as buildHref, locate, type Route } from './lib/route';
import { EINAT_THEME } from './lib/theme';
import { cartView, notificationProduct } from './views/cart';
import { collectionView, homeView } from './views/catalog';
import { checkoutView, EMPTY_FORM, statusView, type CheckoutViewState } from './views/checkout';
import { priceBlock, type Ctx } from './views/common';
import { cartBubble, layout } from './views/layout';
import { buyButtonHtml, productView } from './views/product';
import { notFoundView, pageView, policyView, searchView } from './views/static';

interface State {
  org: string;
  base: string;
  shop: ShopData;
  cart: CartItem[];
}

let state: State | null = null;
let pollTimer: number | undefined;
let checkoutState: CheckoutViewState = { values: { ...EMPTY_FORM }, errors: {} };
const pageCache = new Map<string, ShopPage>();

const root = (): HTMLElement => document.getElementById('shop-root') as HTMLElement;

function ctxOf(s: State): Ctx {
  return { org: s.org, base: s.base, shop: s.shop, theme: EINAT_THEME, cart: s.cart };
}

// ---- organization ---------------------------------------------------------------

const HOST_CACHE_KEY = 'arena_shop_host:';

async function orgForHost(host: string): Promise<string> {
  try {
    const cached = localStorage.getItem(HOST_CACHE_KEY + host);
    if (cached) {
      const { slug, at } = JSON.parse(cached) as { slug: string; at: number };
      if (slug && Date.now() - at < 3600_000) return slug;
    }
  } catch {
    /* no cache */
  }
  const r = await resolveHost(host);
  try {
    localStorage.setItem(HOST_CACHE_KEY + host, JSON.stringify({ slug: r.org_slug, at: Date.now() }));
  } catch {
    /* no cache */
  }
  return r.org_slug;
}

// ---- rendering ------------------------------------------------------------------

function setMeta(title: string, description?: string): void {
  document.title = title;
  const m = document.querySelector('meta[name="description"]');
  if (m && description !== undefined) m.setAttribute('content', description);
}

function resetTransient(): void {
  if (pollTimer !== undefined) {
    window.clearTimeout(pollTimer);
    pollTimer = undefined;
  }
  // <product-modal> moves itself to <body> and would outlive the page that made it.
  document.querySelectorAll('body > product-modal').forEach((n) => n.remove());
  document.body.classList.remove('overflow-hidden', 'overflow-hidden-tablet', 'overflow-hidden-desktop', 'overflow-hidden-mobile');
}

/** Links typed into CMS content ("/pages/contact", the old domain) must stay inside this storefront. */
function fixContentLinks(base: string): void {
  const scope = document.getElementById('MainContent');
  if (!scope) return;
  scope.querySelectorAll<HTMLAnchorElement>('a[href]').forEach((a) => {
    const raw = a.getAttribute('href') ?? '';
    let path: string | null = null;
    if (raw.startsWith('/') && !raw.startsWith('//')) {
      if (base === '' || raw === base || raw.startsWith(base + '/')) return;
      path = raw;
    } else {
      try {
        const u = new URL(raw);
        if (/(^|\.)einatwinery\.com$/i.test(u.hostname)) path = u.pathname + u.search + u.hash;
      } catch {
        return;
      }
    }
    if (path !== null) a.setAttribute('href', buildHref(base, path));
  });
}

async function renderRoute(route: Route): Promise<void> {
  const s = state as State;
  const ctx = ctxOf(s);
  resetTransient();
  const shopName = s.shop.settings.shop_name || EINAT_THEME.shopName;
  const lines = resolveLines(s.cart, s.shop.products);
  let main = '';
  let kind: Route['kind'] = route.kind;
  let title = shopName;
  let description: string | undefined;

  switch (route.kind) {
    case 'home':
      main = homeView(ctx);
      title = `${EINAT_THEME.homeTitle} – ${shopName}`;
      description = EINAT_THEME.homeDescription;
      break;
    case 'collection':
      main = collectionView(ctx);
      title = `${T.collectionProducts} – ${shopName}`;
      break;
    case 'product': {
      let p = s.shop.products.find((x) => x.slug === route.slug);
      if (!p) {
        try {
          p = await getProduct(s.org, route.slug);
        } catch {
          p = undefined;
        }
      }
      if (!p) {
        main = notFoundView(ctx);
        kind = 'not_found';
        title = `${T.notFoundTitle} – ${shopName}`;
      } else {
        main = productView(ctx, p);
        title = `${p.title} – ${shopName}`;
      }
      break;
    }
    case 'cart':
      main = cartView(ctx, lines);
      title = `${T.cart} – ${shopName}`;
      break;
    case 'checkout':
      main = checkoutView(ctx, lines, checkoutState);
      title = `Оформление заказа – ${shopName}`;
      break;
    case 'checkout_result': {
      const q = new URLSearchParams(location.search);
      let token = q.get('checkout_token') || q.get('order_token') || q.get('token') || '';
      if (!token) {
        try {
          token = localStorage.getItem('arena_shop_order:' + s.org) ?? '';
        } catch {
          token = '';
        }
      }
      if (token) {
        history.replaceState({}, '', buildHref(s.base, '/checkout/' + token));
        await renderRoute({ kind: 'status', token });
      } else {
        history.replaceState({}, '', buildHref(s.base, '/cart'));
        await renderRoute({ kind: 'cart' });
      }
      return;
    }
    case 'status': {
      title = `Заказ – ${shopName}`;
      main = statusView(ctx, 'loading');
      draw('status', ctx, main, title, description);
      void pollStatus(route.token);
      return;
    }
    case 'page':
    case 'policy': {
      let page = pageCache.get(route.slug);
      if (!page) {
        try {
          page = await getPage(s.org, route.slug);
          pageCache.set(route.slug, page);
        } catch (e) {
          if (!(e instanceof ApiError) || e.status !== 404) {
            console.error(e);
          }
        }
      }
      if (!page) {
        main = notFoundView(ctx);
        kind = 'not_found';
        title = `${T.notFoundTitle} – ${shopName}`;
      } else {
        main = route.kind === 'policy' ? policyView(page) : pageView(ctx, page);
        title = `${page.title} – ${shopName}`;
      }
      break;
    }
    case 'search':
      main = searchView(ctx, route.query);
      title = `${T.searchResults} – ${shopName}`;
      break;
    default:
      main = notFoundView(ctx);
        kind = 'not_found';
      title = `${T.notFoundTitle} – ${shopName}`;
  }
  draw(kind, ctx, main, title, description);
}

function draw(kind: Route['kind'], ctx: Ctx, main: string, title: string, description?: string): void {
  applyTheme(kind);
  root().innerHTML = layout(ctx, main, kind === 'home');
  setMeta(title, description);
  fixContentLinks(ctx.base);
  applyAgeGate();
}

function currentRoute(): Route {
  return locate(location.hostname, location.pathname, location.search).route;
}

async function rerender(): Promise<void> {
  await renderRoute(currentRoute());
}

async function navigate(to: string, replace = false): Promise<void> {
  if (replace) history.replaceState({}, '', to);
  else history.pushState({}, '', to);
  window.scrollTo(0, 0);
  await rerender();
}

// ---- status polling -------------------------------------------------------------

async function pollStatus(token: string, attempt = 0): Promise<void> {
  const s = state as State;
  const ctx = ctxOf(s);
  let st: OrderStatus | 'error';
  try {
    st = await getOrderStatus(token);
  } catch {
    st = 'error';
  }
  // The buyer may have navigated away while the request was in flight.
  const r = currentRoute();
  if (r.kind !== 'status' || r.token !== token) return;
  if (st !== 'error' && st.status === 'paid') {
    s.cart = [];
    saveCart(s.org, []);
    try {
      localStorage.removeItem('arena_shop_order:' + s.org);
    } catch {
      /* ignore */
    }
  }
  const view = statusView(ctxOf(s), st);
  root().innerHTML = layout(ctxOf(s), view);
  fixContentLinks(ctx.base);
  applyAgeGate();
  if (st !== 'error' && st.status === 'pending' && attempt < 90) {
    pollTimer = window.setTimeout(() => void pollStatus(token, attempt + 1), 4000);
  }
}

// ---- age gate -------------------------------------------------------------------

const ageKey = (): string => 'arena_shop_age_ok:' + (state?.org ?? '');

function ageAccepted(): boolean {
  try {
    return localStorage.getItem(ageKey()) === '1';
  } catch {
    return false;
  }
}

function applyAgeGate(): void {
  const popup = document.getElementById('popup');
  if (popup && !ageAccepted()) popup.style.display = 'block';
}

// ---- cart -----------------------------------------------------------------------

function commitCart(items: CartItem[]): void {
  const s = state as State;
  s.cart = items;
  saveCart(s.org, items);
  const bubble = document.getElementById('cart-icon-bubble');
  if (bubble) bubble.innerHTML = cartBubble(itemCount(items));
}

function openNotification(variantId: string): void {
  const s = state as State;
  const line = resolveLines([{ variant_id: variantId, qty: 1 }], s.shop.products)[0];
  const host = document.getElementById('cart-notification-product');
  const box = document.getElementById('cart-notification');
  if (!line || !host || !box) return;
  host.innerHTML = notificationProduct(line);
  box.classList.add('animate', 'active');
  const btn = box.querySelector<HTMLElement>('.cart-notification__close');
  btn?.focus();
}

function closeNotification(): void {
  document.getElementById('cart-notification')?.classList.remove('active');
}

function addToCart(variantId: string, qty: number): void {
  const s = state as State;
  const hit = resolveLines([{ variant_id: variantId, qty: 1 }], s.shop.products)[0];
  if (!hit || !hit.variant.available) return;
  commitCart(addItem(s.cart, variantId, qty));
  openNotification(variantId);
}

// ---- events ---------------------------------------------------------------------

function onClick(e: MouseEvent): void {
  if (!state) return;
  const target = e.target as HTMLElement;

  // Age gate
  if (target.closest('#allowButton')) {
    try {
      localStorage.setItem(ageKey(), '1');
    } catch {
      /* remembered for this page only */
    }
    const popup = document.getElementById('popup');
    if (popup) popup.style.display = 'none';
    return;
  }
  if (target.closest('#denyButton')) {
    const popup = document.getElementById('popup');
    window.location.href = popup?.dataset.exitUrl || 'https://www.google.com/';
    return;
  }

  // Cart notification
  if (target.closest('.cart-notification__close') || target.closest('.cart-notification__links .button-label')) {
    closeNotification();
    return;
  }

  // Quick add on a product with several variants: pick them on the product page
  const quick = target.closest<HTMLElement>('.quick-add__submit[data-product-url]');
  if (quick) {
    e.preventDefault();
    void navigate(quick.dataset.productUrl as string);
    return;
  }

  // "Купить": put the product in the basket and go straight to checkout
  const buy = target.closest<HTMLElement>('[data-buy-now]');
  if (buy) {
    const form = document.getElementById('product-form-product__main') as HTMLFormElement | null;
    const id = form?.dataset.variantId;
    if (form && id && state) {
      const qtyInput = form.elements.namedItem('quantity') as HTMLInputElement | null;
      const qty = Math.max(1, parseInt(qtyInput?.value ?? '1', 10) || 1);
      commitCart(addItem(state.cart, id, qty));
      void navigate(buildHref(state.base, '/checkout'));
    }
    return;
  }

  // Remove a cart line
  const remove = target.closest<HTMLElement>('[data-remove-variant]');
  if (remove) {
    e.preventDefault();
    const s = state;
    commitCart(removeItem(s.cart, remove.dataset.removeVariant as string));
    void rerender();
    return;
  }

  // Search reset
  const reset = target.closest<HTMLElement>('.reset__button');
  if (reset) {
    const input = reset.closest('form')?.querySelector<HTMLInputElement>('input[name="q"]');
    if (input) {
      input.value = '';
      input.focus();
    }
    reset.classList.add('hidden');
    return;
  }

  // Internal navigation
  const a = target.closest<HTMLAnchorElement>('a[href]');
  if (!a || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  if (a.target && a.target !== '_self') return;
  if (a.hasAttribute('download')) return;
  const rawHref = a.getAttribute('href') ?? '';
  if (rawHref.startsWith('#') || rawHref === '') return;
  let u: URL;
  try {
    u = new URL(a.href, location.href);
  } catch {
    return;
  }
  if (u.origin !== location.origin) return;
  const base = state.base;
  if (base !== '' && !(u.pathname === base || u.pathname.startsWith(base + '/'))) return;
  e.preventDefault();
  closeNotification();
  void navigate(u.pathname + u.search + u.hash);
}

function onSubmit(e: Event): void {
  if (!state) return;
  const form = e.target as HTMLFormElement;
  const s = state;

  if (form.matches('form[data-type="add-to-cart-form"]')) {
    e.preventDefault();
    const qtyInput = form.elements.namedItem('quantity') as HTMLInputElement | null;
    const qty = Math.max(1, parseInt(qtyInput?.value ?? '1', 10) || 1);
    addToCart(form.dataset.variantId as string, qty);
    return;
  }
  if (form.id === 'cart') {
    e.preventDefault();
    if (s.cart.length > 0) void navigate(buildHref(s.base, '/checkout'));
    return;
  }
  if (form.matches('form[data-shop-search]')) {
    e.preventDefault();
    const q = (form.elements.namedItem('q') as HTMLInputElement).value.trim();
    document.querySelector<HTMLElement & { close?: (focus: boolean) => void }>('details-modal')?.close?.(false);
    void navigate(buildHref(s.base, '/search') + (q ? '?q=' + encodeURIComponent(q) : ''));
    return;
  }
  if (form.id === 'checkout-form') {
    e.preventDefault();
    void submitCheckout(form);
    return;
  }
  if (form.id === 'ContactForm') {
    e.preventDefault();
    sendContact(form);
  }
}

function onChange(e: Event): void {
  if (!state) return;
  const el = e.target as HTMLInputElement;
  const s = state;

  // Cart page quantity
  if (el.matches('.cart-item .quantity__input')) {
    const id = el.dataset.quantityVariantId as string;
    const qty = parseInt(el.value, 10);
    commitCart(setQty(s.cart, id, Number.isFinite(qty) ? qty : 1));
    void rerender();
    return;
  }

  // Variant pills on the product page
  if (el.matches('.variant-radios input[type="radio"]')) {
    const info = document.querySelector<HTMLElement>('product-info[data-product-slug]');
    const product = s.shop.products.find((p) => p.slug === info?.dataset.productSlug);
    const variant = product?.variants.find((v) => v.id === el.dataset.variantId);
    if (!product || !variant) return;
    const priceHost = document.getElementById('price-product__main');
    if (priceHost) priceHost.innerHTML = priceBlock(ctxOf(s), product, { variant, showBadges: true, large: true });
    const form = document.getElementById('product-form-product__main') as HTMLFormElement | null;
    if (form) {
      form.dataset.variantId = variant.id;
      const hidden = form.querySelector<HTMLInputElement>('input.product-variant-id');
      if (hidden) hidden.value = variant.id;
    }
    const btn = document.getElementById('ProductSubmitButton-product__main') as HTMLButtonElement | null;
    if (btn) {
      btn.disabled = !variant.available;
      btn.innerHTML = buyButtonHtml(variant);
    }
    const buyNow = document.querySelector<HTMLElement>('.shopify-payment-button');
    if (buyNow) buyNow.hidden = !variant.available;
    return;
  }

  // Keep the checkout form's values when it is re-rendered with errors
  if (el.form?.id === 'checkout-form') {
    checkoutState.values = readCheckoutForm(el.form);
  }
}

function onInput(e: Event): void {
  const el = e.target as HTMLInputElement;
  if (el.matches('.search__input')) {
    el.closest('form')?.querySelector('.reset__button')?.classList.toggle('hidden', el.value === '');
  }
}

function onFocusIn(e: FocusEvent): void {
  const el = e.target as HTMLInputElement;
  if (el.id && el.id.startsWith('ShareUrl-')) el.select();
}

function onKeyUp(e: KeyboardEvent): void {
  if (e.key === 'Escape') closeNotification();
}

// ---- checkout -------------------------------------------------------------------

function readCheckoutForm(form: HTMLFormElement): CheckoutForm {
  const v = (n: string): string => (form.elements.namedItem(n) as HTMLInputElement | null)?.value ?? '';
  return {
    name: v('name'),
    email: v('email'),
    phone: v('phone'),
    address1: v('address1'),
    address2: v('address2'),
    city: v('city'),
    postal: v('postal'),
    consent18: (form.elements.namedItem('consent18') as HTMLInputElement | null)?.checked ?? false,
  };
}

async function submitCheckout(form: HTMLFormElement): Promise<void> {
  const s = state as State;
  const values = readCheckoutForm(form);
  const errors = validateCheckoutForm(values, s.shop.settings.age_gate_min_age);
  checkoutState = { values, errors };
  if (Object.keys(errors).length > 0) {
    await rerender();
    (document.querySelector('.field--with-error input, [aria-invalid="true"]') as HTMLElement | null)?.focus();
    return;
  }
  const lines = resolveLines(s.cart, s.shop.products);
  if (lines.length === 0) return;
  if (!allAvailable(lines)) {
    checkoutState = { values, errors: {}, message: 'Некоторых товаров из вашей корзины уже нет в наличии. Удалите их из корзины.' };
    await rerender();
    return;
  }
  checkoutState = { values, errors: {}, submitting: true };
  const btn = form.querySelector<HTMLButtonElement>('#checkout-pay');
  if (btn) btn.disabled = true;
  try {
    const req = buildCheckoutRequest(
      s.cart,
      values,
      location.origin + buildHref(s.base, '/checkout/result'),
      'ru',
    );
    const created = await createCheckout(s.org, req);
    try {
      localStorage.setItem('arena_shop_order:' + s.org, created.order_token);
    } catch {
      /* the return page then finds the token in the query */
    }
    checkoutState = { values, errors: {} };
    window.location.assign(created.payment_url);
  } catch (e) {
    let message = 'Не удалось оформить заказ. Проверьте соединение и попробуйте ещё раз.';
    if (e instanceof ApiError) {
      if (e.code === 'shop.out_of_stock') {
        message = 'Часть товаров закончилась, пока вы оформляли заказ. Мы обновили наличие, проверьте корзину.';
        try {
          s.shop = await getShop(s.org);
        } catch {
          /* keep the old catalog */
        }
      } else if (e.status === 422 || e.status === 400) {
        message = 'Проверьте введённые данные: ' + e.message;
      } else if (e.status === 503) {
        message = 'Оплата временно недоступна. Попробуйте позже.';
      }
    }
    checkoutState = { values, errors: {}, message };
    await rerender();
  }
}

function sendContact(form: HTMLFormElement): void {
  const s = state as State;
  const to = s.shop.settings.contact_email;
  const get = (sel: string): string => (form.querySelector(sel) as HTMLInputElement | null)?.value ?? '';
  const name = get('#ContactForm-name');
  const email = get('#ContactForm-email');
  const phone = get('#ContactForm-phone');
  const body = get('#ContactForm-body');
  if (!to) return;
  const text = `${name}\n${email}\n${phone}\n\n${body}`;
  window.location.href = `mailto:${to}?subject=${encodeURIComponent(s.shop.settings.shop_name || EINAT_THEME.shopName)}&body=${encodeURIComponent(text)}`;
}

// ---- boot -----------------------------------------------------------------------

function fatal(message: string): void {
  root().innerHTML = `<main id="MainContent" class="content-for-layout focus-none" role="main" tabindex="-1"><div class="template-404 page-width page-margin center"><p>404</p><h1 class="title">${message}</h1></div></main>`;
  document.documentElement.className = document.documentElement.className.replace('no-js', 'js');
}

async function boot(): Promise<void> {
  document.documentElement.className = document.documentElement.className.replace('no-js', 'js');
  if ('scrollRestoration' in history) history.scrollRestoration = 'manual';
  const loc = locate(location.hostname, location.pathname, location.search);
  let org = loc.orgSlug;
  try {
    if (org === null) org = await orgForHost(location.hostname);
    if (!org) {
      fatal(T.notFoundTitle);
      return;
    }
    const shop = await getShop(org);
    state = { org, base: loc.base, shop, cart: loadCart(org) };
  } catch (e) {
    console.error(e);
    fatal(e instanceof ApiError && e.status === 404 ? T.notFoundTitle : 'Магазин временно недоступен');
    return;
  }

  document.addEventListener('click', onClick);
  document.addEventListener('submit', onSubmit);
  document.addEventListener('change', onChange);
  document.addEventListener('input', onInput);
  document.addEventListener('focusin', onFocusIn);
  document.addEventListener('keyup', onKeyUp);
  window.addEventListener('popstate', () => void rerender());

  await rerender();
}

void boot();
