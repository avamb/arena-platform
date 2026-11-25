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
      checkout: { type: 'String', attribute: 'checkout' },
      materials: { type: 'String', attribute: 'materials' },
      funnel: { type: 'String', attribute: 'funnel' },
      promo: { type: 'String', attribute: 'promo' },
      trackingConsent: { type: 'String', attribute: 'tracking-consent' },
      trackingConsentVersion: { type: 'String', attribute: 'tracking-consent-version' },
      preloadPayment: { type: 'String', attribute: 'preload-payment' },
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
  import { onMount, untrack } from 'svelte';
  import { parseLocale, parseFeedToken, parseSessionId, isRtlLocale } from './utils.js';
  import { fetchFeedEvent, fetchFeedEvents, postCheckoutStart, postCheckoutQuote, getCheckoutStatus, postCheckoutRecover, postCheckoutPrepare, postCheckoutBuyer, postCheckoutCancel, ApiError } from './api.js';
  import type { FeedSession, FeedEvent, Geometry, CategoryPrice, SeatStatusValue } from './types.js';
  import type { BuyerFormValues, CheckoutPreparePayload, CheckoutQuotePayload, CheckoutQuoteResponse } from './lib/checkout.js';
  import { buildBuyerPayload, buildCheckoutPayload, buildQuotePayload, getCheckoutI18n, parseConflictsFromApiError, conflictKeySet, filterCartWithoutConflicts } from './lib/checkout.js';
  import { cartTotal } from './lib/cart.js';
  import {
    createPreparedCheckout,
    isPlainCart,
    loadsStripeEarly,
    preloadMode,
    type PreparedCheckout,
  } from './lib/preparedCheckout.js';
  import { cartQuoteKey, promoErrorMessage, isPromoErrorCode } from './lib/promo.js';
  import {
    afterCheckoutErrorChange,
    checkoutError as makeCheckoutError,
    promoRefusal,
    promoCodeChange,
    type CheckoutError,
    type CheckoutErrorChange,
  } from './lib/checkoutError.js';
  import { createPromoController, initialPromoState, promoPayBlocked } from './lib/promoController.js';
  import { createMarketingQuoter, initialMarketingQuoteState, type MarketingQuoteState } from './lib/marketingQuote.js';
  import type { PromoState } from './lib/promoController.js';
  import { getPromoStrings } from './lib/promoI18n.js';
  import { forgetWantedPromo, resolveWantedPromo } from './lib/referral.js';
  import { MAX_CART_SEATS, cartLimitMessage, seatCapMessage } from './lib/cartLimits.js';
  import { buyerErrorText } from './lib/errorI18n.js';
  import { pausedSalesState } from './lib/saleSubscribe.js';
  import { buildTierColorMap } from './lib/seatmap-render.js';
  import { consentErrorMessage, consentPayload, forceConsent } from './lib/consent.js';
  import { isMarketingRewardRefusal, marketingErrorMessage, marketingPayload } from './lib/marketing.js';
  import { captureAttribution, type AttributionPayload } from './lib/attribution.js';
  import { buildBuyerContext, captureClickIds, isTrackingGranted, persistClickIds, readBrowserCookies, type BuyerContextPayload, type ClickIds } from './lib/buyerContext.js';
  import { initialAddToCartState, trackCartChange, withAddToCart } from './lib/addToCart.js';
  import { createFunnel, parseFunnelHost, type Funnel, type FunnelStep } from './lib/funnel.js';
  import { seatCapBlocks, toggleSeatSelection } from './lib/selection.js';
  import {
    PULSE_MS,
    browserStorage,
    createHoldController,
    deleteHoldSeat,
    extendMinutes,
    extendVisible,
    getDeviceId,
    getHold,
    holdSecondsLeft,
    postCheckoutPaying,
    postHold,
    postHoldExtend,
    type HoldController,
    type HoldView,
  } from './lib/holds.js';
  import { fillHold, getHoldStrings, holdCountdownText, holdErrorText } from './lib/holdI18n.js';
  import { formatCountdown } from './lib/cart.js';
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
    seatedTiersOf,
    tiersAreFree,
    planlessGaTiers,
    gaPreselection,
    identifyGaAreas,
    buildGaItems,
    totalSelectionCount,
    pickInitialSession,
    isSessionCancelled,
    allSessionsCancelled,
    type WidgetStage,
    type GaArea,
  } from './lib/store.js';
  import type { CheckoutStatusResponse } from './lib/checkout.js';
  import SessionList from './components/SessionList.svelte';
  import SeatMapView from './components/SeatMapView.svelte';
  import MiniCart from './components/MiniCart.svelte';
  import CartSheet from './components/CartSheet.svelte';
  import GaTierCard from './components/GaTierCard.svelte';
  import SaleStateNotice from './components/SaleStateNotice.svelte';
  import ExternalSaleNotice from './components/ExternalSaleNotice.svelte';
  import { externalSaleOf } from './lib/externalSale.js';
  import SeatedTierCard from './components/SeatedTierCard.svelte';
  import GaAreaPopover from './components/GaAreaPopover.svelte';
  import OrderStatus from './components/OrderStatus.svelte';
  import PaymentScreen from './components/PaymentScreen.svelte';
  import { loadStripeJs, preconnectStripe } from './lib/stripeElements.js';
  import { createPaymentStepsRegistry } from './lib/paymentSteps.js';
  import type { PaymentBlock } from './lib/checkout.js';
  import Materials from './components/Materials.svelte';
  import { PLATFORM_BADGE_URL, getPlatformBadgeStrings, showPlatformBadge } from './lib/platformBadge.js';

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
    /** 'one-step' shows the cart and the buyer form on ONE screen of the cart sheet. */
    checkout?: string;
    /** 'hidden' drops the event's materials block (gallery, videos, map,
     * "where else to buy", photo report). A host page that already shows
     * the same material itself — the hosted tickets page does — sets it so
     * the buyer does not see everything twice. Anything else (or absent)
     * shows the block whenever the event has any material. */
    materials?: string;
    /** Buyer funnel (spec 50 A7): 'hosted_page' or 'tickets_page' says the
     * Arena page around the widget counts the event's view itself (the widget
     * would count a view nobody looked at: a promoter page mounts a widget per
     * card). Anything else (or absent) is a plain embed, which counts its own. */
    funnel?: string;
    /** A promo code to apply to the first cart ("Refer a friend": the hosted
     * page hands over the `?promo=` of its address). Without it the widget
     * reads `?promo=` from the page address itself. */
    promo?: string;
    /** 'granted' or 'denied': the host page's tracking-consent answer. Absent
     * or anything else: no buyer_context is sent. Read when checkout starts. */
    trackingConsent?: string;
    /** Version of the consent text the host showed (sent with the answer). */
    trackingConsentVersion?: string;
    /** 'stripe': the host knows its channel pays through embedded Stripe, so
     * the widget loads Stripe.js as soon as the checkout sheet opens, while
     * the buyer types the e-mail, instead of after "Pay". 'checkout' does
     * that AND prepares the order and the Stripe session when the sheet opens
     * with a plain cart, so the card form loads hidden while the buyer types
     * (lib/preparedCheckout.ts). Anything else (or absent) loads Stripe.js
     * only when the platform answers with it. */
    preloadPayment?: string;
  }

  const { feedToken = '', eventId = '', sessionId = '', locale = 'en', apiBase = '', cover = '', sessions = '', frame = '', checkout = '', materials = '', funnel = '', promo: promoAttribute = '', trackingConsent = '', trackingConsentVersion = '', preloadPayment = '' }: Props = $props();

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
  const oneStep = $derived(checkout.trim().toLowerCase() === 'one-step');
  const frameHidden = $derived(frame.trim().toLowerCase() === 'hidden');
  const materialsHidden = $derived(materials.trim().toLowerCase() === 'hidden');
  const badgeT = $derived(getPlatformBadgeStrings(normLocale));

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

  // UXH-23: a cancelled date keeps its chip (with a "Cancelled" label) but has
  // nothing to buy, so the notice replaces the map, the category steppers and
  // the cart bar — both for a cancelled selection and for an event whose every
  // date was cancelled (no session is selected then).
  const salesClosed = $derived(isSessionCancelled(selectedSession) || allSessionsCancelled(event));

  // A date that is "soon" (sales not started) or "sold_out" has every category at
  // available 0: one notice (and, for "soon", the notify form) replaces the map
  // and the steppers. A buyer who already holds seats keeps the cart bar. A
  // missing or unknown `sales_state` (older API) is not paused.
  const salesPaused = $derived(!salesClosed && selectedSession !== null && pausedSalesState(selectedSession) !== null);

  // A date sold by a partner box office (sales_state "external") has no tiers
  // here: the map, steppers and cart give way to a button to the partner's site.
  const salesExternal = $derived(!salesClosed && externalSaleOf(selectedSession) !== null);

  // ── Widget stage ───────────────────────────────────────────────────────────

  let stage = $state<WidgetStage>('selecting');
  let cartSheetOpen = $state(false);

  // The host page sees the checkout as `data-cart-open` on <arena-tickets>, so it can hide
  // its own "Buy tickets" buttons with CSS alone while the buyer pays: the sheet is open, or
  // the widget has left seat selection (an inline payment step closes the sheet first).
  $effect(() => {
    if (!host) return;
    if (cartSheetOpen || stage !== 'selecting') host.setAttribute('data-cart-open', '');
    else host.removeAttribute('data-cart-open');
  });

  // Stripe.js takes ~0.3 s plus its own connections; loaded after "Pay" it sat
  // on the critical path to the card form. With preload-payment="stripe" it
  // starts when the sheet opens. loadStripeJs is one shared promise, so the
  // payment screen reuses it; a failure here is ignored and retried there.
  // preload-payment="checkout" implies it. With the attribute absent (auto) it
  // happens only when the session's payment is embedded (`embedded_payment`).
  const preload = $derived(preloadMode(preloadPayment));
  const stripeEarly = $derived(loadsStripeEarly(preload, selectedSession?.embedded_payment));
  $effect(() => {
    if (cartSheetOpen && stripeEarly) {
      loadStripeJs().catch(() => {});
    }
  });
  // The buyer's first press or key anywhere in the widget (the host element,
  // which covers the shadow root). Until then the preconnect waits: a
  // preselected quantity must not warm js.stripe.com on page load.
  let buyerActed = $state(false);
  $effect(() => {
    const el = host;
    const mark = () => {
      buyerActed = true;
      el.removeEventListener('pointerdown', mark);
      el.removeEventListener('keydown', mark);
    };
    el.addEventListener('pointerdown', mark);
    el.addEventListener('keydown', mark);
    return () => {
      el.removeEventListener('pointerdown', mark);
      el.removeEventListener('keydown', mark);
    };
  });
  // The first ticket or seat in the cart warms the connection to js.stripe.com
  // (one tag per page); never on page load, never before a buyer action.
  $effect(() => {
    if (cartCount > 0 && stripeEarly && buyerActed) preconnectStripe();
  });

  // ── Selection state ────────────────────────────────────────────────────────

  let selectedSeatKeys = $state<ReadonlySet<string>>(new Set());
  let gaQuantities = $state<ReadonlyMap<string, number>>(new Map());

  // ── Schema index maps (built from onSchemaLoaded) ──────────────────────────

  let seatCategoryIndex = $state<ReadonlyMap<string, number>>(new Map());
  let categoryByCategoryIndex = $state<ReadonlyMap<number, CategoryPrice>>(new Map());
  // Legend colours: each tier in the colour its seats carry on the plan.
  const tierColors = $derived(buildTierColorMap([...categoryByCategoryIndex.values()]));
  let tierById = $state<ReadonlyMap<string, Tier>>(new Map());
  let gaTiers = $state<import('./types.js').Tier[]>([]);
  // AB-40D: GA categories that carry a hit-test polygon and render as
  // clickable areas on the same hall map as the seats. Populated from the
  // schema on load; the picker overlay opens over the map when a buyer
  // taps one of these areas.
  let gaAreas = $state<GaArea[]>([]);
  // Seated categories (taken on the plan, no quantity card), set when the schema loads.
  let seatedTiers = $state<import('./types.js').Tier[]>([]);
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

  // One-time preselection of quantity 1 on the first purchasable GA tier, so
  // the sticky cart bar is visible at once. Done at most once per selected
  // session: a buyer who sets 0 or removes the line is never overridden.
  // Skipped when several pickers share the page (promoter date list: every
  // row mounts its own <arena-tickets cover/frame="hidden">) because each
  // would open its own cart. Detected by the card signature (frame AND cover
  // hidden) or by more than one <arena-tickets> element in the document.
  let gaPreselectDoneFor: string | null = null;

  function sharesPageWithOtherPickers(): boolean {
    if (frameHidden && coverHidden) return true;
    const doc = host?.ownerDocument;
    return !!doc && doc.querySelectorAll('arena-tickets').length > 1;
  }

  $effect(() => {
    const session = selectedSession;
    const tiers = gaTiers;
    if (!session || tiers.length === 0 || stage !== 'selecting') return;
    if (gaPreselectDoneFor === session.id) return;
    untrack(() => {
      gaPreselectDoneFor = session.id;
      if (sharesPageWithOtherPickers()) return;
      const pre = gaPreselection(tiers, gaQuantities, selectedSeatKeys.size);
      if (pre) gaQuantities = pre;
    });
  });

  // ── Checkout state─────────────────────────────────────────────────────────

  let checkoutToken = $state<string | null>(null);
  /**
   * ISO-8601 expiry from the last successful postCheckoutStart or
   * postCheckoutRecover response.  Used to drive the hold countdown timer
   * in MiniCart and CartSheet while the user fills in their details.
   */
  let holdExpiresAt = $state<string | null>(null);
  let checkoutSubmitting = $state(false);
  // The message above the buyer form after checkout/start refused, with its
  // kind: a promo refusal goes away by itself once the buyer applies or takes
  // off a code or types another e-mail (lib/checkoutError.ts).
  let checkoutError = $state<CheckoutError | null>(null);
  function noteCheckoutChange(change: CheckoutErrorChange): void {
    checkoutError = afterCheckoutErrorChange(checkoutError, change);
  }
  // P2: the session the platform said needs the consent although the feed
  // session did not ask for it (the switch was turned on after the page
  // loaded). The checkbox is shown for it instead of a dead end.
  let consentForcedFor = $state<string | null>(null);
  // Spec 51-1: the session whose subscriber discount the platform said is
  // used up for the typed address; the next press sends `discount: false`.
  let marketingDiscountUsedFor = $state<string | null>(null);
  // Spec 43: the sales link / utm touch that brought the buyer, read from the
  // page address on mount (kept 7 days per feed token). Sent with checkout/start.
  let attribution: AttributionPayload | null = null;
  let clickIds: ClickIds = {};
  // Spec 50 A7: the anonymous buyer-funnel beacon (view, session_open,
  // seats_chosen). It reads its utm from the same stored touch as the checkout.
  // Built on first use, when the props are known; a step is sent once per
  // event and session. Fire-and-forget: never blocks or breaks the buyer.
  let funnelBeacon: Funnel | null = null;
  function sendFunnel(step: FunnelStep, sessionId?: string): void {
    if (!event) return;
    funnelBeacon ??= createFunnel({
      apiBase: resolvedApiBase,
      feedToken: normFeedToken,
      viewCountedByHost: parseFunnelHost(funnel),
      touch: () => attribution,
    });
    funnelBeacon.track(step, event.id, sessionId);
  }
  /** Spec 50 A7: the buyer touched the session's seats or tickets, which is the
   * click that opens a session for a single-session event (no chip to press). */
  function openedSession(): void {
    if (selectedSession) sendFunnel('session_open', selectedSession.id);
  }
  /** Why the last tap on a seat was refused (the order is at the seat cap), or null. */
  let cartLimitNotice = $state<string | null>(null);
  /**
   * Set of seat keys that are in conflict after a 409 `reservation.seats_conflict`
   * response from checkout/start or recover (WID-S2).  Passed into SeatMapView
   * so it can apply the WCAG-AA error-red conflict highlight overlay.
   */
  let conflictKeys = $state<ReadonlySet<string>>(new Set());
  let orderStatus = $state<CheckoutStatusResponse | null>(null);

  // Spec 65 E4: the on-page payment screen. The block carries the Stripe
  // client secret, so it lives in memory only (never in a storage or a URL)
  // and is dropped the moment the screen is left.
  let paymentBlock = $state<PaymentBlock | null>(null);
  let paymentExpiresAt = $state<string | null>(null);
  let paymentTotal = $state<number | null>(null);
  let paymentCurrency = $state('');
  // True when the buyer came back from a bank's own page (the URL carries the
  // token): the platform is asked first, the form shows only if still unpaid.
  let paymentAwaitReturn = $state(false);
  // Spec 65 E6a: the screen reports its steps here; the only listener turns
  // them into the `arena:checkout` CustomEvent for the host page. Nothing is
  // sent to any analytics system from the widget.
  // One bus per checkout token, so a second purchase in this tab reports again.
  const paymentSteps = createPaymentStepsRegistry(({ step, ...rest }) => {
    dispatchWidgetEvent(host, ARENA_EVENTS.CHECKOUT, { step, ...rest });
  });
  function clearPaymentBlock(): void {
    paymentBlock = null;
    paymentAwaitReturn = false;
    payingNotice = false;
  }
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
  const effectiveCart = $derived({ ...cart, expiresAt: cartExpiresAt });

  const cartCount = $derived(totalSelectionCount(selectedSeatKeys, gaQuantities));

  // Spec 50 A7: seats or tickets were put in the cart (once per session).
  $effect(() => {
    const sid = selectedSession?.id;
    if (cartCount > 0 && sid) untrack(() => sendFunnel('seats_chosen', sid));
  });

  // ── AddToCart for server-side Meta / GA4 (lib/addToCart.ts) ────────────────
  //
  // One id per cart, from its first item until it empties or a checkout
  // starts; the seats the buyer selected in it are remembered. Every quote
  // carries them, but only with the buyer's explicit tracking yes.

  let addToCart = initialAddToCartState();
  let addToCartSentFor: string | null = null;

  $effect(() => {
    const n = cartCount;
    const keys = selectedSeatKeys;
    untrack(() => {
      addToCart = trackCartChange(addToCart, n, keys);
    });
  });

  /** The buyer context now (the host's attribute may change after mount). */
  function buyerContextNow(): BuyerContextPayload | null {
    return buildBuyerContext({
      consent: trackingConsent,
      consentVersion: trackingConsentVersion,
      clickIds,
      cookies: readBrowserCookies(),
      pageUrl: window.location.origin + window.location.pathname,
    });
  }

  /** A quote of this cart, with the AddToCart fields when the buyer said yes. */
  function postTrackedQuote(payload: CheckoutQuotePayload, signal?: AbortSignal): Promise<CheckoutQuoteResponse> {
    if (!normFeedToken) return Promise.reject(new Error('no feed token'));
    const tracked = withAddToCart(payload, buyerContextNow(), addToCart, gaQuantities);
    if (tracked.add_to_cart_id) addToCartSentFor = tracked.add_to_cart_id;
    return postCheckoutQuote(normFeedToken, tracked, resolvedApiBase, signal);
  }

  /** Opening the cart is the AddToCart moment: one quote per cart, best effort. */
  function sendAddToCart(): void {
    if (!selectedSession || !addToCart.id || addToCartSentFor === addToCart.id) return;
    if (!isTrackingGranted(trackingConsent)) return;
    const payload = buildQuotePayload(selectedSession.id, [...selectedSeatKeys], buildGaItems(gaQuantities), promo.code);
    void postTrackedQuote(payload).catch(() => {});
  }

  // ── Promo code ─────────────────────────────────────────────────────────────
  //
  // The cart sheet never computes a discount: the code is checked by the
  // platform (`checkout/quote`, which holds nothing) and the sheet shows the
  // amounts that come back. All the timing rules (one current request, a quote
  // only for the cart it was asked about, a bounded wait) live in the promo
  // controller; this component only feeds it the cart and renders its state.

  let promo = $state<PromoState>(initialPromoState());
  // "Refer a friend": a code that came with the link (`?promo=`). It is applied
  // ONCE, to the first cart; the platform stays the judge of whether it fits.
  let wantedPromo = $state<string | null>(null);
  let wantedPromoTried = false;
  const promoStrings = $derived(getPromoStrings(normLocale));

  const cartKey = $derived(
    selectedSession ? cartQuoteKey(selectedSession.id, [...selectedSeatKeys], buildGaItems(gaQuantities)) : '',
  );

  const promoCtl = createPromoController({
    getCartKey: () => cartKey,
    buildPayload: (code) =>
      selectedSession ? buildQuotePayload(selectedSession.id, [...selectedSeatKeys], buildGaItems(gaQuantities), code) : null,
    quote: (payload, signal) => postTrackedQuote(payload, signal),
    strings: () => promoStrings,
    conflictText: (err) => (parseConflictsFromApiError(err).length > 0 ? t.conflict_notice : null),
    errorCode: (err) => (err instanceof ApiError ? err.code : undefined),
    onChange: (next) => {
      const change = promoCodeChange(promo.code, next.code);
      promo = next;
      if (change) noteCheckoutChange(change);
    },
  });

  $effect(() => {
    // Re-run whenever the cart's identity or size changes; the controller
    // decides whether the applied code needs a fresh quote.
    void cartKey;
    const lines = cart.lines.length;
    untrack(() => promoCtl.cartChanged(lines));
  });

  $effect(() => {
    const code = wantedPromo;
    if (!code || wantedPromoTried) return;
    // Not before the buyer has chosen something: a quote needs a cart.
    if (stage !== 'selecting' || cart.lines.length === 0 || !cartKey) return;
    wantedPromoTried = true;
    untrack(() => void promoCtl.apply(code));
  });

  /** The buyer took the code off, or the platform refused it: do not bring it back on a reload. */
  function forgetPromoFromLink(): void {
    forgetWantedPromo(normFeedToken);
  }

  // ── The mailing tick's price (spec 51-1) ───────────────────────────────────
  //
  // Ticked, the organizer's reward code may take something off: the pay button
  // then names the platform's price of the cart WITH the tick. It never blocks
  // paying; the payment page applies the discount whatever the button said.

  let marketingTicked = $state(false);
  let marketingQ = $state<MarketingQuoteState>(initialMarketingQuoteState());

  const marketingQuoter = createMarketingQuoter({
    getCartKey: () => cartKey,
    getCode: () => promo.code,
    buildPayload: (code) =>
      selectedSession ? buildQuotePayload(selectedSession.id, [...selectedSeatKeys], buildGaItems(gaQuantities), code) : null,
    consent: () =>
      selectedSession
        ? marketingPayload(selectedSession.marketing_optin, true, marketingDiscountUsedFor === selectedSession.id)
        : undefined,
    quote: (payload, signal) => postTrackedQuote(payload, signal),
    onChange: (next) => {
      marketingQ = next;
    },
  });

  $effect(() => {
    // The tick, the cart, the applied code or the used-up discount changed.
    const ticked = marketingTicked;
    void cartKey;
    void promo.code;
    void marketingDiscountUsedFor;
    untrack(() => void marketingQuoter.refresh(ticked));
  });

  // ── Seat hold on click (spec 67) ───────────────────────────────────────────
  //
  // On a seated session a click on a free seat holds it at once (lib/holds.ts).
  // The seats of the cart ARE the hold's seats (held + clicked-but-not-answered),
  // and the order sheet starts the checkout with the hold's key instead of the
  // seat list. Without a feed token or a plan the old path (seats at checkout)
  // stays.

  const emptyHoldView: HoldView = {
    held: new Set(),
    pending: new Set(),
    holdToken: null,
    step: null,
    expiresAt: null,
    extendAvailable: false,
    policy: null,
    expired: false,
    busy: false,
  };
  let holdView = $state.raw<HoldView>(emptyHoldView);
  // A plain variable: the controller of the selected session (one per session).
  let holdCtl: HoldController | null = null;
  // The platform answered hold.disabled: for the rest of this page view the
  // selection lives in memory and the seats go with the order, as before.
  let holdDisabled = false;
  // The words under the map after a click (taken seat, limit, extension, ...).
  let holdNotice = $state<string | null>(null);
  let holdNoticeTimer: ReturnType<typeof setTimeout> | null = null;
  // "Payment in progress" was sent to the platform: the screen says so.
  let payingNotice = $state(false);
  let nowMs = $state(Date.now());
  // Rendered seat map: refresh the statuses now, light the nearest free seats.
  let seatMap = $state<{ refreshStatuses(): Promise<void>; highlightNearest(seatKey: string): number } | null>(null);
  // seat key -> "row/number" for the list of seats taken meanwhile.
  let seatLabels = new Map<string, string>();

  const holdT = $derived(getHoldStrings(normLocale));
  const holdSecs = $derived(holdSecondsLeft(holdView.expiresAt, nowMs));
  const holdShown = $derived(holdView.held.size > 0 && holdView.expiresAt !== null);
  const holdExtendShown = $derived(extendVisible(holdView.expiresAt, holdView.extendAvailable, nowMs));
  // One expiry for the sheet and the bar: the hold's own (spec 67 §3.3).
  const cartExpiresAt = $derived(holdView.holdToken ? holdView.expiresAt : holdExpiresAt);

  function showHoldNotice(text: string | null, forMs = 0): void {
    if (holdNoticeTimer) clearTimeout(holdNoticeTimer);
    holdNoticeTimer = null;
    holdNotice = text;
    if (text && forMs > 0) {
      holdNoticeTimer = setTimeout(() => {
        holdNotice = null;
      }, forMs);
    }
  }

  function applyHoldView(v: HoldView): void {
    holdView = v;
    selectedSeatKeys = new Set([...v.held, ...v.pending]);
  }

  function seatList(keys: readonly string[]): string {
    return keys.map((k) => seatLabels.get(k) ?? k).join(', ');
  }

  /** A seat was taken by somebody else: look at the statuses now, light free seats nearby. */
  async function onHoldSeatTaken(keys: string[]): Promise<void> {
    const first = keys[0];
    if (!first) return;
    try {
      await seatMap?.refreshStatuses();
    } catch {
      /* the next poll brings them */
    }
    const lit = seatMap?.highlightNearest(first) ?? 0;
    showHoldNotice(lit > 0 ? holdT.seat_taken_near : holdT.seat_taken_none, lit > 0 ? PULSE_MS : 0);
  }

  function switchHold(sessionId: string, feed: string, base: string): void {
    holdCtl?.destroy();
    holdCtl = null;
    holdView = emptyHoldView;
    payingNotice = false;
    if (!sessionId || !feed || holdDisabled) return;
    const ctl: HoldController = createHoldController({
      sessionId,
      deviceId: getDeviceId(feed),
      storage: browserStorage(),
      post: (body, key) => postHold(feed, body, base, key),
      del: (token, seatKey) => deleteHoldSeat(token, seatKey, base),
      get: (token) => getHold(token, base),
      extend: (token) => postHoldExtend(token, base),
      // A prepared order is on its payment step: the hold takes no change
      // until that order is cancelled (keep_hold), so it goes first.
      beforeMutate: async () => {
        preparedCtl.update(false, cartKey);
        await preparedCtl.settled();
      },
      onChange: (v) => {
        if (holdCtl === ctl) applyHoldView(v);
      },
      onDisabled: (keys) => {
        if (holdCtl !== ctl) return;
        holdDisabled = true;
        holdCtl = null;
        ctl.destroy();
        holdView = emptyHoldView;
        // No message: the clicked seats are simply the plain selection now.
        selectedSeatKeys = new Set([...selectedSeatKeys, ...keys]);
      },
      onSeatTaken: (keys) => {
        if (holdCtl === ctl) void onHoldSeatTaken(keys);
      },
      onPartial: (keys) => {
        if (holdCtl !== ctl) return;
        showHoldNotice(fillHold(holdT.hold_rebook_partial, { seats: seatList(keys) }));
        void seatMap?.refreshStatuses().catch(() => {});
      },
      onRejected: (code, details) => {
        if (holdCtl === ctl) showHoldNotice(holdErrorText(code, normLocale, details));
      },
      onExtended: (min) => {
        if (holdCtl === ctl) showHoldNotice(fillHold(holdT.hold_extended, { min }), 5_000);
      },
    });
    holdCtl = ctl;
    void ctl.restore();
  }

  // One controller per session on a plan; a date switch leaves the old hold to
  // run out on its own (its key stays in storage and comes back with the date).
  $effect(() => {
    const sid = selectedSession?.schema_url ? selectedSession.id : '';
    const feed = normFeedToken;
    const base = resolvedApiBase;
    untrack(() => switchHold(sid, feed, base));
  });

  // The hold's clock: one tick a second while there is something to count; at
  // zero the platform is asked whether the hold is over (spec 67 §8.1).
  $effect(() => {
    if (!holdView.expiresAt) return;
    nowMs = Date.now();
    const id = setInterval(() => {
      nowMs = Date.now();
      if (holdSecondsLeft(holdView.expiresAt, nowMs) === 0) void holdCtl?.checkExpiry();
    }, 1000);
    return () => clearInterval(id);
  });

  /** The order sheet's answer carried the hold's clock on its payment step. */
  function noteOrderExpiry(expiresAt: string | null | undefined): void {
    holdCtl?.noteExpiry(expiresAt, 'payment');
  }

  // ── Early order (preload-payment="checkout") ───────────────────────────────
  //
  // A plain cart (no promo code, no marketing tick, total > 0) is prepared when
  // the sheet opens: order, hold and Stripe session without a buyer, and the
  // payment screen loads HIDDEN below. "Pay" attaches the buyer and reveals it.
  // Everything else, and every failure, goes the classic way at "Pay".

  // The prepared, unattached checkout (its block carries the client secret:
  // memory only, like paymentBlock).
  let preparedView = $state.raw<PreparedCheckout | null>(null);

  const prepareWanted = $derived(
    preload === 'checkout' &&
      cartSheetOpen &&
      stage === 'selecting' &&
      !checkoutToken &&
      // Spec 67: not while a click on a seat is still on its way (the hold would be out of date).
      !holdView.busy &&
      !holdView.expired &&
      !!selectedSession &&
      !!normFeedToken &&
      isPlainCart({
        promoCode: promo.code,
        promoBusy: promo.busy,
        marketingTicked,
        lineCount: cart.lines.length,
        total: cartTotal(cart.lines).amount,
      }),
  );

  /** checkout/start with prepare:true for the cart as it is now (no buyer, promo, marketing or consent). */
  function buildPreparePayload(): CheckoutPreparePayload {
    const payload: CheckoutPreparePayload = {
      session_id: selectedSession?.id ?? '',
      capabilities: ['stripe_elements'],
      prepare: true,
    };
    const seats = [...selectedSeatKeys];
    const gaItems = buildGaItems(gaQuantities);
    // Spec 67 §4.4: seats come from the hold, never both.
    if (holdView.holdToken) payload.hold_token = holdView.holdToken;
    else if (seats.length > 0) payload.seats = seats;
    if (gaItems.length > 0) payload.ga_items = gaItems;
    const returnUrl = buildReturnUrl();
    if (returnUrl) payload.return_url = returnUrl;
    if (typeof normLocale === 'string' && normLocale.length > 0) payload.locale = normLocale;
    const ctx = buildBuyerContext({
      consent: trackingConsent,
      consentVersion: trackingConsentVersion,
      clickIds,
      cookies: readBrowserCookies(),
      pageUrl: window.location.origin + window.location.pathname,
    });
    if (ctx) payload.buyer_context = ctx;
    // Spec 43: the same sales link / utm touch as the classic start.
    return attribution ? { ...payload, attribution } : payload;
  }

  // True while "Pay" takes the prepared order over (it is not cancelled then).
  let consumingPrepared = false;

  const preparedCtl = createPreparedCheckout({
    prepare: (idempotencyKey) =>
      postCheckoutPrepare(normFeedToken, buildPreparePayload(), resolvedApiBase, idempotencyKey),
    attach: (token, body) => postCheckoutBuyer(token, body, resolvedApiBase),
    // Spec 67 §4.5: with a hold the order goes and the seats stay in the hold.
    cancel: (token) =>
      postCheckoutCancel(token, resolvedApiBase, { keepalive: true, keepHold: holdView.holdToken !== null }),
    onChange: (p) => {
      const had = preparedView !== null;
      preparedView = p;
      // The sheet's countdown runs on the prepared hold.
      if (!checkoutToken) holdExpiresAt = p ? p.expiresAt : null;
      if (p) noteOrderExpiry(p.expiresAt);
      // The order went (not to be paid): the hold is back on its selection step.
      else if (had && !consumingPrepared) void preparedCtl.settled().then(() => holdCtl?.refresh());
    },
  });

  $effect(() => {
    // The sheet opened or closed, the cart, the promo code or the tick changed.
    const want = prepareWanted;
    const key = cartKey;
    untrack(() => preparedCtl.update(want, key));
  });

  onMount(() => {
    // The page is left: cancel what was prepared (keepalive), so the places
    // are free at once instead of when the hold runs out.
    const onPageHide = (): void => preparedCtl.pageHide();
    const onPageShow = (e: PageTransitionEvent): void => {
      if (e.persisted) preparedCtl.update(prepareWanted, cartKey);
    };
    window.addEventListener('pagehide', onPageHide);
    window.addEventListener('pageshow', onPageShow);
    return () => {
      window.removeEventListener('pagehide', onPageHide);
      window.removeEventListener('pageshow', onPageShow);
      preparedCtl.destroy();
      holdCtl?.destroy();
      if (holdNoticeTimer) clearTimeout(holdNoticeTimer);
    };
  });

  /**
   * The one payment screen: shown for a checkout on stage 'paying', HIDDEN for
   * a prepared one. Both have the same place in the DOM and the same key (the
   * checkout token), so "Pay" on a prepared checkout reveals the very screen
   * that loaded Stripe: its iframes are never moved or mounted again.
   */
  const payView = $derived.by(() => {
    if (stage === 'paying' && paymentBlock && checkoutToken) {
      return {
        token: checkoutToken,
        block: paymentBlock,
        shown: true,
        deferred: false,
        expiresAt: paymentExpiresAt,
        total: paymentTotal,
        currency: paymentCurrency,
      };
    }
    if (preparedView && stage === 'selecting') {
      return {
        token: preparedView.token,
        block: preparedView.block,
        shown: false,
        deferred: true,
        expiresAt: preparedView.paymentExpiresAt,
        total: preparedView.total,
        currency: preparedView.currency,
      };
    }
    return null;
  });

  // ── Helpers ────────────────────────────────────────────────────────────────

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

    // Spec 43: remember which sales link or utm label brought the buyer. The
    // address is only read, never changed (its query also carries checkout_token).
    attribution = captureAttribution(normFeedToken, window.location.search);
    // Storage is read and written only after the host granted tracking; the
    // ids of this page view are kept in memory either way.
    const consentGranted = isTrackingGranted(trackingConsent);
    clickIds = captureClickIds(normFeedToken, window.location.search, {
      storage: consentGranted ? undefined : null,
      persist: consentGranted,
    });
    wantedPromo = resolveWantedPromo(normFeedToken, promoAttribute, window.location.search);

    // Check for checkout_token in URL or sessionStorage first.
    const urlToken = getCheckoutTokenFromSearch(window.location.search);
    const storedToken = restoreCheckoutToken(undefined, tokenScope);
    const resumeToken = urlToken ?? storedToken;

    if (resumeToken) {
      // Restore order status view.
      checkoutToken = resumeToken;
      stage = 'order-status';
      void loadOrderStatus(resumeToken, true, urlToken !== null);
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
      loadError = buyerErrorText(err, normLocale, t.error_load_event);
    } finally {
      loading = false;
    }
  }

  /** Apply a loaded feed event detail to the widget state. */
  function applyFeedEvent(data: import('./types.js').FeedEventDetailResponse): void {
    event = data.event;
    selectedSession = pickInitialSession(data.event, normSessionId);
    // Spec 50 A7: the event was shown (a page around the widget may count it).
    // A session counts as opened only on a buyer action, never on load.
    sendFunnel('view');
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
    // Spec 67: "row/seat" names for the list of seats taken meanwhile.
    const labels = new Map<string, string>();
    for (const section of geometry.sections) {
      for (const row of section.rows) {
        for (const seat of row.seats) labels.set(seat.key, `${row.name}/${seat.number}`);
      }
    }
    seatLabels = labels;
    seatCategoryIndex = buildSeatCategoryIndex(geometry);
    categoryByCategoryIndex = buildCategoryByIndex(categoryPrices);
    tierById = selectedSession ? buildTierById(selectedSession.tiers) : new Map();
    gaTiers = selectedSession ? identifyGaTiers(selectedSession.tiers, categoryPrices) : [];
    seatedTiers = selectedSession ? seatedTiersOf(selectedSession.tiers, categoryPrices) : [];
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
    openedSession();
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
    openedSession();
    const prev = selectedSeatKeys;
    // Spec 67: on a plan the click holds the seat (or lets an own seat go).
    const ctl = holdCtl;
    if (ctl && selectedSession?.schema_url) {
      showHoldNotice(null);
      const own = holdView.held.has(seatKey) || holdView.pending.has(seatKey);
      if (!own && status !== 'available') return;
      if (!own && seatCapBlocks(prev, seatKey, status, MAX_CART_SEATS)) {
        cartLimitNotice = seatCapMessage(normLocale);
        return;
      }
      cartLimitNotice = null;
      const sid = selectedSession.id;
      const done = ctl.tap(seatKey);
      if (done === 'add') {
        dispatchWidgetEvent(host, ARENA_EVENTS.SEAT_SELECTED, { seatKey, sessionId: sid });
      } else if (done === 'remove') {
        if (conflictKeys.has(seatKey)) {
          const remainingConflicts = new Set(conflictKeys);
          remainingConflicts.delete(seatKey);
          conflictKeys = remainingConflicts;
        }
        dispatchWidgetEvent(host, ARENA_EVENTS.SEAT_RELEASED, { seatKey, sessionId: sid });
      }
      return;
    }
    // One order holds at most MAX_CART_SEATS seats (the platform refuses more):
    // say so instead of letting the buyer find out at the payment step.
    if (seatCapBlocks(prev, seatKey, status, MAX_CART_SEATS)) {
      cartLimitNotice = seatCapMessage(normLocale);
      return;
    }
    cartLimitNotice = null;
    const next = toggleSeatSelection(prev, seatKey, status, MAX_CART_SEATS);
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
    openedSession();
    const next = new Map(gaQuantities);
    next.set(tierId, qty);
    gaQuantities = next;
  }

  // ── Cart sheet handlers ────────────────────────────────────────────────────

  function openCartSheet(): void {
    cartSheetOpen = true;
    sendAddToCart();
    // WID-T2: notify host page that the cart sheet was opened (view_cart analytics).
    const sessionId = selectedSession?.id ?? '';
    dispatchWidgetEvent(host, ARENA_EVENTS.CART_OPENED, { sessionId, itemCount: cartCount });
  }

  function closeCartSheet(): void {
    cartSheetOpen = false;
    noteCheckoutChange({ type: 'cart_left' });
  }

  function handleRemoveLine(idx: number): void {
    // Remove from cart lines — requires rebuilding selection/ga state accordingly.
    // For simplicity, we remove the line from the cart.lines concept by adjusting
    // the underlying state (clear the corresponding seats or GA entry).
    const line = cart.lines[idx];
    if (!line) return;
    const sessionId = selectedSession?.id ?? '';
    if (line.type === 'seated' && holdCtl && selectedSession?.schema_url) {
      // Spec 67: the seats go off the hold; the selection follows its answer.
      holdCtl.release(line.seatKeys);
      for (const key of line.seatKeys) {
        dispatchWidgetEvent(host, ARENA_EVENTS.SEAT_RELEASED, { seatKey: key, sessionId });
      }
    } else if (line.type === 'seated') {
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

  /**
   * "Pay" with a prepared checkout: attach the buyer, reveal the hidden screen.
   * Returns false when the classic checkout/start must run instead.
   */
  async function payPrepared(values: BuyerFormValues): Promise<boolean> {
    if (!selectedSession) return false;
    const session = selectedSession;
    promoCtl.cancelPending();
    checkoutSubmitting = true;
    checkoutError = null;
    const outcome = await preparedCtl.pay({
      plain: prepareWanted && values.marketingAccepted !== true,
      cartKey,
      email: values.email.trim(),
      body: buildBuyerPayload(
        values,
        session.buyer_fields as import('./lib/checkout.js').BuyerFieldConfig[],
        {
          consent: consentPayload(values.consentAccepted === true, normLocale) ?? undefined,
          locale: typeof normLocale === 'string' && normLocale.length > 0 ? normLocale : undefined,
        },
      ),
    });
    // A second press while the first is running: nothing to do.
    if (outcome.kind === 'busy') return true;
    checkoutSubmitting = false;
    if (outcome.kind === 'classic') return false;
    if (outcome.kind === 'refused') {
      showCheckoutError(outcome.error, values);
      return true;
    }
    const p = outcome.prepared;
    // All in one synchronous step: the payment screen keeps its key (the token)
    // while it turns from hidden to shown, so Stripe is not mounted again.
    consumingPrepared = true;
    preparedCtl.consume(p.token);
    consumingPrepared = false;
    rememberCheckoutToken(p.token);
    checkoutToken = p.token;
    holdExpiresAt = outcome.attach?.expires_at ?? p.expiresAt;
    noteOrderExpiry(holdExpiresAt);
    dispatchWidgetEvent(host, ARENA_EVENTS.PAYMENT_STARTED, {
      checkoutToken: p.token,
      sessionId: session.id,
    });
    paymentBlock = p.block;
    paymentAwaitReturn = false;
    paymentExpiresAt = outcome.attach?.payment_expires_at ?? p.paymentExpiresAt;
    paymentTotal = p.total;
    paymentCurrency = p.currency;
    cartSheetOpen = false;
    stage = 'paying';
    return true;
  }

  async function handleCheckout(values: BuyerFormValues): Promise<void> {
    if (!selectedSession || !normFeedToken) return;
    // preload-payment="checkout": a prepared checkout is used when it can be;
    // otherwise (or after any failure) the classic path below runs unchanged.
    if (preload === 'checkout') {
      let handled = false;
      try {
        handled = await payPrepared(values);
      } catch {
        handled = false;
        checkoutSubmitting = false;
      }
      if (handled) return;
    }
    // An applied code whose quote is not for this very cart must not ride
    // along: the sheet would show one total and the payment page another.
    // An Apply still waiting when the buyer pays is dropped: the order goes
    // ahead without a code, and its answer can no longer change the total.
    promoCtl.cancelPending();
    if (promoPayBlocked(promo, cartKey)) {
      checkoutError = makeCheckoutError('other', promoStrings.error_generic);
      return;
    }
    checkoutSubmitting = true;
    checkoutError = null;
    try {
      // Spec 67: a click still on its way is answered first, so the hold is the cart.
      if (holdCtl) await holdCtl.idle();
      const holdToken = holdCtl?.view.holdToken ?? null;
      const seats = holdToken ? [] : [...selectedSeatKeys];
      const gaItems = buildGaItems(gaQuantities);
      const payload = buildCheckoutPayload(
        selectedSession.id,
        values,
        seats,
        gaItems,
        selectedSession.buyer_fields as import('./lib/checkout.js').BuyerFieldConfig[],
        promo.code,
      );
      // Where the hosted payment page returns the buyer. Origin + pathname
      // only — the backend appends `?checkout_token=…` itself, which is what
      // `getCheckoutTokenFromSearch` reads on the next load, so any query
      // string or hash we sent along would be dropped or would collide.
      if (holdToken) payload.hold_token = holdToken;
      const returnUrl = buildReturnUrl();
      if (returnUrl) payload.return_url = returnUrl;
      // Spec 65: say the widget can pay on its own page. A platform that does
      // not know the word answers with the hosted page as before.
      payload.capabilities = ['stripe_elements'];
      // Tracking context for server-side Meta / GA4: only when the host page
      // answered granted or denied; read now, the attribute may change later.
      // Consent given after mount: the ids seen on this page are kept now.
      if (isTrackingGranted(trackingConsent)) persistClickIds(normFeedToken, clickIds);
      const buyerCtx = buyerContextNow();
      if (buyerCtx) payload.buyer_context = buyerCtx;
      // Spec 43: the sales link / utm touch, when there is one. The platform
      // cleans it and never refuses a purchase over it.
      if (attribution) payload.attribution = attribution;
      // Spec 51-1: the organizer's mailing tick, sent only when ticked.
      const marketing = marketingPayload(
        selectedSession.marketing_optin,
        values.marketingAccepted === true,
        marketingDiscountUsedFor === selectedSession.id,
      );
      if (marketing) payload.marketing_consent = marketing;
      // Language for the buyer's ticket e-mail / PDF — the same resolved
      // locale the widget renders its own copy in. The backend ignores a
      // locale it has no e-mail template for, so sending it is always safe.
      if (typeof normLocale === 'string' && normLocale.length > 0) {
        payload.locale = normLocale;
      }
      // P2: a ticked consent box goes along with the language it was read in.
      const consent = consentPayload(values.consentAccepted === true, normLocale);
      if (consent) payload.consent = consent;
      const response = await postCheckoutStart(normFeedToken, payload, resolvedApiBase);
      // The checkout started: the next cart is a new one for AddToCart.
      addToCart = initialAddToCartState();
      addToCartSentFor = null;
      // Save token in case user returns after the payment page.
      rememberCheckoutToken(response.checkout_token);
      checkoutToken = response.checkout_token;
      // Store the hold expiry so MiniCart/CartSheet can show the countdown
      // during the brief redirecting stage (WID-S1 fix #3 + #4).
      holdExpiresAt = response.expires_at;
      noteOrderExpiry(response.expires_at);
      // WID-S5: notify host page that payment flow has started.
      dispatchWidgetEvent(host, ARENA_EVENTS.PAYMENT_STARTED, {
        checkoutToken: response.checkout_token,
        sessionId: selectedSession.id,
      });
      // An embedded payment: pay on this page. No `payment` key means the
      // classic hosted redirect below.
      const embedded = response.payment;
      if (
        embedded &&
        embedded.mode === 'elements' &&
        embedded.client_secret &&
        embedded.publishable_key
      ) {
        paymentBlock = embedded;
        paymentAwaitReturn = false;
        paymentExpiresAt = response.payment_expires_at ?? response.expires_at ?? null;
        paymentTotal = response.pricing?.total ?? null;
        paymentCurrency = response.pricing?.currency ?? '';
        cartSheetOpen = false;
        stage = 'paying';
        return;
      }
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
      showCheckoutError(err, values);
    } finally {
      checkoutSubmitting = false;
    }
  }

  /** The sheet's message after checkout/start (or the prepared attach) refused. */
  function showCheckoutError(err: unknown, values: BuyerFormValues): void {
    {
      // WID-S2: parse nested envelope 409 seat conflicts and surface them on
      // the seat map via the conflictKeys prop (SeatMapView → applyConflictHighlight).
      const conflicts = parseConflictsFromApiError(err);
      if (conflicts.length > 0) {
        conflictKeys = conflictKeySet(conflicts);
        checkoutError = makeCheckoutError('conflict', t.conflict_notice);
      } else if (err instanceof ApiError && isPromoErrorCode(err.code)) {
        // The quote said yes but start says no (a cap was used up in between,
        // or the buyer's e-mail is over the per-customer limit, which a quote
        // cannot know before the e-mail is typed). The code comes off so the
        // total on the sheet is the total the buyer will pay.
        conflictKeys = new Set();
        // Another e-mail ends the reason but keeps "code removed, price
        // without the discount" (the form view shows no total); applying a
        // code again, taking it off or leaving the cart ends it all.
        checkoutError = promoRefusal(
          promoErrorMessage(err.code, promoStrings),
          promoStrings.removed_full_price,
          values.email,
        );
        promoCtl.drop();
        forgetPromoFromLink();
      } else if (err instanceof ApiError && consentErrorMessage(err.code, normLocale) !== null) {
        // The channel asks for the consent and the platform did not get it.
        // If the box was not on screen (the feed said "not required"), show
        // it now so the buyer can tick it and pay.
        conflictKeys = new Set();
        checkoutError = makeCheckoutError('consent', consentErrorMessage(err.code, normLocale) ?? '');
        consentForcedFor = selectedSession?.id ?? null;
      } else if (err instanceof ApiError && marketingErrorMessage(err.code, normLocale) !== null) {
        // The reward is once per address: say so and let the buyer pay the
        // regular price with the next press (the tick is still recorded).
        conflictKeys = new Set();
        checkoutError = makeCheckoutError('other', marketingErrorMessage(err.code, normLocale) ?? '');
        if (isMarketingRewardRefusal(err.code)) marketingDiscountUsedFor = selectedSession?.id ?? null;
      } else if (err instanceof ApiError && err.code.startsWith('hold.')) {
        // Spec 67: the hold is over (hold.expired) or sits on another order.
        conflictKeys = new Set();
        checkoutError = makeCheckoutError('other', holdErrorText(err.code, normLocale, err.details));
        if (err.code === 'hold.expired') {
          cartSheetOpen = false;
          void holdCtl?.checkExpiry();
        }
      } else if (err instanceof ApiError && cartLimitMessage(err.code, normLocale) !== null) {
        // The platform refused the size of the cart: say so in the buyer's language.
        conflictKeys = new Set();
        checkoutError = makeCheckoutError('cart_limit', cartLimitMessage(err.code, normLocale) ?? '');
      } else {
        conflictKeys = new Set();
        checkoutError = makeCheckoutError('other', buyerErrorText(err, normLocale, t.error_checkout));
      }
    }
  }

  // ── Order status ───────────────────────────────────────────────────────────

  /**
   * @param resume  The page was (re)opened on an unfinished checkout: ask the
   *                platform to re-supply the embedded payment block, and if it
   *                does, show the payment screen instead of a bare "pending".
   * @param fromBank The address carried the token, i.e. the buyer returned from
   *                 a bank's own page.
   */
  async function loadOrderStatus(token: string, resume = false, fromBank = false): Promise<void> {
    try {
      orderStatus = resume
        ? await getCheckoutStatus(token, resolvedApiBase, { includePayment: true })
        : await getCheckoutStatus(token, resolvedApiBase);
      // WID-T3: use server-side expires_at from order-status to drive countdown.
      holdExpiresAt = orderStatus.expires_at ?? null;
      const resumed = orderStatus.payment;
      if (
        resume &&
        orderStatus.status === 'pending' &&
        resumed &&
        resumed.mode === 'elements' &&
        resumed.client_secret &&
        resumed.publishable_key
      ) {
        paymentBlock = resumed;
        // The secret now lives in paymentBlock only: take it off the status
        // object that other views read.
        orderStatus.payment = null;
        paymentAwaitReturn = fromBank;
        // The clock counts to the end of the payment window, not to the hold
        // (which ends a grace later).
        paymentExpiresAt = orderStatus.payment_expires_at ?? orderStatus.expires_at ?? null;
        paymentTotal = orderStatus.total ?? null;
        paymentCurrency = orderStatus.currency ?? '';
        stage = 'paying';
        return;
      }
      // WID-S5: notify host page about terminal order outcomes.
      const status = orderStatus.status;
      // A finished order must not follow the buyer around. The stored token
      // is only there to resume an UNFINISHED checkout; left in place after
      // 'paid' it made every other event page opened in the same tab show
      // the old order instead of the ticket picker, so a buyer could not buy
      // a second master class (first production events, 2026-09-20). The
      // return page itself keeps working: its token is in the URL.
      if (status === 'paid' || status === 'failed' || status === 'expired' || status === 'cancelled') {
        forgetCheckoutToken();
      }
      if (status === 'paid') {
        // The hold became the order: its key is of no use any more.
        holdCtl?.forget();
        dispatchWidgetEvent(host, ARENA_EVENTS.ORDER_PAID, {
          checkoutToken: token,
          // The order number of the ticket e-mail and the PDF.
          orderRef: orderStatus.order_number != null ? String(orderStatus.order_number) : null,
          totalMinorUnits: orderStatus.total ?? null,
          currency: orderStatus.currency ?? null,
        });
      } else if (status === 'failed' || status === 'expired' || status === 'cancelled') {
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
          await loadOrderStatus(newToken, resume, fromBank);
          return;
        } catch (recoveryErr) {
          // Recovery also failed — clear token, set visible error, fall back
          // to normal init so the user can start a fresh checkout.
          forgetCheckoutToken();
          checkoutToken = null;
          loadError = buyerErrorText(recoveryErr, normLocale, t.error_recovery);
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
        loadError = buyerErrorText(err, normLocale, t.error_order_status);
        stage = 'selecting';
        // Re-load event data so the widget is not left blank.
        if (normFeedToken && normEventId) {
          void loadFromFeed(normFeedToken, normEventId);
        }
        return;
      }
      loadError = buyerErrorText(err, normLocale, t.error_order_status);
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
        orderActionError = buyerErrorText(err, normLocale, t.error_recovery);
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

  /**
   * The payment screen is done: the order is paid or reached a final state.
   * The status is read once more through the ordinary path so the host-page
   * events (order paid / failed) and the token clean-up stay in ONE place.
   */
  async function finishPayment(): Promise<void> {
    clearPaymentBlock();
    stage = 'order-status';
    if (checkoutToken) await loadOrderStatus(checkoutToken);
  }

  /**
   * "Back" from the payment screen to the contacts screen. The cart (seats and
   * tickets) stays as it is and the hold keeps running on the server; pressing
   * "continue" there starts the checkout again, which replaces the open order.
   * The Stripe secret is dropped.
   */
  function backToContacts(): void {
    const leaving = checkoutToken;
    clearPaymentBlock();
    checkoutError = null;
    if (leaving && holdView.holdToken) {
      // Spec 67 section 4.5: the order goes, the seats stay in the hold; the next
      // "continue" starts a new order on the same hold.
      const hold = holdCtl;
      forgetCheckoutToken();
      checkoutToken = null;
      void postCheckoutCancel(leaving, resolvedApiBase, { keepalive: true, keepHold: true })
        .catch(() => null)
        .then(() => hold?.refresh());
    }
    stage = 'selecting';
    cartSheetOpen = true;
  }

  /** "Start again" on the payment screen: the order cannot be paid here any more. */
  function restartPayment(): void {
    handleRetry();
  }

  function handleRetry(): void {
    // The Stripe client secret never outlives the screen that used it.
    clearPaymentBlock();
    // Clear token and return to selecting stage.
    forgetCheckoutToken();
    checkoutToken = null;
    orderStatus = null;
    // Spec 67: the seats are the hold's; ask the platform what is left of it.
    if (holdCtl && holdView.holdToken) void holdCtl.refresh();
    else selectedSeatKeys = new Set();
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
    clearPaymentBlock();
    checkoutToken = null;
    orderStatus = null;
    holdCtl?.forget();
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
        {checkoutToken}
        actionLoading={orderActionLoading}
        actionError={orderActionError}
      />
    </div>

  {:else if stage === 'paying' && paymentBlock && checkoutToken}
    <!-- Pay on this page: the payment screen below, outside this chain, so a
         prepared (hidden) screen is the same instance once shown. -->

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
      class:arena-tickets-frame--flat={salesClosed || !(selectedSession && selectedSession.schema_url)}
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
          {tierColors}
          onSelectSession={(s) => {
            // Reset seat/GA selection when switching sessions so stale keys
            // from a different session don't carry over (WID-S1 fix #6).
            if (s?.id !== selectedSession?.id) {
              selectedSeatKeys = new Set();
              holdView = emptyHoldView;
              showHoldNotice(null);
              gaQuantities = new Map();
              holdExpiresAt = null;
              conflictKeys = new Set();
              checkoutError = null;
              cartLimitNotice = null;
              cartSheetOpen = false;
              openGaAreaTierId = null;
              gaAreas = [];
            }
            selectedSession = s;
            // Spec 50 A7: the buyer opened a session.
            if (s) sendFunnel('session_open', s.id);
          }}
        />
        {/if}
        {#if salesClosed}
          <p class="arena-tickets-cancelled" role="status" data-testid="session-cancelled-notice">{t.session_cancelled_notice}</p>
        {/if}
        {#if salesPaused && selectedSession}
          {#key selectedSession.id}
            <SaleStateNotice
              session={selectedSession}
              feedToken={normFeedToken}
              apiBase={resolvedApiBase}
              locale={normLocale}
              orgName={selectedSession.consent?.seller_legal_name ?? ''}
            />
          {/key}
        {/if}
        {#if cartLimitNotice}
          <p class="arena-tickets-limit" role="status">{cartLimitNotice}</p>
        {/if}
        {#if holdNotice}
          <p class="arena-tickets-limit" role="status" data-testid="hold-notice">{holdNotice}</p>
        {/if}
        <!-- A date sold by a partner box office: no map, no steppers, no cart; one button to the partner's site. -->
        {#if salesExternal && selectedSession}
          <ExternalSaleNotice session={selectedSession} locale={normLocale} />
        {/if}
        {#if !salesExternal}
        <!-- The seated category has no stepper, so it gets a card of the same kind that says where it is taken. -->
        {#if !salesClosed && !salesPaused && selectedSession?.schema_url && seatedTiers.length > 0}
          <div class="ga-tiers-section">
            <SeatedTierCard
              tiers={seatedTiers}
              note={tiersAreFree(seatedTiers) ? t.seat_hint_free : t.seat_hint_paid}
              freeLabel={t.seat_hint_free_label}
            />
          </div>
        {/if}

        <!-- Seat map (only for sessions with schema_url) -->
        {#if !salesClosed && !salesPaused && selectedSession && selectedSession.schema_url}
          <div class="arena-tickets-map-wrap">
            <SeatMapView
              session={selectedSession}
              locale={normLocale}
              selectedKeys={selectedSeatKeys}
              heldKeys={holdView.held}
              pendingKeys={holdView.pending}
              bind:this={seatMap}
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

        <!-- GA tier cards (below the map; the card above the map tells a buyer where the seated category is taken) -->
        {#if !salesClosed && !salesPaused && gaTiers.length > 0}
          <div class="ga-tiers-section">
            {#each gaTiers as tier (tier.id)}
              <GaTierCard
                {tier}
                quantity={gaQuantities.get(tier.id) ?? 0}
                onQuantityChange={onGaQuantityChange}
                locale={normLocale}
                timeZone={selectedSession?.timezone ?? null}
              />
            {/each}
          </div>
        {/if}

        <!-- Spec 67: the hold's clock, the extension and the end of the hold -->
        {#if holdView.expired}
          <div class="arena-hold arena-hold--ended" role="alert" data-testid="hold-expired">
            <span>{holdT.hold_expired}</span>
            <button type="button" class="arena-hold-btn" disabled={holdView.busy} onclick={() => void holdCtl?.rehold()}>
              {holdT.hold_rebook}
            </button>
          </div>
        {:else if holdShown}
          <div class="arena-hold" class:arena-hold--soon={holdSecs <= 120} role="timer" data-testid="hold-countdown">
            <span>{holdCountdownText(normLocale, formatCountdown(holdSecs))}</span>
            {#if holdExtendShown}
              <button type="button" class="arena-hold-btn" disabled={holdView.busy} onclick={() => void holdCtl?.extend()} data-testid="hold-extend">
                {fillHold(holdT.hold_extend, { min: extendMinutes(holdView.policy) })}
              </button>
            {/if}
          </div>
        {/if}

        <!-- Mini cart bar -->
        {#if selectedSession && !salesClosed && (!salesPaused || cart.lines.length > 0)}
          <MiniCart
            lines={cart.lines}
            expiresAt={holdShown ? null : holdExpiresAt}
            locale={normLocale}
            onOpen={openCartSheet}
            cta={t.continue_to_payment}
          />
        {/if}
        {/if}

        <!-- Event materials (task 2-2): gallery, videos, map, partner links,
             photo report. Drawn only when the event has any. -->
        {#if !materialsHidden && (event.materials || (event.venues && event.venues.length > 0))}
          <Materials
            materials={event.materials}
            locale={normLocale}
            apiBase={resolvedApiBase}
            venueNames={event.venue_names ?? []}
            venues={event.venues ?? []}
            rtl={dir === 'rtl'}
          />
        {/if}

        <!-- Platform line (spec 53): shown unless the event's brand has
             white label today. -->
        {#if showPlatformBadge(event.sessions, selectedSession)}
          <p class="arena-tickets-platform" data-arena-platform-badge>
            {badgeT.prefix}
            <a href={PLATFORM_BADGE_URL} target="_blank" rel="noopener" aria-label={`Arena Sold Out (${badgeT.new_tab})`}>Arena Sold Out</a>
          </p>
        {/if}

      {:else if !normSessionId}
        <div class="arena-tickets-placeholder" aria-hidden="true"></div>
      {/if}
    </div>

    <!-- Cart sheet (bottom drawer) -->
    {#if cartSheetOpen && selectedSession}
      <CartSheet
        cart={effectiveCart}
        {oneStep}
        buyerFields={selectedSession.buyer_fields as import('./lib/checkout.js').BuyerFieldConfig[]}
        serviceFeePercent={selectedSession.service_fee_percent}
        promoAvailable={selectedSession.promo_codes_available === true || wantedPromo !== null}
        promoExpanded={wantedPromo !== null}
        promoPrefill={wantedPromo ?? ''}
        showWithdrawalNotice={selectedSession.show_withdrawal_notice === true}
        consent={forceConsent(selectedSession.consent, consentForcedFor === selectedSession.id)}
        marketing={selectedSession.marketing_optin ?? null}
        marketingDiscountUsed={marketingDiscountUsedFor === selectedSession.id}
        {marketingTicked}
        marketingQuote={marketingQ}
        onMarketingChange={(ticked) => (marketingTicked = ticked)}
        cartReminder={selectedSession.cart_reminder === true}
        locale={normLocale}
        submitting={checkoutSubmitting}
        submitError={checkoutError?.text ?? null}
        promoCode={promo.code}
        promoQuote={promo.quote}
        {cartKey}
        promoBusy={promo.busy}
        promoError={promo.error}
        promoNote={promo.note}
        onApplyPromo={(code) => void promoCtl.apply(code)}
        onRemovePromo={() => {
          promoCtl.remove();
          noteCheckoutChange({ type: 'promo_removed' });
          forgetPromoFromLink();
        }}
        onEmailChange={(email) => noteCheckoutChange({ type: 'email_changed', email })}
        onContinue={() => promoCtl.cancelPending()}
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

  <!-- ── Pay on this page (Stripe Checkout Elements, spec 65 E4) ───────────
       Shown on stage 'paying'. With preload-payment="checkout" the screen of a
       prepared checkout is rendered here HIDDEN (laid out off-screen, invisible,
       aria-hidden, inert; never display:none, Stripe must be able to measure it)
       at the very place it is shown later: "Pay" only flips the class. -->
  {#if payView}
    {#key payView.token}
      <div
        class="arena-pay-slot"
        class:arena-pay-slot--hidden={!payView.shown}
        aria-hidden={payView.shown ? undefined : 'true'}
        inert={!payView.shown}
        data-testid="payment-slot"
        data-shown={payView.shown ? 'true' : 'false'}
      >
        <div class="arena-tickets-frame">
          <PaymentScreen
            payment={payView.block}
            checkoutToken={payView.token}
            apiBase={resolvedApiBase}
            locale={normLocale}
            expiresAt={payView.expiresAt}
            total={payView.total}
            currency={payView.currency}
            organizerName={selectedSession?.consent?.seller_legal_name ?? ''}
            awaitReturn={payView.shown && paymentAwaitReturn}
            steps={paymentSteps.forToken(payView.token)}
            deferred={payView.deferred}
            shown={payView.shown}
            onHandle={preparedCtl.setHandle}
            onDeferredFailure={preparedCtl.markFailed}
            onBeforeConfirm={(token) => {
              // Spec 67 section 4.8: tell the platform the payment is going on (never awaited).
              if (!holdView.holdToken) return;
              postCheckoutPaying(token, resolvedApiBase);
              payingNotice = true;
            }}
            onPaid={finishPayment}
            onTerminal={finishPayment}
            onRestart={restartPayment}
            onBack={cart.lines.length > 0 ? backToContacts : undefined}
          />
          {#if payingNotice && payView.shown}
            <p class="arena-tickets-limit" role="status" data-testid="hold-paying">{holdT.hold_paying}</p>
          {/if}
        </div>
      </div>
    {/key}
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
    /* A host with a fixed height squeezes this box; what no longer fits is
       clipped here instead of spilling over the category cards below it
       (this box is positioned, so it would paint ON TOP of them and
       swallow their clicks). */
    overflow: hidden;
  }
  .arena-tickets-map-wrap :global(.ga-popover) {
    top: 3rem;
    right: 0.75rem;
  }

  .arena-tickets-limit {
    margin: 0;
    padding: 0.5rem 1rem;
    font-size: 0.875rem;
    color: #92400e;
    background: #fef3c7;
  }

  /* Spec 67: the hold's clock under the map, above the cart bar. */
  .arena-hold {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
    padding: 0.5rem 1rem;
    font-size: 0.875rem;
    font-weight: 600;
    color: #134e4a;
    background: #ccfbf1;
  }
  .arena-hold--soon {
    color: #92400e;
    background: #fef3c7;
  }
  .arena-hold--ended {
    color: #7f1d1d;
    background: #fee2e2;
  }
  .arena-hold-btn {
    font: inherit;
    font-weight: 600;
    padding: 0.35rem 0.75rem;
    border: 1px solid currentColor;
    border-radius: var(--_radius, 6px);
    background: transparent;
    color: inherit;
    cursor: pointer;
  }
  .arena-hold-btn:disabled {
    opacity: 0.6;
    cursor: default;
  }

  .arena-tickets-cancelled {
    margin: 0;
    padding: 0.75rem 1rem;
    font-size: 0.95rem;
    font-weight: 600;
    color: #991b1b;
    background: #fee2e2;
  }

  .arena-tickets-placeholder {
    display: none;
  }

  /* A prepared payment screen loading while the buyer types: laid out (Stripe
     measures its mount points) but off-screen and invisible. Never
     display:none, and never moved: showing it only drops this class. */
  .arena-pay-slot {
    display: block;
    height: 100%;
  }
  .arena-pay-slot--hidden {
    position: fixed;
    top: 0;
    left: -10000px;
    width: min(100vw, 520px);
    visibility: hidden;
    pointer-events: none;
  }

  .arena-tickets-platform {
    margin: 12px 0 0;
    font-size: 12px;
    line-height: 1.4;
    text-align: center;
    opacity: 0.7;
  }

  .arena-tickets-platform a {
    color: inherit;
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
