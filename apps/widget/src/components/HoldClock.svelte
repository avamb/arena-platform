<script lang="ts">
  /**
   * HoldClock — the hold's clock under the map, the extension and the end of
   * the hold (spec 67). Part of the lazy hold chunk (lib/holdRuntime.ts): the
   * main bundle renders it only once that chunk is loaded.
   */
  import { formatCountdown } from '../lib/cart.js';
  import { holdSecondsLeft } from '../lib/holdBasics.js';
  import { fillHold, getHoldStrings, holdCountdownText } from '../lib/holdI18n.js';
  import { extendMinutes, extendVisible, type HoldView } from '../lib/holds.js';

  interface Props {
    view: HoldView;
    locale: string;
    /** The widget's clock, one tick a second while the hold runs. */
    nowMs: number;
    /** Seats are held and the hold has an expiry. */
    shown: boolean;
    onRehold: () => void;
    onExtend: () => void;
  }

  let { view, locale, nowMs, shown, onRehold, onExtend }: Props = $props();
  const strings = $derived(getHoldStrings(locale));
  const secs = $derived(holdSecondsLeft(view.expiresAt, nowMs));
  const extendShown = $derived(extendVisible(view.expiresAt, view.extendAvailable, nowMs));
</script>

{#if view.expired}
  <div class="arena-hold arena-hold--ended" role="alert" data-testid="hold-expired">
    <span>{strings.hold_expired}</span>
    <button type="button" class="arena-hold-btn" disabled={view.busy} onclick={onRehold}>
      {strings.hold_rebook}
    </button>
  </div>
{:else if shown}
  <div class="arena-hold" class:arena-hold--soon={secs <= 120} role="timer" data-testid="hold-countdown">
    <span>{holdCountdownText(locale, formatCountdown(secs))}</span>
    {#if extendShown}
      <button type="button" class="arena-hold-btn" disabled={view.busy} onclick={onExtend} data-testid="hold-extend">
        {fillHold(strings.hold_extend, { min: extendMinutes(view.policy) })}
      </button>
    {/if}
  </div>
{/if}

<style>
  /* Spec 67: the hold's clock under the map, above the cart bar. */
  .arena-hold {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
    padding: 0.5rem 1rem;
    font-size: 0.875rem;
    font-weight: 600;
    color: #134e4a;
    background: #ccfbf1;
  }
  .arena-hold--soon {
    color: #92400e;
    background: #fef3c7;
  }
  .arena-hold--ended {
    color: #7f1d1d;
    background: #fee2e2;
  }
  .arena-hold-btn {
    font: inherit;
    font-weight: 600;
    padding: 0.35rem 0.75rem;
    border: 1px solid currentColor;
    border-radius: var(--_radius, 6px);
    background: transparent;
    color: inherit;
    cursor: pointer;
  }
  .arena-hold-btn:disabled {
    opacity: 0.6;
    cursor: default;
  }

  /* The seat map's own-hold marks (set by lib/holdDraw.ts) are styled here, in
     the lazy chunk: this component mounts with the chunk, beside the map, and
     its styles stay in the shadow root from then on. Before the chunk there
     are no such marks. */
  /* Spec 67 §9: free seats lit after a seat was taken. The outline pulses for
     6 s (the script removes the mark); with reduced motion it stays thick. */
  :global(.seat-map-container circle[data-pulse='true']) {
    stroke: #2563eb;
    stroke-width: 4;
    animation: arena-seat-pulse 0.9s ease-in-out infinite;
  }
  :global(.seat-map-container circle[data-hold='own']) {
    stroke: #134e4a;
    stroke-width: 2;
  }
  @keyframes arena-seat-pulse {
    0%, 100% { stroke-width: 2; stroke-opacity: 1; }
    50% { stroke-width: 6; stroke-opacity: 0.55; }
  }
  @media (prefers-reduced-motion: reduce) {
    :global(.seat-map-container circle[data-pulse='true']) {
      animation: none;
      stroke-width: 5;
    }
  }
</style>
