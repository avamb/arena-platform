/**
 * A date that is "soon" or "sold out", and the "Notify me when sales open" form.
 *
 * The public feed puts `sales_state` on every session. While it is `soon` or
 * `sold_out` no category is sellable, so the widget says so once on the date
 * and — for `soon` — offers the e-mail form. A missing or unknown value (an
 * older API) must look exactly as before.
 *
 * SessionList and SaleStateNotice are server-rendered (svelte/server) so the
 * assertions are on the markup a buyer gets; ArenaTickets is pinned with source
 * assertions, the idiom of this suite (its state is filled by effects that do
 * not run in SSR).
 */

import { describe, it, expect, vi, afterEach } from 'vitest';
import { render } from 'svelte/server';
import SessionList from './components/SessionList.svelte';
import SaleStateNotice from './components/SaleStateNotice.svelte';
import widgetSource from './ArenaTickets.svelte?raw';
import { ApiError, postSaleSubscription } from './api.js';
import {
  SALE_NOTIFY_CONSENT_VERSION,
  SALE_STATE_I18N,
  buildSaleSubscriptionPayload,
  getSaleStateStrings,
  isPlausibleEmail,
  offersSaleSubscription,
  pausedSalesState,
  saleConsentText,
  saleSubscriptionErrorText,
  saleSubscriptionFailureText,
  submitSaleSubscription,
} from './lib/saleSubscribe.js';
import type { FeedSession } from './types.js';

const LOCALES = ['en', 'ru', 'cs', 'he', 'es', 'de', 'fr'];

function session(id: string, salesState: string | undefined, status = 'scheduled'): FeedSession {
  return {
    id,
    start_at: '2026-12-01T18:00:00Z',
    end_at: '2026-12-01T20:00:00Z',
    capacity_total: 10,
    status,
    ...(salesState === undefined ? {} : { sales_state: salesState }),
    admission_mode: 'general_admission',
    buyer_fields: [],
    tiers: [],
    media_gallery: [],
    timezone: 'Europe/Prague',
  } as unknown as FeedSession;
}

function listHtml(sessions: FeedSession[], locale = 'en'): string {
  return render(SessionList, {
    props: { sessions, selectedSession: null, onSelectSession: () => {}, locale },
  }).body;
}

function noticeHtml(s: FeedSession, locale = 'en', orgName = ''): string {
  return render(SaleStateNotice, {
    props: { session: s, feedToken: 'tok', apiBase: 'https://api.example.com', locale, orgName },
  }).body;
}

describe('sales state helpers', () => {
  it('pauses only soon and sold_out', () => {
    expect(pausedSalesState(session('a', 'soon'))).toBe('soon');
    expect(pausedSalesState(session('a', 'sold_out'))).toBe('sold_out');
    for (const other of ['open', 'ended', 'cancelled', 'something_new', '', undefined]) {
      expect(pausedSalesState(session('a', other))).toBeNull();
    }
    expect(pausedSalesState(null)).toBeNull();
    expect(pausedSalesState(undefined)).toBeNull();
  });

  it('offers the subscription for soon only', () => {
    expect(offersSaleSubscription(session('a', 'soon'))).toBe(true);
    expect(offersSaleSubscription(session('a', 'sold_out'))).toBe(false);
    expect(offersSaleSubscription(session('a', 'open'))).toBe(false);
    expect(offersSaleSubscription(session('a', undefined))).toBe(false);
  });
});

describe('SessionList: the chip of a soon / sold out date', () => {
  it('labels a soon date "Coming soon" and keeps it selectable', () => {
    const html = listHtml([session('s1', 'soon')]);
    expect(html).toContain('data-testid="session-state-label"');
    expect(html).toContain('Coming soon');
    expect(html).not.toMatch(/<button[^>]*disabled/);
  });

  it('labels a sold out date "Sold out"', () => {
    const html = listHtml([session('s1', 'sold_out')]);
    expect(html).toContain('data-testid="session-state-label"');
    expect(html).toContain('Sold out');
  });

  it('keeps the cancelled label on a cancelled date, whatever sales_state says', () => {
    const html = listHtml([session('s1', 'cancelled', 'cancelled')]);
    expect(html).toContain('data-testid="session-cancelled-label"');
    expect(html).not.toContain('session-state-label');
  });

  it.each([['open'], ['ended'], ['mystery'], [undefined]])('prints no state label for %s (older API behaves as before)', (state) => {
    const html = listHtml([session('s1', state)]);
    expect(html).not.toContain('session-state-label');
    expect(html).not.toContain('session-cancelled-label');
  });

  it.each([
    ['en', 'Coming soon', 'Sold out'],
    ['ru', 'Скоро в продаже', 'Продано'],
    ['cs', 'Brzy v prodeji', 'Vyprodáno'],
    ['he', 'בקרוב', 'אזל'],
    ['es', 'Próximamente', 'Agotado'],
    ['de', 'Bald erhältlich', 'Ausverkauft'],
    ['fr', 'Bientôt disponible', 'Complet'],
  ])('is translated: %s', (locale, soon, soldOut) => {
    expect(listHtml([session('s1', 'soon')], locale)).toContain(soon);
    expect(listHtml([session('s1', 'sold_out')], locale)).toContain(soldOut);
  });

  it('falls back to English for a locale the widget has no table for', () => {
    expect(listHtml([session('s1', 'soon')], 'xx')).toContain('Coming soon');
  });
});

describe('SaleStateNotice: the notice and the notify form', () => {
  it('shows the form for a soon date', () => {
    const html = noticeHtml(session('s1', 'soon'));
    expect(html).toContain('data-testid="sale-subscribe-form"');
    expect(html).toContain('Notify me when sales open');
    expect(html).toContain('Ticket sales for this date have not started yet.');
  });

  it('shows no form for a sold out date, only the notice', () => {
    const html = noticeHtml(session('s1', 'sold_out'));
    expect(html).toContain('data-testid="session-sales-notice"');
    expect(html).toContain('All tickets for this date are sold out.');
    expect(html).not.toContain('<form');
    expect(html).not.toContain('<input');
  });

  it.each([['open'], ['ended'], ['cancelled'], ['mystery'], [undefined]])('renders nothing for %s', (state) => {
    expect(noticeHtml(session('s1', state)).trim()).not.toContain('session-sales-state');
    expect(noticeHtml(session('s1', state))).not.toContain('<form');
  });

  it('has one e-mail field and ONE unticked consent checkbox, each with a real label', () => {
    const html = noticeHtml(session('s1', 'soon'));
    expect(html.match(/<input/g)).toHaveLength(2);
    expect(html.match(/type="checkbox"/g)).toHaveLength(1);
    expect(html).toMatch(/<input[^>]*type="email"/);
    expect(html).not.toMatch(/<input[^>]*checked/);
    for (const id of html.match(/<input[^>]*\bid="([^"]+)"/g) ?? []) {
      const value = /\bid="([^"]+)"/.exec(id)![1]!;
      expect(html).toContain(`for="${value}"`);
    }
    expect(html).toMatch(/<label[^>]*for="sale-consent-/);
    expect(html).toMatch(/<button[^>]*type="submit"/);
  });

  it('prints the unsubscribe note under the button', () => {
    const html = noticeHtml(session('s1', 'soon'));
    expect(html).toContain('data-testid="sale-subscribe-note"');
    expect(html).toContain('We will send you a letter to confirm your address.');
    expect(html).toContain('«Unsubscribe»');
    expect(html.indexOf('type="submit"')).toBeLessThan(html.indexOf('sale-subscribe-note'));
  });

  it('names the organizer in the consent when it is known, and uses the nameless wording otherwise', () => {
    expect(noticeHtml(session('s1', 'soon'), 'en', 'Teatro Demo S.L.')).toContain(
      'I agree that Teatro Demo S.L. may e-mail me when sales open for this session',
    );
    const nameless = noticeHtml(session('s1', 'soon'), 'en', '');
    expect(nameless).toContain('I agree that the organizer may e-mail me');
    expect(nameless).not.toContain('{org}');
  });

  it('keeps messages in role="status" and leaves no empty alert behind', () => {
    const html = noticeHtml(session('s1', 'soon'));
    expect(html).toContain('role="status"');
    expect(html).not.toContain('role="alert"');
  });

  it('reads the Russian wording without a gendered "согласен(на)"', () => {
    const html = noticeHtml(session('s1', 'soon'), 'ru', 'Театр');
    expect(html).toContain('Даю согласие, что Театр напишет мне');
    expect(html).not.toContain('согласен(на)');
  });
});

describe('the request', () => {
  it('builds the body with consent, consent_version and page_url', () => {
    expect(buildSaleSubscriptionPayload('  anna@example.com ', 'es-ES', 'https://site.example/event?x=1')).toEqual({
      email: 'anna@example.com',
      locale: 'es',
      consent: true,
      consent_version: 'sale-notify-2026-11-01',
      page_url: 'https://site.example/event?x=1',
    });
    expect(SALE_NOTIFY_CONSENT_VERSION).toBe('sale-notify-2026-11-01');
  });

  it('falls back to en when the locale is empty', () => {
    expect(buildSaleSubscriptionPayload('a@b.co', '', '').locale).toBe('en');
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('POSTs JSON to the sale-subscriptions route of the feed token and session', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ status: 'check_email' }), { status: 202 }));
    vi.stubGlobal('fetch', fetchMock);
    const payload = buildSaleSubscriptionPayload('a@b.co', 'ru', 'https://p.example/');
    const res = await postSaleSubscription('feed tok', 'sess-1', payload, 'https://api.example.com');
    expect(res).toEqual({ status: 'check_email' });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('https://api.example.com/v1/public/feeds/feed%20tok/sessions/sess-1/sale-subscriptions');
    expect(init.method).toBe('POST');
    expect((init.headers as Record<string, string>)['Content-Type']).toBe('application/json');
    expect(JSON.parse(init.body as string)).toEqual(payload);
  });

  it('turns an error envelope into an ApiError carrying the code', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(JSON.stringify({ error: { code: 'subscription.sales_open', message: 'x' } }), { status: 409 })),
    );
    const err = await postSaleSubscription('t', 's', buildSaleSubscriptionPayload('a@b.co', 'en', ''), '').catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.code).toBe('subscription.sales_open');
  });
});

describe('error mapping', () => {
  const en = SALE_STATE_I18N.en!;

  it.each([
    ['subscription.invalid_email', en.error_invalid_email],
    ['subscription.consent_required', en.error_consent_required],
    ['subscription.consent_outdated', en.error_consent_outdated],
    ['subscription.sales_open', en.error_sales_open],
    ['subscription.rate_limited', en.error_rate_limited],
    ['feed.rate_limited', en.error_rate_limited],
    ['something.else', en.error_generic],
    [undefined, en.error_generic],
  ])('%s', (code, text) => {
    expect(saleSubscriptionErrorText(code, 'en')).toBe(text);
  });

  it('maps an ApiError by its code and a network failure to the generic sentence', () => {
    expect(saleSubscriptionFailureText(new ApiError('x', { status: 429, code: 'feed.rate_limited' }), 'en')).toBe(en.error_rate_limited);
    expect(saleSubscriptionFailureText(new TypeError('Failed to fetch'), 'en')).toBe(en.error_generic);
  });

  it('answers in the buyer\'s language', () => {
    expect(saleSubscriptionErrorText('subscription.invalid_email', 'ru')).toBe(SALE_STATE_I18N.ru!.error_invalid_email);
    expect(saleSubscriptionErrorText('subscription.invalid_email', 'ru')).not.toBe(en.error_invalid_email);
  });
});

describe('submitSaleSubscription', () => {
  const form = { email: 'anna@example.com', consent: true, locale: 'en', pageUrl: 'https://p.example/' };

  it('refuses a bad address without sending anything', async () => {
    const post = vi.fn();
    const out = await submitSaleSubscription({ ...form, email: 'not-an-email' }, post);
    expect(out).toEqual({ ok: false, field: 'email', message: en().error_invalid_email });
    expect(post).not.toHaveBeenCalled();
  });

  it('refuses an unticked box without sending anything', async () => {
    const post = vi.fn();
    const out = await submitSaleSubscription({ ...form, consent: false }, post);
    expect(out).toEqual({ ok: false, field: 'consent', message: en().error_consent_required });
    expect(post).not.toHaveBeenCalled();
  });

  it('sends the built payload and reports success', async () => {
    const post = vi.fn(async () => ({ status: 'check_email' as const }));
    expect(await submitSaleSubscription(form, post)).toEqual({ ok: true });
    expect(post).toHaveBeenCalledWith(buildSaleSubscriptionPayload(form.email, form.locale, form.pageUrl));
  });

  it('maps a server code to the friendly text and the field to focus', async () => {
    const post = async () => {
      throw new ApiError('boom', { status: 422, code: 'subscription.invalid_email' });
    };
    expect(await submitSaleSubscription(form, post)).toEqual({ ok: false, field: 'email', message: en().error_invalid_email });
  });

  it('answers a network failure with the generic sentence and no field', async () => {
    const post = async () => {
      throw new TypeError('Failed to fetch');
    };
    expect(await submitSaleSubscription(form, post)).toEqual({ ok: false, field: null, message: en().error_generic });
  });

  it('can be used again after an error (the outcome never throws)', async () => {
    let n = 0;
    const post = async () => {
      if (n++ === 0) throw new ApiError('x', { status: 429, code: 'subscription.rate_limited' });
      return { status: 'check_email' as const };
    };
    expect((await submitSaleSubscription(form, post)).ok).toBe(false);
    expect((await submitSaleSubscription(form, post)).ok).toBe(true);
  });

  function en() {
    return SALE_STATE_I18N.en!;
  }
});

describe('e-mail shape', () => {
  it.each(['a@b.co', ' a.b+c@sub.example.com '])('accepts %s', (v) => expect(isPlausibleEmail(v)).toBe(true));
  it.each(['', 'a', 'a@b', '@b.co', 'a b@c.de', 'a@@b.co'])('rejects "%s"', (v) => expect(isPlausibleEmail(v)).toBe(false));
});

describe('the words exist in every language', () => {
  const keys = Object.keys(SALE_STATE_I18N.en!) as (keyof typeof SALE_STATE_I18N.en)[];

  it('covers all seven widget languages', () => {
    expect(Object.keys(SALE_STATE_I18N).sort()).toEqual([...LOCALES].sort());
  });

  for (const locale of LOCALES) {
    it(`${locale}: every key is a non-empty string`, () => {
      const table = SALE_STATE_I18N[locale]!;
      expect(Object.keys(table).sort()).toEqual([...keys].sort());
      for (const key of keys) {
        expect(typeof table[key], `${locale}.${key}`).toBe('string');
        expect(table[key].trim(), `${locale}.${key}`).not.toBe('');
      }
    });

    it(`${locale}: only the consent carries {org}, and no other placeholder is left`, () => {
      const table = SALE_STATE_I18N[locale]!;
      expect(table.consent).toContain('{org}');
      expect(table.consent_no_org).not.toContain('{org}');
      for (const key of keys) {
        if (key !== 'consent') expect(table[key], `${locale}.${key}`).not.toMatch(/\{[a-z_]+\}/);
      }
    });

    it(`${locale}: the note keeps the «Unsubscribe» quotation marks`, () => {
      expect(SALE_STATE_I18N[locale]!.note).toMatch(/«[^»]+»/);
    });

    it(`${locale}: saleConsentText fills the name`, () => {
      expect(saleConsentText(locale, 'Org X')).toContain('Org X');
      expect(saleConsentText(locale, '')).not.toContain('{org}');
      expect(saleConsentText(locale, undefined)).toBe(SALE_STATE_I18N[locale]!.consent_no_org);
    });
  }

  it('the other languages are real translations, not the English text', () => {
    for (const locale of LOCALES.filter((l) => l !== 'en')) {
      for (const key of keys) {
        // E-mail is the same word in cs and fr.
        if (key === 'email_label') continue;
        expect(SALE_STATE_I18N[locale]![key], `${locale}.${key}`).not.toBe(SALE_STATE_I18N.en![key]);
      }
    }
  });

  it('keeps the owner-approved English and Russian sentences', () => {
    expect(getSaleStateStrings('en').consent).toBe(
      'I agree that {org} may e-mail me when sales open for this session and invite me to its future events. I can withdraw this consent at any time.',
    );
    expect(getSaleStateStrings('en').note).toBe(
      'We will send you a letter to confirm your address. If you later click «Unsubscribe» in any of our letters, letters from us will never reach this address again, tickets included.',
    );
    expect(getSaleStateStrings('ru').note).toBe(
      'Мы отправим письмо, чтобы подтвердить адрес. Если позже вы нажмёте «Отписаться» в любом нашем письме, письма от нас на этот адрес больше никогда не придут, в том числе билеты.',
    );
    expect(getSaleStateStrings('en').done).toBe('Almost done. We sent you a letter: open it and confirm your address.');
  });

  it('the Russian text uses the house glossary', () => {
    const ru = Object.values(SALE_STATE_I18N.ru!).join(' ');
    expect(ru).not.toMatch(/событи|ивент|сессия|сессии/i);
  });

  it('looks a locale up by language subtag and falls back to English', () => {
    expect(getSaleStateStrings('es-ES')).toBe(SALE_STATE_I18N.es);
    expect(getSaleStateStrings('xx')).toBe(SALE_STATE_I18N.en);
  });
});

describe('ArenaTickets: a paused date offers nothing to buy', () => {
  it('derives one flag from the selected date, never for a cancelled one', () => {
    expect(widgetSource).toMatch(
      /const salesPaused = \$derived\(!salesClosed && selectedSession !== null && pausedSalesState\(selectedSession\) !== null\)/,
    );
  });

  it('mounts the notice (and with it the form) when the flag is set', () => {
    expect(widgetSource).toMatch(/\{#if salesPaused && selectedSession\}\s*\{#key selectedSession\.id\}\s*<SaleStateNotice/);
  });

  it('passes the feed token, the API base and the locale to the form', () => {
    expect(widgetSource).toContain('feedToken={normFeedToken}');
    expect(widgetSource).toContain('apiBase={resolvedApiBase}');
  });

  it('keeps the map, the steppers and the seated card behind the flag', () => {
    expect(widgetSource).toContain('{#if !salesClosed && !salesPaused && selectedSession?.schema_url && seatedTiers.length > 0}');
    expect(widgetSource).toContain('{#if !salesClosed && !salesPaused && selectedSession && selectedSession.schema_url}');
    expect(widgetSource).toContain('{#if !salesClosed && !salesPaused && gaTiers.length > 0}');
  });

  it('keeps the cart bar for a buyer who already holds seats', () => {
    expect(widgetSource).toContain('{#if selectedSession && !salesClosed && (!salesPaused || cart.lines.length > 0)}');
  });
});
