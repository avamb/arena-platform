/**
 * checkout.ts - /checkout (the shop's own page: contacts, delivery address,
 * flat delivery, age consent, "Оплатить") and /checkout/{token} (status of one
 * order). Dawn's form, button and typography classes keep it inside the theme;
 * the page layout itself has no Shopify original (Shopify hosts its checkout).
 */
import { MAX_NOTE_LEN, needsShipping, subtotalMinor, shippingMinor, totalMinor, type CartLine, type CheckoutForm, type FormErrors } from '../lib/cart';
import type { DownloadLink, OrderStatus } from '../lib/api';
import { sized } from '../lib/cdn';
import { esc } from '../lib/html';
import { T } from '../lib/i18n';
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
  opts: { type?: string; autocomplete?: string; required?: boolean; inputmode?: string } = {},
): string {
  const err = errors[id];
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
            aria-invalid="true"
            aria-describedby="Checkout-${id}-error"` : ''}
          >
          <label class="field__label" for="Checkout-${id}">${esc(label)}${opts.required ? ' <span aria-hidden="true">*</span>' : ''}</label>
        </div>${err ? `<small class="form__message" id="Checkout-${id}-error"><span class="visually-hidden">${'Ошибка'}</span>${icon('error')}${esc(err)}</small>` : ''}`;
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
  submitting?: boolean;
}

/** Address and delivery fee: only for a cart that holds a physical product. */
function addressBlock(ctx: Ctx, st: CheckoutViewState, label: string, shipping: number): string {
  const e = st.errors;
  return `<h2 class="shop-checkout__heading h3">Адрес доставки</h2>
      ${field('address1', 'Улица, дом, квартира', st.values, e, { autocomplete: 'address-line1', required: true })}
      ${field('address2', 'Подъезд, этаж, комментарий', st.values, e, { autocomplete: 'address-line2' })}
      ${field('city', 'Город', st.values, e, { autocomplete: 'address-level2', required: true })}
      ${field('postal', 'Почтовый индекс', st.values, e, { autocomplete: 'postal-code', inputmode: 'numeric' })}
      <h2 class="shop-checkout__heading h3">Доставка</h2>
      <div class="shop-checkout__delivery">
        <span class="shop-checkout__delivery-name">${label}</span>
        <span>${esc(money(ctx, shipping))}</span>
      </div>`;
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
      }${st.message ? `
      <div class="form__message shop-checkout__error" role="alert">${icon('error')}${esc(st.message)}</div>` : ''}
      <button type="submit" class="button button--full-width shop-checkout__pay" id="checkout-pay"${st.submitting ? ' disabled' : ''}>
        Оплатить ${esc(money(ctx, total))}
      </button>
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
export type DownloadsState = { state: 'loading' | 'waiting' | 'error' } | { state: 'ready'; links: DownloadLink[] };

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

/** The links are signed for 15 minutes, so the block always offers a refresh. */
function downloadsBlock(dl: DownloadsState): string {
  const refresh = `<button type="button" class="button button--secondary" data-refresh-downloads>Обновить ссылки</button>`;
  let body: string;
  switch (dl.state) {
    case 'loading':
    case 'waiting':
      body = `<p>Готовим ваши файлы. Это займёт несколько секунд.</p>`;
      break;
    case 'error':
      body = `<p>Не удалось получить ссылки. Попробуйте ещё раз.</p>${refresh}`;
      break;
    default: {
      const items = dl.links
        .filter((l) => safeHref(l.url))
        .map((l) => {
          const name = l.variant_title ? `${l.product_title} — ${l.variant_title}` : l.product_title;
          return `<li><a href="${esc(l.url)}" class="link" rel="noopener" download>${esc(name)}</a></li>`;
        })
        .join('');
      body = items
        ? `<ul class="shop-downloads__list list-unstyled" role="list">${items}</ul><p class="caption">Ссылки действуют 15 минут. Если время вышло, обновите их.</p>${refresh}`
        : '';
    }
  }
  return body ? `<div class="shop-downloads"><h2 class="h3">Ваши файлы</h2>${body}</div>` : '';
}

export function statusView(ctx: Ctx, st: OrderStatus | 'loading' | 'error', dl?: DownloadsState): string {
  const all = esc(url(ctx, '/collections/all'));
  const wrap = (title: string, body: string): string => `<div class="template-404 page-width page-margin center shop-status">
  <h1 class="title">${esc(title)}</h1>${body}
</div>`;
  if (st === 'loading') return wrap(T.loading, '');
  if (st === 'error') {
    return wrap('Не удалось загрузить заказ', `<p>Проверьте соединение и обновите страницу.</p><a href="${all}" class="button">${T.continueShopping}</a>`);
  }
  const number = st.number != null && st.number !== '' ? ` № ${esc(st.number)}` : '';
  switch (st.status) {
    case 'paid':
      return wrap(
        'Спасибо за заказ!',
        `<p>Заказ${number} оплачен. Мы отправили подтверждение на вашу почту${hasPhysicalLines(st) ? ' и свяжемся с вами по доставке' : ''}.</p>${dl ? downloadsBlock(dl) : ''}<a href="${all}" class="button">${T.continueShopping}</a>`,
      );
    case 'pending':
      return wrap(
        'Ожидаем оплату',
        `<p>Заказ${number} ещё не оплачен. Если вы закрыли страницу оплаты, вернитесь к ней по ссылке.</p>${
          st.payment_url ? `<a href="${esc(st.payment_url)}" class="button" id="return-to-payment">Вернуться к оплате</a>` : ''
        }`,
      );
    case 'cancelled':
      return wrap('Заказ отменён', `<p>Заказ${number} отменён.</p><a href="${all}" class="button">${T.continueShopping}</a>`);
    default:
      return wrap(
        'Время оплаты истекло',
        `<p>Заказ${number} не был оплачен вовремя, товары вернулись в продажу.</p><a href="${all}" class="button">${T.continueShopping}</a>`,
      );
  }
}
