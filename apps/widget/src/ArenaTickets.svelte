<svelte:options
  customElement={{
    tag: 'arena-tickets',
    props: {
      feedToken: { type: 'String', attribute: 'feed-token' },
      sessionId: { type: 'String', attribute: 'session-id' },
      locale: { type: 'String', attribute: 'locale' },
    },
  }}
/>

<script lang="ts">
  /**
   * ArenaTickets — root Web Component for the Arena ticket-purchase widget.
   *
   * WID-R1: wires the full purchase loop:
   *   selecting → (mini-cart) → cart sheet / buyer form → redirecting → order-status
   */
  import { onMount } from 'svelte';
  import { parseLocale, parseFeedToken, parseSessionId, isRtlLocale } from './utils.js';
  import { fetchFeedEvent, postCheckoutStart, getCheckoutStatus, postCheckoutRecover, ApiError } from './api.js';
  import type { FeedSession, FeedEvent, Geometry, CategoryPrice, SeatStatusValue } from './types.js';
  import type { BuyerFormValues } from './lib/checkout.js';
  import { buildCheckoutPayload, getCheckoutI18n } from './lib/checkout.js';
  import { removeCartLine } from './lib/cart.js';
  import { toggleSeatSelection } from './lib/selection.js';
  import {
    saveCheckoutToken,
    restoreCheckoutToken,
    clearCheckoutToken,
    getCheckoutTokenFromSearch,
    buildCartFromSelection,
    buildSeatCategoryIndex,
    buildCategoryByIndex,
    buildTierById,
    identifyGaTiers,
    buildGaItems,
    totalSelectionCount,
    type WidgetStage,
  } from './lib/store.js';
  import type { CheckoutStatusResponse } from './lib/checkout.js';
  import SessionList from './components/SessionList.svelte';
  import SeatMapView from './components/SeatMapView.svelte';
  import MiniCart from './components/MiniCart.svelte';
  import CartSheet from './components/CartSheet.svelte';
  import GaTierCard from './components/GaTierCard.svelte';
  import OrderStatus from './components/OrderStatus.svelte';

  interface Props {
    /** The public feed token identifying the event/catalogue. */
    feedToken?: string;
    /** Optional session ID to deep-link into a specific event session. */
    sessionId?: string;
    /** BCP-47 locale tag, e.g. "en", "ru", "de". Defaults to "en". */
    locale?: string;
  }

  const { feedToken = '', sessionId = '', locale = 'en' }: Props = $props();

  const normLocale = $derived(parseLocale(locale));
  const normFeedToken = $derived(parseFeedToken(feedToken));
  const normSessionId = $derived(parseSessionId(sessionId));
  const hasToken = $derived(normFeedToken !== '');
  const dir = $derived(isRtlLocale(normLocale) ? 'rtl' : 'ltr');
  const t = $derived(getCheckoutI18n(normLocale));

  // ── Event data ─────────────────────────────────────────────────────────────

  let event = $state<FeedEvent | null>(null);
  let selectedSession = $state<FeedSession | null>(null);
  let loading = $state(false);
  let loadError = $state<string | null>(null);

  // ── Widget stage ───────────────────────────────────────────────────────────

  let stage = $state<WidgetStage>('selecting');
  let cartSheetOpen = $state(false);

  // ── Selection state ────────────────────────────────────────────────────────

  let selectedSeatKeys = $state<ReadonlySet<string>>(new Set());
  let gaQuantities = $state<ReadonlyMap<string, number>>(new Map());

  // ── Schema index maps (built from onSchemaLoaded) ──────────────────────────

  let seatCategoryIndex = $state<ReadonlyMap<string, number>>(new Map());
  let categoryByCategoryIndex = $state<ReadonlyMap<number, CategoryPrice>>(new Map());
  let tierById = $state<ReadonlyMap<string, Tier>>(new Map());
  let gaTiers = $state<import('./types.js').Tier[]>([]);

  // ── Checkout state ─────────────────────────────────────────────────────────

  let checkoutToken = $state<string | null>(null);
  /**
   * ISO-8601 expiry from the last successful postCheckoutStart or
   * postCheckoutRecover response.  Used to drive the hold countdown timer
   * in MiniCart and CartSheet while the user fills in their details.
   */
  let holdExpiresAt = $state<string | null>(null);
  let checkoutSubmitting = $state(false);
  let checkoutError = $state<string | null>(null);
  let orderStatus = $state<CheckoutStatusResponse | null>(null);
  let orderActionLoading = $state(false);
  let orderActionError = $state<string | null>(null);

  // ── Derived cart ───────────────────────────────────────────────────────────

  const cart = $derived(
    selectedSession
      ? buildCartFromSelection({
          selectedSeatKeys,
          gaQuantities,
          session: selectedSession,
          seatCategoryIndex,
          categoryByCategoryIndex,
          tierById,
        })
      : { checkoutToken: null, expiresAt: null, lines: [] }
  );

  /**
   * Cart with the hold expiry merged in.  `buildCartFromSelection` always
   * returns `expiresAt: null`; `holdExpiresAt` is set after a successful
   * postCheckoutStart / postCheckoutRecover so the countdown timer in
   * MiniCart and CartSheet has a real value to tick down from.
   */
  const effectiveCart = $derived({ ...cart, expiresAt: holdExpiresAt });

  const cartCount = $derived(totalSelectionCount(selectedSeatKeys, gaQuantities));

  // ── Helpers ────────────────────────────────────────────────────────────────

  /**
   * Pick the initial session when the event loads.
   * Prefers the deep-linked sessionId, then the first non-cancelled upcoming session.
   */
  function pickInitialSession(ev: FeedEvent, deepLinkId: string): FeedSession | null {
    if (deepLinkId) {
      const found = ev.sessions.find((s) => s.id === deepLinkId);
      if (found) return found;
    }
    const upcoming = ev.sessions
      .filter((s) => s.status !== 'cancelled')
      .sort((a, b) => a.start_at.localeCompare(b.start_at));
    return upcoming[0] ?? ev.sessions[0] ?? null;
  }

  // ── Feed loading ────────────────────────────────────────────────────────────

  onMount(() => {
    // Check for checkout_token in URL or sessionStorage first.
    const urlToken = getCheckoutTokenFromSearch(window.location.search);
    const storedToken = restoreCheckoutToken();
    const resumeToken = urlToken ?? storedToken;

    if (resumeToken) {
      // Restore order status view.
      checkoutToken = resumeToken;
      stage = 'order-status';
      loadOrderStatus(resumeToken);
      return;
    }

    if (!normFeedToken || !normSessionId) return;

    loading = true;
    loadError = null;

    // Set a minimal synthetic event so the seat map still renders.
    event = {
      id: normSessionId,
      org_id: '',
      name: '',
      status: 'published',
      start_at: new Date().toISOString(),
      end_at: new Date().toISOString(),
      visibility: 'public',
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
      sessions: [
        {
          id: normSessionId,
          start_at: new Date().toISOString(),
          end_at: new Date().toISOString(),
          capacity_total: 0,
          status: 'published',
          admission_mode: 'assigned_seats',
          schema_url: `/v1/event-sessions/${normSessionId}/schema`,
          seat_status_url: `/v1/event-sessions/${normSessionId}/seat-status`,
          buyer_fields: [],
          tiers: [],
        },
      ],
    };
    selectedSession = event.sessions[0] ?? null;
    loading = false;
  });

  // If an event-id is ever added as an attribute, load via the feed.
  async function loadFromFeed(token: string, eventId: string): Promise<void> {
    loading = true;
    loadError = null;
    try {
      const data = await fetchFeedEvent(token, eventId);
      event = data.event;
      selectedSession = pickInitialSession(data.event, normSessionId);
    } catch (err) {
      loadError = err instanceof Error ? err.message : 'Failed to load event';
    } finally {
      loading = false;
    }
  }

  // Expose for future use.
  void loadFromFeed;

  // ── Schema loaded callback ─────────────────────────────────────────────────

  function onSchemaLoaded(geometry: Geometry, categoryPrices: CategoryPrice[]): void {
    seatCategoryIndex = buildSeatCategoryIndex(geometry);
    categoryByCategoryIndex = buildCategoryByIndex(categoryPrices);
    tierById = selectedSession ? buildTierById(selectedSession.tiers) : new Map();
    gaTiers = selectedSession ? identifyGaTiers(selectedSession.tiers, categoryPrices) : [];
  }

  // ── Seat tap handler ───────────────────────────────────────────────────────

  function onSeatTap(seatKey: string, status: SeatStatusValue): void {
    selectedSeatKeys = toggleSeatSelection(selectedSeatKeys, seatKey, status);
  }

  // ── GA quantity handler ────────────────────────────────────────────────────

  function onGaQuantityChange(tierId: string, qty: number): void {
    const next = new Map(gaQuantities);
    next.set(tierId, qty);
    gaQuantities = next;
  }

  // ── Cart sheet handlers ────────────────────────────────────────────────────

  function openCartSheet(): void {
    cartSheetOpen = true;
  }

  function closeCartSheet(): void {
    cartSheetOpen = false;
  }

  function handleRemoveLine(idx: number): void {
    // Remove from cart lines — requires rebuilding selection/ga state accordingly.
    // For simplicity, we remove the line from the cart.lines concept by adjusting
    // the underlying state (clear the corresponding seats or GA entry).
    const line = cart.lines[idx];
    if (!line) return;
    if (line.type === 'seated') {
      // Remove these specific seat keys from selection.
      const next = new Set(selectedSeatKeys);
      for (const key of line.seatKeys) next.delete(key);
      selectedSeatKeys = next;
    } else if (line.type === 'ga') {
      const next = new Map(gaQuantities);
      next.delete(line.tierId);
      gaQuantities = next;
    }
  }

  // ── Checkout ───────────────────────────────────────────────────────────────

  async function handleCheckout(values: BuyerFormValues): Promise<void> {
    if (!selectedSession || !normFeedToken) return;
    checkoutSubmitting = true;
    checkoutError = null;
    try {
      const seats = [...selectedSeatKeys];
      const gaItems = buildGaItems(gaQuantities);
      const payload = buildCheckoutPayload(
        selectedSession.id,
        values,
        seats,
        gaItems,
        selectedSession.buyer_fields as import('./lib/checkout.js').BuyerFieldConfig[],
      );
      const response = await postCheckoutStart(normFeedToken, payload);
      // Save token in case user returns after the payment page.
      saveCheckoutToken(response.checkout_token);
      checkoutToken = response.checkout_token;
      // Store the hold expiry so MiniCart/CartSheet can show the countdown
      // during the brief redirecting stage (WID-S1 fix #3 + #4).
      holdExpiresAt = response.expires_at;
      stage = 'redirecting';
      // Redirect to payment provider.
      window.location.href = response.redirect_url;
    } catch (err) {
      checkoutError = err instanceof Error ? err.message : 'Checkout failed. Please try again.';
    } finally {
      checkoutSubmitting = false;
    }
  }

  // ── Order status ───────────────────────────────────────────────────────────

  async function loadOrderStatus(token: string): Promise<void> {
    try {
      orderStatus = await getCheckoutStatus(token);
    } catch (err) {
      // Stale / invalid checkout token (401 or 404) → clear it from storage so
      // the widget doesn't brick on the next page refresh (WID-S1 fix #5).
      const apiErr = err as ApiError;
      if (apiErr?.status === 401 || apiErr?.status === 404) {
        clearCheckoutToken();
        checkoutToken = null;
      }
      loadError = err instanceof Error ? err.message : 'Failed to load order status';
      stage = 'selecting';
    }
  }

  async function handleRecover(): Promise<void> {
    if (!checkoutToken) return;
    orderActionLoading = true;
    orderActionError = null;
    try {
      const recovered = await postCheckoutRecover(checkoutToken);
      // Update hold expiry with the fresh timestamp from recovery (WID-S1 fix #3).
      holdExpiresAt = recovered.expires_at;
      // Re-load status after recovery attempt.
      orderStatus = await getCheckoutStatus(checkoutToken);
    } catch (err) {
      orderActionError = err instanceof Error ? err.message : 'Recovery failed. Please try again.';
    } finally {
      orderActionLoading = false;
    }
  }

  function handleRetry(): void {
    // Clear token and return to selecting stage.
    clearCheckoutToken();
    checkoutToken = null;
    orderStatus = null;
    selectedSeatKeys = new Set();
    gaQuantities = new Map();
    stage = 'selecting';
    cartSheetOpen = false;
  }

  // Import Tier type for use inside the script
  type Tier = import('./types.js').Tier;
</script>

<!--
  Arena Tickets Widget — Shadow DOM root.
  Theme is controlled via CSS custom properties on the host.
-->
<div
  class="arena-tickets-root"
  data-locale={normLocale}
  data-feed-token={normFeedToken}
  data-session-id={normSessionId}
  aria-label="Arena Tickets"
  role="region"
  dir={dir}
>
  {#if stage === 'order-status' && orderStatus}
    <!-- ── Order status view ──────────────────────────────────────────────── -->
    <div class="arena-tickets-frame">
      <OrderStatus
        status={orderStatus}
        locale={normLocale}
        onRecover={handleRecover}
        onRetry={handleRetry}
        actionLoading={orderActionLoading}
        actionError={orderActionError}
      />
    </div>

  {:else if stage === 'redirecting'}
    <!-- ── Redirecting to payment ─────────────────────────────────────────── -->
    <div class="arena-tickets-frame">
      <div class="arena-tickets-loading" aria-live="polite" aria-busy="true">{t.redirecting_to_payment}</div>
    </div>

  {:else if hasToken}
    <!-- ── Selecting / cart stage ─────────────────────────────────────────── -->
    <div class="arena-tickets-frame">
      {#if loading}
        <div class="arena-tickets-loading" aria-live="polite" aria-busy="true">{t.loading}</div>
      {:else if loadError}
        <div class="arena-tickets-error" role="alert">{loadError}</div>
      {:else if event && event.sessions.length > 0}
        <!-- Session date chips + legend -->
        <SessionList
          sessions={event.sessions}
          {selectedSession}
          onSelectSession={(s) => {
            // Reset seat/GA selection when switching sessions so stale keys
            // from a different session don't carry over (WID-S1 fix #6).
            if (s?.id !== selectedSession?.id) {
              selectedSeatKeys = new Set();
              gaQuantities = new Map();
              holdExpiresAt = null;
              cartSheetOpen = false;
            }
            selectedSession = s;
          }}
        />
        <!-- Seat map (only for sessions with schema_url) -->
        {#if selectedSession && selectedSession.schema_url}
          <SeatMapView
            session={selectedSession}
            locale={normLocale}
            selectedKeys={selectedSeatKeys}
            {onSeatTap}
            {onSchemaLoaded}
          />
        {:else if selectedSession}
          <div class="arena-tickets-ga" aria-label="General admission session">
            <!-- GA tier list -->
          </div>
        {/if}

        <!-- GA tier cards (shown below the map for hybrid/GA sessions) -->
        {#if gaTiers.length > 0}
          <div class="ga-tiers-section">
            {#each gaTiers as tier (tier.id)}
              <GaTierCard
                {tier}
                quantity={gaQuantities.get(tier.id) ?? 0}
                onQuantityChange={onGaQuantityChange}
              />
            {/each}
          </div>
        {/if}

        <!-- Mini cart bar -->
        {#if selectedSession}
          <MiniCart
            lines={cart.lines}
            expiresAt={holdExpiresAt}
            locale={normLocale}
            onOpen={openCartSheet}
          />
        {/if}

      {:else if !normSessionId}
        <div class="arena-tickets-placeholder" aria-hidden="true"></div>
      {/if}
    </div>

    <!-- Cart sheet (bottom drawer) -->
    {#if cartSheetOpen && selectedSession}
      <CartSheet
        cart={effectiveCart}
        buyerFields={selectedSession.buyer_fields as import('./lib/checkout.js').BuyerFieldConfig[]}
        locale={normLocale}
        submitting={checkoutSubmitting}
        submitError={checkoutError}
        onClose={closeCartSheet}
        onRemoveLine={handleRemoveLine}
        onCheckout={handleCheckout}
      />
    {/if}

  {:else}
    <div class="arena-tickets-placeholder" aria-hidden="true">
      <!-- No feed-token provided -->
    </div>
  {/if}
</div>

<style>
  :host {
    display: block;
    box-sizing: border-box;
    font-family: var(--arena-font-family, system-ui, -apple-system, sans-serif);
    color: var(--arena-color-primary, #1a1a1a);
    background: var(--arena-bg, transparent);
    --_accent: var(--arena-accent, #4f46e5);
    --_radius: var(--arena-radius, 8px);
    --_border: var(--arena-border-color, #e5e7eb);
    --_text-muted: var(--arena-color-secondary, #6b7280);
    /* Focus ring — defaults to accent colour. Override with --arena-focus-ring. */
    --_focus-ring: var(--arena-focus-ring, var(--arena-accent, #4f46e5));
  }

  /* Global focus-visible rule for all focusable children. */
  :host *:focus-visible {
    outline: 3px solid var(--_focus-ring);
    outline-offset: 2px;
  }

  .arena-tickets-root {
    display: block;
    width: 100%;
    height: 100%;
  }

  .arena-tickets-frame {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 400px;
    border: 1px solid var(--_border);
    border-radius: var(--_radius);
    overflow: hidden;
  }

  .arena-tickets-loading {
    display: flex;
    align-items: center;
    justify-content: center;
    flex: 1;
    color: var(--_text-muted);
    font-size: 0.9rem;
  }

  .arena-tickets-error {
    padding: 1rem;
    color: #b91c1c;
    background: #fef2f2;
    border-radius: var(--_radius);
    margin: 1rem;
  }

  .arena-tickets-ga {
    flex: 1;
    padding: 1rem;
  }

  .arena-tickets-placeholder {
    display: none;
  }

  .ga-tiers-section {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    padding: 0.75rem 1rem;
    border-top: 1px solid var(--_border);
  }

  /* ── RTL layout adjustments ─────────────────────────────────────────────── */
  [dir='rtl'] {
    text-align: start;
    direction: rtl;
  }

  [dir='rtl'] .arena-tickets-frame {
    /* Flex direction and border radius are direction-agnostic; no change needed. */
  }
</style>
