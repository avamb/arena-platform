/**
 * api.test.ts — structured-error contract for the public API client.
 *
 * Focuses on `postCheckoutStart`: on non-2xx it must throw an `ApiError`
 * (still an `Error`, message format unchanged) carrying machine-readable
 * `status`, `code`, and `details` so UI code can read e.g.
 * `details.conflicts` after a 409 seat conflict.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';
import { postCheckoutStart, ApiError } from './api.js';
import type { CheckoutStartPayload } from './lib/checkout.js';

const payload: CheckoutStartPayload = {
  session_id: 'sess-1',
  holder_email: 'a@b.cz',
  seats: ['P|1|1'],
};

function mockFetchResponse(init: {
  ok: boolean;
  status?: number;
  statusText?: string;
  body?: unknown;
  jsonThrows?: boolean;
}): void {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({
      ok: init.ok,
      status: init.status ?? 200,
      statusText: init.statusText ?? '',
      json: init.jsonThrows
        ? () => Promise.reject(new SyntaxError('not json'))
        : () => Promise.resolve(init.body ?? {}),
    }),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('postCheckoutStart — success', () => {
  it('returns the parsed response on 2xx', async () => {
    const body = {
      checkout_session: {},
      redirect_url: 'https://pay.example/x',
      checkout_token: 'ct_1',
      expires_at: '2026-07-11T00:00:00Z',
    };
    mockFetchResponse({ ok: true, body });
    await expect(postCheckoutStart('ft', payload)).resolves.toEqual(body);
  });
});

describe('postCheckoutStart — structured errors', () => {
  it('throws an ApiError that is still an Error with the legacy message format', async () => {
    mockFetchResponse({
      ok: false,
      status: 409,
      body: { error: 'seat_conflict' },
    });
    const err = await postCheckoutStart('ft', payload).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(Error);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).message).toBe('postCheckoutStart HTTP 409: seat_conflict');
  });

  it('exposes status, code, and details (e.g. details.conflicts on 409)', async () => {
    mockFetchResponse({
      ok: false,
      status: 409,
      body: {
        error: 'seat_conflict',
        code: 'seat_conflict',
        details: { conflicts: ['P|1|1', 'P|1|2'] },
      },
    });
    const err = (await postCheckoutStart('ft', payload).catch((e: unknown) => e)) as ApiError;
    expect(err.status).toBe(409);
    expect(err.code).toBe('seat_conflict');
    expect(err.details.conflicts).toEqual(['P|1|1', 'P|1|2']);
  });

  it('falls back to the body error string as code when code is absent', async () => {
    mockFetchResponse({
      ok: false,
      status: 422,
      body: { error: 'validation_failed' },
    });
    const err = (await postCheckoutStart('ft', payload).catch((e: unknown) => e)) as ApiError;
    expect(err.code).toBe('validation_failed');
    expect(err.details).toEqual({});
  });

  it('uses http_<status> code and empty details for non-JSON error bodies', async () => {
    mockFetchResponse({ ok: false, status: 502, jsonThrows: true });
    const err = (await postCheckoutStart('ft', payload).catch((e: unknown) => e)) as ApiError;
    expect(err.message).toBe('postCheckoutStart HTTP 502');
    expect(err.status).toBe(502);
    expect(err.code).toBe('http_502');
    expect(err.details).toEqual({});
  });

  it('supports the message field as detail fallback', async () => {
    mockFetchResponse({
      ok: false,
      status: 400,
      body: { message: 'bad payload' },
    });
    const err = (await postCheckoutStart('ft', payload).catch((e: unknown) => e)) as ApiError;
    expect(err.message).toBe('postCheckoutStart HTTP 400: bad payload');
    expect(err.code).toBe('http_400');
  });

  it('parses real arena-backend nested envelope {"error": {...}} on 409 (WID-S2)', async () => {
    // Backend sends: {"error": {"code": "...", "message": "...", "details": {...}}}
    // NOT flat {"error": "...", "code": "...", "details": {...}}
    mockFetchResponse({
      ok: false,
      status: 409,
      body: {
        error: {
          code: 'reservation.seats_conflict',
          message: 'one or more requested seats are not available',
          request_id: 'req-abc-123',
          trace_id:   'trace-xyz',
          details: { conflicts: [{ seat_key: 'B01', status: 'held' }, { seat_key: 'B02', status: 'held' }] },
        },
      },
    });
    const err = (await postCheckoutStart('ft', payload).catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(409);
    expect(err.code).toBe('reservation.seats_conflict');
    expect(err.message).toContain('one or more requested seats are not available');
    expect(Array.isArray(err.details['conflicts'])).toBe(true);
    expect((err.details['conflicts'] as unknown[]).length).toBe(2);
  });
});
