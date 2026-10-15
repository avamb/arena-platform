/**
 * The checkout consent (P2): the rules the buyer form and the checkout follow,
 * the words in all seven languages, and the markup a buyer gets.
 */

import { describe, it, expect } from 'vitest';
import { render } from 'svelte/server';
import BuyerForm from '../components/BuyerForm.svelte';
import buyerFormSource from '../components/BuyerForm.svelte?raw';
import cartSheetSource from '../components/CartSheet.svelte?raw';
import widgetSource from '../ArenaTickets.svelte?raw';
import {
  CONSENT_REQUIRED_CODE,
  consentErrorMessage,
  consentPayBlocked,
  consentPayload,
  consentRequired,
  consentSegments,
  forceConsent,
  safeConsentUrl,
} from './consent.js';
import { CONSENT_I18N, getConsentStrings } from './consentI18n.js';
import { buildCheckoutPayload } from './checkout.js';

const LOCALES = ['en', 'ru', 'cs', 'he', 'es', 'de', 'fr'];

const REQUIRED = {
  required: true,
  seller_legal_name: 'Teatro Demo S.L.',
  terms_url: 'https://arenasoldout.com/legal/terms',
  privacy_url: 'https://arenasoldout.com/legal/privacy',
};

function formHtml(props: Record<string, unknown>): string {
  return render(BuyerForm, {
    props: { buyerFields: [], onSubmit: () => {}, ...props },
  }).body;
}

describe('consent rules', () => {
  it('is required only when the session says so', () => {
    expect(consentRequired(REQUIRED)).toBe(true);
    expect(consentRequired({ required: false })).toBe(false);
    expect(consentRequired(undefined)).toBe(false);
    expect(consentRequired(null)).toBe(false);
  });

  it('holds the pay button only while a required box is unticked', () => {
    expect(consentPayBlocked(true, false)).toBe(true);
    expect(consentPayBlocked(true, true)).toBe(false);
    expect(consentPayBlocked(false, false)).toBe(false);
  });

  it('sends the consent only when ticked, with the language it was read in', () => {
    expect(consentPayload(false, 'ru')).toBeUndefined();
    expect(consentPayload(true, 'ru')).toEqual({ accepted: true, locale: 'ru' });
    expect(consentPayload(true, '')).toEqual({ accepted: true });
    expect(consentPayload(true, null)).toEqual({ accepted: true });
  });

  it('maps the platform refusal to the buyer text and nothing else', () => {
    expect(consentErrorMessage(CONSENT_REQUIRED_CODE, 'ru')).toBe(CONSENT_I18N.ru!.error_required);
    expect(consentErrorMessage('checkout.cart_too_large', 'ru')).toBeNull();
    expect(consentErrorMessage(undefined, 'en')).toBeNull();
  });

  it('shows the box after a refusal the feed did not announce', () => {
    expect(forceConsent({ required: false, terms_url: 'https://x.example/t' }, true)).toEqual({
      required: true,
      terms_url: 'https://x.example/t',
    });
    expect(forceConsent(undefined, true)).toEqual({ required: true });
    expect(forceConsent({ required: false }, false)).toEqual({ required: false });
    expect(forceConsent(REQUIRED, true)).toBe(REQUIRED);
    expect(forceConsent(undefined, false)).toBeNull();
  });

  it('links only to http(s) pages', () => {
    expect(safeConsentUrl('https://arenasoldout.com/legal/terms')).toBe('https://arenasoldout.com/legal/terms');
    expect(safeConsentUrl('javascript:alert(1)')).toBeNull();
    expect(safeConsentUrl('')).toBeNull();
    expect(safeConsentUrl('not a url')).toBeNull();
  });
});

describe('consent words', () => {
  it('has every language with both links and the seller slot in brackets', () => {
    for (const loc of LOCALES) {
      const s = CONSENT_I18N[loc];
      expect(s, loc).toBeDefined();
      expect(s!.label, loc).toMatch(/\[terms\][^[]+\[\/terms\]/);
      expect(s!.label, loc).toMatch(/\[privacy\][^[]+\[\/privacy\]/);
      // Until P3 the link is the platform terms: the seller is named in
      // brackets, never as the author of the terms.
      expect(s!.label, loc).toMatch(/\([^()]*\{seller\}\)/);
      expect(s!.hint.length, loc).toBeGreaterThan(0);
      expect(s!.error_required.length, loc).toBeGreaterThan(0);
      expect(s!.new_tab.length, loc).toBeGreaterThan(0);
    }
  });

  it('looks languages up by subtag and falls back to English', () => {
    expect(getConsentStrings('es-ES')).toBe(CONSENT_I18N.es);
    expect(getConsentStrings('xx')).toBe(CONSENT_I18N.en);
  });

  it('speaks Russian with the glossary words', () => {
    const ru = CONSENT_I18N.ru!;
    expect(ru.label).toContain('условия продажи');
    expect(ru.label).toContain('продавец');
    expect(ru.label).toContain('политику конфиденциальности');
    expect(ru.label).not.toContain('организатора');
    expect(`${ru.label} ${ru.hint} ${ru.error_required}`).not.toMatch(/ивент|событи|сесси/);
  });

  it('cuts the text into the links and the name', () => {
    const parts = consentSegments('en', 'Teatro Demo S.L.');
    expect(parts.map((p) => p.kind)).toEqual(['text', 'terms', 'text', 'seller', 'text', 'privacy', 'text']);
    expect(parts.map((p) => p.text).join('')).toBe(
      'I accept the sale terms (seller: Teatro Demo S.L.) and the privacy policy.',
    );
    const noName = consentSegments('en', '');
    expect(noName.some((p) => p.kind === 'seller')).toBe(false);
    expect(noName.map((p) => p.text).join('')).toBe('I accept the sale terms and the privacy policy.');
    for (const loc of LOCALES) {
      expect(consentSegments(loc, '').map((p) => p.text).join(''), loc).not.toMatch(/[()]/);
    }
    for (const loc of LOCALES) {
      const kinds = consentSegments(loc, 'X').map((p) => p.kind);
      expect(kinds, loc).toContain('terms');
      expect(kinds, loc).toContain('privacy');
      expect(kinds, loc).toContain('seller');
    }
  });
});

describe('consent checkbox in the buyer form', () => {
  it('is not there when the session does not ask for it', () => {
    expect(formHtml({})).not.toContain('checkout-consent');
    expect(formHtml({ consent: { required: false } })).not.toContain('checkout-consent');
  });

  it('shows the box with both links in a new tab and holds the pay button', () => {
    const html = formHtml({ consent: REQUIRED });
    expect(html).toContain('data-testid="checkout-consent-box"');
    expect(html).toContain('Teatro Demo S.L.');
    expect(html).toContain('href="https://arenasoldout.com/legal/terms"');
    expect(html).toContain('href="https://arenasoldout.com/legal/privacy"');
    expect(html.match(/target="_blank"/g)?.length).toBe(2);
    expect(html.match(/rel="noopener noreferrer"/g)?.length).toBe(2);
    expect(html).toContain(CONSENT_I18N.en!.hint);
    // The pay button is disabled until the box is ticked.
    expect(html).toMatch(/<button type="submit" class="submit-btn[^"]*"[^>]*disabled/);
    expect(formHtml({})).not.toMatch(/<button type="submit" class="submit-btn[^"]*"[^>]*disabled/);
    // The box starts unticked: nothing is remembered.
    expect(html).not.toMatch(/checkout-consent-box"[^>]*checked/);
  });

  it('reads in the buyer language', () => {
    expect(formHtml({ consent: REQUIRED, locale: 'ru' })).toContain('условия продажи');
    expect(formHtml({ consent: REQUIRED, locale: 'he' })).toContain('תנאי המכירה');
  });

  it('keeps the tick out of storage and sends it through the submit values', () => {
    expect(buyerFormSource).not.toMatch(/localStorage|sessionStorage/);
    expect(buyerFormSource).toContain('values.consentAccepted = consentAccepted');
    expect(cartSheetSource).toContain('{consent}');
    expect(widgetSource).toContain('consent={forceConsent(selectedSession.consent, consentForcedFor === selectedSession.id)}');
    expect(widgetSource).toContain('consentForcedFor = selectedSession?.id ?? null');
    expect(widgetSource).toContain('consentPayload(values.consentAccepted === true, normLocale)');
    expect(widgetSource).toContain('consentErrorMessage(err.code, normLocale)');
  });

  it('leaves the checkout payload without consent unless the widget adds it', () => {
    const p = buildCheckoutPayload('s', { email: 'a@b.c', name: '', phone: '', consentAccepted: true }, [], [], []);
    expect(p).not.toHaveProperty('consent');
  });
});
