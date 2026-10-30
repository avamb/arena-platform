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
  getDownloads,
  getOrderStatus,
  getPage,
  getProduct,
  getShop,
  isPolicySlug,
  resolveHost,
  type OrderStatus,
  type ShopData,
  type ShopProduct,
  type ShopVariant,
  type ShopPage,
} from './lib/api';
import {
  addItem,
  allAvailable,
  applyShortage,
  buildCheckoutRequest,
  needsShipping,
  loadCart,
  limitMessage,
  overLineLimit,
  parseShortage,
  clampField,
  reconcileCart,
  removeItem,
  resolveLines,
  saveCart,
  setQty,
  totalMinor,
  variantLimit,
  variantLimitById,
  visibleCount,
  visibleItems,
  wantedQty,
  validateCheckoutForm,
  validatePostal,
  type CartItem,
  type CheckoutForm,
} from './lib/cart';
import { captchaToken, mountCaptcha, resetCaptcha, unmountCaptcha } from './lib/captcha';
import { checkoutErrorMessage, isNotFound, isTransient } from './lib/errors';
import { MSG, T } from './lib/i18n';
import { isShopHost } from './lib/links';
import { formatMoney } from './lib/money';
import { isOwnOrder, nextDelayMs, phaseFor } from './lib/poll';
import { fitLoadedImages, watchImageRatios } from './lib/ratio';
import { applyTheme } from './theme';
import { esc } from './lib/html';
import { canonicalPagePath, href as buildHref, isPlatformHost, locate, type Route } from './lib/route';
import { EINAT_THEME } from './lib/theme';
import { cartView, notificationProduct } from './views/cart';
import { collectionView, homeView } from './views/catalog';
import { checkoutView, EMPTY_FORM, hasDigitalLines, statusView, type CheckoutViewState, type DownloadsState, type ResumeOffer } from './views/checkout';
import { priceBlock, type Ctx } from './views/common';
import { cartBubble, layout } from './views/layout';
import { buyButtonHtml, productView } from './views/product';
import { notFoundView, pageView, platformHomeView, policyView, searchView, unreachableView } from './views/static';

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
/** The files of the order the status page shows; the signed links are kept in memory only. */
let downloads: DownloadsState | undefined;
/** The cart line whose typed quantity was just cut to the shop's limit: its note is drawn once. */
let cartLimited: string | null = null;
let lastStatus: OrderStatus | undefined;

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
        if (isShopHost(u.hostname, [...EINAT_THEME.domains, state?.shop.settings.shop_domain])) path = u.pathname + u.search + u.hash;
      } catch {
        return;
      }
    }
    if (path !== null) a.setAttribute('href', buildHref(base, path));
  });
}

async function renderRoute(route: Route, fresh = false): Promise<void> {
  const s = state as State;
  // Opening the basket or the checkout: prices and stock may have moved since the catalog was loaded.
  if (fresh && (route.kind === 'cart' || route.kind === 'checkout')) await refreshCatalog();
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
      let unreachable = false;
      if (!p) {
        try {
          p = await getProduct(s.org, route.slug);
        } catch (e) {
          p = undefined;
          // Only a real 404 is "not found"; the network, a 5xx and a 429 are "try again".
          unreachable = isTransient(e);
          if (!isNotFound(e)) console.error(e);
        }
      }
      if (!p && unreachable) {
        main = unreachableView();
        kind = 'not_found';
        title = `${MSG.unreachableTitle} – ${shopName}`;
      } else if (!p) {
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
      main = cartView(ctx, lines, cartLimited);
      cartLimited = null;
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
          token = storedOrderToken(s.org) ?? '';
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
      let pageUnreachable = false;
      if (!page) {
        try {
          page = await getPage(s.org, route.slug);
          pageCache.set(route.slug, page);
        } catch (e) {
          if (!isNotFound(e)) console.error(e);
          pageUnreachable = isTransient(e);
        }
      }
      if (!page && pageUnreachable) {
        main = unreachableView();
        kind = 'not_found';
        title = `${MSG.unreachableTitle} – ${shopName}`;
      } else if (!page) {
        main = notFoundView(ctx);
        kind = 'not_found';
        title = `${T.notFoundTitle} – ${shopName}`;
      } else {
        const canonical = canonicalPagePath(s.base, route.slug, page.slug, isPolicySlug(page.slug ?? ''));
        if (canonical) history.replaceState({}, '', canonical + location.search + location.hash);
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
  fitLoadedImages();
  mountCheckoutCaptcha();
}

/** The checkout form carries a Turnstile widget while the shop publishes a site key. */
function mountCheckoutCaptcha(): void {
  const host = document.getElementById('checkout-captcha');
  const key = host?.dataset.sitekey;
  if (!host || !key) {
    unmountCaptcha();
    return;
  }
  void mountCaptcha(host, key).then((ok) => {
    if (!ok && host.isConnected) host.textContent = MSG.captchaLoad;
  });
}

function currentRoute(): Route {
  return locate(location.hostname, location.pathname, location.search).route;
}

async function rerender(fresh = false): Promise<void> {
  await renderRoute(currentRoute(), fresh);
}

async function navigate(to: string, replace = false): Promise<void> {
  if (replace) history.replaceState({}, '', to);
  else history.pushState({}, '', to);
  window.scrollTo(0, 0);
  await rerender(true);
}

// ---- catalog freshness ----------------------------------------------------------

/**
 * Loads the catalog again (bypassing the 30 s browser cache) and brings the basket in line with it:
 * a hidden or deleted product leaves the basket, a quantity past the stock is cut. A failed request keeps the old catalog.
 */
async function refreshCatalog(): Promise<void> {
  const s = state;
  if (!s) return;
  try {
    s.shop = await getShop(s.org, true);
  } catch {
    return;
  }
  syncCart();
}

function syncCart(): void {
  const s = state as State;
  const r = reconcileCart(s.cart, s.shop.products, s.shop.settings.max_line_qty);
  if (r.changed) commitCart(r.items);
}

const orderKey = (org: string): string => 'arena_shop_order:' + org;

function storedOrderToken(org: string): string | null {
  try {
    return localStorage.getItem(orderKey(org));
  } catch {
    return null;
  }
}

function forgetOrderToken(org: string): void {
  try {
    localStorage.removeItem(orderKey(org));
  } catch {
    /* ignore */
  }
}

// ---- status polling -------------------------------------------------------------

/** The files of a paid order. 404: not paid yet, keep polling. No digital line: nothing to show. */
async function loadDownloads(token: string, st: OrderStatus): Promise<DownloadsState | undefined> {
  if (!hasDigitalLines(st)) return undefined;
  try {
    const links = await getDownloads(token);
    return links.length > 0 ? { state: 'ready', links } : undefined;
  } catch (e) {
    return e instanceof ApiError && e.status === 404 ? { state: 'waiting' } : { state: 'error' };
  }
}

function paintStatus(view: string): void {
  const s = state as State;
  const ctx = ctxOf(s);
  root().innerHTML = layout(ctx, view);
  fixContentLinks(ctx.base);
  applyAgeGate();
}

/** When each order's status page first started waiting: a page redrawn mid-wait keeps its clock. */
const pollStarts = new Map<string, number>();

async function pollStatus(token: string, failures = 0): Promise<void> {
  const s = state as State;
  if (!pollStarts.has(token)) pollStarts.set(token, Date.now());
  const startedAt = pollStarts.get(token) as number;
  let st: OrderStatus | 'error';
  try {
    st = await getOrderStatus(token);
  } catch {
    st = 'error';
  }
  // The buyer may have navigated away while the request was in flight.
  const r = currentRoute();
  if (r.kind !== 'status' || r.token !== token) return;
  failures = st === 'error' ? failures + 1 : 0;
  lastStatus = st === 'error' ? undefined : st;
  downloads = undefined;
  if (st !== 'error') {
    // Only the order this browser started may empty its basket: an old link to another order must not.
    const own = isOwnOrder(token, storedOrderToken(s.org));
    if (st.status === 'paid') {
      if (own) {
        commitCart([]);
        forgetOrderToken(s.org);
      }
      downloads = await loadDownloads(token, st);
      if (currentRoute().kind !== 'status') return;
    } else if (own && (st.status === 'expired' || st.status === 'cancelled')) {
      forgetOrderToken(s.org);
    }
  }
  const elapsed = Date.now() - startedAt;
  const waiting = st === 'error' || st.status === 'pending' || (st.status === 'paid' && downloads?.state === 'waiting');
  const delay = waiting ? nextDelayMs(elapsed, failures) : null;
  // Out of time with the files still not ready: stop saying "preparing" and offer the refresh button.
  if (waiting && delay === null && downloads?.state === 'waiting') downloads = { state: 'error' };
  paintStatus(statusView(ctxOf(s), st, downloads, phaseFor(elapsed)));
  if (delay !== null) pollTimer = window.setTimeout(() => void pollStatus(token, failures), delay);
}

/** "Обновить ссылки": the signed URLs live 15 minutes, so ask for new ones. */
async function refreshDownloads(): Promise<void> {
  const r = currentRoute();
  if (!state || r.kind !== 'status' || !lastStatus) return;
  const st = lastStatus;
  downloads = { state: 'loading' };
  paintStatus(statusView(ctxOf(state), st, downloads));
  const fresh = await loadDownloads(r.token, st);
  if (currentRoute().kind !== 'status') return;
  // Say what happened next to the button: new links carry their expiry, a failure a short line.
  downloads = fresh?.state === 'ready' ? { ...fresh, refreshed: true } : fresh?.state === 'waiting' ? fresh : { state: 'error', refreshed: true };
  paintStatus(statusView(ctxOf(state), st, downloads));
}

// ---- age gate -------------------------------------------------------------------

const ageKey = (): string => 'arena_shop_age_ok:' + (state?.org ?? '');

/** The answer for this page's life, for a browser whose storage is closed (private window): asked once, not on every page. */
let ageAnswered = false;

function ageAccepted(): boolean {
  if (ageAnswered) return true;
  try {
    return localStorage.getItem(ageKey()) === '1';
  } catch {
    return false;
  }
}

function applyAgeGate(): void {
  const popup = document.getElementById('popup') as HTMLDialogElement | null;
  if (!popup || ageAccepted() || popup.hasAttribute('open')) return;
  // Escape must not dismiss the gate: only "allow" does.
  popup.addEventListener('cancel', (ev) => ev.preventDefault());
  if (typeof popup.showModal === 'function') popup.showModal();
  else popup.setAttribute('open', '');
}

function closeAgeGate(): void {
  const popup = document.getElementById('popup') as HTMLDialogElement | null;
  if (!popup) return;
  if (typeof popup.close === 'function') popup.close();
  popup.removeAttribute('open');
}

// ---- cart -----------------------------------------------------------------------

function commitCart(items: CartItem[]): void {
  const s = state as State;
  s.cart = items;
  saveCart(s.org, items);
  const bubble = document.getElementById('cart-icon-bubble');
  if (bubble) bubble.innerHTML = cartBubble(visibleCount(items, s.shop.products));
}

function openNotification(variantId: string, limitNote?: string): void {
  const s = state as State;
  const line = resolveLines([{ variant_id: variantId, qty: 1 }], s.shop.products)[0];
  const host = document.getElementById('cart-notification-product');
  const box = document.getElementById('cart-notification');
  if (!line || !host || !box) return;
  host.innerHTML = notificationProduct(line, limitNote);
  const viewCart = document.getElementById('cart-notification-button');
  if (viewCart) viewCart.textContent = `${T.viewCart} (${visibleCount(s.cart, s.shop.products)})`;
  box.classList.add('animate', 'active');
  const btn = box.querySelector<HTMLElement>('.cart-notification__close');
  btn?.focus();
}

function closeNotification(): void {
  document.getElementById('cart-notification')?.classList.remove('active');
}

/** False when the line is gone or sold out. `limitNote` is shown in the notification when the shop's per-line limit cut the order. */
function addToCart(variantId: string, qty: number, limitNote?: string): boolean {
  const s = state as State;
  const hit = resolveLines([{ variant_id: variantId, qty: 1 }], s.shop.products)[0];
  if (!hit || !hit.variant.available) return false;
  commitCart(addItem(s.cart, variantId, qty, variantLimit(hit.variant, s.shop.settings.max_line_qty)));
  syncInCart(variantId);
  openNotification(variantId, limitNote);
  return true;
}

/**
 * The line limit (max_line_qty) next to a product page quantity field: the note under the
 * field while the buyer's number was cut, and a hint on "+" once the field sits at the limit.
 */
function showQtyLimit(input: HTMLInputElement, limited: boolean): void {
  const lineMax = (state as State).shop.settings.max_line_qty;
  // The field's own max is the variant's limit: the shop's line limit cut to the stock.
  const max = Number(input.max) > 0 ? Number(input.max) : lineMax;
  const note = limitMessage(max, lineMax);
  input.dataset.limited = limited ? '1' : '';
  const rules = input.closest('.product-form__quantity')?.querySelector<HTMLElement>('.quantity__rules');
  if (rules) {
    rules.textContent = limited ? note : '';
    rules.setAttribute('role', 'status');
  }
  const plus = input.closest('quantity-input')?.querySelector<HTMLElement>('button[name="plus"]');
  if (plus) {
    const atMax = (parseInt(input.value, 10) || 0) >= max;
    if (atMax) {
      plus.setAttribute('title', note);
      plus.setAttribute('aria-disabled', 'true');
    } else {
      plus.removeAttribute('title');
      plus.removeAttribute('aria-disabled');
    }
  }
}

/** "(В корзине: N)" next to the quantity label of the product page. */
function syncInCart(variantId: string): void {
  const s = state as State;
  const wrap = document.querySelector<HTMLElement>('.quantity__rules-cart');
  if (!wrap) return;
  const n = s.cart.find((i) => i.variant_id === variantId)?.qty ?? 0;
  const out = wrap.querySelector('.quantity-cart');
  if (out) out.textContent = String(n);
  wrap.classList.toggle('hidden', n === 0);
  document.getElementById('Quantity-product__main')?.setAttribute('data-cart-quantity', String(n));
}

// ---- events ---------------------------------------------------------------------

function onClick(e: MouseEvent): void {
  if (!state) return;
  const target = e.target as HTMLElement;

  // Age gate
  if (target.closest('#allowButton')) {
    ageAnswered = true;
    try {
      localStorage.setItem(ageKey(), '1');
    } catch {
      /* remembered for this page only */
    }
    closeAgeGate();
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
      commitCart(addItem(state.cart, id, qty, variantLimitById(state.shop.products, id, state.shop.settings.max_line_qty)));
      void navigate(buildHref(state.base, '/checkout'));
    }
    return;
  }

  // "Попробовать ещё раз" after a failed read
  if (target.closest('[data-retry]')) {
    e.preventDefault();
    void rerender(true);
    return;
  }

  // "Оформить новый заказ" next to the offer to finish the unpaid one
  if (target.closest('[data-resume-ignore]')) {
    e.preventDefault();
    const f = document.getElementById('checkout-form') as HTMLFormElement | null;
    skipResume = true;
    if (f) void submitCheckout(f);
    return;
  }

  // Fresh download links on the paid order page
  if (target.closest('[data-refresh-downloads]')) {
    e.preventDefault();
    void refreshDownloads();
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
    const qty = wantedQty(qtyInput?.value);
    const variantId = form.dataset.variantId as string;
    const lineMax = s.shop.settings.max_line_qty;
    const max = variantLimitById(s.shop.products, variantId, lineMax);
    const inCart = s.cart.find((i) => i.variant_id === variantId)?.qty ?? 0;
    // The field may already have been cut to the limit on change: it remembers that.
    const limited = overLineLimit(inCart, qty, max) || qtyInput?.dataset.limited === '1';
    if (addToCart(variantId, qty, limited ? limitMessage(max, lineMax) : undefined) && qtyInput) {
      qtyInput.value = String(clampField(qty, max).value);
      showQtyLimit(qtyInput, limited);
    }
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

/** The option values the buyer has picked, per option group, in option order. */
function pickedOptions(): (string | null)[] {
  const out: (string | null)[] = [null, null, null];
  document.querySelectorAll<HTMLInputElement>('.variant-radios input[type="radio"][data-option-index]:checked').forEach((r) => {
    out[Number(r.dataset.optionIndex)] = r.value;
  });
  return out;
}

const optionValues = (v: ShopVariant): (string | null)[] => [v.option1 ?? null, v.option2 ?? null, v.option3 ?? null];

/**
 * Several options: the variant whose values all match the picks. When that combination
 * is not sold, fall back to the first variant that has the value just picked and move
 * the other groups to it.
 */
function variantForOptionPick(product: ShopProduct, changed: HTMLInputElement): ShopVariant | undefined {
  const picked = pickedOptions();
  const exact = product.variants.find((v) => optionValues(v).every((val, i) => i >= product.option_names.length || val === picked[i]));
  if (exact) return exact;
  const idx = Number(changed.dataset.optionIndex);
  const near = product.variants.find((v) => optionValues(v)[idx] === changed.value);
  if (near) {
    optionValues(near).forEach((val, i) => {
      document
        .querySelectorAll<HTMLInputElement>(`.variant-radios input[data-option-index="${i}"]`)
        .forEach((r) => (r.checked = r.value === val));
    });
  }
  return near;
}

/** Everything on the product page that follows the chosen variant. */
function applyVariant(product: ShopProduct, variant: ShopVariant): void {
  const s = state as State;
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
  // The field follows the variant: its limit is this variant's stock (cut to the shop's line limit).
  const qtyField = document.getElementById('Quantity-product__main') as HTMLInputElement | null;
  if (qtyField) {
    const limit = variantLimit(variant, s.shop.settings.max_line_qty);
    qtyField.max = String(limit);
    const cut = clampField(qtyField.value, limit);
    if (qtyField.value !== '' && cut.limited) qtyField.value = String(cut.value);
    showQtyLimit(qtyField, cut.limited);
  }
  syncInCart(variant.id);
  const buyNow = document.querySelector<HTMLElement>('.shopify-payment-button');
  if (buyNow) buyNow.hidden = !variant.available;
}

function onChange(e: Event): void {
  if (!state) return;
  const el = e.target as HTMLInputElement;
  const s = state;

  // Cart page quantity
  if (el.matches('.cart-item .quantity__input')) {
    const id = el.dataset.quantityVariantId as string;
    const qty = parseInt(el.value, 10);
    const max = variantLimitById(s.shop.products, id, s.shop.settings.max_line_qty);
    commitCart(setQty(s.cart, id, Number.isFinite(qty) ? qty : 1, max));
    cartLimited = Number.isFinite(qty) && clampField(qty, max).limited ? id : null;
    void rerender();
    return;
  }

  // Product page quantity: a number past the shop's per-line limit is cut to it, with the reason
  if (el.matches('.product-form__quantity .quantity__input')) {
    const { value, limited } = clampField(el.value, Number(el.max) > 0 ? Number(el.max) : s.shop.settings.max_line_qty);
    if (el.value !== '' && limited) el.value = String(value);
    showQtyLimit(el, limited);
    return;
  }

  // Variant pills on the product page: one group for a single option, one group per option for several
  if (el.matches('.variant-radios input[type="radio"]')) {
    const info = document.querySelector<HTMLElement>('product-info[data-product-slug]');
    const product = s.shop.products.find((p) => p.slug === info?.dataset.productSlug);
    if (!product) return;
    const variant = el.dataset.optionIndex !== undefined ? variantForOptionPick(product, el) : product.variants.find((v) => v.id === el.dataset.variantId);
    if (variant) applyVariant(product, variant);
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
    note: v('note'),
    consent18: (form.elements.namedItem('consent18') as HTMLInputElement | null)?.checked ?? false,
  };
}

/** The buyer's language for the order's e-mails and the payment page: the one the page is in. */
function pageLocale(): string {
  const lang = (document.documentElement.lang || 'ru').toLowerCase().split('-')[0];
  return lang || 'ru';
}

/** Set by "Оформить новый заказ": the next submit does not look for an unpaid order first. */
let skipResume = false;

/**
 * An unpaid order this browser started and whose payment page can still be opened. The buyer who
 * came back with "Назад" is offered it instead of a second order and a second reservation.
 */
async function findResumeOffer(s: State): Promise<ResumeOffer | undefined> {
  const token = storedOrderToken(s.org);
  if (!token) return undefined;
  try {
    const st = await getOrderStatus(token);
    if (st.status === 'pending' && st.payment_url) {
      const currency = st.currency || s.shop.settings.currency;
      return {
        paymentUrl: st.payment_url,
        number: st.number,
        totalLabel: st.total_minor != null ? formatMoney(st.total_minor, currency) : undefined,
      };
    }
    // Paid, expired or cancelled: nothing to come back to.
    forgetOrderToken(s.org);
  } catch (e) {
    if (isNotFound(e)) forgetOrderToken(s.org);
  }
  return undefined;
}

async function submitCheckout(form: HTMLFormElement): Promise<void> {
  const s = state as State;
  const values = readCheckoutForm(form);
  const lines = resolveLines(s.cart, s.shop.products);
  // A cart without a physical product takes no address and no delivery fee.
  const ships = needsShipping(lines);
  const errors = validateCheckoutForm(values, s.shop.settings.age_gate_min_age, ships, s.shop.settings.country_iso2 || 'IL');
  checkoutState = { values, errors };
  if (Object.keys(errors).length > 0) {
    await rerender();
    (document.querySelector('.field--with-error input, [aria-invalid="true"]') as HTMLElement | null)?.focus();
    return;
  }
  if (lines.length === 0) return;
  if (!allAvailable(lines)) {
    checkoutState = { values, errors: {}, message: 'Некоторых товаров из вашей корзины уже нет в наличии. Удалите их из корзины.' };
    await rerender();
    return;
  }
  const captchaKey = s.shop.settings.captcha_site_key || '';
  const captcha = captchaKey ? captchaToken(form) : '';
  if (captchaKey && !captcha) {
    checkoutState = { values, errors: {}, message: MSG.captchaWait };
    await rerender();
    return;
  }
  const btn = form.querySelector<HTMLButtonElement>('#checkout-pay');
  if (btn) btn.disabled = true;
  if (!skipResume) {
    const offer = await findResumeOffer(s);
    if (offer) {
      checkoutState = { values, errors: {}, resume: offer };
      await rerender();
      document.getElementById('checkout-resume')?.scrollIntoView?.({ block: 'center' });
      return;
    }
  }
  skipResume = false;
  checkoutState = { values, errors: {}, submitting: true };
  try {
    const req = buildCheckoutRequest(
      visibleItems(s.cart, s.shop.products),
      values,
      location.origin + buildHref(s.base, '/checkout/result'),
      pageLocale(),
      s.shop.settings.country_iso2 || 'IL',
      ships,
      captcha || undefined,
    );
    const created = await createCheckout(s.org, req);
    // A Turnstile token is good for one request.
    resetCaptcha();
    try {
      localStorage.setItem(orderKey(s.org), created.order_token);
    } catch {
      /* the return page then finds the token in the query */
    }
    const expected = totalMinor(s.shop.settings, lines);
    if (created.payment_url && created.total_minor !== expected) {
      // The prices moved between the catalog and the order: never send the buyer to pay a sum they were not shown.
      const currency = created.currency || s.shop.settings.currency;
      await refreshCatalog();
      checkoutState = {
        values,
        errors: {},
        message: `Сумма заказа изменилась: ${formatMoney(created.total_minor, currency)} вместо ${formatMoney(expected, currency)}. Мы обновили цены, проверьте корзину.`,
        resume: { paymentUrl: created.payment_url, number: created.number, totalLabel: formatMoney(created.total_minor, currency) },
      };
      await rerender();
      return;
    }
    checkoutState = { values, errors: {} };
    if (created.payment_url) {
      window.location.assign(created.payment_url);
    } else {
      // A free order is paid on the spot: no payment page, straight to its status.
      await navigate(buildHref(s.base, '/checkout/' + created.order_token));
    }
  } catch (e) {
    resetCaptcha();
    let message = checkoutErrorMessage(e, s.shop.settings.contact_email);
    let notes: string[] | undefined;
    let resume: ResumeOffer | undefined;
    if (e instanceof ApiError) {
      if (e.code === 'shop.out_of_stock') {
        // Cut the basket to what the server says is left, say what changed, then bring the catalog up to date.
        const cut = applyShortage(s.cart, s.shop.products, parseShortage(e.details));
        commitCart(cut.items);
        await refreshCatalog();
        notes = cut.notes;
        message = 'Часть товаров закончилась, пока вы оформляли заказ. Мы обновили корзину, проверьте её и оформите заказ ещё раз.';
      } else if (e.code === 'shop.postal_required' || e.code === 'shop.invalid_postal') {
        // The server's own verdict on the postal code: show it under the field, in Russian.
        const country = s.shop.settings.country_iso2 || 'IL';
        checkoutState = { values, errors: { postal: validatePostal(values.postal, country) ?? 'Проверьте индекс: 7 цифр' } };
        await rerender();
        (document.querySelector('#Checkout-postal') as HTMLElement | null)?.focus();
        return;
      } else if (e.code === 'shop.invalid_item' || e.code === 'shop.stock_changed') {
        const bad = (e.details as { variant_id?: unknown } | null | undefined)?.variant_id;
        if (typeof bad === 'string') commitCart(removeItem(s.cart, bad));
        await refreshCatalog();
      } else if (e.code === 'shop.too_many_pending') {
        resume = await findResumeOffer(s);
      }
    }
    checkoutState = { values, errors: {}, message, notes, resume };
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

/** A page with no shop behind it (bare platform address, unknown organization): styled with the theme, no header or footer. */
function standalone(html: string, title: string): void {
  document.documentElement.className = document.documentElement.className.replace('no-js', 'js');
  applyTheme('not_found');
  root().innerHTML = `<main id="MainContent" class="content-for-layout focus-none" role="main" tabindex="-1">${html}</main>`;
  setMeta(title);
}

/** The shop could not be shown. */
function fatal(message: string): void {
  if (message === T.notFoundTitle) {
    standalone(notFoundView(null), T.notFoundTitle);
    return;
  }
  standalone(`<div class="template-404 page-width page-margin center"><h1 class="title">${esc(message)}</h1></div>`, message);
}

/** The shop could not be reached (not a 404): say so and offer another try. */
function fatalRetry(): void {
  standalone(unreachableView(), MSG.unreachableTitle);
  document.querySelector('[data-retry]')?.addEventListener('click', () => location.reload());
}

async function boot(): Promise<void> {
  document.documentElement.className = document.documentElement.className.replace('no-js', 'js');
  if ('scrollRestoration' in history) history.scrollRestoration = 'manual';
  const loc = locate(location.hostname, location.pathname, location.search);
  let org = loc.orgSlug;
  try {
    if (org === null) org = await orgForHost(location.hostname);
    if (!org) {
      // The bare platform address on a platform host: nobody's shop.
      if (isPlatformHost(location.hostname) && location.pathname.replace(/\/+$/, '') === '') {
        standalone(platformHomeView(), 'Arena Sold Out');
      } else {
        fatal(T.notFoundTitle);
      }
      return;
    }
    const shop = await getShop(org);
    // A product that was hidden or deleted since the basket was saved must not stay in it.
    const stored = loadCart(org);
    const cart = reconcileCart(stored, shop.products, shop.settings.max_line_qty);
    state = { org, base: loc.base, shop, cart: cart.items };
    if (cart.changed) saveCart(org, cart.items);
  } catch (e) {
    console.error(e);
    if (isNotFound(e)) fatal(T.notFoundTitle);
    else fatalRetry();
    return;
  }

  watchImageRatios();
  document.addEventListener('click', onClick);
  document.addEventListener('submit', onSubmit);
  document.addEventListener('change', onChange);
  document.addEventListener('input', onInput);
  document.addEventListener('focusin', onFocusIn);
  document.addEventListener('keyup', onKeyUp);
  window.addEventListener('popstate', () => void rerender(true));
  // Back from the payment page: the browser may restore this page from its cache with the pay button still disabled.
  window.addEventListener('pageshow', (ev) => {
    if (!(ev as PageTransitionEvent).persisted) return;
    checkoutState = { ...checkoutState, submitting: false };
    skipResume = false;
    void rerender(true);
  });

  await rerender();
}

void boot();
