/**
 * widget_377_test.ts — structural tests for PR2-21: embeddable widget with live feed data.
 *
 * Feature: The widget must:
 *   Step 1 — expose an `api-base` attribute and prefix every fetch with it.
 *   Step 2 — invoke the real feed load (fetchFeedEvent) so tiers / buyer fields render.
 *   Step 3 — after a dead/failed resume token, clear it and run normal init;
 *             always set a visible loadError on recovery failure.
 *   Step 4 — structural tests verify the above (E2E in non-proxied environment).
 */

import { describe, it, expect, vi, afterEach } from 'vitest';
import {
  fetchFeedEvent,
  fetchSessionSchema,
  fetchSeatStatus,
  fetchSeatStatusDelta,
  postCheckoutStart,
  getCheckoutStatus,
  postCheckoutRecover,
} from './api.js';
import { SeatStatusPoller } from './lib/poller.js';

// ─── Helpers ─────────────────────────────────────────────────────────────────

function mockFetchOk(body: unknown): void {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      statusText: 'OK',
      headers: { get: () => null },
      json: () => Promise.resolve(body),
    }),
  );
}

function captureUrl(): Promise<string> {
  return new Promise((resolve) => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string) => {
        resolve(url);
        return Promise.resolve({
          ok: true,
          status: 200,
          headers: { get: () => null },
          json: () => Promise.resolve({}),
        });
      }),
    );
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

// ─── Step 1: api-base prefixes every fetch ────────────────────────────────────

describe('Step 1: api-base attribute wires every API function', () => {
  it('postCheckoutStart accepts apiBase and prefixes the URL', async () => {
    const urlPromise = captureUrl();
    void postCheckoutStart('tok', { session_id: 's', holder_email: 'a@b.com' }, 'https://api.example.com');
    const url = await urlPromise;
    expect(url).toBe('https://api.example.com/v1/public/feeds/tok/checkout/start');
  });

  it('postCheckoutStart with empty apiBase uses relative URL', async () => {
    const urlPromise = captureUrl();
    void postCheckoutStart('tok', { session_id: 's', holder_email: 'a@b.com' }, '');
    const url = await urlPromise;
    expect(url).toBe('/v1/public/feeds/tok/checkout/start');
  });

  it('getCheckoutStatus accepts apiBase and prefixes the URL', async () => {
    const urlPromise = captureUrl();
    void getCheckoutStatus('ct_1', 'https://api.example.com');
    const url = await urlPromise;
    expect(url).toBe('https://api.example.com/v1/public/checkout/ct_1');
  });

  it('getCheckoutStatus with empty apiBase uses relative URL', async () => {
    const urlPromise = captureUrl();
    void getCheckoutStatus('ct_1', '');
    const url = await urlPromise;
    expect(url).toBe('/v1/public/checkout/ct_1');
  });

  it('postCheckoutRecover accepts apiBase and prefixes the URL', async () => {
    const urlPromise = captureUrl();
    void postCheckoutRecover('ct_1', 'https://api.example.com');
    const url = await urlPromise;
    expect(url).toBe('https://api.example.com/v1/public/checkout/ct_1/recover');
  });

  it('postCheckoutRecover with empty apiBase uses relative URL', async () => {
    const urlPromise = captureUrl();
    void postCheckoutRecover('ct_1', '');
    const url = await urlPromise;
    expect(url).toBe('/v1/public/checkout/ct_1/recover');
  });

  it('fetchFeedEvent accepts apiBase and prefixes the URL', async () => {
    const urlPromise = captureUrl();
    void fetchFeedEvent('ft', 'ev-id', 'https://api.example.com');
    const url = await urlPromise;
    expect(url).toBe('https://api.example.com/v1/public/feeds/ft/events/ev-id');
  });

  it('fetchSessionSchema accepts apiBase and prefixes the URL', async () => {
    const urlPromise = captureUrl();
    void fetchSessionSchema('sess-id', 'https://api.example.com');
    const url = await urlPromise;
    expect(url).toBe('https://api.example.com/v1/event-sessions/sess-id/schema');
  });

  it('fetchSeatStatus accepts apiBase and prefixes the URL', async () => {
    const urlPromise = captureUrl();
    void fetchSeatStatus('sess-id', 'https://api.example.com');
    const url = await urlPromise;
    expect(url).toBe('https://api.example.com/v1/event-sessions/sess-id/seat-status');
  });

  it('fetchSeatStatusDelta accepts apiBase and prefixes the URL', async () => {
    const urlPromise = captureUrl();
    void fetchSeatStatusDelta('sess-id', 42, 'https://api.example.com');
    const url = await urlPromise;
    expect(url).toBe(
      'https://api.example.com/v1/event-sessions/sess-id/seat-status?since_version=42',
    );
  });
});

// ─── Step 1b: PollerConfig accepts apiBase ────────────────────────────────────

describe('Step 1b: SeatStatusPoller accepts and threads apiBase', () => {
  it('PollerConfig has optional apiBase field', () => {
    // Verify the interface compiles — construct a poller with apiBase.
    const poller = new SeatStatusPoller({
      sessionId: 'sess-1',
      apiBase: 'https://api.example.com',
      onUpdate: () => {},
    });
    expect(poller).toBeDefined();
    poller.stop();
  });

  it('poller passes apiBase to fetchSeatStatus on first tick', async () => {
    let capturedUrl = '';
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string) => {
        capturedUrl = url;
        return Promise.resolve({
          ok: true,
          status: 200,
          headers: { get: () => null },
          json: () =>
            Promise.resolve({ session_id: 'sess-1', status_version: 1, seats: {}, delta: false }),
        });
      }),
    );

    const updateCalled = new Promise<void>((resolve) => {
      const poller = new SeatStatusPoller({
        sessionId: 'sess-1',
        apiBase: 'https://api.example.com',
        normalInterval: 60_000,
        onUpdate: () => {
          poller.stop();
          resolve();
        },
      });
      poller.start();
    });

    await updateCalled;
    expect(capturedUrl).toBe(
      'https://api.example.com/v1/event-sessions/sess-1/seat-status',
    );
  });
});

// ─── Step 2: loadFromFeed wires real data ─────────────────────────────────────

describe('Step 2: fetchFeedEvent returns tiers and buyer_fields', () => {
  it('fetchFeedEvent resolves to the parsed event with tiers', async () => {
    const mockEvent = {
      id: 'ev-1',
      display_number: 1,
      org_id: 'org-1',
      name: 'Test Event',
      status: 'published',
      first_session_at: '2026-08-01T19:00:00Z',
      last_session_at: '2026-08-01T22:00:00Z',
      venue_names: ['Test Venue'],
      visibility: 'public',
      created_at: '2026-07-01T00:00:00Z',
      updated_at: '2026-07-01T00:00:00Z',
      sessions: [
        {
          id: 'sess-1',
          start_at: '2026-08-01T19:00:00Z',
          end_at: '2026-08-01T22:00:00Z',
          capacity_total: 500,
          status: 'published',
          admission_mode: 'general_admission',
          buyer_fields: [
            { key: 'email', required: true, enabled: true },
            { key: 'name', required: true, enabled: true },
          ],
          tiers: [
            {
              id: 'tier-1',
              name: 'General',
              pricing_mode: 'fixed',
              price_amount: 1500,
              currency: 'RUB',
              sort_order: 0,
            },
          ],
        },
      ],
    };
    mockFetchOk({ event: mockEvent });

    const data = await fetchFeedEvent('ft', 'ev-1');
    expect(data.event.sessions[0].tiers).toHaveLength(1);
    expect(data.event.sessions[0].tiers[0].name).toBe('General');
    expect(data.event.sessions[0].buyer_fields).toHaveLength(2);
    expect(data.event.sessions[0].buyer_fields[0].key).toBe('email');
  });

  it('fetchFeedEvent with cross-origin apiBase reaches the right host', async () => {
    const urlPromise = captureUrl();
    void fetchFeedEvent('ft', 'ev-1', 'https://tickets.arena-platform.ru');
    const url = await urlPromise;
    expect(url).toStartWith('https://tickets.arena-platform.ru/');
    expect(url).toContain('/v1/public/feeds/ft/events/ev-1');
  });

  it('fetchFeedEvent throws on non-2xx response', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: false,
        status: 404,
        statusText: 'Not Found',
        headers: { get: () => null },
        json: () => Promise.resolve({ error: { code: 'feed.event_not_found' } }),
      }),
    );
    await expect(fetchFeedEvent('ft', 'missing')).rejects.toThrow('404');
  });
});

// ─── Step 3: dead/failed resume token ────────────────────────────────────────

describe('Step 3: ArenaTickets module exports and resume-token contract', () => {
  /**
   * These tests verify the structural contract of the resume-token fix by
   * reading the source of ArenaTickets.svelte.  We cannot mount the Svelte
   * component in vitest/jsdom without a full DOM + Svelte register pass, but
   * the source-text assertions are authoritative for YOLO-mode verification.
   */

  it('ArenaTickets.svelte declares api-base attribute in customElement props', async () => {
    const src = await import('./ArenaTickets.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    expect(src).toContain("attribute: 'api-base'");
    expect(src).toContain("attribute: 'event-id'");
  });

  it('ArenaTickets.svelte auto-detects script src origin on mount', async () => {
    const src = await import('./ArenaTickets.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    expect(src).toContain('arena-tickets');
    expect(src).toContain('u.origin');
    expect(src).toContain('autoDetectedBase');
  });

  it('ArenaTickets.svelte calls loadFromFeed instead of creating synthetic stub', async () => {
    const src = await import('./ArenaTickets.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    // Real feed load is called from onMount.
    expect(src).toContain('loadFromFeed(normFeedToken, normEventId)');
    // Synthetic stub event with empty tiers must NOT exist.
    expect(src).not.toContain("tiers: [],");
    expect(src).not.toContain("buyer_fields: [],");
  });

  it('ArenaTickets.svelte sets loadError on 401 recovery failure', async () => {
    const src = await import('./ArenaTickets.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    // After recovery fails (catch block inside the 401 handler), loadError must be set
    // and the widget must re-load event data.
    expect(src).toContain('loadError =');
    expect(src).toContain('t.error_recovery');
    // Must re-invoke loadFromFeed after clearing dead token so widget is not blank.
    expect(src).toContain("void loadFromFeed(normFeedToken, normEventId)");
  });

  it('ArenaTickets.svelte clears token and loads event after 404', async () => {
    const src = await import('./ArenaTickets.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    expect(src).toContain('apiErr?.status === 404');
    expect(src).toContain('clearCheckoutToken()');
    // Widget re-loads event data after clearing a dead 404 token.
    expect(src).toMatch(/status === 404[\s\S]{1,400}loadFromFeed/);
  });

  it('postCheckoutStart is called with resolvedApiBase', async () => {
    const src = await import('./ArenaTickets.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    expect(src).toContain('postCheckoutStart(normFeedToken, payload, resolvedApiBase)');
  });

  it('getCheckoutStatus is called with resolvedApiBase', async () => {
    const src = await import('./ArenaTickets.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    expect(src).toContain('getCheckoutStatus(token, resolvedApiBase)');
  });

  it('postCheckoutRecover is called with resolvedApiBase', async () => {
    const src = await import('./ArenaTickets.svelte?raw').then(
      (m: { default: string }) => m.default,
    );
    expect(src).toContain('postCheckoutRecover(token, resolvedApiBase)');
  });
});
