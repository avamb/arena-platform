<svelte:options
  customElement={{
    tag: 'arena-tickets',
    props: {
      feedToken: { type: 'String', attribute: 'feed-token' },
      eventId: { type: 'String', attribute: 'event-id' },
      sessionId: { type: 'String', attribute: 'session-id' },
      locale: { type: 'String', attribute: 'locale' },
      apiBase: { type: 'String', attribute: 'api-base' },
      cover: { type: 'String', attribute: 'cover' },
      sessions: { type: 'String', attribute: 'sessions' },
      frame: { type: 'String', attribute: 'frame' },
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
  import { fetchFeedEvent, fetchFeedEvents, postCheckoutStart, getCheckoutStatus, postCheckoutRecover, ApiError } from './api.js';
  import type { FeedSession, FeedEvent, Geometry, CategoryPrice, SeatStatusValue } from './types.js';
  import type { BuyerFormValues } from './lib/checkout.js';
  import { buildCheckoutPayload, getCheckoutI18n, parseConflictsFromApiError, conflictKeySet, filterCartWithoutConflicts } from './lib/checkout.js';
  import { toggleSeatSelection } from './lib/selection.js';
  import { dispatchWidgetEvent, ARENA_EVENTS } from './lib/events.js';
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
    planlessGaTiers,
    identifyGaAreas,
    buildGaItems,
    totalSelectionCount,
    type WidgetStage,
    type GaArea,
  } from './lib/store.js';
  import type { CheckoutStatusResponse } from './lib/checkout.js';
  import SessionList from './components/SessionList.svelte';
  import SeatMapView from './components/SeatMapView.svelte';
  import MiniCart from './components/MiniCart.svelte';
  import CartSheet from './components/CartSheet.svelte';
  import GaTierCard from './components/GaTierCard.svelte';
  import GaAreaPopover from './components/GaAreaPopover.svelte';
  import OrderStatus from './components/OrderStatus.svelte';

  interface Props {
    /** The public feed token identifying the event/catalogue. */
    feedToken?: string;
    /** The event UUID to load via the public feed. Required for live data. */
    eventId?: string;
    /** Optional session ID to deep-link into a specific event session. */
    sessionId?: string;
    /** BCP-47 locale tag, e.g. "en", "ru", "de". Defaults to "en". */
    locale?: string;
    /**
     * Base URL of the Arena API server (e.g. "https://api.tickets.example.com").
     * When omitted, the widget tries to infer the origin from the script src
     * attribute on mount. Falls back to relative URLs (same-origin proxy).
     */
    apiBase?: string;
    /** 'hidden' drops the poster cover. The hosted promoter page already
     * shows the season's artwork above the date list, so repeating it
     * inside every opened row is the same picture twice on one screen.
     * Anything else (or absent) keeps the cover. */
    cover?: string;
    /** 'hidden' drops the session date chips WHEN THE EVENT HAS ONLY ONE
     * session — the hosted promoter page already prints that date in the
     * row the picker opens under. With two or more sessions the chips are
     * the only way to choose between them, so they are always shown and
     * this attribute is ignored. */
    sessions?: string;
    /** 'hidden' drops the widget's own outer border and rounded corners.
     * The hosted promoter page draws one card per date and mounts the
     * picker INSIDE it, so the widget's frame is a second box around the
     * first — nested outlines are what made one date read as two separate
     * things. Anything else (or absent) keeps the frame, which is what a
     * standalone embed on someone else's site wants. */
    frame?: string;
  }

  const { feedToken = '', eventId = '', sessionId = '', locale = 'en', apiBase = '', cover = '', sessions = '', frame = '' }: Props = $props();

  /**
   * Host element reference for CustomEvent dispatch (WID-S5).
   * $host() returns the <arena-tickets> DOM node when compiled as a custom
   * element; events dispatched from it with composed:true are observable
   * by host pages outside the Shadow DOM.
   */
  const host = $host<HTMLElement>();

  const normLocale = $derived(parseLocale(locale));
  const normFeedToken = $derived(parseFeedToken(feedToken));
  const normEventId = $derived(parseSessionId(eventId)); // reuse UUID parser
  const normSessionId = $derived(parseSessionId(sessionId));
  // Case- and space-insensitive so cover="Hidden" behaves; any other value
  // keeps the cover, since a typo must not silently strip the artwork.
  const coverHidden = $derived(cover.trim().toLowerCase() === 'hidden');
  const frameHidden = $derived(frame.trim().toLowerCase() === 'hidden');

  /**
   * Scope for the stored checkout token (see `checkoutTokenKey`).
   *
   * `tickets.arenasoldout.com` serves every promoter's every event from ONE
   * origin, so an origin-wide sessionStorage key let one event's checkout
   * resume on another event's page. Scoping by the mounted event (or session)
   * keeps each page's checkout to itself; an embed that sets neither keeps the
   * legacy unscoped key.
   */
  const tokenScope = $derived(normEventId || normSessionId || '');

  function rememberCheckoutToken(token: string): void {
    saveCheckoutToken(token, undefined, tokenScope);
  }

  function forgetCheckoutToken(): void {
    clearCheckoutToken(undefined, tokenScope);
  }
  const hasToken = $derived(normFeedToken !== '');
  const dir = $derived(isRtlLocale(normLocale) ? 'rtl' : 'ltr');
  const t = $derived(getCheckoutI18n(normLocale));

  /**
   * Auto-detected API base from the script src origin (set once on mount).
   * Empty string means same-origin / relative URLs.
   */
  let autoDetectedBase = $state('');

  /**
   * Resolved API base URL. Explicit `api-base` attribute takes precedence.
   * When absent, auto-detected on mount from the widget script's src origin
   * so cross-origin embeds (WordPress, third-party pages) reach the correct
   * backend instead of sending requests to the host page's own origin.
   */
  const resolvedApiBase = $derived(
    apiBase.trim().replace(/\/$/, '') || autoDetectedBase,
  );

  // ── Event data ─────────────────────────────────────────────────────────────

  let event = $state<FeedEvent | null>(null);
  let selectedSession = $state<FeedSession | null>(null);
  let loading = $state(false);
  let loadError = $state<string | null>(null);

  // Never hides a real choice: with more than one session the chips stay.
  const sessionsHidden = $derived(
    sessions.trim().toLowerCase() === 'hidden' && (event?.sessions.length ?? 0) <= 1,
  );

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
  // AB-40D: GA categories that carry a hit-test polygon and render as
  // clickable areas on the same hall map as the seats. Populated from the
  // schema on load; the picker overlay opens over the map when a buyer
  // taps one of these areas.
  let gaAreas = $state<GaArea[]>([]);
  let openGaAreaTierId = $state<string | null>(null);

  // A plan-less general-admission session has no schema_url, so SeatMapView is
  // never mounted and onSchemaLoaded never fires — yet that callback was the
  // only place gaTiers/tierById were filled. The widget then rendered the
  // session chip and the price legend above an EMPTY body: nothing to buy.
  // (Found on the first production GA events, 2026-09-20; every earlier test
  // used a hall with a schema.) Without a schema every tier of the session is
  // a GA tier, so derive the cards straight from the session.
  $effect(() => {
    const tiers = planlessGaTiers(selectedSession);
    if (tiers === null) return;
    tierById = buildTierById(tiers);
    gaTiers = tiers;
    gaAreas = [];
    seatCategoryIndex = new Map();
    categoryByCategoryIndex = new Map();
  });

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
  /**
   * Set of seat keys that are in conflict after a 409 `reservation.seats_conflict`
   * response from checkout/start or recover (WID-S2).  Passed into SeatMapView
   * so it can apply the WCAG-AA error-red conflict highlight overlay.
   */
  let conflictKeys = $state<ReadonlySet<string>>(new Set());
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
    // Auto-detect apiBase from the widget script's src when not set explicitly.
    if (!apiBase) {
      try {
        const script =
          (document.querySelector('script[src*="arena-tickets"]') as HTMLScriptElement | null) ??
          (document.currentScript as HTMLScriptElement | null);
        if (script?.src) {
          const u = new URL(script.src);
          // Only set a cross-origin base; same-origin stays relative ('').
          if (u.origin !== window.location.origin) {
            autoDetectedBase = u.origin;
          }
        }
      } catch {
        // Non-browser or URL parse failure — keep empty (relative URLs).
      }
    }

    // Check for checkout_token in URL or sessionStorage first.
    const urlToken = getCheckoutTokenFromSearch(window.location.search);
    const storedToken = restoreCheckoutToken(undefined, tokenScope);
    const resumeToken = urlToken ?? storedToken;

    if (resumeToken) {
      // Restore order status view.
      checkoutToken = resumeToken;
      stage = 'order-status';
      loadOrderStatus(resumeToken);
      return;
    }

    if (!normFeedToken) return;

    // Load real event data from the public feed API.
    void resolveAndLoadFromFeed(normFeedToken);
  });

  /**
   * Resolve which event to load and populate `event` / `selectedSession`.
   *
   * Resolution ladder (PR2-21 regression fix — the WordPress shortcode only
   * emits feed-token + session-id, so event-id must stay optional):
   *
   *  1. `event-id` attribute set → fetch that event's detail directly.
   *  2. `session-id` only → list the feed's events and probe details until
   *     one contains the session. If the list endpoint is unreachable (e.g.
   *     legacy backend), fall back to a synthetic single-session event —
   *     the pre-PR2-21 behaviour — so the seat map still boots; the real
   *     data paths (schema / seat-status / checkout) remain fully live.
   *  3. Neither → load the first event in the feed.
   */
  async function resolveAndLoadFromFeed(token: string): Promise<void> {
    loading = true;
    loadError = null;
    try {
      if (normEventId) {
        applyFeedEvent(await fetchFeedEvent(token, normEventId, resolvedApiBase));
        return;
      }

      let list: import('./types.js').FeedEventsListResponse;
      try {
        list = await fetchFeedEvents(token, resolvedApiBase);
      } catch (listErr) {
        if (normSessionId) {
          // Feed listing unavailable — keep the embed alive on the
          // session-scoped synthetic event (pre-PR2-21 contract).
          console.warn('arena-tickets: feed event list unavailable, using session-only bootstrap:', listErr);
          applySyntheticSessionEvent(normSessionId);
          return;
        }
        throw listErr;
      }

      // Probe at most the first 20 events for the deep-linked session.
      const candidates = (list.events ?? []).slice(0, 20);
      if (candidates.length === 0) {
        if (normSessionId) {
          applySyntheticSessionEvent(normSessionId);
          return;
        }
        throw new Error(t.error_load_event);
      }

      if (normSessionId) {
        for (const candidate of candidates) {
          const data = await fetchFeedEvent(token, candidate.id, resolvedApiBase);
          if (data.event.sessions.some((s) => s.id === normSessionId)) {
            applyFeedEvent(data);
            return;
          }
        }
        // Session not published under this token — surface the first event
        // rather than a blank widget so the misconfiguration is visible.
        console.warn(`arena-tickets: session-id ${normSessionId} not found in feed; showing first event`);
      }

      applyFeedEvent(await fetchFeedEvent(token, candidates[0]!.id, resolvedApiBase));
    } catch (err) {
      loadError = err instanceof Error ? err.message : t.error_load_event;
    } finally {
      loading = false;
    }
  }

  /** Apply a loaded feed event detail to the widget state. */
  function applyFeedEvent(data: import('./types.js').FeedEventDetailResponse): void {
    event = data.event;
    selectedSession = pickInitialSession(data.event, normSessionId);
  }

  /**
   * Build the pre-PR2-21 synthetic single-session event so a
   * feed-token + session-id embed still boots when the feed listing is
   * unavailable. Schema, seat-status, and checkout all use live endpoints.
   */
  function applySyntheticSessionEvent(sessId: string): void {
    const now = new Date().toISOString();
    const syntheticEvent: FeedEvent = {
      id: sessId,
      display_number: 0,
      org_id: '',
      name: '',
      status: 'published',
      first_session_at: now,
      last_session_at: now,
      venue_names: [],
      visibility: 'public',
      created_at: now,
      updated_at: now,
      sessions: [
        {
          id: sessId,
          start_at: now,
          end_at: now,
          capacity_total: 0,
          status: 'published',
          admission_mode: 'assigned_seats',
          schema_url: `/v1/event-sessions/${sessId}/schema`,
          seat_status_url: `/v1/event-sessions/${sessId}/seat-status`,
          buyer_fields: [],
          tiers: [],
          media_gallery: [],
        },
      ],
    };
    event = syntheticEvent;
    selectedSession = syntheticEvent.sessions[0] ?? null;
  }

  // ── Schema loaded callback ─────────────────────────────────────────────────

  function onSchemaLoaded(geometry: Geometry, categoryPrices: CategoryPrice[]): void {
    seatCategoryIndex = buildSeatCategoryIndex(geometry);
    categoryByCategoryIndex = buildCategoryByIndex(categoryPrices);
    tierById = selectedSession ? buildTierById(selectedSession.tiers) : new Map();
    gaTiers = selectedSession ? identifyGaTiers(selectedSession.tiers, categoryPrices) : [];
    // AB-40D: identify GA polygons that render on the hall map. GA tiers
    // WITHOUT polygons stay as always-visible cards below the map — the
    // widget spec requires both surfaces (geography for polygons, cards for
    // geography-less GA) and forbids any mode toggle between them.
    gaAreas = identifyGaAreas(geometry, categoryPrices);
    // Filter out GA tiers already bound to a polygon area — otherwise a
    // buyer would see the same GA offer twice (card + polygon).
    if (gaAreas.length > 0) {
      const polygonTierIds = new Set(gaAreas.map((a) => a.tierId));
      gaTiers = gaTiers.filter((t) => !polygonTierIds.has(t.id));
    }
  }

  // ── GA area tap handler (AB-40D) ───────────────────────────────────────────

  function onGaAreaTap(_categoryIndex: number, tierId: string): void {
    // The overlay is a single-open picker: tapping a different area swaps
    // the target; tapping the same area again with the picker already open
    // does nothing (Done / × closes it).
    openGaAreaTierId = tierId;
  }

  function closeGaAreaPopover(): void {
    openGaAreaTierId = null;
  }

  const openGaArea = $derived(
    openGaAreaTierId ? gaAreas.find((a) => a.tierId === openGaAreaTierId) ?? null : null,
  );

  // ── Seat tap handler ───────────────────────────────────────────────────────

  function onSeatTap(seatKey: string, status: SeatStatusValue): void {
    const prev = selectedSeatKeys;
    const next = toggleSeatSelection(prev, seatKey, status);
    selectedSeatKeys = next;

    // WID-S5: emit seat lifecycle events so host pages can track selection.
    const sessionId = selectedSession?.id ?? '';
    if (next.has(seatKey) && !prev.has(seatKey)) {
      dispatchWidgetEvent(host, ARENA_EVENTS.SEAT_SELECTED, { seatKey, sessionId });
    } else if (!next.has(seatKey) && prev.has(seatKey)) {
      if (conflictKeys.has(seatKey)) {
        const remainingConflicts = new Set(conflictKeys);
        remainingConflicts.delete(seatKey);
        conflictKeys = remainingConflicts;
      }
      dispatchWidgetEvent(host, ARENA_EVENTS.SEAT_RELEASED, { seatKey, sessionId });
    }
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
    // WID-T2: notify host page that the cart sheet was opened (view_cart analytics).
    const sessionId = selectedSession?.id ?? '';
    dispatchWidgetEvent(host, ARENA_EVENTS.CART_OPENED, { sessionId, itemCount: cartCount });
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
    const sessionId = selectedSession?.id ?? '';
    if (line.type === 'seated') {
      // Remove these specific seat keys from selection.
      const next = new Set(selectedSeatKeys);
      for (const key of line.seatKeys) {
        next.delete(key);
        // WID-S5: notify host page that seat was released via cart removal.
        dispatchWidgetEvent(host, ARENA_EVENTS.SEAT_RELEASED, { seatKey: key, sessionId });
      }
      selectedSeatKeys = next;
    } else if (line.type === 'ga') {
      const next = new Map(gaQuantities);
      next.delete(line.tierId);
      gaQuantities = next;
    }
  }

  // ── Checkout ───────────────────────────────────────────────────────────────

  /**
   * Current page URL without query string or hash, for the hosted payment
   * provider's success/cancel redirect. Returns null outside a browser (SSR,
   * prerender), where the checkout cannot run anyway.
   */
  function buildReturnUrl(): string | null {
    try {
      if (typeof window === 'undefined' || !window.location) return null;
      const { origin, pathname } = window.location;
      if (!origin || origin === 'null') return null;
      return `${origin}${pathname}`;
    } catch {
      return null;
    }
  }

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
      // Where the hosted payment page returns the buyer. Origin + pathname
      // only — the backend appends `?checkout_token=…` itself, which is what
      // `getCheckoutTokenFromSearch` reads on the next load, so any query
      // string or hash we sent along would be dropped or would collide.
      const returnUrl = buildReturnUrl();
      if (returnUrl) payload.return_url = returnUrl;
      // Language for the buyer's ticket e-mail / PDF — the same resolved
      // locale the widget renders its own copy in. The backend ignores a
      // locale it has no e-mail template for, so sending it is always safe.
      if (typeof normLocale === 'string' && normLocale.length > 0) {
        payload.locale = normLocale;
      }
      const response = await postCheckoutStart(normFeedToken, payload, resolvedApiBase);
      // Save token in case user returns after the payment page.
      rememberCheckoutToken(response.checkout_token);
      checkoutToken = response.checkout_token;
      // Store the hold expiry so MiniCart/CartSheet can show the countdown
      // during the brief redirecting stage (WID-S1 fix #3 + #4).
      holdExpiresAt = response.expires_at;
      // WID-S5: notify host page that payment flow has started.
      dispatchWidgetEvent(host, ARENA_EVENTS.PAYMENT_STARTED, {
        checkoutToken: response.checkout_token,
        sessionId: selectedSession.id,
      });
      const redirectUrl = (response.redirect_url ?? '').trim();
      if (!redirectUrl) {
        // No hosted payment page to send the buyer to (e.g. no return URL is
        // configured on the channel). Stay put and show the order status for
        // the token we already hold instead of navigating to "".
        stage = 'order-status';
        await loadOrderStatus(response.checkout_token);
        return;
      }
      stage = 'redirecting';
      // Redirect to payment provider.
      window.location.href = redirectUrl;
    } catch (err) {
      // WID-S2: parse nested envelope 409 seat conflicts and surface them on
      // the seat map via the conflictKeys prop (SeatMapView → applyConflictHighlight).
      const conflicts = parseConflictsFromApiError(err);
      if (conflicts.length > 0) {
        conflictKeys = conflictKeySet(conflicts);
        checkoutError = t.conflict_notice;
      } else {
        conflictKeys = new Set();
        checkoutError = err instanceof Error ? err.message : t.error_checkout;
      }
    } finally {
      checkoutSubmitting = false;
    }
  }

  // ── Order status ───────────────────────────────────────────────────────────

  async function loadOrderStatus(token: string): Promise<void> {
    try {
      orderStatus = await getCheckoutStatus(token, resolvedApiBase);
      // WID-T3: use server-side expires_at from order-status to drive countdown.
      holdExpiresAt = orderStatus.expires_at ?? null;
      // WID-S5: notify host page about terminal order outcomes.
      const status = orderStatus.status;
      // A finished order must not follow the buyer around. The stored token
      // is only there to resume an UNFINISHED checkout; left in place after
      // 'paid' it made every other event page opened in the same tab show
      // the old order instead of the ticket picker, so a buyer could not buy
      // a second master class (first production events, 2026-09-20). The
      // return page itself keeps working: its token is in the URL.
      if (status === 'paid' || status === 'failed' || status === 'expired') {
        forgetCheckoutToken();
      }
      if (status === 'paid') {
        dispatchWidgetEvent(host, ARENA_EVENTS.ORDER_PAID, {
          checkoutToken: token,
          orderRef: null,           // arena API v1 does not surface order_ref yet
          totalMinorUnits: orderStatus.total ?? null,
          currency: orderStatus.currency ?? null,
        });
      } else if (status === 'failed' || status === 'expired') {
        dispatchWidgetEvent(host, ARENA_EVENTS.ORDER_FAILED, {
          checkoutToken: token,
          reason: status,
        });
      }
    } catch (err) {
      const apiErr = err as ApiError;
      if (apiErr?.status === 401) {
        // WID-T3: Token may have expired — attempt silent recovery without page reload.
        try {
          const recovered = await postCheckoutRecover(token, resolvedApiBase);
          holdExpiresAt = recovered.expires_at;
          const newToken = recovered.checkout_token;
          checkoutToken = newToken;
          rememberCheckoutToken(newToken);
          dispatchWidgetEvent(host, ARENA_EVENTS.RECOVERY, {
            checkoutToken: newToken,
            expiresAt: recovered.expires_at,
          });
          // Reload status with the refreshed token.
          await loadOrderStatus(newToken);
          return;
        } catch (recoveryErr) {
          // Recovery also failed — clear token, set visible error, fall back
          // to normal init so the user can start a fresh checkout.
          forgetCheckoutToken();
          checkoutToken = null;
          loadError =
            recoveryErr instanceof Error ? recoveryErr.message : t.error_recovery;
          stage = 'selecting';
          // Re-load event data so the widget is not left blank.
          if (normFeedToken && normEventId) {
            void loadFromFeed(normFeedToken, normEventId);
          }
          return;
        }
      }
      // 404 = token doesn't exist in the backend; clear and reset to normal init.
      if (apiErr?.status === 404) {
        forgetCheckoutToken();
        checkoutToken = null;
        loadError = err instanceof Error ? err.message : t.error_order_status;
        stage = 'selecting';
        // Re-load event data so the widget is not left blank.
        if (normFeedToken && normEventId) {
          void loadFromFeed(normFeedToken, normEventId);
        }
        return;
      }
      loadError = err instanceof Error ? err.message : t.error_order_status;
      stage = 'selecting';
    }
  }

  async function handleRecover(): Promise<void> {
    if (!checkoutToken) return;
    orderActionLoading = true;
    orderActionError = null;
    try {
      const recovered = await postCheckoutRecover(checkoutToken, resolvedApiBase);
      // Update hold expiry with the fresh timestamp from recovery (WID-S1 fix #3).
      holdExpiresAt = recovered.expires_at;
      // WID-T2: notify host page that session was successfully recovered.
      dispatchWidgetEvent(host, ARENA_EVENTS.RECOVERY, {
        checkoutToken,
        expiresAt: recovered.expires_at,
      });
      // Re-load status after recovery attempt.
      orderStatus = await getCheckoutStatus(checkoutToken, resolvedApiBase);
    } catch (err) {
      // WID-S2: parse nested envelope 409 seat conflicts from recovery and
      // surface them on the seat map so the user sees which seats are gone.
      const conflicts = parseConflictsFromApiError(err);
      if (conflicts.length > 0) {
        conflictKeys = conflictKeySet(conflicts);
        orderActionError = t.conflict_notice;
      } else {
        conflictKeys = new Set();
        orderActionError = err instanceof Error ? err.message : t.error_recovery;
      }
    } finally {
      orderActionLoading = false;
    }
  }

  /**
   * WID-T4: "Continue without conflicts" one-click action.
   *
   * Removes conflicting seats from the selection so the user can proceed
   * to checkout without the unavailable seats.  Clears conflict highlights
   * and the inline conflict notice so the map and form return to a clean state.
   */
  function handleContinueWithoutConflicts(): void {
    const remaining = filterCartWithoutConflicts([...selectedSeatKeys], conflictKeys);
    selectedSeatKeys = new Set(remaining);
    conflictKeys = new Set();
    checkoutError = null;
  }

  function handleRetry(): void {
    // Clear token and return to selecting stage.
    forgetCheckoutToken();
    checkoutToken = null;
    orderStatus = null;
    selectedSeatKeys = new Set();
    gaQuantities = new Map();
    holdExpiresAt = null;
    conflictKeys = new Set(); // WID-S2: clear conflict highlights on retry
    stage = 'selecting';
    cartSheetOpen = false;
  }

  /**
   * Leaves a PAID order's success panel and shows the picker again.
   *
   * Without it the success panel is a dead end: the widget stays mounted on
   * that view, so the same buyer could not start a second purchase for this
   * event — on the hosted page, where a date row keeps its widget alive, the
   * only way out was reloading the page (first production purchases,
   * 2026-09-20). The stored token is already gone by then (loadOrderStatus
   * forgets it on any terminal status), so this only resets the view and the
   * cart it was built from; the availability the picker shows is reloaded so
   * the seats just sold are not offered again.
   */
  function handleDone(): void {
    checkoutToken = null;
    orderStatus = null;
    selectedSeatKeys = new Set();
    gaQuantities = new Map();
    holdExpiresAt = null;
    conflictKeys = new Set();
    checkoutError = null;
    orderActionError = null;
    cartSheetOpen = false;
    stage = 'selecting';
    if (normFeedToken) void resolveAndLoadFromFeed(normFeedToken);
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
        expiresAt={holdExpiresAt}
        onRecover={handleRecover}
        onRetry={handleRetry}
        onDone={orderStatus.status === 'paid' ? handleDone : undefined}
        apiBase={resolvedApiBase}
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
    <!-- The 400px floor exists so a seat map has room to draw. A session
         with no map (every general-admission event) has nothing to fill it
         with, and the reserved space showed up as a hole under the
         category list — so the floor applies only when a map is shown. -->
    <div
      class="arena-tickets-frame"
      class:arena-tickets-frame--flat={!(selectedSession && selectedSession.schema_url)}
      class:arena-tickets-frame--bare={frameHidden}
    >
      {#if loading}
        <div class="arena-tickets-loading" aria-live="polite" aria-busy="true">{t.loading}</div>
      {:else if loadError}
        <div class="arena-tickets-error" role="alert">{loadError}</div>
      {:else if event && event.sessions.length > 0}
        {#if coverHidden ? false : (selectedSession?.poster_url ?? event.poster_url)}
          <!-- AB-47c: resolved poster cover (session ?? event fallback).
               The gallery beyond the cover lives in the data layer
               (selectedSession.media_gallery) — see FeedSession type. -->
          <div
            class="arena-tickets-cover"
            data-arena-poster-cover
            data-arena-poster-source={selectedSession?.poster_url ? 'session' : 'event'}
          >
            <img
              src={selectedSession?.poster_url ?? event.poster_url ?? ''}
              alt=""
              data-arena-poster-media-id={selectedSession?.poster_media_id ?? event.poster_media_id ?? ''}
              loading="lazy"
            />
          </div>
        {/if}
        <!-- Session date chips + legend -->
        {#if !sessionsHidden}
        <SessionList
          sessions={event.sessions}
          locale={normLocale}
          {selectedSession}
          onSelectSession={(s) => {
            // Reset seat/GA selection when switching sessions so stale keys
            // from a different session don't carry over (WID-S1 fix #6).
            if (s?.id !== selectedSession?.id) {
              selectedSeatKeys = new Set();
              gaQuantities = new Map();
              holdExpiresAt = null;
              conflictKeys = new Set();
              checkoutError = null;
              cartSheetOpen = false;
              openGaAreaTierId = null;
              gaAreas = [];
            }
            selectedSession = s;
          }}
        />
        {/if}
        <!-- Seat map (only for sessions with schema_url) -->
        {#if selectedSession && selectedSession.schema_url}
          <div class="arena-tickets-map-wrap">
            <SeatMapView
              session={selectedSession}
              locale={normLocale}
              selectedKeys={selectedSeatKeys}
              {conflictKeys}
              {onSeatTap}
              {onGaAreaTap}
              {onSchemaLoaded}
              apiBase={resolvedApiBase}
            />
            {#if openGaArea}
              <GaAreaPopover
                area={openGaArea}
                quantity={gaQuantities.get(openGaArea.tierId) ?? 0}
                onQuantityChange={onGaQuantityChange}
                onClose={closeGaAreaPopover}
              />
            {/if}
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
            cta={frameHidden ? t.continue_to_payment : null}
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
        {conflictKeys}
        onContinueWithoutConflicts={handleContinueWithoutConflicts}
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

    /* Primary-action palette, kept SEPARATE from --arena-accent on purpose:
       the accent still colours links, selected chips and focus rings, while
       the one control a buyer presses to spend money can be given its own
       colour. A yellow CTA converts better, and dark-on-yellow (#101010 on
       #fcdc54, ≈13:1) is the only readable pairing — never white text there.

       The DEFAULT here stays the embedder's accent, NOT the yellow: this
       widget is embedded on customers' own sites (the Lampyris and Vino&Co
       WordPress storefronts), and a default change would repaint their live
       buy button on the next deploy without anyone asking them. Arena's own
       hosted page opts in by setting --arena-button-* (see
       apps/tickets-page/src/style.css); any embedder can do the same.
       The disabled pair falls back to the host's own border/muted tokens so
       it stays visibly dead under any theme, light or dark. */
    --_btn-bg: var(--arena-button-bg, var(--arena-accent, #4f46e5));
    --_btn-bg-hover: var(--arena-button-bg-hover, var(--arena-accent-hover, var(--arena-accent, #4338ca)));
    --_btn-text: var(--arena-button-text, var(--arena-accent-contrast, #ffffff));
    --_btn-disabled-bg: var(--arena-button-disabled-bg, var(--arena-border-color, #e5e7eb));
    --_btn-disabled-text: var(--arena-button-disabled-text, var(--arena-color-secondary, #6b7280));
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

  .arena-tickets-frame--flat {
    min-height: 0;
  }

  /* frame="hidden": the embedder has already drawn the box — the hosted
     promoter page's date card. Only the outer outline goes; the children
     keep their own borders, so the category row and the stepper stay as
     legible as in a standalone embed. */
  .arena-tickets-frame--bare {
    border: none;
    border-radius: 0;
    overflow: visible;
  }

  /* In a frame of its own the category list needs its own gutter and a rule
     separating it from the cover and the date chips above. Inside someone
     else's card it needs neither: the card supplies the gutter, and the
     second hairline only indented the category row away from the text it
     belongs to. */
  .arena-tickets-frame--bare .ga-tiers-section {
    padding-inline: 0;
    padding-top: 0;
    border-top: none;
  }

  .arena-tickets-cover {
    display: block;
    width: 100%;
    background: #0f172a;
    line-height: 0;
  }

  .arena-tickets-cover img {
    display: block;
    width: 100%;
    max-height: 260px;
    object-fit: cover;
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

  /* AB-40D: positioning context for the GA area popover overlay. The
     popover is absolutely positioned inside this wrapper and pinned to
     the top-right of the seat map surface so it never occludes the seats
     the buyer is choosing from. */
  .arena-tickets-map-wrap {
    position: relative;
    display: flex;
    flex-direction: column;
    flex: 1;
    min-height: 0;
  }
  .arena-tickets-map-wrap :global(.ga-popover) {
    top: 3rem;
    right: 0.75rem;
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
