<script lang="ts">
  /** NewsletterBlock - consent + subscribe for the order e-mail (double opt-in). */
  import { consentText, subscribeToNews } from '../../lib/thankYou.js';
  import type { MarketingOffer } from '../../lib/thankYou.js';
  import type { ThankYouStrings } from '../../lib/thankYouI18n.js';

  interface Props {
    s: ThankYouStrings;
    checkoutToken: string;
    apiBase?: string;
    /** marketing_offer of the order status; the block is drawn only when it is set. */
    offer?: MarketingOffer | null;
  }
  const { s, checkoutToken, apiBase = '', offer = null }: Props = $props();

  let agreed = $state(false);
  let busy = $state(false);
  let outcome = $state('');

  const message = $derived(
    outcome === 'pending' ? s.news_pending
    : outcome ? s.news_error
    : '',
  );

  async function submit(): Promise<void> {
    if (busy) return;
    if (!agreed || !offer) return;
    busy = true;
    outcome = await subscribeToNews(checkoutToken, apiBase, typeof window === 'undefined' ? '' : window.location.href, offer.text_version);
    busy = false;
  }
</script>

{#if offer && checkoutToken && outcome !== 'already_confirmed'}
  <section class="ty-card ty-block" data-testid="ty-newsletter">
    <h3 class="ty-title">{s.news_title}</h3>
    {#if outcome === 'pending'}
      <p class="ty-text" role="status" data-testid="ty-news-done">{message}</p>
    {:else}
      <label class="ty-check">
        <input type="checkbox" bind:checked={agreed} data-testid="ty-news-consent" />
        {consentText(s.news_consent, offer.org_name)}
      </label>
      <button type="button" class="ty-btn ty-btn--primary" onclick={submit} disabled={busy || !agreed} aria-busy={busy} data-testid="ty-news-submit">
        {s.news_button}
      </button>
      {#if message}
        <p class="ty-text ty-err" role="alert" data-testid="ty-news-error">{message}</p>
      {/if}
    {/if}
  </section>
{/if}
