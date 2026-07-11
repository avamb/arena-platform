/**
 * poller.ts — seat-status polling with Page Visibility backoff.
 *
 * `SeatStatusPoller` polls /event-sessions/{id}/seat-status on a configurable
 * interval.  It uses the Page Visibility API to back off aggressively when the
 * browser tab is hidden, and resumes normal polling immediately when the tab
 * becomes visible again.
 *
 * First tick fetches a full snapshot; subsequent ticks use the
 * `?since_version=N` delta endpoint so only changed seats are transmitted.
 */

import { fetchSeatStatus, fetchSeatStatusDelta } from '../api.js';
import type { SeatStatusValue } from '../types.js';

// ─── Public types ─────────────────────────────────────────────────────────────

export interface PollerConfig {
  /** Session ID to poll seat status for. */
  sessionId: string;
  /**
   * Polling interval in ms when the browser tab is visible.
   * Must be between 2 000 and 5 000 ms per spec (default: 3 000).
   */
  normalInterval?: number;
  /**
   * Polling interval in ms when the browser tab is hidden.
   * Reduces server load during background browsing (default: 30 000).
   */
  hiddenInterval?: number;
  /**
   * Called when new seat-status data is available.
   * `seats` is the delta (may be empty when there are no changes);
   * `version` is the new monotonic cursor.
   */
  onUpdate: (seats: Record<string, SeatStatusValue>, version: number) => void;
  /**
   * Called when a polling request fails.  Non-fatal by default — the poller
   * retries on the next scheduled tick.
   */
  onError?: (err: Error) => void;
}

// ─── SeatStatusPoller ─────────────────────────────────────────────────────────

/**
 * Polls seat status for a single event session.
 *
 * ```ts
 * const poller = new SeatStatusPoller({ sessionId, onUpdate });
 * poller.start();
 * // …later:
 * poller.stop();
 * ```
 */
export class SeatStatusPoller {
  private readonly sessionId: string;
  private readonly normalInterval: number;
  private readonly hiddenInterval: number;
  private readonly onUpdate: PollerConfig['onUpdate'];
  private readonly onError: PollerConfig['onError'];

  private currentVersion = 0;
  private isFirstFetch = true;
  private running = false;
  private timer: ReturnType<typeof setTimeout> | null = null;

  constructor(config: PollerConfig) {
    this.sessionId = config.sessionId;
    this.normalInterval = config.normalInterval ?? 3_000;
    this.hiddenInterval = config.hiddenInterval ?? 30_000;
    this.onUpdate = config.onUpdate;
    this.onError = config.onError;
  }

  // ── Lifecycle ──────────────────────────────────────────────────────────────

  /** Start polling.  Safe to call multiple times (idempotent). */
  start(): void {
    if (this.running) return;
    this.running = true;
    this.isFirstFetch = true;
    this.currentVersion = 0;

    if (typeof document !== 'undefined') {
      document.addEventListener('visibilitychange', this.onVisibilityChange);
    }
    this.scheduleNext(0); // kick off immediately
  }

  /** Stop polling and remove event listeners. */
  stop(): void {
    this.running = false;
    this.clearTimer();
    if (typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', this.onVisibilityChange);
    }
  }

  // ── Internal ───────────────────────────────────────────────────────────────

  private readonly onVisibilityChange = (): void => {
    // When the tab becomes visible, poll immediately to catch up on changes
    // that occurred while the tab was hidden.
    if (typeof document !== 'undefined' && document.visibilityState === 'visible') {
      this.clearTimer();
      this.scheduleNext(0);
    }
  };

  private interval(): number {
    if (typeof document !== 'undefined' && document.visibilityState === 'hidden') {
      return this.hiddenInterval;
    }
    return this.normalInterval;
  }

  private scheduleNext(delayMs?: number): void {
    if (!this.running) return;
    this.timer = setTimeout(() => void this.tick(), delayMs ?? this.interval());
  }

  private clearTimer(): void {
    if (this.timer !== null) {
      clearTimeout(this.timer);
      this.timer = null;
    }
  }

  private async tick(): Promise<void> {
    if (!this.running) return;
    try {
      let response;
      if (this.isFirstFetch) {
        response = await fetchSeatStatus(this.sessionId);
        this.isFirstFetch = false;
      } else {
        response = await fetchSeatStatusDelta(this.sessionId, this.currentVersion);
      }
      this.currentVersion = response.status_version;
      this.onUpdate(response.seats, response.status_version);
    } catch (err) {
      this.onError?.(err instanceof Error ? err : new Error(String(err)));
    }
    // Schedule next tick after completing (success or error).
    this.scheduleNext();
  }
}
