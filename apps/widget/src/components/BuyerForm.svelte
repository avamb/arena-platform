<script lang="ts">
  /**
   * BuyerForm — Buyer contact-info form for anonymous checkout (WID-D).
   *
   * Driven by the session's `buyer_fields` array:
   *   - email is always shown (mandatory for all checkouts)
   *   - name (one field: first and last name) and phone shown when their
   *     field is `enabled`; marked "(optional)" unless `required`
   *
   * Features:
   *  • Real-time email typo suggestions (gmail/outlook/yandex/seznam…)
   *  • Inline validation on submit
   *  • All copy localised (en/ru/cs/he)
   */

  import {
    validateBuyerForm,
    isBuyerFormValid,
    suggestEmailFix,
    interpolate,
    getCheckoutI18n,
    getPaymentCommitStrings,
    type BuyerFieldConfig,
    type BuyerFormValues,
    type BuyerFormErrors,
  } from '../lib/checkout.js';
  import {
    consentRequired,
    consentPayBlocked,
    consentSegments,
    safeConsentUrl,
    type SessionConsent,
  } from '../lib/consent.js';
  import { getConsentStrings } from '../lib/consentI18n.js';

  interface Props {
    buyerFields: BuyerFieldConfig[];
    locale?: string;
    submitting?: boolean;
    /**
     * Label of the submit button. The cart sheet passes "Pay 50.00 EUR" so
     * the press that creates the obligation to pay says so; absent, the
     * generic `submit_label` is used.
     */
    submitText?: string;
    /**
     * Show the "no right of withdrawal" notice directly above the submit
     * button (the session's `show_withdrawal_notice`).
     */
    withdrawalNotice?: boolean;
    /**
     * The session's `consent` block (P2). When it is required, a checkbox
     * "I accept the sale terms of the organizer … and the platform policy"
     * is shown above the pay button, which stays disabled until it is ticked.
     */
    consent?: SessionConsent | null;
    onSubmit: (values: BuyerFormValues) => void;
  }

  const {
    buyerFields,
    locale = 'en',
    submitting = false,
    submitText,
    withdrawalNotice = false,
    consent = null,
    onSubmit,
  }: Props = $props();

  // The checkout consent (P2). The tick lives in this component only: it is
  // never stored, so a buyer who comes back starts unticked.
  const needsConsent = $derived(consentRequired(consent));
  const consentWords = $derived(getConsentStrings(locale));
  const consentParts = $derived(consentSegments(locale, consent?.seller_legal_name));
  const termsHref = $derived(safeConsentUrl(consent?.terms_url));
  const privacyHref = $derived(safeConsentUrl(consent?.privacy_url));
  let consentAccepted = $state(false);
  const payWaitsForConsent = $derived(consentPayBlocked(needsConsent, consentAccepted));

  const t = $derived(getCheckoutI18n(locale));
  const commit = $derived(getPaymentCommitStrings(locale));

  // Form values
  let email = $state('');
  let name = $state('');
  let phone = $state('');

  // Errors shown after first submit attempt
  let errors = $state<BuyerFormErrors>({});
  let touched = $state(false);

  // Email typo suggestion
  let emailSuggestion = $state<string | null>(null);

  function onEmailInput(e: Event): void {
    email = (e.target as HTMLInputElement).value;
    emailSuggestion = suggestEmailFix(email);
    if (touched) {
      errors = validateBuyerForm({ email, name, phone }, buyerFields, locale as never);
    }
  }

  function acceptSuggestion(): void {
    email = emailSuggestion ?? email;
    emailSuggestion = null;
    if (touched) {
      errors = validateBuyerForm({ email, name, phone }, buyerFields, locale as never);
    }
  }

  function handleSubmit(e: Event): void {
    e.preventDefault();
    touched = true;
    const values: BuyerFormValues = { email, name, phone };
    errors = validateBuyerForm(values, buyerFields, locale as never);
    if (!isBuyerFormValid(errors)) return;
    if (payWaitsForConsent) return;
    if (needsConsent) values.consentAccepted = consentAccepted;
    onSubmit(values);
  }

  // Derived field visibility
  const nameField = $derived(buyerFields.find((f) => f.key === 'name'));
  const phoneField = $derived(buyerFields.find((f) => f.key === 'phone'));
  const showName = $derived(nameField?.enabled === true);
  const showPhone = $derived(phoneField?.enabled === true);
</script>

<form class="buyer-form" onsubmit={handleSubmit} novalidate>
  <!-- Email -->
  <div class="field" class:has-error={!!errors.email}>
    <label for="bf-email" class="field-label">{t.email_label}</label>
    <input
      id="bf-email"
      type="email"
      class="field-input"
      placeholder={t.email_placeholder}
      value={email}
      oninput={onEmailInput}
      autocomplete="email"
      aria-invalid={!!errors.email}
      aria-describedby={errors.email ? 'bf-email-err' : undefined}
      disabled={submitting}
    />
    {#if errors.email}
      <p id="bf-email-err" class="field-error" role="alert">{errors.email}</p>
    {:else if emailSuggestion}
      <p class="field-suggestion">
        {interpolate(t.email_suggestion, { suggestion: emailSuggestion })}
        <button type="button" class="suggestion-btn" onclick={acceptSuggestion}>
          {emailSuggestion}
        </button>
      </p>
    {/if}
  </div>

  <!-- Name (conditional) -->
  {#if showName}
    <div class="field" class:has-error={!!errors.name}>
      <label for="bf-name" class="field-label">
        {t.name_label}
        {#if !nameField?.required}<span class="optional" data-testid="bf-name-optional">{t.optional_label}</span>{/if}
      </label>
      <input
        id="bf-name"
        type="text"
        class="field-input"
        placeholder={t.name_placeholder}
        bind:value={name}
        autocomplete="name"
        aria-invalid={!!errors.name}
        aria-describedby={errors.name ? 'bf-name-err' : undefined}
        disabled={submitting}
      />
      {#if errors.name}
        <p id="bf-name-err" class="field-error" role="alert">{errors.name}</p>
      {/if}
    </div>
  {/if}

  <!-- Phone (conditional) -->
  {#if showPhone}
    <div class="field" class:has-error={!!errors.phone}>
      <label for="bf-phone" class="field-label">
        {t.phone_label}
        {#if !phoneField?.required}<span class="optional" data-testid="bf-phone-optional">{t.optional_label}</span>{/if}
      </label>
      <input
        id="bf-phone"
        type="tel"
        class="field-input"
        placeholder={t.phone_placeholder}
        bind:value={phone}
        autocomplete="tel"
        aria-invalid={!!errors.phone}
        aria-describedby={errors.phone ? 'bf-phone-err' : undefined}
        disabled={submitting}
      />
      {#if errors.phone}
        <p id="bf-phone-err" class="field-error" role="alert">{errors.phone}</p>
      {/if}
    </div>
  {/if}

  {#if needsConsent}
    <!-- P2: the buyer accepts the organizer's sale terms and the platform
         policy before the obligation to pay; the pay button waits for it. -->
    <div class="consent" data-testid="checkout-consent">
      <label class="consent-label">
        <input
          type="checkbox"
          class="consent-box"
          bind:checked={consentAccepted}
          disabled={submitting}
          aria-describedby={payWaitsForConsent ? 'bf-consent-hint' : undefined}
          data-testid="checkout-consent-box"
        />
        <span class="consent-text">
          {#each consentParts as part}
            {#if part.kind === 'terms' && termsHref}
              <a href={termsHref} target="_blank" rel="noopener noreferrer" title={consentWords.new_tab}>{part.text}</a>
            {:else if part.kind === 'privacy' && privacyHref}
              <a href={privacyHref} target="_blank" rel="noopener noreferrer" title={consentWords.new_tab}>{part.text}</a>
            {:else if part.kind === 'seller'}
              <bdi class="consent-seller">{part.text}</bdi>
            {:else}
              {part.text}
            {/if}
          {/each}
        </span>
      </label>
      {#if payWaitsForConsent}
        <p id="bf-consent-hint" class="consent-hint">{consentWords.hint}</p>
      {/if}
    </div>
  {/if}

  {#if withdrawalNotice}
    <!-- Date-bound tickets carry no right of withdrawal; say so right where the
         buyer takes on the obligation to pay. -->
    <p class="withdrawal-notice" data-testid="withdrawal-notice">{commit.withdrawal_notice}</p>
  {/if}

  <button type="submit" class="submit-btn" disabled={submitting || payWaitsForConsent} aria-busy={submitting}>
    {#if submitting}
      <span class="spinner" aria-hidden="true"></span>
    {/if}
    {submitText ?? t.submit_label}
  </button>
</form>

<style>
  .buyer-form {
    display: flex;
    flex-direction: column;
    gap: 1rem;
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

  .optional {
    font-weight: 400;
    color: var(--arena-color-secondary, #6b7280);
    margin-left: 0.25rem;
  }

  .field-input {
    padding: 0.5rem 0.75rem;
    border: 1px solid var(--arena-border-color, #e5e7eb);
    border-radius: var(--arena-radius, 8px);
    font-size: 1rem;
    font-family: inherit;
    /* Own tokens, NOT --arena-color-primary: a dark host theme sets the primary
       text colour to near-white, which on the hardcoded white field made the
       buyer's typing invisible (first production page, 2026-09-20). */
    color: var(--arena-input-color, #1a1a1a);
    background: var(--arena-input-bg, #fff);
    outline: none;
    transition: border-color 0.15s;
  }

  .field-input:focus,
  .field-input:focus-visible {
    border-color: var(--arena-accent, #4f46e5);
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--arena-accent, #4f46e5) 20%, transparent);
    outline: 3px solid var(--arena-focus-ring, var(--arena-accent, #4f46e5));
    outline-offset: 2px;
  }

  .has-error .field-input {
    border-color: #b91c1c;
  }

  .field-error {
    font-size: 0.8125rem;
    color: #b91c1c;
    margin: 0;
  }

  .field-suggestion {
    font-size: 0.8125rem;
    color: var(--arena-color-secondary, #6b7280);
    margin: 0;
  }

  .suggestion-btn {
    background: none;
    border: none;
    color: var(--arena-accent, #4f46e5);
    cursor: pointer;
    font-size: inherit;
    font-family: inherit;
    text-decoration: underline;
    padding: 0;
    margin-left: 0.25rem;
  }

  /* Checkout consent (P2): checkbox and text in the reading direction;
     logical properties only (he is rtl). */
  .consent {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
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

  .consent-text a {
    color: var(--arena-accent, #4f46e5);
    text-decoration: underline;
  }

  .consent-seller {
    font-weight: 600;
  }

  .consent-hint {
    margin: 0;
    font-size: 0.8125rem;
    color: var(--arena-color-secondary, #6b7280);
    text-align: start;
  }

  /* "No right of withdrawal" notice, directly above the pay button. Plain
     muted text in the reading direction of the widget (he is rtl); logical
     properties only. */
  .withdrawal-notice {
    margin: 0;
    font-size: 0.8125rem;
    line-height: 1.4;
    color: var(--arena-color-secondary, #6b7280);
    text-align: start;
  }

  /* The pay button — the primary CTA of the whole widget. Yellow fill, dark
     label; see the --_btn-* block on :host in ArenaTickets.svelte. */
  .submit-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.5rem;
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

  /* opacity stays 1: a washed-out yellow still looks pressable, so the fill
     itself goes neutral instead. */
  .submit-btn:disabled {
    background: var(--_btn-disabled-bg, #e5e7eb);
    color: var(--_btn-disabled-text, #6b7280);
    opacity: 1;
    cursor: not-allowed;
  }

  .suggestion-btn:focus-visible {
    outline: 2px solid var(--arena-focus-ring, var(--arena-accent, #4f46e5));
    outline-offset: 2px;
    border-radius: 2px;
  }

  /* RTL: form fields stack the same way, so no directional changes needed.
   * Text alignment inherits from the widget root direction. */

  .spinner {
    display: inline-block;
    width: 1em;
    height: 1em;
    /* currentColor, not white: the spinner sits on the yellow button and
       inherits its dark label colour. */
    border: 2px solid color-mix(in srgb, currentColor 35%, transparent);
    border-top-color: currentColor;
    border-radius: 50%;
    animation: spin 0.6s linear infinite;
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }
</style>
