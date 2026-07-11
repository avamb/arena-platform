<script lang="ts">
  /**
   * OrderStatus — Order status view returned via the ?checkout_token=… deep-link (WID-D).
   *
   * Handles three terminal states:
   *   paid    → success panel with order ref, seat list, inline human_code + PDF link
   *             + "Resend tickets" CTA
   *   expired → "Your hold expired" panel with "Reclaim seats" CTA (WID-0c)
   *   failed  → "Payment failed" panel with "Try again" CTA
   *
   * And one transient state:
   *   pending → spinner with "Processing…" copy
   *
   * All copy is localised (en/ru/cs/he).
   */

  import { getCheckoutI18n } from '../lib/checkout.js';
  import type {
    CheckoutStatusResponse,
    CheckoutStatusTicketItem,
  } from '../lib/checkout.js';

  interface Props {
    status: CheckoutStatusResponse;
    locale?: string;
    /** Called when buyer clicks "Reclaim seats" (expired state). */
    onRecover?: () => void;
    /** Called when buyer clicks "Try again" (failed state). */
    onRetry?: () => void;
    /** Called when buyer clicks "Resend tickets" (paid state). */
    onSendAgain?: () => void;
    /** Whether a recovery or resend action is in flight. */
    actionLoading?: boolean;
    /** Error from a recovery or resend action. */
    actionError?: string | null;
  }

  const {
    status,
    locale = 'en',
    onRecover,
    onRetry,
    onSendAgain,
    actionLoading = false,
    actionError = null,
  }: Props = $props();

  const t = $derived(getCheckoutI18n(locale));

  function seatLabel(ticket: CheckoutStatusTicketItem): string {
    const parts: string[] = [];
    if (ticket.sector) parts.push(ticket.sector);
    if (ticket.row) parts.push(`Row ${ticket.row}`);
    if (ticket.number) parts.push(`Seat ${ticket.number}`);
    return parts.join(', ') || ticket.ticket_id;
  }
</script>

<div class="order-status" data-status={status.status}>

  {#if status.status === 'pending'}
    <!-- ── Pending ─────────────────────────────────────────────────────────── -->
    <div class="status-pending" aria-live="polite" aria-busy="true">
      <span class="spinner large" aria-hidden="true"></span>
      <p class="status-message">{t.status_pending}</p>
    </div>

  {:else if status.status === 'paid'}
    <!-- ── Paid / Success ─────────────────────────────────────────────────── -->
    <div class="status-paid">
      <div class="status-icon success" aria-hidden="true">✓</div>
      <h2 class="status-title">{t.status_paid}</h2>

      {#if status.checkout_session_id}
        <p class="order-ref">
          <span class="label">{t.order_ref_label}:</span>
          <span class="value" data-testid="order-ref">{status.checkout_session_id.slice(0, 8).toUpperCase()}</span>
        </p>
      {/if}

      {#if status.tickets && status.tickets.length > 0}
        <div class="tickets-section">
          <h3 class="section-heading">{t.ticket_heading}</h3>
          <ul class="ticket-list" role="list">
            {#each status.tickets as ticket (ticket.ticket_id)}
              <li class="ticket-item">
                <div class="ticket-seat">{seatLabel(ticket)}</div>
                {#if ticket.human_code}
                  <div class="ticket-code">
                    <span class="code-label">{t.human_code_label}:</span>
                    <span class="code-value" data-testid="human-code">{ticket.human_code}</span>
                  </div>
                {/if}
                {#if ticket.pdf_url}
                  <a
                    class="pdf-link"
                    href={ticket.pdf_url}
                    target="_blank"
                    rel="noopener noreferrer"
                    data-testid="pdf-link"
                  >
                    {t.download_pdf}
                  </a>
                {/if}
              </li>
            {/each}
          </ul>
        </div>
      {/if}

      {#if actionError}
        <p class="action-error" role="alert">{actionError}</p>
      {/if}

      {#if onSendAgain}
        <button
          type="button"
          class="action-btn secondary"
          onclick={onSendAgain}
          disabled={actionLoading}
          aria-busy={actionLoading}
        >
          {t.send_again}
        </button>
      {/if}
    </div>

  {:else if status.status === 'expired'}
    <!-- ── Expired ─────────────────────────────────────────────────────────── -->
    <div class="status-expired">
      <div class="status-icon warning" aria-hidden="true">⏱</div>
      <h2 class="status-title">{t.status_expired}</h2>

      {#if actionError}
        <p class="action-error" role="alert">{actionError}</p>
      {/if}

      {#if onRecover}
        <button
          type="button"
          class="action-btn primary"
          onclick={onRecover}
          disabled={actionLoading}
          aria-busy={actionLoading}
        >
          {#if actionLoading}<span class="spinner small" aria-hidden="true"></span>{/if}
          {t.recover_label}
        </button>
      {/if}
    </div>

  {:else}
    <!-- ── Failed / abandoned ─────────────────────────────────────────────── -->
    <div class="status-failed">
      <div class="status-icon error" aria-hidden="true">✕</div>
      <h2 class="status-title">{t.status_failed}</h2>

      {#if actionError}
        <p class="action-error" role="alert">{actionError}</p>
      {/if}

      {#if onRetry}
        <button
          type="button"
          class="action-btn primary"
          onclick={onRetry}
          disabled={actionLoading}
        >
          {t.retry_label}
        </button>
      {/if}
    </div>
  {/if}

</div>

<style>
  .order-status {
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: 2rem 1rem;
    text-align: center;
    gap: 1rem;
  }

  /* Pending */
  .status-pending {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 1rem;
  }

  /* Paid / expired / failed share the same layout */
  .status-paid,
  .status-expired,
  .status-failed {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 1rem;
    width: 100%;
    max-width: 480px;
  }

  .status-icon {
    width: 3rem;
    height: 3rem;
    border-radius: 50%;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 1.5rem;
    font-weight: 700;
  }

  .status-icon.success { background: #dcfce7; color: #16a34a; }
  .status-icon.warning { background: #fef9c3; color: #ca8a04; }
  .status-icon.error   { background: #fee2e2; color: #dc2626; }

  .status-title {
    font-size: 1.25rem;
    font-weight: 600;
    margin: 0;
    color: var(--arena-color-primary, #1a1a1a);
  }

  .status-message {
    color: var(--arena-color-secondary, #6b7280);
    margin: 0;
  }

  .order-ref {
    font-size: 0.875rem;
    color: var(--arena-color-secondary, #6b7280);
    margin: 0;
  }

  .order-ref .label { font-weight: 500; }
  .order-ref .value { font-family: monospace; margin-left: 0.25rem; }

  /* Tickets */
  .tickets-section {
    width: 100%;
    text-align: left;
  }

  .section-heading {
    font-size: 0.9375rem;
    font-weight: 600;
    margin: 0 0 0.5rem;
    color: var(--arena-color-primary, #1a1a1a);
  }

  .ticket-list {
    list-style: none;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  .ticket-item {
    border: 1px solid var(--arena-border-color, #e5e7eb);
    border-radius: var(--arena-radius, 8px);
    padding: 0.75rem;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }

  .ticket-seat {
    font-weight: 500;
    font-size: 0.9375rem;
  }

  .ticket-code {
    font-size: 0.875rem;
    color: var(--arena-color-secondary, #6b7280);
  }

  .code-label { font-weight: 500; }
  .code-value {
    font-family: monospace;
    font-size: 1.0625rem;
    letter-spacing: 0.04em;
    color: var(--arena-color-primary, #1a1a1a);
    margin-left: 0.25rem;
  }

  .pdf-link {
    display: inline-block;
    font-size: 0.8125rem;
    color: var(--arena-accent, #6366f1);
    text-decoration: underline;
    cursor: pointer;
  }

  /* CTAs */
  .action-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.5rem;
    padding: 0.625rem 1.5rem;
    border-radius: var(--arena-radius, 8px);
    font-size: 1rem;
    font-family: inherit;
    font-weight: 500;
    cursor: pointer;
    border: none;
    transition: opacity 0.15s;
  }

  .action-btn:disabled { opacity: 0.6; cursor: not-allowed; }

  .action-btn.primary {
    background: var(--arena-accent, #6366f1);
    color: #fff;
  }

  .action-btn.secondary {
    background: transparent;
    border: 1px solid var(--arena-border-color, #e5e7eb);
    color: var(--arena-color-primary, #1a1a1a);
  }

  .action-error {
    font-size: 0.875rem;
    color: #dc2626;
    background: #fef2f2;
    padding: 0.5rem 0.75rem;
    border-radius: var(--arena-radius, 8px);
    margin: 0;
    width: 100%;
    text-align: left;
  }

  /* Spinners */
  .spinner {
    display: inline-block;
    border-radius: 50%;
    border-style: solid;
    border-color: rgba(0, 0, 0, 0.1);
    border-top-color: currentColor;
    animation: spin 0.6s linear infinite;
  }

  .spinner.large {
    width: 2.5rem;
    height: 2.5rem;
    border-width: 3px;
  }

  .spinner.small {
    width: 1em;
    height: 1em;
    border-width: 2px;
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }
</style>
