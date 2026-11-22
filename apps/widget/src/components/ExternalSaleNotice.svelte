<script lang="ts">
  /**
   * ExternalSaleNotice — a date sold by a partner box office: no map, no cart,
   * one button to the partner's site (new tab) and "from <price>" when known.
   */
  import type { FeedSession } from '../types.js';
  import { externalBuyLabel, externalPriceLine, externalSaleOf } from '../lib/externalSale.js';

  interface Props {
    session: FeedSession;
    locale?: string;
  }

  const { session, locale = 'en' }: Props = $props();

  const info = $derived(externalSaleOf(session));
  const priceLine = $derived(info ? externalPriceLine(locale, info) : '');
</script>

{#if info}
  <div class="external-sale" data-testid="session-external-sale">
    {#if priceLine}
      <p class="external-price" data-testid="external-price">{priceLine}</p>
    {/if}
    <a
      class="external-btn"
      data-testid="external-buy"
      href={info.url}
      target="_blank"
      rel="noopener noreferrer"
    >
      {externalBuyLabel(locale, info.seller_name)}
    </a>
  </div>
{/if}

<style>
  .external-sale {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    padding: 0.75rem 1rem;
    border-top: 1px solid var(--_border, #e5e7eb);
  }

  .external-price {
    margin: 0;
    font-size: 0.95rem;
    font-weight: 600;
    color: var(--arena-color-primary, #1a1a1a);
  }

  .external-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 0.625rem 1.25rem;
    background: var(--_btn-bg, #fcdc54);
    color: var(--_btn-text, #101010);
    border-radius: var(--arena-radius, 8px);
    font-size: 1rem;
    font-family: inherit;
    font-weight: 700;
    text-decoration: none;
  }

  .external-btn:hover {
    background: var(--_btn-bg-hover, #f5d23a);
  }

  .external-btn:focus-visible {
    outline: 3px solid var(--arena-focus-ring, var(--arena-accent, #4f46e5));
    outline-offset: 3px;
  }
</style>
