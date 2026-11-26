/**
 * holds.ts: one Idempotency-Key per click, requests one at a time, the hold key
 * in storage, the clock and the Extend window, the nearest free seats.
 */

import { describe, expect, it } from 'vitest';
import { ApiError } from '../api.js';
import {
  EXTEND_WINDOW_SECONDS,
  createHoldController,
  holdFeatureOff,
  deviceStorageKey,
  extendMinutes,
  extendVisible,
  getDeviceId,
  holdSecondsLeft,
  holdStorageKey,
  nearestSeats,
  conflictKeysOf,
  type HoldControllerDeps,
  type HoldRequest,
  type HoldResponse,
  type HoldView,
  type KeyValueStorage,
  type SeatSpot,
} from './holds.js';

function memoryStorage(): KeyValueStorage & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return {
    data,
    getItem: (k) => data.get(k) ?? null,
    setItem: (k, v) => void data.set(k, v),
    removeItem: (k) => void data.delete(k),
  };
}

const T0 = Date.parse('2026-11-25T10:00:00Z');
const iso = (offsetSec: number): string => new Date(T0 + offsetSec * 1000).toISOString();

function hold(seats: string[], extra: Partial<HoldResponse> = {}): HoldResponse {
  return { hold_token: 'tok-1', session_id: 's1', seat_keys: seats, step: 'select', expires_at: iso(600), extend_available: false, ...extra };
}

interface Rig {
  deps: HoldControllerDeps;
  storage: ReturnType<typeof memoryStorage>;
  views: HoldView[];
  posts: Array<{ body: HoldRequest; key: string }>;
  events: { taken: string[][]; partial: string[][]; rejected: Array<{ code: string; details: Record<string, unknown> }>; extended: number[] };
}

function rig(over: Partial<HoldControllerDeps> = {}): Rig {
  const storage = memoryStorage();
  const views: HoldView[] = [];
  const posts: Rig['posts'] = [];
  const events: Rig['events'] = { taken: [], partial: [], rejected: [], extended: [] };
  let n = 0;
  const deps: HoldControllerDeps = {
    sessionId: 's1',
    deviceId: 'dev-1',
    storage,
    post: async (body, key) => {
      posts.push({ body, key });
      return hold([...(body.hold_token ? ['A-1'] : []), ...body.seat_keys]);
    },
    del: async () => hold([]),
    get: async () => hold(['A-1']),
    extend: async () => hold(['A-1'], { expires_at: iso(900) }),
    onChange: (v) => views.push(v),
    onSeatTaken: (k) => events.taken.push(k),
    onPartial: (k) => events.partial.push(k),
    onRejected: (code, details) => events.rejected.push({ code, details }),
    onExtended: (m) => events.extended.push(m),
    newIdempotencyKey: () => `key-${++n}`,
    now: () => T0,
    retryDelayMs: 0,
    ...over,
  };
  return { deps, storage, views, posts, events };
}

describe('storage keys and the device id', () => {
  it('names the keys as the spec does', () => {
    expect(holdStorageKey('sess-1')).toBe('arena_hold:sess-1');
    expect(deviceStorageKey('feed-1')).toBe('arena_device:feed-1');
  });

  it('makes a UUID once per feed token and keeps it', () => {
    const st = memoryStorage();
    const a = getDeviceId('feed-x', st, () => '11111111-2222-4333-8444-555555555555');
    expect(a).toBe('11111111-2222-4333-8444-555555555555');
    expect(st.data.get('arena_device:feed-x')).toBe(a);
    expect(getDeviceId('feed-x', st, () => 'other')).toBe(a);
    expect(getDeviceId('feed-y', st, () => '99999999-2222-4333-8444-555555555555')).not.toBe(a);
  });

  it('survives a storage that refuses', () => {
    const broken: KeyValueStorage = {
      getItem: () => {
        throw new Error('no');
      },
      setItem: () => {
        throw new Error('no');
      },
      removeItem: () => {
        throw new Error('no');
      },
    };
    const id = getDeviceId('feed-broken', broken, () => 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee');
    expect(id).toBe('aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee');
    expect(getDeviceId('feed-broken', broken, () => 'zzz')).toBe(id);
  });
});

describe('one key per click, requests one at a time', () => {
  it('draws a clicked seat as pending at once and as held after the answer', async () => {
    const r = rig();
    const c = createHoldController(r.deps);
    expect(c.tap('A-2')).toBe('add');
    expect(c.view.pending.has('A-2')).toBe(true);
    expect(c.view.held.size).toBe(0);
    await c.idle();
    expect(c.view.pending.size).toBe(0);
    expect([...c.view.held]).toEqual(['A-2']);
    expect(c.view.holdToken).toBe('tok-1');
    expect(r.storage.data.get('arena_hold:s1')).toBe('tok-1');
  });

  it('gives each click its own key and sends the device id', async () => {
    const r = rig();
    const c = createHoldController(r.deps);
    c.tap('A-2');
    c.tap('A-3');
    await c.idle();
    expect(r.posts.map((p) => p.key)).toEqual(['key-1', 'key-2']);
    expect(r.posts[0]!.body).toEqual({ session_id: 's1', seat_keys: ['A-2'], device_id: 'dev-1' });
  });

  it('holds the second click back until the first answer brings the hold_token', async () => {
    let release!: (r: HoldResponse) => void;
    let calls = 0;
    const r = rig({
      post: (body, key) => {
        r.posts.push({ body, key });
        calls += 1;
        if (calls === 1) return new Promise<HoldResponse>((res) => (release = res));
        return Promise.resolve(hold(['A-2', 'A-3']));
      },
    });
    const c = createHoldController(r.deps);
    c.tap('A-2');
    c.tap('A-3');
    await Promise.resolve();
    await Promise.resolve();
    // The second request has not gone out: there is no hold_token yet.
    expect(r.posts).toHaveLength(1);
    expect(r.posts[0]!.body.hold_token).toBeUndefined();
    release(hold(['A-2']));
    await c.idle();
    expect(r.posts).toHaveLength(2);
    // It carries the key the first answer brought, so it joins the SAME hold.
    expect(r.posts[1]!.body.hold_token).toBe('tok-1');
    expect([...c.view.held].sort()).toEqual(['A-2', 'A-3']);
  });

  it('sends a request that got no answer once more with the SAME key', async () => {
    let n = 0;
    const r = rig({
      post: async (body, key) => {
        r.posts.push({ body, key });
        n += 1;
        if (n === 1) throw new TypeError('network');
        return hold(['A-2']);
      },
    });
    const c = createHoldController(r.deps);
    c.tap('A-2');
    await c.idle();
    expect(r.posts.map((p) => p.key)).toEqual(['key-1', 'key-1']);
    expect([...c.view.held]).toEqual(['A-2']);
  });

  it('does not repeat a refusal', async () => {
    const r = rig({
      post: async (body, key) => {
        r.posts.push({ body, key });
        throw new ApiError('no', { status: 422, code: 'hold.too_many_seats' });
      },
    });
    const c = createHoldController(r.deps);
    c.tap('A-2');
    await c.idle();
    expect(r.posts).toHaveLength(1);
    expect(r.events.rejected.map((e) => e.code)).toEqual(['hold.too_many_seats']);
    expect(c.view.pending.size).toBe(0);
    expect(c.view.held.size).toBe(0);
  });

  it('reports a taken seat with the conflicts of the answer', async () => {
    const r = rig({
      post: async () => {
        throw new ApiError('taken', { status: 409, code: 'hold.seats_taken', details: { conflicts: [{ seat_key: 'A-2', status: 'sold' }] } });
      },
    });
    const c = createHoldController(r.deps);
    c.tap('A-2');
    await c.idle();
    expect(r.events.taken).toEqual([['A-2']]);
    expect(c.view.pending.size).toBe(0);
    expect(r.events.rejected).toEqual([]);
  });

  it('reports the seats a partial answer could not hold', async () => {
    const r = rig({
      post: async () => hold(['A-2'], { conflicts: [{ seat_key: 'A-3', status: 'held' }] }),
    });
    const c = createHoldController(r.deps);
    c.tap('A-2');
    await c.idle();
    expect(r.events.taken).toEqual([['A-3']]);
    expect([...c.view.held]).toEqual(['A-2']);
  });

  it('reads the conflicts from either place', () => {
    expect(conflictKeysOf({ conflicts: [{ seat_key: 'a' }] })).toEqual(['a']);
    expect(conflictKeysOf({ details: { conflicts: [{ seat_key: 'b' }, { nope: 1 }] } })).toEqual(['b']);
    expect(conflictKeysOf(undefined)).toEqual([]);
  });
});

describe('taking an own seat off', () => {
  it('frees it at once and asks the platform in order', async () => {
    const dels: string[] = [];
    const r = rig({
      post: async (body, key) => {
        r.posts.push({ body, key });
        return hold(['A-2', 'A-3']);
      },
      del: async (_t, seat) => {
        dels.push(seat);
        return hold(['A-3']);
      },
    });
    const c = createHoldController(r.deps);
    c.tap('A-2');
    await c.idle();
    expect(c.tap('A-2')).toBe('remove');
    expect(c.view.held.has('A-2')).toBe(false);
    await c.idle();
    expect(dels).toEqual(['A-2']);
  });

  it('closes the hold with its last seat and forgets the key', async () => {
    const r = rig();
    const c = createHoldController(r.deps);
    c.tap('A-2');
    await c.idle();
    c.tap('A-2');
    await c.idle();
    expect(c.view.holdToken).toBeNull();
    expect(r.storage.data.has('arena_hold:s1')).toBe(false);
  });

  it('puts the seat back when the platform refuses (hold.in_payment)', async () => {
    const r = rig({
      del: async () => {
        throw new ApiError('in payment', { status: 409, code: 'hold.in_payment' });
      },
    });
    const c = createHoldController(r.deps);
    c.tap('A-2');
    await c.idle();
    c.tap('A-2');
    await c.idle();
    expect(c.view.held.has('A-2')).toBe(true);
    expect(r.events.rejected.map((e) => e.code)).toEqual(['hold.in_payment']);
  });

  it('lets the sheet cancel a prepared order before the request (beforeMutate)', async () => {
    const order: string[] = [];
    const r = rig({
      beforeMutate: async () => {
        order.push('cancel');
      },
      post: async (body) => {
        order.push('post');
        return hold(body.seat_keys);
      },
    });
    const c = createHoldController(r.deps);
    c.tap('A-2');
    await c.idle();
    expect(order).toEqual(['cancel', 'post']);
  });
});

describe('restore, expiry, extend, hold again', () => {
  it('restores the selection and the clock from the stored key', async () => {
    const r = rig();
    r.storage.setItem('arena_hold:s1', 'tok-1');
    const c = createHoldController(r.deps);
    await c.restore();
    expect([...c.view.held]).toEqual(['A-1']);
    expect(c.view.expiresAt).toBe(iso(600));
  });

  it('forgets a stored key the platform no longer knows', async () => {
    const r = rig({
      get: async () => {
        throw new ApiError('gone', { status: 404, code: 'hold.not_found' });
      },
    });
    r.storage.setItem('arena_hold:s1', 'tok-old');
    const c = createHoldController(r.deps);
    await c.restore();
    expect(c.view.held.size).toBe(0);
    expect(r.storage.data.has('arena_hold:s1')).toBe(false);
  });

  it('keeps the key when the restore got no answer', async () => {
    const r = rig({
      get: async () => {
        throw new TypeError('network');
      },
    });
    r.storage.setItem('arena_hold:s1', 'tok-1');
    const c = createHoldController(r.deps);
    await c.restore();
    expect(r.storage.data.get('arena_hold:s1')).toBe('tok-1');
  });

  it('offers "hold again" once the platform says the hold is over, and sends rehold_of', async () => {
    let expired = false;
    const r = rig({
      get: async () => {
        if (expired) throw new ApiError('over', { status: 409, code: 'hold.expired' });
        return hold(['A-2', 'A-3']);
      },
      post: async (body, key) => {
        r.posts.push({ body, key });
        return hold(['A-2'], { hold_token: 'tok-2', conflicts: [{ seat_key: 'A-3', status: 'sold' }] });
      },
    });
    r.storage.setItem('arena_hold:s1', 'tok-1');
    const c = createHoldController(r.deps);
    await c.restore();
    expired = true;
    await c.checkExpiry();
    expect(c.view.expired).toBe(true);
    expect(c.view.held.size).toBe(0);
    expect(r.storage.data.has('arena_hold:s1')).toBe(false);
    await c.rehold();
    expect(r.posts[0]!.body).toEqual({ session_id: 's1', seat_keys: ['A-2', 'A-3'], device_id: 'dev-1', rehold_of: 'tok-1' });
    expect(c.view.expired).toBe(false);
    expect(c.view.holdToken).toBe('tok-2');
    expect(r.events.partial).toEqual([['A-3']]);
  });

  it('treats a hold whose clock ran out as over even if the sweep has not run', async () => {
    const r = rig({ get: async () => hold(['A-2'], { expires_at: iso(900) }), now: () => T0 });
    r.storage.setItem('arena_hold:s1', 'tok-1');
    const c = createHoldController(r.deps);
    await c.restore();
    r.deps.get = async () => hold(['A-2'], { expires_at: iso(-1) });
    await c.checkExpiry();
    expect(c.view.expired).toBe(true);
  });

  it('extends and says how many minutes', async () => {
    const r = rig({
      post: async (body) => hold(body.seat_keys, { extend_available: true, policy: { select_seconds: 600, payment_seconds: 900, extend_seconds: 300 } }),
      extend: async () => hold(['A-2'], { expires_at: iso(900), extend_available: false, policy: { select_seconds: 600, payment_seconds: 900, extend_seconds: 300 } }),
    });
    const c = createHoldController(r.deps);
    c.tap('A-2');
    await c.idle();
    expect(c.view.extendAvailable).toBe(true);
    await c.extend();
    expect(c.view.expiresAt).toBe(iso(900));
    expect(c.view.extendAvailable).toBe(false);
    expect(r.events.extended).toEqual([5]);
  });

  it('takes the expiry the order sheet brought', async () => {
    const r = rig();
    const c = createHoldController(r.deps);
    c.tap('A-2');
    await c.idle();
    c.noteExpiry(iso(1500), 'payment');
    expect(c.view.expiresAt).toBe(iso(1500));
    expect(c.view.step).toBe('payment');
  });

  it('forgets everything when the order is paid', async () => {
    const r = rig();
    const c = createHoldController(r.deps);
    c.tap('A-2');
    await c.idle();
    c.forget();
    expect(c.view.holdToken).toBeNull();
    expect(c.view.held.size).toBe(0);
    expect(r.storage.data.has('arena_hold:s1')).toBe(false);
  });
});

describe('the clock and the Extend window', () => {
  it('counts down to expires_at', () => {
    expect(holdSecondsLeft(iso(125), T0)).toBe(125);
    expect(holdSecondsLeft(iso(-5), T0)).toBe(0);
    expect(holdSecondsLeft(null, T0)).toBe(0);
  });

  it('shows Extend from 60 s to the end, only when an extension is left', () => {
    expect(EXTEND_WINDOW_SECONDS).toBe(60);
    expect(extendVisible(iso(61), true, T0)).toBe(false);
    expect(extendVisible(iso(60), true, T0)).toBe(true);
    expect(extendVisible(iso(1), true, T0)).toBe(true);
    expect(extendVisible(iso(0), true, T0)).toBe(false);
    expect(extendVisible(iso(30), false, T0)).toBe(false);
    expect(extendVisible(null, true, T0)).toBe(false);
  });

  it('reads the extension minutes from the policy', () => {
    expect(extendMinutes({ select_seconds: 600, payment_seconds: 900, extend_seconds: 300 })).toBe(5);
    expect(extendMinutes({ select_seconds: 600, payment_seconds: 900, extend_seconds: 90 })).toBe(2);
    expect(extendMinutes(undefined)).toBe(5);
  });
});

describe('nearest free seats', () => {
  const spot = (key: string, x: number, y: number, over: Partial<SeatSpot> = {}): SeatSpot => ({
    key,
    x,
    y,
    rowKey: 'r1',
    tierId: 't1',
    price: 5000,
    status: 'available',
    ...over,
  });

  it('takes up to 4 free seats of the same tier and price, the same row first, then by distance', () => {
    const target = spot('T', 100, 100);
    const all = [
      target,
      spot('far-same-row', 400, 100),
      spot('near-other-row', 100, 124, { rowKey: 'r2' }),
      spot('same-row-1', 124, 100),
      spot('same-row-2', 148, 100),
      spot('same-row-3', 76, 100),
      spot('other-tier', 110, 100, { tierId: 't2' }),
      spot('other-price', 110, 100, { price: 7000 }),
      spot('taken', 112, 100, { status: 'held' }),
      spot('sold', 112, 100, { status: 'sold' }),
    ];
    expect(nearestSeats(target, all)).toEqual(['same-row-1', 'same-row-3', 'same-row-2', 'far-same-row']);
  });

  it('falls to other rows when the row has fewer than 4', () => {
    const target = spot('T', 0, 0);
    const all = [target, spot('a', 10, 0), spot('b', 0, 30, { rowKey: 'r2' }), spot('c', 0, 20, { rowKey: 'r2' })];
    expect(nearestSeats(target, all)).toEqual(['a', 'c', 'b']);
  });

  it('returns nothing when no seat fits, and never the target itself', () => {
    const target = spot('T', 0, 0);
    expect(nearestSeats(target, [target, spot('x', 1, 1, { price: 1 })])).toEqual([]);
    expect(nearestSeats(target, [])).toEqual([]);
  });

  it('honours the limit', () => {
    const target = spot('T', 0, 0);
    const all = [target, ...Array.from({ length: 10 }, (_, i) => spot(`s${i}`, i + 1, 0))];
    expect(nearestSeats(target, all, 2)).toEqual(['s0', 's1']);
  });
});

describe('the feature is off on the platform (hold.disabled)', () => {
  it('falls back silently: no error, the clicked seats are handed over, later taps are ignored', async () => {
    for (const status of [404, 503]) {
      const r = rig({
        post: async (body, key) => {
          r.posts.push({ body, key });
          throw new ApiError('off', { status, code: 'hold.disabled' });
        },
      });
      const handed: string[][] = [];
      r.deps.onDisabled = (k) => handed.push(k);
      const c = createHoldController(r.deps);
      c.tap('A-2');
      c.tap('A-3');
      await c.idle();
      // Sent once (no retry of a refusal), the second click never went out.
      expect(r.posts).toHaveLength(1);
      expect(handed).toEqual([['A-2', 'A-3']]);
      expect(r.events.rejected).toEqual([]);
      expect(r.events.taken).toEqual([]);
      expect(c.view.pending.size).toBe(0);
      expect(c.view.holdToken).toBeNull();
      expect(c.tap('A-4')).toBe('ignored');
    }
  });

  it('treats a platform without the hold routes (404/405, not a hold code) as off', async () => {
    for (const [status, code] of [
      [404, 'http.not_found'],
      [404, undefined],
      [405, 'http.method_not_allowed'],
    ] as const) {
      const r = rig({
        post: async (body, key) => {
          r.posts.push({ body, key });
          throw new ApiError('no route', { status, code });
        },
      });
      const handed: string[][] = [];
      r.deps.onDisabled = (k) => handed.push(k);
      const c = createHoldController(r.deps);
      c.tap('B-1');
      await c.idle();
      expect(handed).toEqual([['B-1']]);
      expect(r.events.rejected).toEqual([]);
    }
  });

  it('keeps an existing hold: the click is refused with hold.disabled, the feature stays on', async () => {
    let off = false;
    const r = rig({
      post: async (body, key) => {
        r.posts.push({ body, key });
        if (off) throw new ApiError('off', { status: 503, code: 'hold.disabled' });
        return hold([...(body.hold_token ? ['A-1'] : []), ...body.seat_keys]);
      },
    });
    const handed: string[][] = [];
    r.deps.onDisabled = (k) => handed.push(k);
    const c = createHoldController(r.deps);
    c.tap('A-1');
    await c.idle();
    expect(c.view.holdToken).toBe('tok-1');
    off = true;
    c.tap('A-2');
    await c.idle();
    expect(handed).toEqual([]);
    expect(r.events.rejected.map((e) => e.code)).toEqual(['hold.disabled']);
    expect(c.view.pending.has('A-2')).toBe(false);
    expect(c.view.holdToken).toBe('tok-1');
    expect([...c.view.held]).toEqual(['A-1']);
    expect(r.storage.data.get(holdStorageKey('s1'))).toBe('tok-1');
    // Not disabled: a later click is still sent.
    expect(c.tap('A-3')).toBe('add');
    await c.idle();
    expect(r.posts).toHaveLength(3);
  });

  it('a 404 with a hold code is not a disabled feature', () => {
    expect(holdFeatureOff(new ApiError('x', { status: 404, code: 'hold.expired' }))).toBe(false);
    expect(holdFeatureOff(new ApiError('x', { status: 409, code: 'hold.seats_taken' }))).toBe(false);
    expect(holdFeatureOff(new ApiError('x', { status: 503, code: 'hold.disabled' }))).toBe(true);
    expect(holdFeatureOff(new Error('network'))).toBe(false);
  });
});
