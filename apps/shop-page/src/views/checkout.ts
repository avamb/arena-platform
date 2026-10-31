/**
 * checkout.ts - /checkout (the shop's own page: contacts, delivery address,
 * flat delivery, age consent, "Оплатить") and /checkout/{token} (status of one
 * order). Dawn's form, button and typography classes keep it inside the theme;
 * the page layout itself has no Shopify original (Shopify hosts its checkout).
 */
import { MAX_NOTE_LEN, needsShipping, postalHint, subtotalMinor, shippingMinor, totalMinor, type CartLine, type CheckoutForm, type FormErrors } from '../lib/cart';
import type { DownloadLink, OrderStatus } from '../lib/api';
import { sized } from '../lib/cdn';
import { esc } from '../lib/html';
import { itemsLabel, MSG, resumeOrderText, T } from '../lib/i18n';
import type { StatusPhase } from '../lib/poll';
import { cartSrcset, icon, money, optionPairs, ratioOf, url, type Ctx } from './common';

export const EMPTY_FORM: CheckoutForm = {
  name: '',
  email: '',
  phone: '',
  address1: '',
  address2: '',
  city: '',
  postal: '',
  note: '',
  consent18: false,
};

function field(
  id: keyof CheckoutForm,
  label: string,
  values: CheckoutForm,
  errors: FormErrors,
  opts: { type?: string; autocomplete?: string; required?: boolean; inputmode?: string; hint?: string } = {},
): string {
  const err = errors[id];
  const describedBy = [err ? `Checkout-${id}-error` : '', opts.hint ? `Checkout-${id}-hint` : ''].filter(Boolean).join(' ');
  return `<div class="field${err ? ' field--with-error' : ''}">
          <input
            class="field__input"
            id="Checkout-${id}"
            name="${id}"
            type="${opts.type ?? 'text'}"
            value="${esc(values[id] as string)}"
            placeholder="${esc(label)}"${opts.autocomplete ? `
            autocomplete="${opts.autocomplete}"` : ''}${opts.inputmode ? `
            inputmode="${opts.inputmode}"` : ''}${err ? `
            aria-invalid="true"` : ''}${describedBy ? `
            aria-describedby="${describedBy}"` : ''}
          >
          <label class="field__label" for="Checkout-${id}">${esc(label)}${opts.required ? ' <span aria-hidden="true">*</span>' : ''}</label>
        </div>${opts.hint ? `<small class="shop-checkout__hint caption" id="Checkout-${id}-hint">${esc(opts.hint)}</small>` : ''}${err ? `<small class="form__message" id="Checkout-${id}-error"><span class="visually-hidden">${'Ошибка'}</span>${icon('error')}${esc(err)}</small>` : ''}`;
}

function summaryRow(ctx: Ctx, line: CartLine): string {
  const img = line.product.images[0];
  const image = img
    ? `<div class="shop-checkout__thumb"><img src="${esc(sized(img.url, 165))}"${cartSrcset(img, '70px')} alt="${esc(img.alt ?? '')}" width="70" height="${Math.ceil(70 / ratioOf(img))}" loading="lazy"><span class="shop-checkout__qty">${line.qty}</span></div>`
    : '';
  const picked = optionPairs(line.product, line.variant).map(([, v]) => v).join(' / ');
  const variant = picked ? `<div class="product-option">${esc(picked)}</div>` : '';
  return `<li class="shop-checkout__line">${image}<div class="shop-checkout__line-name"><span class="h5">${esc(line.product.title)}</span>${variant}</div><span class="shop-checkout__line-price">${esc(money(ctx, line.lineMinor))}</span></li>`;
}

/** "Комментарий к заказу": goes to the seller as delivery_note, for any cart (a gift note, "call an hour ahead"). */
function noteField(values: CheckoutForm): string {
  return `<div class="field shop-checkout__note">
          <textarea
            class="text-area field__input"
            id="Checkout-note"
            name="note"
            rows="3"
            maxlength="${MAX_NOTE_LEN}"
            placeholder="Комментарий к заказу"
          >${esc(values.note)}</textarea>
          <label class="field__label" for="Checkout-note">Комментарий к заказу</label>
        </div>`;
}

export interface CheckoutViewState {
  values: CheckoutForm;
  errors: FormErrors;
  /** A request-level problem (out of stock, payment unavailable, network). */
  message?: string;
  /** What changed in the basket after a shortage answer, one line each. */
  notes?: string[];
  /** An unpaid order of this buyer is still waiting: offered instead of a second order. */
  resume?: ResumeOffer;
  submitting?: boolean;
}

export interface ResumeOffer {
  paymentUrl: string;
  number?: string | number | null;
  /** The order's total, already formatted. */
  totalLabel?: string;
}

function resumePanel(r: ResumeOffer): string {
  // A hint, not a form error: no red, a title, one line, the two ways on under it.
  return `<div class="shop-checkout__resume" role="status" id="checkout-resume">
        <h2 class="shop-checkout__resume-title h4">${MSG.resumeTitle}</h2>
        <p class="shop-checkout__resume-text">${esc(resumeOrderText(r.number, r.totalLabel))} ${MSG.resumeText}</p>
        <div class="shop-checkout__resume-actions">
          <a href="${esc(r.paymentUrl)}" class="button" id="return-to-payment">${MSG.returnToPayment}</a>
          <button type="button" class="button button--secondary" data-resume-ignore>${MSG.resumeNew}</button>
        </div>
      </div>`;
}

function messageBlock(st: CheckoutViewState): string {
  if (!st.message && !(st.notes && st.notes.length > 0)) return '';
  const notes = st.notes && st.notes.length > 0 ? `<ul class="list-unstyled shop-checkout__notes" role="list">${st.notes.map((n) => `<li>${esc(n)}</li>`).join('')}</ul>` : '';
  return `
      <div class="form__message shop-checkout__error" role="alert">${icon('error')}${st.message ? esc(st.message) : ''}${notes}</div>`;
}

/** The country's name in the storefront's language ("IL" -> "Израиль"); the code itself when the browser does not know it. */
export function countryLabel(iso2: string | null | undefined, locale = 'ru'): string {
  const code = (iso2 ?? '').trim().toUpperCase();
  if (code === '') return '';
  try {
    return new Intl.DisplayNames([locale], { type: 'region' }).of(code) ?? code;
  } catch {
    return code;
  }
}

/** The ship country is the shop's own and is not chosen by the buyer: shown, never edited. */
function countryField(country: string): string {
  return `<div class="field shop-checkout__country">
          <input class="field__input" id="Checkout-country" type="text" value="${esc(countryLabel(country))}" placeholder="Страна" readonly aria-readonly="true" autocomplete="country-name">
          <label class="field__label" for="Checkout-country">Страна</label>
        </div>`;
}

/** Address and delivery fee: only for a cart that holds a physical product. */
function addressBlock(ctx: Ctx, st: CheckoutViewState, label: string, shipping: number): string {
  const e = st.errors;
  const country = ctx.shop.settings.country_iso2 || 'IL';
  const il = country.toUpperCase() === 'IL';
  return `<h2 class="shop-checkout__heading h3">Адрес доставки</h2>
      ${field('address1', 'Улица и дом', st.values, e, { autocomplete: 'address-line1', required: true })}
      ${field('address2', 'Квартира, подъезд, этаж, домофон', st.values, e, { autocomplete: 'address-line2' })}
      ${field('city', 'Город', st.values, e, { autocomplete: 'address-level2', required: true })}
      ${countryField(country)}
      ${field('postal', 'Почтовый индекс', st.values, e, { autocomplete: 'postal-code', required: true, hint: postalHint(country), ...(il ? { inputmode: 'numeric' } : {}) })}
      <h2 class="shop-checkout__heading h3">Доставка</h2>
      <div class="shop-checkout__delivery">
        <span class="shop-checkout__delivery-name">${label}</span>
        <span>${esc(money(ctx, shipping))}</span>
      </div>`;
}

/**
 * Phone width (under 768 px): the order on one folded line above the form, "Ваш заказ · 2 товара · 580.00 NIS",
 * opened by a tap. The full block below the form is hidden there by CSS, so the buyer sees what they pay for before the button.
 */
function mobileSummary(ctx: Ctx, lines: CartLine[], subtotal: number, shipping: number, total: number, ships: boolean, label: string): string {
  const units = lines.reduce((n, l) => n + l.qty, 0);
  return `<details class="shop-checkout__mobile-summary" id="checkout-mobile-summary">
    <summary><span>Ваш заказ · ${itemsLabel(units)} · ${esc(money(ctx, total))}</span></summary>
    <ul class="shop-checkout__lines list-unstyled" role="list">${lines.map((l) => summaryRow(ctx, l)).join('')}</ul>
    <dl class="shop-checkout__totals">
      <div><dt>Товары</dt><dd>${esc(money(ctx, subtotal))}</dd></div>
      ${ships ? `<div><dt>${label}</dt><dd>${esc(money(ctx, shipping))}</dd></div>` : ''}
      <div class="shop-checkout__total"><dt>Итого</dt><dd>${esc(money(ctx, total))}</dd></div>
    </dl>
  </details>`;
}

export function checkoutView(ctx: Ctx, lines: CartLine[], st: CheckoutViewState): string {
  const s = ctx.shop.settings;
  const all = esc(url(ctx, '/collections/all'));
  const style = `<style data-shopify>.section-checkout-padding {
    padding-top: 27px;
    padding-bottom: 27px;
  }
  @media screen and (min-width: 750px) {
    .section-checkout-padding {
      padding-top: 36px;
      padding-bottom: 36px;
    }
  }</style>`;
  if (lines.length === 0) {
    return `<div id="shopify-section-checkout__main" class="shopify-section">${style}<div class="page-width section-checkout-padding template-404 center">
  <h1 class="title">${T.cartEmpty}</h1>
  <a href="${all}" class="button">${T.continueShopping}</a>
</div></div>`;
  }
  const subtotal = subtotalMinor(lines);
  const shipping = shippingMinor(s, lines);
  const total = totalMinor(s, lines);
  const minAge = s.age_gate_min_age ?? 0;
  const ships = needsShipping(lines);
  const e = st.errors;
  const label = esc(s.shipping_label || 'Доставка');
  return `<div id="shopify-section-checkout__main" class="shopify-section">${style}<div class="page-width section-checkout-padding shop-checkout">
  <div class="title-wrapper-with-link">
    <h1 class="title title--primary">Оформление заказа</h1>
    <a href="${esc(url(ctx, '/cart'))}" class="underlined-link">Вернуться в корзину</a>
  </div>
  ${mobileSummary(ctx, lines, subtotal, shipping, total, ships, label)}
  <div class="shop-checkout__grid">
    <form id="checkout-form" class="shop-checkout__form" method="post" action="${esc(url(ctx, '/checkout'))}" novalidate>
      <h2 class="shop-checkout__heading h3">Контактные данные</h2>
      ${field('name', 'Имя и фамилия', st.values, e, { autocomplete: 'name', required: true })}
      ${field('email', 'Адрес электронной почты', st.values, e, { type: 'email', autocomplete: 'email', required: true })}
      ${field('phone', 'Номер телефона', st.values, e, { type: 'tel', autocomplete: 'tel', required: true, inputmode: 'tel' })}
      ${ships ? addressBlock(ctx, st, label, shipping) : ''}
      ${noteField(st.values)}${
        minAge > 0
          ? `
      <div class="shop-checkout__consent">
        <input type="checkbox" id="Checkout-consent18" name="consent18"${st.values.consent18 ? ' checked' : ''}${e.consent18 ? ' aria-invalid="true"' : ''}>
        <label for="Checkout-consent18">Подтверждаю, что мне исполнилось ${minAge} лет</label>
      </div>${e.consent18 ? `<small class="form__message" id="Checkout-consent18-error"><span class="visually-hidden">Ошибка</span>${icon('error')}${esc(e.consent18)}</small>` : ''}`
          : ''
      }${s.captcha_site_key ? `
      <div id="checkout-captcha" class="shop-checkout__captcha" data-sitekey="${esc(s.captcha_site_key)}"></div>` : ''}${messageBlock(st)}${st.resume ? resumePanel(st.resume) : ''}
      ${
        // While the unpaid-order hint is up, "Оплатить" would compete with "Вернуться к оплате".
        st.resume
          ? ''
          : `<button type="submit" class="button button--full-width shop-checkout__pay" id="checkout-pay"${st.submitting ? ' disabled' : ''}>
        Оплатить ${esc(money(ctx, total))}
      </button>`
      }
    </form>
    <aside class="shop-checkout__summary" aria-label="Ваш заказ">
      <h2 class="shop-checkout__heading h3">Ваш заказ</h2>
      <ul class="shop-checkout__lines list-unstyled" role="list">${lines.map((l) => summaryRow(ctx, l)).join('')}</ul>
      <dl class="shop-checkout__totals">
        <div><dt>Товары</dt><dd>${esc(money(ctx, subtotal))}</dd></div>
        ${ships ? `<div><dt>${label}</dt><dd>${esc(money(ctx, shipping))}</dd></div>` : ''}
        <div class="shop-checkout__total"><dt>Итого</dt><dd>${esc(money(ctx, total))}</dd></div>
      </dl>
    </aside>
  </div>
</div></div>`;
}

/** Where the paid order's files stand: asked for, not ready yet, failed, or listed. */
export type DownloadsState =
  | { state: 'loading' | 'waiting' }
  /** 410 shop.download_expired: the period or the link count is used up; a refresh cannot help. */
  | { state: 'expired' }
  | { state: 'error'; refreshed?: boolean }
  | { state: 'ready'; links: DownloadLink[]; refreshed?: boolean };

/**
 * An order line's name: the product, with its variant after a dash unless the
 * variant is empty or the single default one (the backend's `shop.ItemLabel`).
 */
export function lineLabel(productTitle: string, variantTitle: string | null | undefined): string {
  const v = (variantTitle ?? '').trim();
  return v !== '' && v !== 'Default Title' ? `${productTitle} — ${v}` : productTitle;
}

/** "28 ноября 2026 г." - the end of the 30-day download window, in `timeZone` (default the buyer's own). */
function windowEnd(links: DownloadLink[], timeZone?: string): string {
  const times = links.map((l) => Date.parse(l.expires_at)).filter((t) => !Number.isNaN(t));
  if (times.length === 0) return '';
  return new Date(Math.min(...times)).toLocaleDateString('ru-RU', { day: 'numeric', month: 'long', year: 'numeric', timeZone });
}

/** The line under the file list: how long the links work and how many downloads a file has. */
export function downloadsCaption(links: DownloadLink[], timeZone?: string): string {
  const until = windowEnd(links, timeZone);
  return until ? `Ссылки действуют до ${until} и не больше 20 скачиваний на файл.` : 'Не больше 20 скачиваний на файл.';
}

/**
 * What the buyer is told right after pressing "Обновить ссылки". Only a state
 * reached through a refresh carries a message: «Ссылки обновлены, действуют до 28 ноября 2026 г.»
 * (the earliest `expires_at` of the new links) or a short error line.
 */
export function refreshFeedback(dl: DownloadsState | undefined, timeZone?: string): string {
  if (!dl) return '';
  if (dl.state === 'error') return dl.refreshed ? 'Не удалось обновить ссылки. Попробуйте ещё раз.' : '';
  if (dl.state !== 'ready' || !dl.refreshed) return '';
  const until = windowEnd(dl.links, timeZone);
  return until ? `Ссылки обновлены, действуют до ${until}` : 'Ссылки обновлены';
}

/** The order holds a digital line (or the API did not say what kind the lines are). */
export function hasDigitalLines(st: OrderStatus): boolean {
  const items = st.items ?? [];
  return items.length === 0 || items.some((i) => !i.product_kind || i.product_kind === 'digital');
}

function hasPhysicalLines(st: OrderStatus): boolean {
  const items = st.items ?? [];
  return items.length === 0 || items.some((i) => !i.product_kind || i.product_kind === 'physical');
}

const safeHref = (u: string): boolean => /^https?:\/\//i.test(u);

/** The file links work for the order's 30-day window; the refresh button asks the list again. */
function downloadsBlock(dl: DownloadsState): string {
  const note = refreshFeedback(dl);
  const refresh = `<button type="button" class="button button--secondary" data-refresh-downloads>Обновить ссылки</button>`;
  let body: string;
  switch (dl.state) {
    case 'loading':
    case 'waiting':
      body = `<p>Готовим ваши файлы. Это займёт несколько секунд.</p>`;
      break;
    case 'error':
      body = `<p role="status" data-downloads-note>${esc(note || 'Не удалось получить ссылки. Попробуйте ещё раз.')}</p>${refresh}`;
      break;
    case 'expired':
      body = `<p role="status" data-downloads-note>Срок скачивания файлов этого заказа закончился. Если файлы нужны снова, напишите продавцу.</p>`;
      break;
    default: {
      const items = dl.links
        .filter((l) => safeHref(l.url))
        .map((l) => {
          const name = lineLabel(l.product_title, l.variant_title);
          return `<li><a href="${esc(l.url)}" class="link" rel="noopener" download>${esc(name)}</a></li>`;
        })
        .join('');
      body = items
        ? `<ul class="shop-downloads__list list-unstyled" role="list">${items}</ul><p class="caption" data-downloads-window>${esc(downloadsCaption(dl.links))}</p>${refresh}${note ? `<p class="caption" role="status" data-downloads-note>${esc(note)}</p>` : ''}`
        : '';
    }
  }
  return body ? `<div class="shop-downloads"><h2 class="h3">Ваши файлы</h2>${body}</div>` : '';
}

export function statusView(ctx: Ctx, st: OrderStatus | 'loading' | 'error', dl?: DownloadsState, phase: StatusPhase = 'late'): string {
  const all = esc(url(ctx, '/collections/all'));
  const wrap = (title: string, body: string): string => `<div class="template-404 page-width page-margin center shop-status">
  <h1 class="title">${esc(title)}</h1>${body}
</div>`;
  if (st === 'loading') return wrap(T.loading, '');
  const checking = (): string => wrap(MSG.checking, `<p role="status">${MSG.checkingText}</p>`);
  if (st === 'error') {
    // One failed request is not the end: while the page is still polling it keeps saying it is checking.
    if (phase !== 'timeout') return checking();
    return wrap(MSG.timeoutTitle, `<p>${MSG.timeoutText}</p><a href="${all}" class="button">${T.continueShopping}</a>`);
  }
  const number = st.number != null && st.number !== '' ? ` № ${esc(st.number)}` : '';
  // A person is looking at the order: say what the server says, in its own words, and offer no way back to payment
  // (these orders have no payment_url). The wait is not over: a payment that is being checked may still turn paid.
  if (st.review_reason) {
    const stock = st.review_reason === 'late_payment_no_stock';
    const title = stock ? MSG.reviewStockTitle : MSG.reviewUnconfirmedTitle;
    const text = st.review_message || (stock ? MSG.reviewStockText : MSG.reviewUnconfirmedText);
    return wrap(title, `<p role="status" data-review-message>${esc(text)}</p><a href="${all}" class="button">${T.continueShopping}</a>`);
  }
  switch (st.status) {
    case 'paid':
      return wrap(
        'Спасибо за заказ!',
        `<p>Заказ${number} оплачен. Мы отправили подтверждение на вашу почту${hasPhysicalLines(st) ? ' и свяжемся с вами по доставке' : ''}.</p>${dl ? downloadsBlock(dl) : ''}<a href="${all}" class="button">${T.continueShopping}</a>`,
      );
    case 'pending': {
      // The buyer is usually back from the provider before its webhook reaches us: no "go back and pay" for the first minutes.
      if (phase === 'checking') return checking();
      const back = st.payment_url ? `<a href="${esc(st.payment_url)}" class="button" id="return-to-payment">${MSG.returnToPayment}</a>` : '';
      if (phase === 'timeout') return wrap(MSG.timeoutTitle, `<p>${MSG.timeoutText}</p>${back}`);
      return wrap(
        MSG.waitingTitle,
        `<p>Заказ${number} ещё не оплачен. Если вы закрыли страницу оплаты, вернитесь к ней по ссылке.</p>${back}`,
      );
    }
    case 'cancelled':
      return wrap('Заказ отменён', `<p>Заказ${number} отменён.</p><a href="${all}" class="button">${T.continueShopping}</a>`);
    default:
      return wrap(
        'Время оплаты истекло',
        `<p>Заказ${number} не был оплачен вовремя, товары вернулись в продажу.</p><a href="${all}" class="button">${T.continueShopping}</a>`,
      );
  }
}
