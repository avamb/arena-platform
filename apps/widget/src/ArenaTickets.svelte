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
   * WID-B: orchestrates feed loading, session selection, seat-map render
   * and status polling.  Seat-map and session list are sub-components.
   */
  import { onMount } from 'svelte';
  import { parseLocale, parseFeedToken, parseSessionId, isRtlLocale } from './utils.js';
  import { fetchFeedEvent } from './api.js';
  import type { FeedSession, FeedEvent } from './types.js';
  import SessionList from './components/SessionList.svelte';
  import SeatMapView from './components/SeatMapView.svelte';

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

  // ── Event data ─────────────────────────────────────────────────────────────

  let event = $state<FeedEvent | null>(null);
  let selectedSession = $state<FeedSession | null>(null);
  let loading = $state(false);
  let loadError = $state<string | null>(null);

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
    // The feed requires both a token and an event ID.  The token alone isn't
    // enough to know which event to show — for now the widget expects a
    // session-id that can be used to back-derive the event (future: feed
    // browsing with event list will use the feed list endpoint).
    if (!normFeedToken || !normSessionId) return;

    loading = true;
    loadError = null;

    // Derive eventId from sessionId for the public feed endpoint.
    // The session ID IS used as the route (we call /schema directly).
    // For now, if session-id is provided, we use it directly with the
    // schema endpoint (no event detail needed for the seated view).
    // The full event-detail flow (date chips) requires an event-id attribute.
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
  // (kept as a no-op hook so WID-C can extend without refactoring the mount)
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

  // Expose for future use (suppresses noUnusedLocals via void-assignment in template).
  void loadFromFeed;
</script>

<!--
  Arena Tickets Widget — Shadow DOM root.
  Theme is controlled via CSS custom properties on the host:

    arena-tickets {
      --arena-font-family: 'Inter', sans-serif;
      --arena-color-primary: #1a1a1a;
      --arena-accent: #6366f1;
      --arena-bg: #fff;
      --arena-radius: 8px;
      --arena-border-color: #e5e7eb;
    }
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
  {#if hasToken}
    <div class="arena-tickets-frame">
      {#if loading}
        <div class="arena-tickets-loading" aria-live="polite" aria-busy="true">Loading…</div>
      {:else if loadError}
        <div class="arena-tickets-error" role="alert">{loadError}</div>
      {:else if event && event.sessions.length > 0}
        <!-- Session date chips + legend -->
        <SessionList
          sessions={event.sessions}
          {selectedSession}
          onSelectSession={(s) => { selectedSession = s; }}
        />
        <!-- Seat map (only for sessions with schema_url) -->
        {#if selectedSession && selectedSession.schema_url}
          <SeatMapView session={selectedSession} locale={normLocale} />
        {:else if selectedSession}
          <div class="arena-tickets-ga" aria-label="General admission session">
            <!-- GA tier list will be rendered by WID-C -->
          </div>
        {/if}
      {:else if !normSessionId}
        <div class="arena-tickets-placeholder" aria-hidden="true"></div>
      {/if}
    </div>
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
    --_accent: var(--arena-accent, #6366f1);
    --_radius: var(--arena-radius, 8px);
    --_border: var(--arena-border-color, #e5e7eb);
    --_text-muted: var(--arena-color-secondary, #6b7280);
    /* Focus ring — defaults to accent colour. Override with --arena-focus-ring. */
    --_focus-ring: var(--arena-focus-ring, var(--arena-accent, #6366f1));
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
    color: #dc2626;
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

  /* ── RTL layout adjustments ────────────────────────────────────────────────
   * When locale="he" (or any RTL locale), dir="rtl" is set on
   * .arena-tickets-root, flipping text alignment and flex order.
   * CSS logical properties (margin-inline-start, padding-inline-end, etc.)
   * are used in sub-components to automatically adapt to the writing direction.
   */
  [dir='rtl'] {
    text-align: start; /* logical: maps to right for RTL */
    direction: rtl;
  }

  [dir='rtl'] .arena-tickets-frame {
    /* Flex direction and border radius are direction-agnostic; no change needed. */
  }
</style>
