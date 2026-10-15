/**
 * P1b: the buyer's name (ONE field, first and last name) and phone are
 * offered on every checkout and are optional unless the session's
 * `buyer_fields` mark them required. BuyerForm is server-rendered here, the
 * idiom of this suite.
 */

import { describe, expect, it } from 'vitest';
import { render } from 'svelte/server';
import BuyerForm from './components/BuyerForm.svelte';
import {
  CHECKOUT_I18N,
  buildCheckoutPayload,
  isBuyerFormValid,
  validateBuyerForm,
  type BuyerFieldConfig,
} from './lib/checkout.js';

function fields(nameRequired: boolean, phoneRequired: boolean): BuyerFieldConfig[] {
  return [
    { key: 'email', required: true, enabled: true },
    { key: 'name', required: nameRequired, enabled: true },
    { key: 'phone', required: phoneRequired, enabled: true },
  ];
}

function formHtml(buyerFields: BuyerFieldConfig[], locale = 'en'): string {
  return render(BuyerForm, {
    props: { buyerFields, locale, onSubmit: () => {} },
  }).body;
}

describe('BuyerForm optional marks', () => {
  it('marks name and phone optional when the channel does not require them', () => {
    const html = formHtml(fields(false, false));
    expect(html).toContain('id="bf-name"');
    expect(html).toContain('id="bf-phone"');
    expect(html).toContain('data-testid="bf-name-optional"');
    expect(html).toContain('data-testid="bf-phone-optional"');
    expect(html).toContain('First and last name');
    expect(html).toContain('(optional)');
  });

  it('marks only the field that is not required', () => {
    const html = formHtml(fields(true, false));
    expect(html).not.toContain('bf-name-optional');
    expect(html).toContain('bf-phone-optional');
    const html2 = formHtml(fields(false, true));
    expect(html2).toContain('bf-name-optional');
    expect(html2).not.toContain('bf-phone-optional');
  });

  it('marks neither when both are required', () => {
    const html = formHtml(fields(true, true));
    expect(html).not.toContain('(optional)');
  });

  it('does not render a field that is not enabled', () => {
    const html = formHtml([
      { key: 'email', required: true, enabled: true },
      { key: 'name', required: false, enabled: false },
      { key: 'phone', required: false, enabled: false },
    ]);
    expect(html).not.toContain('id="bf-name"');
    expect(html).not.toContain('id="bf-phone"');
  });

  it('says "Имя и фамилия" and "(необязательно)" in Russian', () => {
    const html = formHtml(fields(false, false), 'ru');
    expect(html).toContain('Имя и фамилия');
    expect(html).toContain('(необязательно)');
  });

  it('has an optional mark and a first-and-last-name label in every locale', () => {
    for (const [locale, t] of Object.entries(CHECKOUT_I18N)) {
      expect(t.optional_label, locale).toBeTruthy();
      expect(t.name_label, locale).toBeTruthy();
    }
  });
});

describe('buyer fields validation and payload', () => {
  const empty = { email: 'a@b.com', name: '', phone: '' };

  it('lets an empty optional name and phone through', () => {
    expect(isBuyerFormValid(validateBuyerForm(empty, fields(false, false)))).toBe(true);
  });

  it('refuses an empty name or phone only when required', () => {
    expect(validateBuyerForm(empty, fields(true, false)).name).toBeTruthy();
    expect(validateBuyerForm(empty, fields(true, false)).phone).toBeUndefined();
    expect(validateBuyerForm(empty, fields(false, true)).phone).toBeTruthy();
    expect(validateBuyerForm({ ...empty, name: '   ' }, fields(true, false)).name).toBeTruthy();
  });

  it('sends null for an empty optional value and the typed text otherwise', () => {
    const p1 = buildCheckoutPayload('s1', empty, [], [], fields(false, false));
    expect(p1.buyer).toEqual({ email: 'a@b.com', name: null, phone: null });
    const p2 = buildCheckoutPayload(
      's1', { email: 'a@b.com', name: ' Jane Smith ', phone: '+34600111222' }, [], [], fields(false, false));
    expect(p2.buyer).toEqual({ email: 'a@b.com', name: 'Jane Smith', phone: '+34600111222' });
  });
});