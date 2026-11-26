/**
 * holdWidget.ts — the widget's wiring of the hold controller (spec 67): the
 * routes, the words under the map and the nearest free seats after a seat
 * was taken. Part of the lazy hold chunk (lib/holdRuntime.ts); ArenaTickets
 * passes in only what lives in its own state.
 */

import { PULSE_MS } from './holdDraw.js';
import { fillHold, getHoldStrings, holdErrorText } from './holdI18n.js';
import {
  browserStorage,
  createHoldController,
  deleteHoldSeat,
  getDeviceId,
  getHold,
  postHold,
  postHoldExtend,
  type HoldController,
  type HoldView,
} from './holds.js';

/** The rendered seat map: refresh the statuses now, light the nearest free seats. */
export interface HoldSeatMap {
  refreshStatuses(): Promise<void>;
  highlightNearest(seatKey: string): number;
}

/** What the widget keeps in its own state. */
export interface WidgetHoldHooks {
  sessionId: string;
  feed: string;
  base: string;
  /** The buyer's language now. */
  locale(): string;
  /** This controller is still the one of the selected session. */
  current(ctl: HoldController): boolean;
  /** Called before every queued request. */
  beforeMutate(): Promise<void>;
  /** The hold's view changed. */
  view(v: HoldView): void;
  /**
   * The platform has the feature off and there is no hold: the widget drops
   * this controller and the clicked `keys` become the plain selection.
   */
  disabled(ctl: HoldController, keys: string[]): void;
  /** The words under the map (null clears them); `forMs` > 0 hides them after that time. */
  notice(text: string | null, forMs?: number): void;
  seatMap(): HoldSeatMap | null;
  /** "row/number" of a seat key, when known. */
  seatLabel(key: string): string | undefined;
}

/** The controller of one plan session, wired to the widget. Not restored yet. */
export function startWidgetHold(h: WidgetHoldHooks): HoldController {
  const { feed, base } = h;
  const words = () => getHoldStrings(h.locale());
  const seatList = (keys: readonly string[]): string => keys.map((k) => h.seatLabel(k) ?? k).join(', ');

  /** A seat was taken by somebody else: look at the statuses now, light free seats nearby. */
  async function onSeatTaken(keys: string[]): Promise<void> {
    const first = keys[0];
    if (!first) return;
    try {
      await h.seatMap()?.refreshStatuses();
    } catch {
      /* the next poll brings them */
    }
    const lit = h.seatMap()?.highlightNearest(first) ?? 0;
    const t = words();
    h.notice(lit > 0 ? t.seat_taken_near : t.seat_taken_none, lit > 0 ? PULSE_MS : 0);
  }

  const ctl: HoldController = createHoldController({
    sessionId: h.sessionId,
    deviceId: getDeviceId(feed),
    storage: browserStorage(),
    post: (body, key) => postHold(feed, body, base, key),
    del: (token, seatKey) => deleteHoldSeat(token, seatKey, base),
    get: (token) => getHold(token, base),
    extend: (token) => postHoldExtend(token, base),
    // A prepared order is on its payment step: the hold takes no change
    // until that order is cancelled (keep_hold), so it goes first.
    beforeMutate: () => h.beforeMutate(),
    onChange: (v) => {
      if (h.current(ctl)) h.view(v);
    },
    onDisabled: (keys) => {
      if (!h.current(ctl)) return;
      // An existing hold is never dropped: its seats stay the buyer's.
      if (ctl.view.holdToken) return;
      // No message: the clicked seats are simply the plain selection now.
      h.disabled(ctl, keys);
    },
    onSeatTaken: (keys) => {
      if (h.current(ctl)) void onSeatTaken(keys);
    },
    onPartial: (keys) => {
      if (!h.current(ctl)) return;
      h.notice(fillHold(words().hold_rebook_partial, { seats: seatList(keys) }));
      void h.seatMap()?.refreshStatuses().catch(() => {});
    },
    onRejected: (code, details) => {
      if (h.current(ctl)) h.notice(holdErrorText(code, h.locale(), details));
    },
    onExtended: (min) => {
      if (h.current(ctl)) h.notice(fillHold(words().hold_extended, { min }), 5_000);
    },
  });
  return ctl;
}
