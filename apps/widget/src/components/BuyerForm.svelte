<script lang="ts">
  /**
   * BuyerForm — Buyer contact-info form for anonymous checkout (WID-D).
   *
   * Driven by the session's `buyer_fields` array:
   *   - email is always shown (mandatory for all checkouts)
   *   - name and phone shown only when their field is `enabled`
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
    type BuyerFieldConfig,
    type BuyerFormValues,
    type BuyerFormErrors,
  } from '../lib/checkout.js';

  interface Props {
    buyerFields: BuyerFieldConfig[];
    locale?: string;
    submitting?: boolean;
    onSubmit: (values: BuyerFormValues) => void;
  }

  const {
    buyerFields,
    locale = 'en',
    submitting = false,
    onSubmit,
  }: Props = $props();

  const t = $derived(getCheckoutI18n(locale));

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
        {#if !nameField?.required}<span class="optional">(optional)</span>{/if}
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
        {#if !phoneField?.required}<span class="optional">(optional)</span>{/if}
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

  <button type="submit" class="submit-btn" disabled={submitting} aria-busy={submitting}>
    {#if submitting}
      <span class="spinner" aria-hidden="true"></span>
    {/if}
    {t.submit_label}
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
    color: var(--arena-color-primary, #1a1a1a);
    background: #fff;
    outline: none;
    transition: border-color 0.15s;
  }

  .field-input:focus {
    border-color: var(--arena-accent, #6366f1);
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--arena-accent, #6366f1) 20%, transparent);
  }

  .has-error .field-input {
    border-color: #dc2626;
  }

  .field-error {
    font-size: 0.8125rem;
    color: #dc2626;
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
    color: var(--arena-accent, #6366f1);
    cursor: pointer;
    font-size: inherit;
    font-family: inherit;
    text-decoration: underline;
    padding: 0;
    margin-left: 0.25rem;
  }

  .submit-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.5rem;
    padding: 0.625rem 1.25rem;
    background: var(--arena-accent, #6366f1);
    color: #fff;
    border: none;
    border-radius: var(--arena-radius, 8px);
    font-size: 1rem;
    font-family: inherit;
    font-weight: 500;
    cursor: pointer;
    transition: opacity 0.15s;
  }

  .submit-btn:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .spinner {
    display: inline-block;
    width: 1em;
    height: 1em;
    border: 2px solid rgba(255, 255, 255, 0.4);
    border-top-color: #fff;
    border-radius: 50%;
    animation: spin 0.6s linear infinite;
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }
</style>
