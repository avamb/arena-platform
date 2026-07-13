/**
 * events.test.ts — Unit tests for the widget CustomEvent contract (WID-S5).
 *
 * Tests cover:
 *  1. ARENA_EVENTS constant — all five names present and correctly namespaced.
 *  2. dispatchWidgetEvent — dispatches a CustomEvent with the right name,
 *     bubbles:true, composed:true, and the supplied detail payload.
 *  3. Each of the five event types end-to-end.
 */

import { describe, it, expect, beforeEach } from 'vitest';
import {
  ARENA_EVENTS,
  dispatchWidgetEvent,
  type ArenaSeatSelectedDetail,
  type ArenaSeatReleasedDetail,
  type ArenaPaymentStartedDetail,
  type ArenaOrderPaidDetail,
  type ArenaOrderFailedDetail,
} from './events.js';

// ─── ARENA_EVENTS constant ────────────────────────────────────────────────────

describe('ARENA_EVENTS', () => {
  it('defines all five event names', () => {
    expect(ARENA_EVENTS.SEAT_SELECTED).toBe('arena:seat_selected');
    expect(ARENA_EVENTS.SEAT_RELEASED).toBe('arena:seat_released');
    expect(ARENA_EVENTS.PAYMENT_STARTED).toBe('arena:payment_started');
    expect(ARENA_EVENTS.ORDER_PAID).toBe('arena:order_paid');
    expect(ARENA_EVENTS.ORDER_FAILED).toBe('arena:order_failed');
  });

  it('all names are namespaced with "arena:" prefix', () => {
    for (const name of Object.values(ARENA_EVENTS)) {
      expect(name).toMatch(/^arena:/);
    }
  });

  it('has exactly five event names', () => {
    expect(Object.keys(ARENA_EVENTS)).toHaveLength(5);
  });
});

// ─── dispatchWidgetEvent helper ───────────────────────────────────────────────

describe('dispatchWidgetEvent', () => {
  let target: EventTarget;

  beforeEach(() => {
    target = new EventTarget();
  });

  it('dispatches a CustomEvent with the correct type', () => {
    let received: Event | null = null;
    target.addEventListener('arena:seat_selected', (e) => { received = e; });

    const detail: ArenaSeatSelectedDetail = { seatKey: 'A01', sessionId: 'sess-1' };
    dispatchWidgetEvent(target, ARENA_EVENTS.SEAT_SELECTED, detail);

    expect(received).not.toBeNull();
    expect((received as CustomEvent).type).toBe('arena:seat_selected');
  });

  it('attaches the detail payload', () => {
    let received: CustomEvent | null = null;
    target.addEventListener('arena:seat_selected', (e) => { received = e as CustomEvent; });

    const detail: ArenaSeatSelectedDetail = { seatKey: 'B05', sessionId: 'sess-abc' };
    dispatchWidgetEvent(target, ARENA_EVENTS.SEAT_SELECTED, detail);

    expect(received?.detail).toEqual({ seatKey: 'B05', sessionId: 'sess-abc' });
  });

  it('dispatches with bubbles:true', () => {
    let received: CustomEvent | null = null;
    target.addEventListener('arena:seat_released', (e) => { received = e as CustomEvent; });

    const detail: ArenaSeatReleasedDetail = { seatKey: 'C03', sessionId: 'sess-2' };
    dispatchWidgetEvent(target, ARENA_EVENTS.SEAT_RELEASED, detail);

    expect(received?.bubbles).toBe(true);
  });

  it('dispatches with composed:true', () => {
    let received: CustomEvent | null = null;
    target.addEventListener('arena:payment_started', (e) => { received = e as CustomEvent; });

    const detail: ArenaPaymentStartedDetail = {
      checkoutToken: 'ckout-token-1',
      sessionId: 'sess-3',
    };
    dispatchWidgetEvent(target, ARENA_EVENTS.PAYMENT_STARTED, detail);

    expect(received?.composed).toBe(true);
  });
});

// ─── Per-event end-to-end checks ──────────────────────────────────────────────

describe('arena:seat_selected', () => {
  it('carries seatKey and sessionId', () => {
    const target = new EventTarget();
    let detail: ArenaSeatSelectedDetail | null = null;
    target.addEventListener('arena:seat_selected', (e) => {
      detail = (e as CustomEvent<ArenaSeatSelectedDetail>).detail;
    });

    dispatchWidgetEvent(target, ARENA_EVENTS.SEAT_SELECTED, { seatKey: 'D12', sessionId: 's1' });

    expect(detail).toEqual({ seatKey: 'D12', sessionId: 's1' });
  });
});

describe('arena:seat_released', () => {
  it('carries seatKey and sessionId', () => {
    const target = new EventTarget();
    let detail: ArenaSeatReleasedDetail | null = null;
    target.addEventListener('arena:seat_released', (e) => {
      detail = (e as CustomEvent<ArenaSeatReleasedDetail>).detail;
    });

    dispatchWidgetEvent(target, ARENA_EVENTS.SEAT_RELEASED, { seatKey: 'E07', sessionId: 's2' });

    expect(detail).toEqual({ seatKey: 'E07', sessionId: 's2' });
  });
});

describe('arena:payment_started', () => {
  it('carries checkoutToken and sessionId', () => {
    const target = new EventTarget();
    let detail: ArenaPaymentStartedDetail | null = null;
    target.addEventListener('arena:payment_started', (e) => {
      detail = (e as CustomEvent<ArenaPaymentStartedDetail>).detail;
    });

    dispatchWidgetEvent(target, ARENA_EVENTS.PAYMENT_STARTED, {
      checkoutToken: 'tok-xyz',
      sessionId: 'sess-pay',
    });

    expect(detail).toEqual({ checkoutToken: 'tok-xyz', sessionId: 'sess-pay' });
  });
});

describe('arena:order_paid', () => {
  it('carries checkoutToken, orderRef, totalMinorUnits, currency', () => {
    const target = new EventTarget();
    let detail: ArenaOrderPaidDetail | null = null;
    target.addEventListener('arena:order_paid', (e) => {
      detail = (e as CustomEvent<ArenaOrderPaidDetail>).detail;
    });

    dispatchWidgetEvent(target, ARENA_EVENTS.ORDER_PAID, {
      checkoutToken: 'tok-paid',
      orderRef: 'ORD-99',
      totalMinorUnits: 4400,
      currency: 'EUR',
    });

    expect(detail).toEqual({
      checkoutToken: 'tok-paid',
      orderRef: 'ORD-99',
      totalMinorUnits: 4400,
      currency: 'EUR',
    });
  });

  it('accepts null orderRef and null totals', () => {
    const target = new EventTarget();
    let detail: ArenaOrderPaidDetail | null = null;
    target.addEventListener('arena:order_paid', (e) => {
      detail = (e as CustomEvent<ArenaOrderPaidDetail>).detail;
    });

    dispatchWidgetEvent(target, ARENA_EVENTS.ORDER_PAID, {
      checkoutToken: 'tok-paid-2',
      orderRef: null,
      totalMinorUnits: null,
      currency: null,
    });

    expect(detail?.orderRef).toBeNull();
    expect(detail?.totalMinorUnits).toBeNull();
    expect(detail?.currency).toBeNull();
  });
});

describe('arena:order_failed', () => {
  it('carries checkoutToken and reason "failed"', () => {
    const target = new EventTarget();
    let detail: ArenaOrderFailedDetail | null = null;
    target.addEventListener('arena:order_failed', (e) => {
      detail = (e as CustomEvent<ArenaOrderFailedDetail>).detail;
    });

    dispatchWidgetEvent(target, ARENA_EVENTS.ORDER_FAILED, {
      checkoutToken: 'tok-fail',
      reason: 'failed',
    });

    expect(detail).toEqual({ checkoutToken: 'tok-fail', reason: 'failed' });
  });

  it('carries reason "expired" with null checkoutToken', () => {
    const target = new EventTarget();
    let detail: ArenaOrderFailedDetail | null = null;
    target.addEventListener('arena:order_failed', (e) => {
      detail = (e as CustomEvent<ArenaOrderFailedDetail>).detail;
    });

    dispatchWidgetEvent(target, ARENA_EVENTS.ORDER_FAILED, {
      checkoutToken: null,
      reason: 'expired',
    });

    expect(detail?.checkoutToken).toBeNull();
    expect(detail?.reason).toBe('expired');
  });
});

// ─── Multiple listeners on the same target ────────────────────────────────────

describe('dispatchWidgetEvent — multiple listeners', () => {
  it('notifies all listeners in order', () => {
    const target = new EventTarget();
    const calls: number[] = [];

    target.addEventListener('arena:seat_selected', () => calls.push(1));
    target.addEventListener('arena:seat_selected', () => calls.push(2));
    target.addEventListener('arena:seat_selected', () => calls.push(3));

    dispatchWidgetEvent(target, ARENA_EVENTS.SEAT_SELECTED, {
      seatKey: 'F01',
      sessionId: 'sess-multi',
    });

    expect(calls).toEqual([1, 2, 3]);
  });
});

// ─── Event does not fire on unrelated listeners ───────────────────────────────

describe('dispatchWidgetEvent — no cross-event leakage', () => {
  it('seat_selected does not trigger seat_released listener', () => {
    const target = new EventTarget();
    let released = false;
    target.addEventListener('arena:seat_released', () => { released = true; });

    dispatchWidgetEvent(target, ARENA_EVENTS.SEAT_SELECTED, {
      seatKey: 'G02',
      sessionId: 'sess-x',
    });

    expect(released).toBe(false);
  });
});
