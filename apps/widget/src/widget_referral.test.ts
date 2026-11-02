/**
 * "Refer a friend" on the buyer's side (spec 52 sections 3.3 and 7.2).
 *
 * Two things are pinned: the friend's `?promo=` (parsing, keeping it for the
 * tab, the words for the two new refusals) and the referrer's block on a paid
 * order (when it shows, what it copies and shares, its words). Component markup
 * is checked on the SOURCE (`?raw`), the idiom of this suite.
 */

import { describe, expect, it, vi } from 'vitest';
import orderStatusSource from './components/OrderStatus.svelte?raw';
import referralSource from './components/ReferralInvite.svelte?raw';
import widgetSource from './ArenaTickets.svelte?raw';
import {
  canNativeShare,
  copyToClipboard,
  copyValue,
  forgetWantedPromo,
  loadReferralBlock,
  parsePromoParam,
  promoFromSearch,
  promoStorageKey,
  referralText,
  resolveWantedPromo,
  shareData,
} from './lib/referral.js';
import type { ReferralCode } from './lib/referral.js';
import { REFERRAL_I18N, getReferralStrings } from './lib/referralI18n.js';
import { PROMO_I18N, getPromoStrings } from './lib/promoI18n.js';
import { isPromoErrorCode, promoErrorMessage } from './lib/promo.js';
import { ApiError } from './api.js';

const LOCALES = ['en', 'ru', 'cs', 'he', 'es', 'de', 'fr'] as const;

/** A minimal Storage that can be told to fail. */
function memoryStorage(opts: { failRead?: boolean; failWrite?: boolean } = {}): Storage {
  const data = new Map<string, string>();
  return {
    get length() {
      return data.size;
    },
    clear: () => data.clear(),
    getItem: (k: string) => {
      if (opts.failRead) throw new Error('blocked');
      return data.get(k) ?? null;
    },
    key: (i: number) => [...data.keys()][i] ?? null,
    removeItem: (k: string) => void data.delete(k),
    setItem: (k: string, v: string) => {
      if (opts.failWrite) throw new Error('blocked');
      data.set(k, v);
    },
  };
}

const CODE: ReferralCode = {
  code: 'ANNA-7K2Q',
  friend_discount_pct: 10,
  cashback_pct: 5,
  share_url: 'https://tickets.example.com/org/show?promo=ANNA-7K2Q&utm_source=referral',
};

describe('?promo= parsing', () => {
  it('trims and upper-cases a good code', () => {
    expect(parsePromoParam(' anna-7k2q ')).toBe('ANNA-7K2Q');
    expect(promoFromSearch('?session=x&promo=anna-7k2q&utm_source=referral')).toBe('ANNA-7K2Q');
  });

  it('accepts only letters, digits and dashes, 3 to 64 long', () => {
    expect(parsePromoParam('AB')).toBeNull();
    expect(parsePromoParam('A'.repeat(64))).toBe('A'.repeat(64));
    expect(parsePromoParam('A'.repeat(65))).toBeNull();
    expect(parsePromoParam('AB C')).toBeNull();
    expect(parsePromoParam('AB_CD')).toBeNull();
    expect(parsePromoParam('<script>')).toBeNull();
    expect(parsePromoParam('')).toBeNull();
    expect(parsePromoParam(null)).toBeNull();
    expect(parsePromoParam(undefined)).toBeNull();
  });

  it('is null when the address has no promo', () => {
    expect(promoFromSearch('')).toBeNull();
    expect(promoFromSearch('?ref=abc')).toBeNull();
    expect(promoFromSearch('?promo=')).toBeNull();
  });
});

describe('keeping the code for the tab', () => {
  it('reads the address, remembers it and finds it again on a page without it', () => {
    const store = memoryStorage();
    expect(resolveWantedPromo('ft1', '', '?promo=anna-7k2q', store)).toBe('ANNA-7K2Q');
    expect(store.getItem(promoStorageKey('ft1'))).toBe('ANNA-7K2Q');
    // The next page of the same tab has no ?promo=.
    expect(resolveWantedPromo('ft1', '', '', store)).toBe('ANNA-7K2Q');
  });

  it('keys the memory by feed token', () => {
    const store = memoryStorage();
    resolveWantedPromo('ft1', '', '?promo=ANNA-7K2Q', store);
    expect(resolveWantedPromo('ft2', '', '', store)).toBeNull();
  });

  it('prefers the attribute handed over by the hosted page, then the address', () => {
    const store = memoryStorage();
    expect(resolveWantedPromo('ft1', 'from-attr', '?promo=FROM-URL', store)).toBe('FROM-ATTR');
    expect(resolveWantedPromo('ft1', 'bad code!', '?promo=FROM-URL', store)).toBe('FROM-URL');
  });

  it('a newer code replaces the kept one', () => {
    const store = memoryStorage();
    resolveWantedPromo('ft1', '', '?promo=OLD-ONE', store);
    resolveWantedPromo('ft1', '', '?promo=NEW-ONE', store);
    expect(resolveWantedPromo('ft1', '', '', store)).toBe('NEW-ONE');
  });

  it('never trusts a tampered stored value', () => {
    const store = memoryStorage();
    store.setItem(promoStorageKey('ft1'), '<img onerror=x>');
    expect(resolveWantedPromo('ft1', '', '', store)).toBeNull();
  });

  it('works without storage: the code of the address still applies', () => {
    expect(resolveWantedPromo('ft1', '', '?promo=ANNA-7K2Q', null)).toBe('ANNA-7K2Q');
    expect(resolveWantedPromo('ft1', '', '', null)).toBeNull();
    expect(resolveWantedPromo('ft1', '', '?promo=ANNA-7K2Q', memoryStorage({ failWrite: true }))).toBe('ANNA-7K2Q');
    expect(resolveWantedPromo('ft1', '', '', memoryStorage({ failRead: true }))).toBeNull();
  });

  it('forgets on request', () => {
    const store = memoryStorage();
    resolveWantedPromo('ft1', '', '?promo=ANNA-7K2Q', store);
    forgetWantedPromo('ft1', store);
    expect(resolveWantedPromo('ft1', '', '', store)).toBeNull();
  });
});

describe('the two new refusals', () => {
  it('have their own words in every language, not the neutral answer', () => {
    for (const l of LOCALES) {
      const p = getPromoStrings(l);
      expect(p.error_own_referral, l).toBeTruthy();
      expect(p.error_not_active, l).toBeTruthy();
      expect(p.error_own_referral, l).not.toBe(p.error_not_applicable);
      expect(p.error_not_active, l).not.toBe(p.error_not_applicable);
      expect(p.error_own_referral, l).not.toBe(p.error_not_active);
      expect(promoErrorMessage('promo.own_referral_code', p), l).toBe(p.error_own_referral);
      expect(promoErrorMessage('promo.not_active', p), l).toBe(p.error_not_active);
    }
    expect(isPromoErrorCode('promo.own_referral_code')).toBe(true);
    expect(isPromoErrorCode('promo.not_active')).toBe(true);
  });

  it('every promo locale still carries exactly the same keys', () => {
    const keys = Object.keys(PROMO_I18N.en!).sort();
    for (const l of LOCALES) expect(Object.keys(PROMO_I18N[l]!).sort(), l).toEqual(keys);
  });
});

describe('the block on a paid order', () => {
  it('shows what the platform answered', async () => {
    const res = await loadReferralBlock(async () => CODE);
    expect(res).toEqual({ show: true, data: CODE });
  });

  it('treats a missing share_url as null', async () => {
    const res = await loadReferralBlock(async () => ({ code: 'AB-12', friend_discount_pct: 5, cashback_pct: 5 }));
    expect(res).toEqual({ show: true, data: { code: 'AB-12', friend_discount_pct: 5, cashback_pct: 5, share_url: null } });
  });

  it('is not shown on 404 referral.not_available', async () => {
    const res = await loadReferralBlock(async () => {
      throw new ApiError('not found', { status: 404, code: 'referral.not_available' });
    });
    expect(res).toEqual({ show: false });
  });

  it('is not shown on any other failure or a malformed answer', async () => {
    expect(await loadReferralBlock(async () => Promise.reject(new Error('network')))).toEqual({ show: false });
    expect(await loadReferralBlock(async () => ({}))).toEqual({ show: false });
    expect(await loadReferralBlock(async () => null)).toEqual({ show: false });
    expect(await loadReferralBlock(async () => ({ ...CODE, code: '' }))).toEqual({ show: false });
    expect(await loadReferralBlock(async () => ({ ...CODE, cashback_pct: '5' }))).toEqual({ show: false });
  });

  it('copies the link, or the code when the page has no address', () => {
    expect(copyValue(CODE)).toBe(CODE.share_url);
    expect(copyValue({ ...CODE, share_url: null })).toBe('ANNA-7K2Q');
  });

  it('copies through the clipboard and reports a refusal instead of throwing', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    expect(await copyToClipboard('x', { clipboard: { writeText } })).toBe(true);
    expect(writeText).toHaveBeenCalledWith('x');
    expect(await copyToClipboard('x', { clipboard: { writeText: () => Promise.reject(new Error('denied')) } })).toBe(false);
    expect(await copyToClipboard('x', {})).toBe(false);
    expect(await copyToClipboard('x', null)).toBe(false);
  });

  it('offers Share only where the browser can share', () => {
    expect(canNativeShare({ share: () => Promise.resolve() })).toBe(true);
    expect(canNativeShare({})).toBe(false);
    expect(canNativeShare(undefined)).toBe(false);
  });

  it('hands the share sheet the offer and the link', () => {
    const s = getReferralStrings('en');
    expect(shareData(s, CODE)).toEqual({
      title: s.title,
      text: 'Get 10% off tickets with my code ANNA-7K2Q.',
      url: CODE.share_url,
    });
    expect(shareData(s, { ...CODE, share_url: null }).url).toBeUndefined();
  });

  it('puts the two percentages into the offer', () => {
    expect(referralText(getReferralStrings('en'), CODE)).toBe(
      'Your friend gets 10% off with your code, and up to 5% of their purchase comes back to your card.',
    );
    expect(referralText(getReferralStrings('ru'), CODE)).toContain('10%');
    expect(referralText(getReferralStrings('ru'), CODE)).toContain('5%');
  });
});

describe('the words of the block', () => {
  const keys = Object.keys(REFERRAL_I18N.en!).sort();

  it('exist in all seven languages with the same keys', () => {
    for (const l of LOCALES) {
      expect(Object.keys(REFERRAL_I18N[l]!).sort(), l).toEqual(keys);
      for (const k of keys) expect((REFERRAL_I18N[l] as unknown as Record<string, string>)[k], `${l}.${k}`).toBeTruthy();
    }
  });

  it('keep the placeholders of the English text', () => {
    const holes = (s: string) => (s.match(/\{\w+\}/g) ?? []).sort().join(',');
    for (const l of LOCALES) {
      expect(holes(REFERRAL_I18N[l]!.text), `${l}.text`).toBe('{cashback},{discount}');
      expect(holes(REFERRAL_I18N[l]!.share_text), `${l}.share_text`).toBe('{code},{discount}');
    }
  });

  it('promises the cashback as "up to", because the cap can cut it', () => {
    const upTo: Record<string, string> = {
      en: 'up to {cashback}%', ru: 'до {cashback}%', cs: 'až {cashback} %', he: 'עד {cashback}%',
      es: 'hasta el {cashback}%', de: 'bis zu {cashback}%', fr: "jusqu'à {cashback} %",
    };
    for (const l of LOCALES) expect(REFERRAL_I18N[l]!.text, l).toContain(upTo[l]!);
  });

  it('is looked up by language subtag with an English fallback', () => {
    expect(getReferralStrings('ru-RU')).toBe(REFERRAL_I18N.ru);
    expect(getReferralStrings('xx')).toBe(REFERRAL_I18N.en);
  });
});

describe('wiring', () => {
  it('the paid screen asks for the code with the order token, and only when paid', () => {
    expect(orderStatusSource).toContain('<ReferralInvite {checkoutToken} {locale} {apiBase} />');
    const paid = orderStatusSource.indexOf("status.status === 'paid'");
    const expired = orderStatusSource.indexOf("status.status === 'expired'");
    const block = orderStatusSource.indexOf('<ReferralInvite');
    expect(block).toBeGreaterThan(paid);
    expect(block).toBeLessThan(expired);
    expect(widgetSource).toContain('{checkoutToken}');
  });

  it('draws nothing unless the platform gave a code, and never styles inline', () => {
    expect(referralSource).toContain('{#if block.show}');
    expect(referralSource).not.toMatch(/\sstyle=/);
    expect(referralSource).toContain('typeof navigator !== \'undefined\'');
  });

  it('applies the code of the link once, to the first cart, and opens the field', () => {
    expect(widgetSource).toContain("promo: { type: 'String', attribute: 'promo' }");
    expect(widgetSource).toContain('resolveWantedPromo(normFeedToken, promoAttribute, window.location.search)');
    expect(widgetSource).toContain('wantedPromoTried = true');
    expect(widgetSource).toContain('promoCtl.apply(code)');
    expect(widgetSource).toContain('promoExpanded={wantedPromo !== null}');
    expect(widgetSource).toContain("promoPrefill={wantedPromo ?? ''}");
  });
});
