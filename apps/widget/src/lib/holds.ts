/**
 * holds.ts — a seat is held at the moment of the click (spec 67, §4 and §8).
 *
 * On a seated session the widget does not wait for "Pay": the first click on a
 * free seat opens a hold (POST .../holds), the next clicks add to the same hold,
 * a click on an own seat takes it off. The hold has its own key (`hold_token`),
 * kept in localStorage as `arena_hold:<session_id>`, and its own clock
 * (`expires_at`). The order sheet then starts the checkout with the key instead
 * of the seat list.
 *
 * Rules kept here, with no DOM and no Svelte so tests can drive them:
 *  - every click has ONE Idempotency-Key; a request that failed without an
 *    answer (network, 5xx) is sent once more with the SAME key;
 *  - hold requests go ONE AT A TIME, in the order of the clicks: until the first
 *    answer brings the `hold_token`, a second click waits, otherwise two quick
 *    clicks would open two holds. (Later clicks are queued the same way: an add
 *    and a take-off of one seat must not overtake each other.)
 *  - the seats shown as "held by you" come from the hold's own answer, not from
 *    the seat-status poll (which reports them as somebody's hold).
 *
 * TODO(spec 67): the types of the new routes below are written by hand. Replace
 * them with the generated client types once the OpenAPI client is regenerated
 * for spec 67 (the backend is built in parallel).
 */

import { ApiError, apiErrorFromResponse } from '../api.js';
import { countdownSeconds } from './cart.js';

// ─── Types of the routes (hand-written for now, see the TODO above) ──────────

export interface HoldPolicy {
  select_seconds: number;
  payment_seconds: number;
  extend_seconds: number;
}

export interface HoldConflict {
  seat_key: string;
  status?: string;
}

/** Answer of POST .../holds, GET .../holds/{token}, POST .../extend and DELETE .../seats/{key}. */
export interface HoldResponse {
  hold_token: string;
  session_id?: string;
  seat_keys: string[];
  step: 'select' | 'payment';
  expires_at: string;
  extend_available?: boolean;
  policy?: HoldPolicy;
  /** The seats that could not be held when part of the request went through. */
  conflicts?: HoldConflict[];
  /** GET only: the order of a hold on its payment step. */
  checkout_token?: string;
}

/** Body of POST /v1/public/feeds/{feed_token}/holds. */
export interface HoldRequest {
  session_id: string;
  seat_keys: string[];
  hold_token?: string;
  device_id: string;
  rehold_of?: string;
}

export type HoldStep = 'select' | 'payment';

// ─── Constants ───────────────────────────────────────────────────────────────

/** "Extend" shows when this many seconds are left (the server allows 75: clock drift). */
export const EXTEND_WINDOW_SECONDS = 60;
/** How many nearest free seats are pulsed after a seat was taken. */
export const NEAREST_SEATS = 4;
/** How long they pulse. */
export const PULSE_MS = 6_000;
/** The poll interval when the seat-status answer names none. */
export const DEFAULT_POLL_MS = 3_000;
/** A retry of a request that got no answer waits this long. */
export const HOLD_RETRY_MS = 300;

// ─── Storage keys and the device id ──────────────────────────────────────────

export interface KeyValueStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

export function holdStorageKey(sessionId: string): string {
  return `arena_hold:${sessionId}`;
}

export function deviceStorageKey(feedToken: string): string {
  return `arena_device:${feedToken}`;
}

/** localStorage, or null where it is missing or refuses (private mode, a sandboxed frame). */
export function browserStorage(): KeyValueStorage | null {
  try {
    const s = (globalThis as { localStorage?: KeyValueStorage }).localStorage;
    if (!s) return null;
    // Some browsers throw only on the first write.
    const probe = '__arena_probe__';
    s.setItem(probe, '1');
    s.removeItem(probe);
    return s;
  } catch {
    return null;
  }
}

/** A random UUID v4. */
export function randomUuid(): string {
  try {
    const c = (globalThis as { crypto?: { randomUUID?: () => string; getRandomValues?: (a: Uint8Array) => Uint8Array } }).crypto;
    if (c?.randomUUID) return c.randomUUID();
    if (c?.getRandomValues) {
      const b = c.getRandomValues(new Uint8Array(16));
      b[6] = (b[6]! & 0x0f) | 0x40;
      b[8] = (b[8]! & 0x3f) | 0x80;
      const h = [...b].map((x) => x.toString(16).padStart(2, '0')).join('');
      return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
    }
  } catch {
    /* fall through */
  }
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (ch) => {
    const r = Math.floor(Math.random() * 16);
    return (ch === 'x' ? r : (r & 0x3) | 0x8).toString(16);
  });
}

const memoryDevice = new Map<string, string>();

/**
 * The id of this browser for the hold limits (spec 67 §6.1): a random UUID in
 * localStorage under `arena_device:<feed_token>`. Not an advertising id, never
 * sent to analytics. Without a usable storage it lives for the page view.
 */
export function getDeviceId(
  feedToken: string,
  storage: KeyValueStorage | null = browserStorage(),
  newId: () => string = randomUuid,
): string {
  const key = deviceStorageKey(feedToken);
  try {
    const stored = storage?.getItem(key);
    if (stored && /^[0-9a-f-]{16,}$/i.test(stored)) return stored;
  } catch {
    /* fall through */
  }
  const kept = memoryDevice.get(key);
  if (kept) return kept;
  const id = newId();
  memoryDevice.set(key, id);
  try {
    storage?.setItem(key, id);
  } catch {
    /* kept in memory */
  }
  return id;
}

// ─── The routes ──────────────────────────────────────────────────────────────

const JSON_HEADERS = { 'Content-Type': 'application/json' } as const;

/** POST /v1/public/feeds/{feed_token}/holds — take seats (spec 67 §4.1, §4.6). */
export async function postHold(
  feedToken: string,
  body: HoldRequest,
  apiBase: string,
  idempotencyKey: string,
): Promise<HoldResponse> {
  const url = `${apiBase}/v1/public/feeds/${encodeURIComponent(feedToken)}/holds`;
  const res = await fetch(url, {
    method: 'POST',
    headers: { ...JSON_HEADERS, 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw await apiErrorFromResponse(res, 'postHold');
  return (await res.json()) as HoldResponse;
}

/** DELETE /v1/public/holds/{hold_token}/seats/{seat_key} — take one seat off (§4.2). */
export async function deleteHoldSeat(holdToken: string, seatKey: string, apiBase: string): Promise<HoldResponse | null> {
  const url = `${apiBase}/v1/public/holds/${encodeURIComponent(holdToken)}/seats/${encodeURIComponent(seatKey)}`;
  const res = await fetch(url, { method: 'DELETE' });
  if (!res.ok) throw await apiErrorFromResponse(res, 'deleteHoldSeat');
  return readBody(res);
}

/** DELETE /v1/public/holds/{hold_token} — let the whole hold go (§4.2). */
export async function deleteHold(holdToken: string, apiBase: string, opts: { keepalive?: boolean } = {}): Promise<void> {
  const url = `${apiBase}/v1/public/holds/${encodeURIComponent(holdToken)}`;
  const res = await fetch(url, { method: 'DELETE', keepalive: opts.keepalive === true });
  if (!res.ok) throw await apiErrorFromResponse(res, 'deleteHold');
}

/** GET /v1/public/holds/{hold_token} — read the hold back after a reload (§4.3). */
export async function getHold(holdToken: string, apiBase: string): Promise<HoldResponse> {
  const url = `${apiBase}/v1/public/holds/${encodeURIComponent(holdToken)}`;
  const res = await fetch(url, { cache: 'no-store' });
  if (!res.ok) throw await apiErrorFromResponse(res, 'getHold');
  return (await res.json()) as HoldResponse;
}

/** POST /v1/public/holds/{hold_token}/extend (§4.7). */
export async function postHoldExtend(holdToken: string, apiBase: string): Promise<HoldResponse> {
  const url = `${apiBase}/v1/public/holds/${encodeURIComponent(holdToken)}/extend`;
  const res = await fetch(url, { method: 'POST' });
  if (!res.ok) throw await apiErrorFromResponse(res, 'postHoldExtend');
  return (await res.json()) as HoldResponse;
}

/**
 * POST /v1/public/checkout/{checkout_token}/paying (§4.8): "payment is going
 * on", sent right before Stripe confirms. Fire and forget: the payment never
 * waits for it. A network failure is sent once more.
 */
export function postCheckoutPaying(checkoutToken: string, apiBase: string): void {
  const url = `${apiBase}/v1/public/checkout/${encodeURIComponent(checkoutToken)}/paying`;
  const send = (): Promise<unknown> => fetch(url, { method: 'POST', keepalive: true });
  try {
    void send().catch(() => {
      setTimeout(() => {
        try {
          void send().catch(() => {});
        } catch {
          /* nothing to do */
        }
      }, 1_000);
    });
  } catch {
    /* nothing to do */
  }
}

async function readBody(res: Response): Promise<HoldResponse | null> {
  try {
    return (await res.json()) as HoldResponse;
  } catch {
    return null;
  }
}

// ─── The clock ───────────────────────────────────────────────────────────────

/** Seconds left on the hold's own clock (never below zero). */
export function holdSecondsLeft(expiresAt: string | null, nowMs: number = Date.now()): number {
  return expiresAt ? countdownSeconds(expiresAt, nowMs) : 0;
}

/** The "Extend" button: the platform still has an extension and 60 s or fewer are left. */
export function extendVisible(expiresAt: string | null, extendAvailable: boolean, nowMs: number = Date.now()): boolean {
  if (!extendAvailable || !expiresAt) return false;
  const left = holdSecondsLeft(expiresAt, nowMs);
  return left > 0 && left <= EXTEND_WINDOW_SECONDS;
}

/** Whole minutes of the policy's extension (at least 1). */
export function extendMinutes(policy: HoldPolicy | null | undefined): number {
  const s = policy?.extend_seconds;
  return typeof s === 'number' && s > 0 ? Math.max(1, Math.round(s / 60)) : 5;
}

// ─── The nearest free seats (spec 67 §9) ─────────────────────────────────────

/** One rendered seat: the centre comes from `getBBox()`, never from the plan's cx/cy. */
export interface SeatSpot {
  key: string;
  x: number;
  y: number;
  rowKey: string;
  tierId: string;
  /** Price in minor units, or null when the category has none. */
  price: number | null;
  status: string;
}

/**
 * Up to `limit` free seats of the same category (tier) and the same price as
 * `target`, the seats of the same row first, then by distance.
 */
export function nearestSeats(target: SeatSpot, all: readonly SeatSpot[], limit: number = NEAREST_SEATS): string[] {
  const dist = (s: SeatSpot): number => Math.hypot(s.x - target.x, s.y - target.y);
  return all
    .filter((s) => s.key !== target.key && s.status === 'available' && s.tierId === target.tierId && s.price === target.price)
    .map((s) => ({ key: s.key, same: s.rowKey === target.rowKey, d: dist(s) }))
    .sort((a, b) => Number(b.same) - Number(a.same) || a.d - b.d || (a.key < b.key ? -1 : 1))
    .slice(0, Math.max(0, limit))
    .map((s) => s.key);
}

// ─── Error texts ─────────────────────────────────────────────────────────────

/** The hold.* codes the widget has words for. */
export function isHoldErrorCode(code: string | undefined): boolean {
  return typeof code === 'string' && code.startsWith('hold.');
}

/** The seats of a `hold.seats_taken` / partial answer. */
export function conflictKeysOf(source: unknown): string[] {
  // The spec writes the list at the top of a partial 200/201 and under
  // `details` of a 409 and of a "hold again" 200: both places are read.
  const src = source as { conflicts?: unknown; details?: { conflicts?: unknown } } | null | undefined;
  const list = src?.conflicts ?? src?.details?.conflicts;
  if (!Array.isArray(list)) return [];
  const keys: string[] = [];
  for (const c of list) {
    const k = (c as { seat_key?: unknown } | null)?.seat_key;
    if (typeof k === 'string' && k) keys.push(k);
  }
  return keys;
}

// ─── The controller ──────────────────────────────────────────────────────────

export interface HoldView {
  /** Seats the platform confirmed as held for this buyer. */
  held: ReadonlySet<string>;
  /** Seats clicked, the answer not back yet (drawn at once, "waiting"). */
  pending: ReadonlySet<string>;
  holdToken: string | null;
  step: HoldStep | null;
  expiresAt: string | null;
  extendAvailable: boolean;
  policy: HoldPolicy | null;
  /** The hold has ended: "Hold the seats again" is offered. */
  expired: boolean;
  /** A request is queued or running. */
  busy: boolean;
}

export interface HoldControllerDeps {
  sessionId: string;
  deviceId: string;
  storage: KeyValueStorage | null;
  post: (body: HoldRequest, idempotencyKey: string) => Promise<HoldResponse>;
  del: (holdToken: string, seatKey: string) => Promise<HoldResponse | null>;
  get: (holdToken: string) => Promise<HoldResponse>;
  extend: (holdToken: string) => Promise<HoldResponse>;
  /** Called before every queued request (the widget lets a prepared order go first). */
  beforeMutate?: () => Promise<void>;
  onChange: (view: HoldView) => void;
  /** Seats that could not be taken (409 hold.seats_taken, or part of a request). */
  onSeatTaken?: (seatKeys: string[]) => void;
  /** Part of a "hold again" request was taken by others. */
  onPartial?: (seatKeys: string[]) => void;
  /** The platform refused with this code (hold.too_many_seats, ...) or the call failed (`hold.failed`). */
  onRejected?: (code: string, details: Record<string, unknown>) => void;
  /**
   * The platform has the feature off (404/503 `hold.disabled`) and there is no
   * hold: the widget goes back to today's way for the whole page view, with no
   * message. `seatKeys` are the seats clicked and not yet held; they become the
   * plain selection. With a hold the click goes to `onRejected('hold.disabled')`
   * instead and the hold is kept.
   */
  onDisabled?: (seatKeys: string[]) => void;
  /** The extension was taken, `minutes` long. */
  onExtended?: (minutes: number) => void;
  newIdempotencyKey?: () => string;
  now?: () => number;
  retryDelayMs?: number;
}

export interface HoldController {
  readonly view: HoldView;
  /** Click on a free seat (add) or on an own seat (take off). Returns what was done. */
  tap(seatKey: string): 'add' | 'remove' | 'ignored';
  /** Take these seats off (the order sheet's trash can). */
  release(seatKeys: readonly string[]): void;
  /** Read the stored hold back (after a reload). */
  restore(): Promise<void>;
  /** Read the hold again (after the order was cancelled with keep_hold). */
  refresh(): Promise<void>;
  /** The clock reached zero: ask the platform whether the hold is over. */
  checkExpiry(): Promise<void>;
  extend(): Promise<void>;
  /** "Hold the seats again" (`rehold_of`). */
  rehold(): Promise<void>;
  /** The order sheet's answer carried a new expiry. */
  noteExpiry(expiresAt: string | null | undefined, step?: HoldStep): void;
  /** The hold is over for good (paid, or left): forget the key. */
  forget(): void;
  /** Resolves when every queued request has answered. */
  idle(): Promise<void>;
  destroy(): void;
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

/**
 * The platform takes no hold at the click: `hold.disabled`, or a platform
 * without the hold routes at all (404/405 with a code that is not a hold
 * code, e.g. `http.not_found` from a backend older than spec 67).
 */
export function holdFeatureOff(err: unknown): boolean {
  if (!(err instanceof ApiError)) return false;
  if (err.code === 'hold.disabled') return true;
  return (err.status === 404 || err.status === 405) && !err.code.startsWith('hold.');
}

/** An error worth sending again with the same key: no answer at all, or a 5xx. */
function retryable(err: unknown): boolean {
  if (err instanceof ApiError) return err.status >= 500 && err.code !== 'hold.disabled';
  return true;
}

export function createHoldController(deps: HoldControllerDeps): HoldController {
  const now = deps.now ?? Date.now;
  const newKey = deps.newIdempotencyKey ?? randomUuid;
  const retryDelay = deps.retryDelayMs ?? HOLD_RETRY_MS;
  const storeKey = holdStorageKey(deps.sessionId);

  let token: string | null = null;
  let held = new Set<string>();
  let pending = new Set<string>();
  /** Seats taken off by a click whose request is still queued (a late answer must not bring them back). */
  const removing = new Set<string>();
  let step: HoldStep | null = null;
  let expiresAt: string | null = null;
  let extendAvailable = false;
  let policy: HoldPolicy | null = null;
  let expired = false;
  let expiredToken: string | null = null;
  let lastSeats: string[] = [];
  let queued = 0;
  let chain: Promise<void> = Promise.resolve();
  let checking = false;
  let destroyed = false;
  let disabled = false;
  let view: HoldView = snapshot();

  function snapshot(): HoldView {
    return {
      held: new Set(held),
      pending: new Set(pending),
      holdToken: token,
      step,
      expiresAt,
      extendAvailable,
      policy,
      expired,
      busy: queued > 0 || pending.size > 0,
    };
  }

  function emit(): void {
    view = snapshot();
    if (!destroyed) deps.onChange(view);
  }

  function store(next: string | null): void {
    try {
      if (next) deps.storage?.setItem(storeKey, next);
      else deps.storage?.removeItem(storeKey);
    } catch {
      /* the hold still works for this page view */
    }
  }

  function readStored(): string | null {
    try {
      return deps.storage?.getItem(storeKey) || null;
    } catch {
      return null;
    }
  }

  /** The platform's answer is the truth about the hold. */
  function apply(res: HoldResponse): void {
    token = res.hold_token || token;
    if (token) store(token);
    held = new Set((res.seat_keys ?? []).filter((k) => !removing.has(k)));
    step = res.step ?? step;
    expiresAt = res.expires_at ?? expiresAt;
    extendAvailable = res.extend_available === true;
    policy = res.policy ?? policy;
    expired = false;
  }

  /** The last seat went: no hold any more. */
  function clearHold(): void {
    token = null;
    store(null);
    held = new Set();
    step = null;
    expiresAt = null;
    extendAvailable = false;
    expired = false;
  }

  function markExpired(): void {
    if (token) expiredToken = token;
    if (held.size > 0) lastSeats = [...held];
    held = new Set();
    pending = new Set();
    removing.clear();
    token = null;
    store(null);
    step = null;
    extendAvailable = false;
    expired = lastSeats.length > 0 && expiredToken !== null;
    expiresAt = null;
  }

  function enqueue(task: () => Promise<void>): Promise<void> {
    queued += 1;
    const run = async (): Promise<void> => {
      try {
        if (deps.beforeMutate) await deps.beforeMutate();
        await task();
      } catch {
        /* a task answers for itself; nothing may break the queue */
      } finally {
        queued -= 1;
        emit();
      }
    };
    const p = chain.then(run, run);
    chain = p;
    return p;
  }

  /** One request, sent once more with the SAME key when it got no answer. */
  async function sendWithRetry(body: HoldRequest, key: string): Promise<HoldResponse> {
    try {
      return await deps.post(body, key);
    } catch (err) {
      if (!retryable(err)) throw err;
      await sleep(retryDelay);
      return deps.post(body, key);
    }
  }

  function reject(err: unknown): void {
    if (err instanceof ApiError) deps.onRejected?.(err.code, err.details);
    else deps.onRejected?.('hold.failed', {});
  }

  function isExpiredError(err: unknown): boolean {
    return err instanceof ApiError && (err.code === 'hold.expired' || err.status === 404 || err.status === 410);
  }

  function add(seatKey: string): void {
    pending.add(seatKey);
    const key = newKey(); // ONE key for this click, whatever the retries
    emit();
    void enqueue(async () => {
      if (disabled || !pending.has(seatKey)) return;
      const body: HoldRequest = { session_id: deps.sessionId, seat_keys: [seatKey], device_id: deps.deviceId };
      if (token) body.hold_token = token;
      let res: HoldResponse;
      try {
        res = await sendWithRetry(body, key);
      } catch (err) {
        if (holdFeatureOff(err) && token) {
          // The feature went off while this buyer has a hold: the hold is
          // kept (its seats are the buyer's and its order may be paying), only
          // this click is refused.
          pending.delete(seatKey);
          emit();
          deps.onRejected?.('hold.disabled', err instanceof ApiError ? err.details : {});
          return;
        }
        if (holdFeatureOff(err)) {
          // The feature is off on the platform and there is no hold: every
          // click waiting behind this one falls back with it, silently.
          disabled = true;
          const keys = [...pending];
          pending = new Set();
          emit();
          deps.onDisabled?.(keys);
          return;
        }
        pending.delete(seatKey);
        if (err instanceof ApiError && err.code === 'hold.seats_taken') {
          const keys = conflictKeysOf(err.details);
          emit();
          deps.onSeatTaken?.(keys.length > 0 ? keys : [seatKey]);
        } else if (err instanceof ApiError && err.code === 'hold.expired') {
          markExpired();
          emit();
          reject(err);
        } else {
          emit();
          reject(err);
        }
        return;
      }
      pending.delete(seatKey);
      apply(res);
      emit();
      const lost = conflictKeysOf(res);
      if (lost.length > 0) deps.onSeatTaken?.(lost);
    });
  }

  function remove(seatKey: string): void {
    held.delete(seatKey);
    removing.add(seatKey);
    emit();
    void enqueue(async () => {
      const current = token;
      if (!current) {
        removing.delete(seatKey);
        return;
      }
      try {
        const res = await deps.del(current, seatKey);
        removing.delete(seatKey);
        if (!res || !Array.isArray(res.seat_keys) || res.seat_keys.length === 0) {
          // Nothing left in the hold (it was closed with its last seat). A seat
          // clicked meanwhile is queued behind and opens a new one.
          clearHold();
        } else {
          apply(res);
        }
      } catch (err) {
        removing.delete(seatKey);
        if (isExpiredError(err)) {
          // The platform has no such hold (or seat) any more: the seat is free.
          if (err instanceof ApiError && err.code === 'hold.expired') {
            held.add(seatKey);
            markExpired();
          }
          return;
        }
        // Refused (hold.in_payment) or no answer: the seat is still held.
        held.add(seatKey);
        reject(err);
      }
    });
  }

  function applyReplace(res: HoldResponse): void {
    apply(res);
    emit();
  }

  const api: HoldController = {
    get view() {
      return view;
    },

    tap(seatKey) {
      if (destroyed || disabled) return 'ignored';
      if (held.has(seatKey)) {
        remove(seatKey);
        return 'remove';
      }
      if (pending.has(seatKey) || removing.has(seatKey)) return 'ignored';
      add(seatKey);
      return 'add';
    },

    release(seatKeys) {
      for (const k of seatKeys) if (held.has(k)) remove(k);
      for (const k of seatKeys) pending.delete(k);
      emit();
    },

    async restore() {
      const stored = readStored();
      if (!stored || destroyed) return;
      try {
        const res = await deps.get(stored);
        if (destroyed) return;
        token = stored;
        applyReplace(res);
        if (holdSecondsLeft(expiresAt, now()) === 0) void api.checkExpiry();
      } catch (err) {
        if (isExpiredError(err)) store(null);
        // A network error keeps the key: the next reload tries again.
      }
    },

    async refresh() {
      const current = token;
      if (!current || destroyed) return;
      try {
        const res = await deps.get(current);
        if (token === current) applyReplace(res);
      } catch (err) {
        if (isExpiredError(err) && token === current) {
          markExpired();
          emit();
        }
      }
    },

    async checkExpiry() {
      const current = token;
      if (!current || checking || destroyed) return;
      checking = true;
      try {
        const res = await deps.get(current);
        if (token !== current) return;
        apply(res);
        // The platform's sweep may still be on its way: the clock decides.
        if (holdSecondsLeft(expiresAt, now()) === 0) markExpired();
        emit();
      } catch (err) {
        if (isExpiredError(err) && token === current) {
          markExpired();
          emit();
        }
      } finally {
        checking = false;
      }
    },

    extend() {
      return enqueue(async () => {
        const current = token;
        if (!current) return;
        try {
          const res = await deps.extend(current);
          apply(res);
          emit();
          deps.onExtended?.(extendMinutes(policy));
        } catch (err) {
          if (err instanceof ApiError && err.code === 'hold.expired') {
            markExpired();
            emit();
          } else if (err instanceof ApiError && err.code === 'hold.extend_used') {
            extendAvailable = false;
            emit();
          } else if (err instanceof ApiError && err.code === 'hold.extend_not_yet') {
            // Clock drift: not yet. The button comes back by itself.
          } else {
            reject(err);
          }
        }
      });
    },

    rehold() {
      if (!expired || lastSeats.length === 0 || !expiredToken) return Promise.resolve();
      const seats = [...lastSeats];
      const old = expiredToken;
      const key = newKey();
      return enqueue(async () => {
        const body: HoldRequest = {
          session_id: deps.sessionId,
          seat_keys: seats,
          device_id: deps.deviceId,
          rehold_of: old,
        };
        try {
          const res = await sendWithRetry(body, key);
          expired = false;
          expiredToken = null;
          lastSeats = [];
          apply(res);
          emit();
          const lost = conflictKeysOf(res);
          if (lost.length > 0) deps.onPartial?.(lost);
        } catch (err) {
          if (err instanceof ApiError && err.code === 'hold.seats_taken') {
            // Nothing could be held again: the panel goes, the seats are others'.
            expired = false;
            expiredToken = null;
            lastSeats = [];
            emit();
            deps.onPartial?.(conflictKeysOf(err.details).length > 0 ? conflictKeysOf(err.details) : seats);
          } else {
            reject(err);
          }
        }
      });
    },

    noteExpiry(next, nextStep) {
      if (!token || !next) return;
      expiresAt = next;
      if (nextStep) step = nextStep;
      emit();
    },

    forget() {
      clearHold();
      pending = new Set();
      removing.clear();
      expired = false;
      expiredToken = null;
      lastSeats = [];
      emit();
    },

    idle() {
      return chain.then(
        () => {},
        () => {},
      );
    },

    destroy() {
      destroyed = true;
    },
  };

  return api;
}
