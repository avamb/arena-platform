/**
 * The "no right of withdrawal" notice and the pay-button label of the cart
 * sheet (HP-Q).
 *
 * BuyerForm is server-rendered here (svelte/server) so the assertions are on
 * the markup a buyer actually gets; the wiring from the feed session through
 * CartSheet is pinned with source assertions, the idiom of this suite.
 */

import { describe, it, expect } from 'vitest';
import { render } from 'svelte/server';
import BuyerForm from './components/BuyerForm.svelte';
import buyerFormSource from './components/BuyerForm.svelte?raw';
import cartSheetSource from './components/CartSheet.svelte?raw';
import widgetSource from './ArenaTickets.svelte?raw';
import typesSource from './types.ts?raw';
import {
  CHECKOUT_I18N,
  PAYMENT_COMMIT_I18N,
  getPaymentCommitStrings,
  payButtonLabel,
} from './lib/checkout.js';

const EN_NOTICE =
  'Tickets for an event on a specific date are not covered by the right of withdrawal. Your order becomes binding when you pay.';

function formHtml(props: Record<string, unknown>): string {
  return render(BuyerForm, {
    props: { buyerFields: [], onSubmit: () => {}, ...props },
  }).body;
}

describe('withdrawal notice in the buyer form', () => {
  it('prints the notice directly above the pay button when asked', () => {
    const html = formHtml({ withdrawalNotice: true });
    expect(html).toContain(EN_NOTICE);
    expect(html.indexOf(EN_NOTICE)).toBeLessThan(html.indexOf('<button type="submit"'));
    // Directly above: nothing but the notice paragraph between the two.
    const between = html.slice(html.indexOf(EN_NOTICE), html.indexOf('<button type="submit"'));
    expect(between).not.toContain('<input');
    expect(between).not.toContain('<label');
  });

  it('does not print it when the flag is false or absent', () => {
    expect(formHtml({ withdrawalNotice: false })).not.toContain('withdrawal');
    expect(formHtml({})).not.toContain('right of withdrawal');
    expect(formHtml({})).not.toContain('withdrawal-notice');
  });

  it('uses the buyer language for the notice, Spanish and Czech included', () => {
    expect(formHtml({ withdrawalNotice: true, locale: 'es' })).toContain('derecho de desistimiento');
    expect(formHtml({ withdrawalNotice: true, locale: 'cs' })).toContain('právo na odstoupení od smlouvy');
    expect(formHtml({ withdrawalNotice: true, locale: 'es-ES' })).toContain('derecho de desistimiento');
    expect(formHtml({ withdrawalNotice: true, locale: 'xx' })).toContain(EN_NOTICE);
  });

  it('keeps the notice in the reading direction (logical CSS, no physical sides)', () => {
    const html = formHtml({ withdrawalNotice: true, locale: 'he' });
    expect(html).toContain('זכות הביטול');
    // Style text is compiled out of the markup: pin it in the source.
    const rule = buyerFormSource.match(/\.withdrawal-notice\s*\{[^}]*\}/)?.[0] ?? '';
    expect(rule).toContain('text-align: start');
    expect(rule).not.toMatch(/left|right/);
  });

  it('labels the submit button with the text it is given, else the generic label', () => {
    expect(formHtml({ submitText: 'Pay €50' })).toContain('Pay €50');
    expect(formHtml({})).toContain(CHECKOUT_I18N.en.submit_label);
  });
});

describe('pay button label', () => {
  it('names the amount and the act', () => {
    const label = payButtonLabel(5000, 'EUR', 'en');
    expect(label).toMatch(/^Pay /);
    expect(label).toMatch(/50/);
  });

  it('is localized and says "pay" in every language', () => {
    expect(payButtonLabel(5000, 'EUR', 'es')).toMatch(/^Pagar /);
    expect(payButtonLabel(5000, 'EUR', 'cs')).toMatch(/^Zaplatit /);
    expect(payButtonLabel(5000, 'EUR', 'ru')).toMatch(/^Оплатить /);
    expect(payButtonLabel(5000, 'EUR', 'he')).toMatch(/^לתשלום /);
    expect(payButtonLabel(5000, 'EUR', 'fr')).toMatch(/^Payer /);
    expect(payButtonLabel(5000, 'EUR', 'de')).toMatch(/^Zahlungspflichtig bestellen/);
  });

  it('falls back to a plain pay verb without a currency and to the generic label for a free order', () => {
    expect(payButtonLabel(5000, '', 'en')).toBe('Pay now');
    expect(payButtonLabel(0, 'EUR', 'en')).toBeUndefined();
  });
});

describe('payment commitment strings', () => {
  it('exist for every widget locale and for es, de, fr', () => {
    for (const locale of ['en', 'ru', 'cs', 'he', 'es', 'de', 'fr']) {
      const s = PAYMENT_COMMIT_I18N[locale];
      expect(s, locale).toBeDefined();
      expect(s!.withdrawal_notice.length, locale).toBeGreaterThan(40);
      expect(s!.pay_label).toContain('{amount}');
      expect(s!.pay_label_plain.length).toBeGreaterThan(0);
    }
  });

  it('are really translated, never the English text under another code', () => {
    for (const locale of ['ru', 'cs', 'he', 'es', 'de', 'fr']) {
      expect(PAYMENT_COMMIT_I18N[locale]!.withdrawal_notice, locale).not.toBe(EN_NOTICE);
      expect(PAYMENT_COMMIT_I18N[locale]!.pay_label, locale).not.toBe(PAYMENT_COMMIT_I18N.en!.pay_label);
    }
  });

  it('fall back to English for an unknown language', () => {
    expect(getPaymentCommitStrings('ja').withdrawal_notice).toBe(EN_NOTICE);
  });
});

describe('wiring from the feed session to the pay button', () => {
  it('reads show_withdrawal_notice off the session, true only on a real true', () => {
    expect(typesSource).toContain('show_withdrawal_notice?: boolean');
    expect(widgetSource).toContain('showWithdrawalNotice={selectedSession.show_withdrawal_notice === true}');
  });

  it('passes the flag and the pay label to the buyer form, not to the cart step', () => {
    expect(cartSheetSource).toContain('withdrawalNotice={showWithdrawalNotice}');
    expect(cartSheetSource).toContain('submitText={payLabel}');
    expect(cartSheetSource).toContain('showWithdrawalNotice = false');
  });
});
