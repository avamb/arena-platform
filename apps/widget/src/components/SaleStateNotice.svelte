<script lang="ts">
  /**
   * SaleStateNotice — what a date says while nothing on it can be bought.
   *
   * A session that is "soon" (sales not started) or "sold_out" has every
   * category at available 0, so the widget prints ONE sentence instead of the
   * map and the category steppers. For "soon" it also offers "Notify me when
   * sales open": an e-mail, one consent box (unticked) and a button. The
   * platform answers 202 and sends a letter; nothing is subscribed until the
   * buyer opens it, so the form is replaced by "check your e-mail" and nothing
   * else is promised.
   */
  import type { FeedSession } from '../types.js';
  import { postSaleSubscription } from '../api.js';
  import {
    getSaleStateStrings,
    offersSaleSubscription,
    pausedSalesState,
    saleConsentText,
    submitSaleSubscription,
  } from '../lib/saleSubscribe.js';

  interface Props {
    session: FeedSession;
    feedToken: string;
    apiBase?: string;
    locale?: string;
    /** The organizer's name for the consent sentence; empty = the nameless wording. */
    orgName?: string;
  }

  const { session, feedToken, apiBase = '', locale = 'en', orgName = '' }: Props = $props();

  const s = $derived(getSaleStateStrings(locale));
  const state = $derived(pausedSalesState(session));
  const showForm = $derived(offersSaleSubscription(session));

  let email = $state('');
  let consent = $state(false);
  let sending = $state(false);
  let done = $state(false);
  let errorText = $state('');
  let errorField = $state<'email' | 'consent' | null>(null);

  // Element ids are unique per mounted notice (several widgets can share a page).
  const uid = Math.random().toString(36).slice(2, 8);
  const emailId = `sale-email-${uid}`;
  const consentId = `sale-consent-${uid}`;
  const errorId = `sale-error-${uid}`;

  async function onSubmit(e: Event): Promise<void> {
    e.preventDefault();
    if (sending) return;
    errorText = '';
    errorField = null;
    sending = true;
    const outcome = await submitSaleSubscription(
      { email, consent, locale, pageUrl: typeof window !== 'undefined' ? window.location.href : '' },
      (payload) => postSaleSubscription(feedToken, session.id, payload, apiBase),
    );
    sending = false;
    if (outcome.ok) {
      done = true;
      return;
    }
    errorText = outcome.message;
    errorField = outcome.field;
  }
</script>

{#if state}
  <div class="sale-state" data-testid="session-sales-state" data-state={state}>
    <p class="sale-notice" role="status" data-testid="session-sales-notice">
      {state === 'soon' ? s.soon_notice : s.sold_out_notice}
    </p>

    {#if showForm}
      {#if done}
        <p class="sale-done" role="status" data-testid="sale-subscribe-done">{s.done}</p>
      {:else}
        <form class="sale-form" data-testid="sale-subscribe-form" novalidate onsubmit={onSubmit}>
          <h3 class="sale-title">{s.form_title}</h3>

          <div class="field">
            <label class="field-label" for={emailId}>{s.email_label}</label>
            <input
              id={emailId}
              class="field-input"
              type="email"
              name="email"
              autocomplete="email"
              inputmode="email"
              required
              bind:value={email}
              aria-invalid={errorField === 'email'}
              aria-describedby={errorField === 'email' ? errorId : undefined}
            />
          </div>

          <div class="consent">
            <label class="consent-label" for={consentId}>
              <input
                id={consentId}
                class="consent-box"
                type="checkbox"
                name="consent"
                required
                bind:checked={consent}
                aria-invalid={errorField === 'consent'}
                aria-describedby={errorField === 'consent' ? errorId : undefined}
              />
              <span class="consent-text">{saleConsentText(locale, orgName)}</span>
            </label>
          </div>

          {#if errorText}
            <p class="sale-error" id={errorId} role="alert" data-testid="sale-subscribe-error">{errorText}</p>
          {/if}

          <button class="submit-btn" type="submit" disabled={sending} aria-busy={sending}>
            {sending ? s.sending : s.submit}
          </button>

          <p class="sale-note" data-testid="sale-subscribe-note">{s.note}</p>
        </form>
      {/if}
    {/if}
  </div>
{/if}

<style>
  .sale-state {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
    padding: 0.75rem 1rem;
    border-top: 1px solid var(--_border, #e5e7eb);
    text-align: start;
  }

  .sale-notice {
    margin: 0;
    font-size: 0.95rem;
    font-weight: 600;
    color: var(--arena-color-primary, #1a1a1a);
  }

  .sale-form {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  .sale-title {
    margin: 0;
    font-size: 1rem;
    font-weight: 700;
    color: var(--arena-color-primary, #1a1a1a);
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }

  .field-label {
    font-size: 0.875rem;
    font-weight: 500;
    color: var(--arena-color-primary, #1a1a1a);
  }

  .field-input {
    padding: 0.5rem 0.75rem;
    border: 1px solid var(--arena-border-color, #e5e7eb);
    border-radius: var(--arena-radius, 8px);
    font-size: 1rem;
    font-family: inherit;
    /* Own tokens, NOT --arena-color-primary: a dark host theme would turn the
       typing white on the white field (see BuyerForm). */
    color: var(--arena-input-color, #1a1a1a);
    background: var(--arena-input-bg, #fff);
    min-inline-size: 0;
    box-sizing: border-box;
    inline-size: 100%;
  }

  .field-input:focus-visible {
    border-color: var(--arena-accent, #4f46e5);
    outline: 3px solid var(--arena-focus-ring, var(--arena-accent, #4f46e5));
    outline-offset: 2px;
  }

  .field-input[aria-invalid='true'] {
    border-color: #b91c1c;
  }

  .consent-label {
    display: flex;
    align-items: flex-start;
    gap: 0.5rem;
    font-size: 0.875rem;
    line-height: 1.4;
    color: var(--arena-color-primary, #1a1a1a);
    text-align: start;
    cursor: pointer;
  }

  .consent-box {
    flex: none;
    margin-block-start: 0.15rem;
    inline-size: 1rem;
    block-size: 1rem;
    accent-color: var(--arena-accent, #4f46e5);
  }

  .consent-box:focus-visible {
    outline: 3px solid var(--arena-focus-ring, var(--arena-accent, #4f46e5));
    outline-offset: 2px;
  }

  .sale-error {
    margin: 0;
    font-size: 0.8125rem;
    color: #b91c1c;
  }

  .sale-note {
    margin: 0;
    font-size: 0.8125rem;
    line-height: 1.4;
    color: var(--arena-color-secondary, #6b7280);
  }

  .sale-done {
    margin: 0;
    padding: 0.75rem 1rem;
    font-size: 0.95rem;
    font-weight: 600;
    color: #166534;
    background: #dcfce7;
    border-radius: var(--arena-radius, 8px);
  }

  .submit-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 0.625rem 1.25rem;
    background: var(--_btn-bg, #fcdc54);
    color: var(--_btn-text, #101010);
    border: none;
    border-radius: var(--arena-radius, 8px);
    font-size: 1rem;
    font-family: inherit;
    font-weight: 700;
    cursor: pointer;
    transition: background-color 0.15s;
  }

  .submit-btn:hover:not(:disabled) {
    background: var(--_btn-bg-hover, #f5d23a);
  }

  .submit-btn:focus-visible {
    outline: 3px solid var(--arena-focus-ring, var(--arena-accent, #4f46e5));
    outline-offset: 3px;
  }

  .submit-btn:disabled {
    background: var(--_btn-disabled-bg, #e5e7eb);
    color: var(--_btn-disabled-text, #6b7280);
    cursor: not-allowed;
  }
</style>
