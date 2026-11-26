<script lang="ts">
  /**
   * ThankYouBlocks - everything under a paid order: share, follow, promo,
   * newsletter, "you may also like". /site and the events list are fetched
   * once when the paid view shows; any failure hides that block silently.
   */
  import { getThankYouStrings } from '../../lib/thankYouI18n.js';
  import { loadFeedSite, loadRelatedEvents, shareTarget } from '../../lib/thankYou.js';
  import type { FeedSite, MarketingOffer, RelatedEvent } from '../../lib/thankYou.js';
  import ShareBlock from './ShareBlock.svelte';
  import FollowBlock from './FollowBlock.svelte';
  import PromoBlock from './PromoBlock.svelte';
  import NewsletterBlock from './NewsletterBlock.svelte';
  import RelatedBlock from './RelatedBlock.svelte';

  interface Props {
    locale?: string;
    apiBase?: string;
    feedToken?: string;
    checkoutToken?: string | null;
    /** Current event: its page_url and name feed the share links. */
    event?: { id?: string; name?: string; page_url?: string } | null;
    /** marketing_offer of the order status: the newsletter block is drawn only when it is set. */
    marketingOffer?: MarketingOffer | null;
  }
  const {
    locale = 'en',
    apiBase = '',
    feedToken = '',
    checkoutToken = null,
    event = null,
    marketingOffer = null,
  }: Props = $props();

  const s = $derived(getThankYouStrings(locale));
  let site = $state<FeedSite | null>(null);
  let related = $state<RelatedEvent[]>([]);

  const target = $derived(
    shareTarget(event?.page_url, typeof window === 'undefined' ? null : window.location),
  );

  let loadedFor = '';
  $effect(() => {
    const token = feedToken;
    const base = apiBase;
    const eventId = event?.id ?? null;
    if (!token) return;
    const key = `${token}|${base}|${eventId ?? ''}`;
    if (key === loadedFor) return;
    loadedFor = key;
    let cancelled = false;
    void loadFeedSite(token, base).then((r) => {
      if (!cancelled) site = r;
    });
    void loadRelatedEvents(token, base, eventId).then((r) => {
      if (!cancelled) related = r;
    });
    return () => {
      cancelled = true;
    };
  });
</script>

<div class="ty" data-testid="thank-you-blocks">
  <ShareBlock {s} url={target} name={event?.name ?? ''} />
  <FollowBlock {s} socials={site?.socials ?? []} />
  <PromoBlock {s} {locale} note={site?.thank_you ?? null} />
  {#if checkoutToken && marketingOffer}
    <NewsletterBlock {s} {checkoutToken} {apiBase} offer={marketingOffer} />
  {/if}
  <RelatedBlock {s} events={related} {locale} />
</div>

<style>
  .ty { width: 100%; display: flex; flex-direction: column; align-items: center; gap: 1.25rem; }
  .ty :global(svg) { width: 1.25rem; height: 1.25rem; }
  .ty :global(.ty-block) { width: 100%; display: flex; flex-direction: column; align-items: center; gap: 0.5rem; }
  .ty :global(.ty-card) { box-sizing: border-box; padding: 1rem; border: 1px solid var(--arena-border-color, #e5e7eb); border-radius: var(--arena-radius, 8px); }
  .ty :global(.ty-title) { margin: 0; font-size: 1rem; font-weight: 600; color: var(--arena-color-primary, #1a1a1a); }
  .ty :global(.ty-text) { margin: 0; font-size: 0.875rem; color: var(--arena-color-secondary, #6b7280); }
  .ty :global(.ty-err) { color: #b91c1c; }
  .ty :global(.ty-row) { list-style: none; margin: 0; padding: 0; display: flex; flex-wrap: wrap; justify-content: center; align-items: center; gap: 0.5rem; }
  .ty :global(.ty-round) { display: flex; align-items: center; justify-content: center; width: 2.5rem; height: 2.5rem; border-radius: 50%; }
  .ty :global(.ty-pill) { display: inline-flex; align-items: center; gap: 0.4rem; padding: 0.4rem 0.9rem; border-radius: 999px; border: 1px solid var(--arena-border-color, #e5e7eb); color: var(--arena-color-primary, #1a1a1a); font-size: 0.875rem; text-decoration: none; }
  .ty :global(.ty-code) { font-family: monospace; font-size: 1.25rem; font-weight: 700; letter-spacing: 0.06em; word-break: break-all; user-select: all; direction: ltr; unicode-bidi: isolate; }
  .ty :global(.ty-btn) { padding: 0.4rem 1rem; border: 1px solid var(--arena-border-color, #e5e7eb); border-radius: var(--arena-radius, 8px); background: transparent; color: inherit; font: inherit; cursor: pointer; }
  .ty :global(.ty-btn--primary) { border: none; background: var(--_btn-bg, #fcdc54); color: var(--_btn-text, #101010); font-weight: 700; }
  .ty :global(.ty-btn:disabled) { opacity: 0.6; }
  .ty :global(.ty-check) { display: flex; gap: 0.5rem; font-size: 0.8125rem; text-align: start; }
  .ty :global(.ty-link) { font-size: 0.875rem; color: var(--arena-accent, #4f46e5); word-break: break-all; }
  .ty :global(.ty-cards) { list-style: none; margin: 0; padding: 0; width: 100%; display: flex; flex-direction: column; gap: 0.75rem; }
  .ty :global(.ty-ev) { display: flex; flex-direction: column; gap: 0.25rem; text-decoration: none; text-align: start; color: var(--arena-color-primary, #1a1a1a); font-size: 0.875rem; }
  .ty :global(.ty-ev small) { opacity: 0.7; font-size: 0.8125rem; }
</style>
