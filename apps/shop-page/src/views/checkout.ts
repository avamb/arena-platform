/**
 * checkout.ts - /checkout (the shop's own page: contacts, delivery address,
 * flat delivery, age consent, "Оплатить") and /checkout/{token} (status of one
 * order). Dawn's form, button and typography classes keep it inside the theme;
 * the page layout itself has no Shopify original (Shopify hosts its checkout).
 */
import { subtotalMinor, shippingMinor, totalMinor, type CartLine, type CheckoutForm, type FormErrors } from '../lib/cart';
import type { OrderStatus } from '../lib/api';
import { sized } from '../lib/cdn';
import { esc } from '../lib/html';
import { T } from '../lib/i18n';
import { icon, money, ratioOf, url, type Ctx } from './common';

export const EMPTY_FORM: CheckoutForm = {
  name: '',
  email: '',
  phone: '',
  address1: '',
  address2: '',
  city: '',
  postal: '',
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
    ? `<div class="shop-checkout__thumb"><img src="${esc(sized(img.url, 140))}" alt="${esc(img.alt ?? '')}" width="70" height="${Math.ceil(70 / ratioOf(img))}" loading="lazy"><span class="shop-checkout__qty">${line.qty}</span></div>`
    : '';
  const variant = line.product.variants.length > 1 ? `<div class="product-option">${esc(line.variant.title)}</div>` : '';
  return `<li class="shop-checkout__line">${image}<div class="shop-checkout__line-name"><span class="h5">${esc(line.product.title)}</span>${variant}</div><span class="shop-checkout__line-price">${esc(money(ctx, line.lineMinor))}</span></li>`;
}

export interface CheckoutViewState {
  values: CheckoutForm;
  errors: FormErrors;
  /** A request-level problem (out of stock, payment unavailable, network). */
  message?: string;
  submitting?: boolean;
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
      <h2 class="shop-checkout__heading h3">Адрес доставки</h2>
      ${field('address1', 'Улица, дом, квартира', st.values, e, { autocomplete: 'address-line1', required: true })}
      ${field('address2', 'Дополнительно (подъезд, этаж, комментарий курьеру)', st.values, e, { autocomplete: 'address-line2' })}
      ${field('city', 'Город', st.values, e, { autocomplete: 'address-level2', required: true })}
      ${field('postal', 'Почтовый индекс', st.values, e, { autocomplete: 'postal-code', inputmode: 'numeric' })}
      <h2 class="shop-checkout__heading h3">Доставка</h2>
      <div class="shop-checkout__delivery">
        <span class="shop-checkout__delivery-name">${label}</span>
        <span>${esc(money(ctx, shipping))}</span>
      </div>${
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
        <div><dt>${label}</dt><dd>${esc(money(ctx, shipping))}</dd></div>
        <div class="shop-checkout__total"><dt>Итого</dt><dd>${esc(money(ctx, total))}</dd></div>
      </dl>
    </aside>
  </div>
</div></div>`;
}

export function statusView(ctx: Ctx, st: OrderStatus | 'loading' | 'error'): string {
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
        `<p>Заказ${number} оплачен. Мы отправили подтверждение на вашу почту и свяжемся с вами по доставке.</p><a href="${all}" class="button">${T.continueShopping}</a>`,
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
