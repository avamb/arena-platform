/**
 * poller.test.ts — unit tests for SeatStatusPoller.
 *
 * Tests use vitest fake timers and stubbed globals to avoid real network
 * requests and DOM dependencies.
 *
 * Strategy: advance timers by a fixed amount + flush microtasks via
 * multiple Promise.resolve() calls instead of runAllTimersAsync (which
 * would cause an infinite loop since the poller reschedules itself).
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { SeatStatusPoller } from './poller.js';
import type { SeatStatusValue } from '../types.js';

// ─── Helpers ──────────────────────────────────────────────────────────────────

function makeSeatStatusResponse(
  version: number,
  seats: Record<string, SeatStatusValue>,
  delta = false,
) {
  return {
    session_id: 'sess-1',
    status_version: version,
    seats,
    delta,
  };
}

function mockFetch(responses: object[]): void {
  let call = 0;
  vi.stubGlobal(
    'fetch',
    vi.fn(() => {
      const body = responses[call] ?? responses[responses.length - 1]!;
      call++;
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: 'OK',
        json: () => Promise.resolve(body),
        headers: { get: () => null },
      });
    }),
  );
}

function mockDocument(visibilityState: DocumentVisibilityState = 'visible'): void {
  vi.stubGlobal('document', {
    visibilityState,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  });
}

/** Flush a few rounds of microtasks so async tick() can complete. */
async function flushMicrotasks(rounds = 5): Promise<void> {
  for (let i = 0; i < rounds; i++) {
    await Promise.resolve();
  }
}

beforeEach(() => {
  vi.useFakeTimers();
  mockDocument('visible');
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

// ─── Tests ────────────────────────────────────────────────────────────────────

describe('SeatStatusPoller', () => {
  it('calls onUpdate with full snapshot on first tick', async () => {
    const seats = { 'P|1|1': 'available' as SeatStatusValue };
    mockFetch([makeSeatStatusResponse(1, seats)]);

    const updates: Array<Record<string, SeatStatusValue>> = [];
    const poller = new SeatStatusPoller({
      sessionId: 'sess-1',
      normalInterval: 3_000,
      onUpdate: (s) => updates.push(s),
    });

    poller.start();
    // Fire the immediate (0 ms) first tick.
    await vi.advanceTimersByTimeAsync(0);
    await flushMicrotasks();
    poller.stop();

    expect(updates.length).toBeGreaterThanOrEqual(1);
    expect(updates[0]).toEqual(seats);
  });

  it('passes version cursor to delta requests', async () => {
    const snapshot = makeSeatStatusResponse(5, { 'P|1|1': 'available' as SeatStatusValue });
    const delta = makeSeatStatusResponse(6, { 'P|1|2': 'held' as SeatStatusValue }, true);
    mockFetch([snapshot, delta]);

    const fetchMock = vi.mocked(globalThis.fetch as typeof fetch);

    const poller = new SeatStatusPoller({
      sessionId: 'sess-1',
      normalInterval: 100,
      onUpdate: () => undefined,
    });

    poller.start();
    // Tick 1 (immediate = snapshot).
    await vi.advanceTimersByTimeAsync(0);
    await flushMicrotasks();
    // Tick 2 (after 100 ms = delta).
    await vi.advanceTimersByTimeAsync(100);
    await flushMicrotasks();
    poller.stop();

    const calls = fetchMock.mock.calls;
    const deltaCall = calls.find(([url]) => (url as string).includes('since_version'));
    expect(deltaCall).toBeDefined();
    expect(deltaCall![0]).toContain('since_version=5');
  });

  it('calls onError when fetch fails (non-fatal)', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.reject(new Error('network error'))),
    );

    const errors: Error[] = [];
    const poller = new SeatStatusPoller({
      sessionId: 'sess-1',
      normalInterval: 100,
      onUpdate: () => undefined,
      onError: (e) => errors.push(e),
    });

    poller.start();
    await vi.advanceTimersByTimeAsync(0);
    await flushMicrotasks();
    poller.stop();

    expect(errors.length).toBeGreaterThanOrEqual(1);
    expect(errors[0]!.message).toContain('network error');
  });

  it('stop() prevents further ticks', async () => {
    const seats = { 'P|1|1': 'available' as SeatStatusValue };
    mockFetch(Array.from({ length: 20 }, () => makeSeatStatusResponse(1, seats)));

    let updateCount = 0;
    const poller = new SeatStatusPoller({
      sessionId: 'sess-1',
      normalInterval: 100,
      onUpdate: () => { updateCount++; },
    });

    poller.start();
    await vi.advanceTimersByTimeAsync(0);
    await flushMicrotasks();
    poller.stop();

    const countAfterStop = updateCount;
    // Advance time further — stop() should have cleared the timer.
    await vi.advanceTimersByTimeAsync(1_000);
    await flushMicrotasks();

    expect(updateCount).toBe(countAfterStop);
  });

  it('start() is idempotent (calling twice does not double-poll)', async () => {
    const seats = { 'P|1|1': 'available' as SeatStatusValue };
    mockFetch(Array.from({ length: 20 }, () => makeSeatStatusResponse(1, seats)));

    let updateCount = 0;
    const poller = new SeatStatusPoller({
      sessionId: 'sess-1',
      normalInterval: 100,
      onUpdate: () => { updateCount++; },
    });

    poller.start();
    poller.start(); // second call is a no-op
    await vi.advanceTimersByTimeAsync(0);
    await flushMicrotasks();
    poller.stop();

    // Should only poll once, not twice.
    expect(updateCount).toBe(1);
  });

  it('uses hiddenInterval when document.visibilityState is hidden', async () => {
    vi.stubGlobal('document', {
      visibilityState: 'hidden',
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });

    const seats = { 'P|1|1': 'available' as SeatStatusValue };
    mockFetch(Array.from({ length: 20 }, () => makeSeatStatusResponse(1, seats)));

    let updateCount = 0;
    const poller = new SeatStatusPoller({
      sessionId: 'sess-1',
      normalInterval: 1_000,
      hiddenInterval: 30_000,
      onUpdate: () => { updateCount++; },
    });

    poller.start();
    // First tick fires immediately.
    await vi.advanceTimersByTimeAsync(0);
    await flushMicrotasks();
    const countAfterFirst = updateCount;

    // After 15 000 ms the hidden interval (30 s) has not elapsed yet.
    await vi.advanceTimersByTimeAsync(15_000);
    await flushMicrotasks();
    poller.stop();

    // No second update should have fired (next tick is at 30 s).
    expect(updateCount).toBe(countAfterFirst);
  });

  it('removes visibilitychange listener on stop()', () => {
    const removeListener = vi.fn();
    vi.stubGlobal('document', {
      visibilityState: 'visible',
      addEventListener: vi.fn(),
      removeEventListener: removeListener,
    });
    mockFetch([makeSeatStatusResponse(1, {})]);

    const poller = new SeatStatusPoller({
      sessionId: 'sess-1',
      normalInterval: 100,
      onUpdate: () => undefined,
    });

    poller.start();
    poller.stop();

    expect(removeListener).toHaveBeenCalledWith('visibilitychange', expect.any(Function));
  });

  it('updates currentVersion after snapshot', async () => {
    const version7Seats = { 'P|1|1': 'sold' as SeatStatusValue };
    mockFetch([makeSeatStatusResponse(7, version7Seats)]);

    const versions: number[] = [];
    const poller = new SeatStatusPoller({
      sessionId: 'sess-1',
      normalInterval: 100,
      onUpdate: (_seats, version) => versions.push(version),
    });

    poller.start();
    await vi.advanceTimersByTimeAsync(0);
    await flushMicrotasks();
    poller.stop();

    expect(versions[0]).toBe(7);
  });
});
