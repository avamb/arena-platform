<script lang="ts">
  /**
   * ReferralInvite — the "Invite a friend" block of a paid order (spec 52 §3.3).
   *
   * Asks the platform for the order's personal code once. A 404 (no programme,
   * not paid, programme over) and any other failure draw NOTHING: the paid
   * screen must never show an error for something the buyer did not ask for.
   * Shows the offer, the code, "Copy link" and, only where the browser has a
   * share sheet, "Share". Stores nothing.
   */

  import { getCheckoutReferral } from '../api.js';
  import {
    canNativeShare,
    copyToClipboard,
    copyValue,
    loadReferralBlock,
    referralText,
    shareData,
  } from '../lib/referral.js';
  import type { ReferralBlock } from '../lib/referral.js';
  import { getReferralStrings } from '../lib/referralI18n.js';

  interface Props {
    /** The paid order's checkout token (the credential of the route). */
    checkoutToken: string;
    locale?: string;
    apiBase?: string;
  }

  const { checkoutToken, locale = 'en', apiBase = '' }: Props = $props();

  const s = $derived(getReferralStrings(locale));

  let block = $state<ReferralBlock>({ show: false });
  /** 'copied' | 'failed' | '' — the line under the buttons. */
  let copyNote = $state<'' | 'copied' | 'failed'>('');

  const canShare = $derived(typeof navigator !== 'undefined' && canNativeShare(navigator));

  $effect(() => {
    const token = checkoutToken;
    const base = apiBase;
    let cancelled = false;
    block = { show: false };
    copyNote = '';
    if (!token) return;
    void loadReferralBlock(() => getCheckoutReferral(token, base)).then((b) => {
      if (!cancelled) block = b;
    });
    return () => {
      cancelled = true;
    };
  });

  async function copy(): Promise<void> {
    if (!block.show) return;
    const ok = await copyToClipboard(copyValue(block.data), typeof navigator === 'undefined' ? null : navigator);
    copyNote = ok ? 'copied' : 'failed';
  }

  async function share(): Promise<void> {
    if (!block.show || typeof navigator === 'undefined' || !canNativeShare(navigator)) return;
    try {
      await navigator.share(shareData(s, block.data));
    } catch {
      // The buyer closed the share sheet, or the browser refused: nothing to say.
    }
  }
</script>

{#if block.show}
  <section class="referral" aria-labelledby="arena-referral-title" data-testid="referral-block">
    <h3 id="arena-referral-title" class="referral-title">{s.title}</h3>
    <p class="referral-text" data-testid="referral-text">{referralText(s, block.data)}</p>
    <p class="referral-code-label">{s.code_label}</p>
    <p class="referral-code" data-testid="referral-code">{block.data.code}</p>
    <div class="referral-actions">
      <button type="button" class="referral-btn" onclick={copy} data-testid="referral-copy">
        {block.data.share_url ? s.copy_link : s.copy_code}
      </button>
      {#if canShare && block.data.share_url}
        <button type="button" class="referral-btn" onclick={share} data-testid="referral-share">{s.share}</button>
      {/if}
    </div>
    {#if copyNote === 'copied'}
      <p class="referral-note" role="status">{s.copied}</p>
    {:else if copyNote === 'failed'}
      <p class="referral-note referral-note--error" role="alert">{s.copy_failed}</p>
    {/if}
  </section>
{/if}

<style>
  .referral {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    box-sizing: border-box;
    padding: 1rem;
    border: 1px solid var(--arena-border-color, #e5e7eb);
    border-radius: var(--arena-radius, 8px);
    background: var(--arena-color-surface, transparent);
  }

  .referral-title {
    margin: 0;
    font-size: 1rem;
    font-weight: 600;
    color: var(--arena-color-primary, #1a1a1a);
  }

  .referral-text {
    margin: 0;
    font-size: 0.875rem;
    color: var(--arena-color-secondary, #6b7280);
  }

  .referral-code-label {
    margin: 0.25rem 0 0;
    font-size: 0.75rem;
    color: var(--arena-color-secondary, #6b7280);
  }

  /* The code is Latin letters and digits whatever the page language is. */
  .referral-code {
    margin: 0;
    font-family: monospace;
    font-size: 1.5rem;
    font-weight: 700;
    letter-spacing: 0.06em;
    word-break: break-all;
    user-select: all;
    direction: ltr;
    unicode-bidi: isolate;
    color: var(--arena-color-primary, #1a1a1a);
  }

  .referral-actions {
    display: flex;
    flex-wrap: wrap;
    justify-content: center;
    gap: 0.5rem;
  }

  .referral-btn {
    padding: 0.5rem 1.25rem;
    border-radius: var(--arena-radius, 8px);
    border: 1px solid var(--arena-border-color, #e5e7eb);
    background: transparent;
    color: var(--arena-color-primary, #1a1a1a);
    font-family: inherit;
    font-size: 0.9375rem;
    font-weight: 500;
    cursor: pointer;
  }

  .referral-btn:hover {
    background: rgba(0, 0, 0, 0.04);
  }

  .referral-note {
    margin: 0;
    font-size: 0.8125rem;
    color: var(--arena-color-secondary, #6b7280);
  }

  .referral-note--error {
    color: #b91c1c;
  }
</style>
