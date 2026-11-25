/**
 * The two API calls the embedded payment screen adds (spec 65 §3.4, §3.5):
 * GET /v1/public/checkout/{token}?include=payment and
 * POST /v1/public/checkout/{token}/hosted.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';
import { ApiError, getCheckoutStatus, postCheckoutHosted } from './api.js';

afterEach(() => {
  vi.unstubAllGlobals();
});

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

describe('getCheckoutStatus include=payment', () => {
  it('keeps the plain URL when the block is not asked for', async () => {
    const f = vi.fn(async () => json(200, { status: 'pending', items: [], tickets: [] }));
    vi.stubGlobal('fetch', f);
    await getCheckoutStatus('tok', 'https://api.test');
    expect(f).toHaveBeenCalledWith('https://api.test/v1/public/checkout/tok');
  });

  it('asks with include=payment and never lets the answer be cached', async () => {
    const f = vi.fn(async () =>
      json(200, {
        status: 'pending',
        items: [],
        tickets: [],
        payment: { provider: 'stripe', mode: 'elements', client_secret: 'cs', publishable_key: 'pk' },
      }),
    );
    vi.stubGlobal('fetch', f);
    const res = await getCheckoutStatus('tok', 'https://api.test', { includePayment: true });
    expect(f).toHaveBeenCalledWith('https://api.test/v1/public/checkout/tok?include=payment', {
      cache: 'no-store',
    });
    expect(res.payment?.mode).toBe('elements');
  });
});

describe('postCheckoutHosted', () => {
  it('posts to /hosted and returns the hosted page address', async () => {
    const f = vi.fn(async () => json(200, { checkout_token: 'tok', redirect_url: 'https://checkout.stripe.com/x' }));
    vi.stubGlobal('fetch', f);
    const out = await postCheckoutHosted('tok', 'https://api.test');
    expect(f).toHaveBeenCalledWith('https://api.test/v1/public/checkout/tok/hosted', { method: 'POST' });
    expect(out.redirect_url).toBe('https://checkout.stripe.com/x');
  });

  it.each(['checkout.hosted_fallback_unavailable', 'checkout.not_payable'])(
    'throws an ApiError carrying %s on a 409',
    async (code) => {
      vi.stubGlobal('fetch', vi.fn(async () => json(409, { error: { code, message: 'no' } })));
      const err = (await postCheckoutHosted('tok').catch((e: unknown) => e)) as ApiError;
      expect(err).toBeInstanceOf(ApiError);
      expect(err.status).toBe(409);
      expect(err.code).toBe(code);
    },
  );
});
