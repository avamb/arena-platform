<script lang="ts">
  /**
   * SessionList — date chips for session selection + price-category legend.
   *
   * Renders a horizontal scrollable row of date chips (one per session).
   * When a seated/hybrid session is selected, its price categories are shown
   * as colored swatches below the chips.
   */
  import type { FeedSession, Tier } from '../types.js';

  interface Props {
    sessions: FeedSession[];
    selectedSession: FeedSession | null;
    onSelectSession: (session: FeedSession) => void;
  }

  const { sessions, selectedSession, onSelectSession }: Props = $props();

  /** Format a session start_at ISO string as a short date chip label. */
  function formatDate(isoDate: string, locale: string): string {
    try {
      return new Date(isoDate).toLocaleDateString(locale, {
        weekday: 'short',
        month: 'short',
        day: 'numeric',
      });
    } catch {
      return isoDate.slice(0, 10);
    }
  }

  /** Format a session start_at ISO string as a short time label. */
  function formatTime(isoDate: string, locale: string): string {
    try {
      return new Date(isoDate).toLocaleTimeString(locale, {
        hour: '2-digit',
        minute: '2-digit',
      });
    } catch {
      return isoDate.slice(11, 16);
    }
  }

  /** Format a price for the legend chip. */
  function formatPrice(tier: Tier): string {
    if (tier.pricing_mode === 'free') return 'Free';
    const amount = (tier.price_amount / 100).toFixed(0);
    return `${amount} ${tier.currency.toUpperCase()}`;
  }

  /** Categories derived from tiers of the selected session (for legend). */
  const legendTiers = $derived(
    selectedSession ? [...selectedSession.tiers].sort((a, b) => a.sort_order - b.sort_order) : [],
  );

  const displayLocale = 'en'; // TODO: wire from parent when i18n wave lands
</script>

<div class="session-list">
  <!-- ── Date chips ── -->
  <div class="chips-row" role="tablist" aria-label="Event sessions">
    {#each sessions as session (session.id)}
      {@const isSelected = selectedSession?.id === session.id}
      <button
        role="tab"
        aria-selected={isSelected}
        class="chip"
        class:chip--selected={isSelected}
        class:chip--cancelled={session.status === 'cancelled'}
        onclick={() => onSelectSession(session)}
        disabled={session.status === 'cancelled'}
      >
        <span class="chip-date">{formatDate(session.start_at, displayLocale)}</span>
        <span class="chip-time">{formatTime(session.start_at, displayLocale)}</span>
      </button>
    {/each}
  </div>

  <!-- ── Price-category legend ── -->
  {#if legendTiers.length > 0}
    <div class="legend" aria-label="Price categories">
      {#each legendTiers as tier (tier.id)}
        <div class="legend-item">
          <span class="legend-dot" style="background:{tier.currency ? '#6366f1' : '#d1d5db'}"></span>
          <span class="legend-name">{tier.name}</span>
          <span class="legend-price">{formatPrice(tier)}</span>
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .session-list {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    padding: 0.75rem;
    border-bottom: 1px solid var(--_border, #e5e7eb);
  }

  .chips-row {
    display: flex;
    flex-wrap: nowrap;
    overflow-x: auto;
    gap: 0.5rem;
    padding-bottom: 0.25rem;
    scrollbar-width: thin;
  }

  .chip {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.125rem;
    padding: 0.375rem 0.75rem;
    border: 1px solid var(--_border, #e5e7eb);
    border-radius: var(--_radius, 6px);
    background: transparent;
    cursor: pointer;
    font-size: 0.8rem;
    white-space: nowrap;
    transition: background 0.1s, border-color 0.1s;
    color: inherit;
    font-family: inherit;
  }

  .chip:hover:not(:disabled) {
    background: color-mix(in srgb, var(--_accent, #6366f1) 10%, transparent);
    border-color: var(--_accent, #6366f1);
  }

  .chip--selected {
    background: var(--_accent, #6366f1);
    border-color: var(--_accent, #6366f1);
    color: #fff;
  }

  .chip--cancelled {
    opacity: 0.4;
    cursor: not-allowed;
    text-decoration: line-through;
  }

  .chip-date {
    font-weight: 600;
  }

  .chip-time {
    font-size: 0.7rem;
    opacity: 0.8;
  }

  .legend {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
  }

  .legend-item {
    display: flex;
    align-items: center;
    gap: 0.25rem;
    font-size: 0.78rem;
    color: inherit;
  }

  .legend-dot {
    display: inline-block;
    width: 10px;
    height: 10px;
    border-radius: 50%;
    flex-shrink: 0;
    border: 1px solid rgba(0, 0, 0, 0.1);
  }

  .legend-name {
    font-weight: 500;
  }

  .legend-price {
    opacity: 0.7;
  }
</style>
