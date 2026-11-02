import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import {
  MARKETING_TEXT_VERSION,
  formatMarketingDiscount,
  isMarketingRewardRefusal,
  marketingErrorMessage,
  marketingLabel,
  marketingOffered,
  marketingPayload,
} from './marketing.js';
import { MARKETING_I18N, getMarketingStrings } from './marketingI18n.js';

const offer = { enabled: true, org_name: 'Teatro Sur', text_version: MARKETING_TEXT_VERSION, discount: { type: 'percent', value: 10 } };

describe('marketing tick', () => {
  it('is offered only when the session says so', () => {
    expect(marketingOffered(offer)).toBe(true);
    expect(marketingOffered({ enabled: false })).toBe(false);
    expect(marketingOffered(undefined)).toBe(false);
  });

  it('sends a payload only for a ticked box of an offering session', () => {
    expect(marketingPayload(offer, false, false)).toBeUndefined();
    expect(marketingPayload({ enabled: false }, true, false)).toBeUndefined();
    expect(marketingPayload(offer, true, false)).toEqual({ accepted: true, text_version: MARKETING_TEXT_VERSION });
    expect(marketingPayload(offer, true, true)).toEqual({ accepted: true, text_version: MARKETING_TEXT_VERSION, discount: false });
  });

  it('sends the version of the text it shows, not the one the feed names', () => {
    // A widget that shows the older text must be refused by the platform,
    // never recorded as having shown the newer one.
    const newer = { ...offer, text_version: 'marketing-optin-2099-01-01' };
    expect(marketingPayload(newer, true, false)).toEqual({ accepted: true, text_version: MARKETING_TEXT_VERSION });
  });

  it('names the discount until it is used up', () => {
    expect(marketingLabel(offer, 'en', false)).toBe('Send me news and offers from Teatro Sur. You get 10 % off this order.');
    expect(marketingLabel(offer, 'en', true)).toBe('Send me news and offers from Teatro Sur.');
    expect(marketingLabel({ enabled: true }, 'ru', false)).toBe('Присылайте мне новости и предложения организатора.');
  });

  it('formats a fixed discount in its currency', () => {
    expect(formatMarketingDiscount({ type: 'fixed_amount', value: 500, currency: 'EUR' }, 'en')).toContain('5.00');
    expect(formatMarketingDiscount({ type: 'percent', value: 0 }, 'en')).toBe('');
  });

  it('maps only its own error codes', () => {
    expect(marketingErrorMessage('marketing.discount_used', 'es')).toBe(MARKETING_I18N.es!.discount_used);
    expect(marketingErrorMessage('marketing.consent_outdated', 'de-DE')).toBe(MARKETING_I18N.de!.outdated);
    expect(marketingErrorMessage('marketing.discount_unavailable', 'ru')).toBe(MARKETING_I18N.ru!.discount_unavailable);
    expect(marketingErrorMessage('promo.not_applicable', 'en')).toBeNull();
  });

  it('asks again without the reward after either refusal of it', () => {
    expect(isMarketingRewardRefusal('marketing.discount_used')).toBe(true);
    expect(isMarketingRewardRefusal('marketing.discount_unavailable')).toBe(true);
    expect(isMarketingRewardRefusal('marketing.consent_outdated')).toBe(false);
    expect(isMarketingRewardRefusal(undefined)).toBe(false);
  });

  it('carries every key and marker in all seven languages', () => {
    const keys = Object.keys(MARKETING_I18N.en!);
    for (const lang of ['en', 'ru', 'cs', 'he', 'es', 'de', 'fr']) {
      const w = MARKETING_I18N[lang];
      expect(w, lang).toBeDefined();
      expect(Object.keys(w!).sort(), lang).toEqual([...keys].sort());
      expect(w!.label, lang).toContain('{org}');
      expect(w!.discount, lang).toContain('{discount}');
      expect(w!.label_no_org, lang).not.toContain('{');
    }
    expect(getMarketingStrings('xx')).toBe(MARKETING_I18N.en);
  });

  it('keeps the text version equal to the platform constant', () => {
    const go = readFileSync(new URL('../../../backend/internal/platform/marketing/marketing.go', import.meta.url), 'utf8');
    expect(go).toContain(`const TextVersion = "${MARKETING_TEXT_VERSION}"`);
  });
});
