<script lang="ts">
  /** PromoBlock - the organizer thank-you note: title, text, promo code with copy, link. */
  import { copyToClipboard } from '../../lib/referral.js';
  import { getReferralStrings } from '../../lib/referralI18n.js';
  import { safeHttpUrl } from '../../lib/thankYou.js';
  import type { ThankYouNote } from '../../lib/thankYou.js';
  import type { ThankYouStrings } from '../../lib/thankYouI18n.js';

  interface Props {
    s: ThankYouStrings;
    note: ThankYouNote | null;
    locale?: string;
  }
  const { s, note, locale = 'en' }: Props = $props();

  // The copy button and its notes are the referral block words.
  const r = $derived(getReferralStrings(locale));
  let copyNote = $state<'' | 'copied' | 'failed'>('');
  const link = $derived(safeHttpUrl(note?.url));

  async function copy(): Promise<void> {
    if (!note?.promo_code) return;
    const ok = await copyToClipboard(note.promo_code, typeof navigator === 'undefined' ? null : navigator);
    copyNote = ok ? 'copied' : 'failed';
  }
</script>

{#if note}
  <section class="ty-card ty-block" data-testid="ty-promo">
    {#if note.title}<h3 class="ty-title">{note.title}</h3>{/if}
    {#if note.text}<p class="ty-text">{note.text}</p>{/if}
    {#if note.promo_code}
      <p class="ty-text">{r.code_label}</p>
      <div class="ty-row">
        <span class="ty-code" data-testid="ty-promo-code">{note.promo_code}</span>
        <button type="button" class="ty-btn" onclick={copy} data-testid="ty-promo-copy">{r.copy_code}</button>
      </div>
      {#if copyNote}
        <p class="ty-text" role="status">{copyNote === 'copied' ? r.copied : r.copy_failed}</p>
      {/if}
    {/if}
    {#if link}
      <a class="ty-link" href={link} target="_blank" rel="noopener noreferrer">{link.replace(/^https?:\/\//i, '')}</a>
    {/if}
  </section>
{/if}
