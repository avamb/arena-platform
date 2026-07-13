<script lang="ts">
  /**
   * SeatMapView — SVG seat map with pan/zoom and live status polling.
   *
   * WID-B requirements:
   *   • Fetch schema honoring ETag (immutable cache after first load).
   *   • Render SVG: sections/rows/seats + decor backdrop + standing zones.
   *   • Pan/zoom via pointer events (drag, pinch, wheel) + fit/reset buttons.
   *   • Poll seat-status snapshot + delta every 2–5 s; backoff on hidden tab.
   *   • Keyed status recolor without re-render (applySeatStatusUpdate).
   *   • 1 500-seat map interactive <100 ms after data.
   */

  import { onMount, onDestroy } from 'svelte';
  import type { FeedSession, CategoryPrice, SeatStatusValue } from '../types.js';
  import { fetchSessionSchema } from '../api.js';
  import {
    buildSeatMapSVG,
    applySeatStatusUpdate,
    applyConflictHighlight,
    clearConflictHighlight,
    applySelectionHighlights,
    buildCategoryColorMap,
  } from '../lib/seatmap-render.js';
  import {
    IDENTITY,
    fitTransform,
    wheelZoom,
    pan,
    pinchZoom,
    pointerDist,
    pointerMid,
    toSVGTransform,
    type PanZoomState,
  } from '../lib/pan-zoom.js';
  import { SeatStatusPoller } from '../lib/poller.js';

  interface Props {
    session: FeedSession;
    locale?: string;
    /**
     * Set of seat_key strings that are in conflict (from a 409
     * `reservation.seats_conflict` response).  When provided, those seats are
     * highlighted with the WCAG-AA error red overlay via `applyConflictHighlight`.
     * Pass an empty Set or `undefined` to clear the conflict highlight.
     */
    conflictKeys?: ReadonlySet<string>;
    /** Set of currently selected seat keys for highlight rendering. */
    selectedKeys?: ReadonlySet<string>;
    /** Called when user taps/clicks a seat circle. */
    onSeatTap?: (seatKey: string, status: SeatStatusValue) => void;
    /** Called after the seat map schema is loaded with geometry + category prices. */
    onSchemaLoaded?: (geometry: import('../types.js').Geometry, categoryPrices: import('../types.js').CategoryPrice[]) => void;
  }

  const { session, locale = 'en', conflictKeys, selectedKeys = new Set(), onSeatTap, onSchemaLoaded }: Props = $props();

  // ── State ──────────────────────────────────────────────────────────────────

  let svgContainer: HTMLDivElement | undefined = $state();
  let svgHTML = $state('');
  let categoryPrices = $state<CategoryPrice[]>([]);
  let seatStatuses = $state<Record<string, SeatStatusValue>>({});
  let transform = $state<PanZoomState>(IDENTITY);
  let schemaLoading = $state(true);
  let schemaError = $state<string | null>(null);
  let canvasW = $state(800);
  let canvasH = $state(600);

  // Category-color lookup, memoized: rebuilt only when categoryPrices changes,
  // NOT on every seat-status poll tick (2–5 s cadence).
  const catColorMap = $derived(buildCategoryColorMap(categoryPrices, []));

  // ── Schema fetch ────────────────────────────────────────────────────────────

  async function loadSchema(sessionId: string): Promise<void> {
    schemaLoading = true;
    schemaError = null;
    try {
      const schema = await fetchSessionSchema(sessionId);
      canvasW = schema.geometry.canvas.width || 800;
      canvasH = schema.geometry.canvas.height || 600;
      categoryPrices = schema.category_prices;
      // Initial render with empty statuses (status poll follows immediately).
      svgHTML = buildSeatMapSVG(schema.geometry, schema.category_prices, seatStatuses);
      onSchemaLoaded?.(schema.geometry, schema.category_prices);
    } catch (err) {
      schemaError = err instanceof Error ? err.message : 'Failed to load seat map';
    } finally {
      schemaLoading = false;
    }
  }

  // ── Status polling ──────────────────────────────────────────────────────────

  let poller: SeatStatusPoller | null = null;

  function startPoller(sessionId: string): void {
    poller?.stop();
    poller = new SeatStatusPoller({
      sessionId,
      normalInterval: 3_000,
      hiddenInterval: 30_000,
      onUpdate(seats, _version) {
        seatStatuses = { ...seatStatuses, ...seats };
        // Keyed DOM update — no re-render, no per-tick map allocation.
        if (svgContainer) {
          applySeatStatusUpdate(svgContainer, seats, catColorMap);
        }
      },
      onError(err) {
        // Non-fatal — log and continue polling.
        console.warn('[arena-tickets] seat-status poll error:', err.message);
      },
    });
    poller.start();
  }

  // ── Fit/reset ───────────────────────────────────────────────────────────────

  function applyFit(): void {
    if (!svgContainer) return;
    const { offsetWidth: w, offsetHeight: h } = svgContainer;
    transform = fitTransform(canvasW, canvasH, w, h);
  }

  function applyReset(): void {
    transform = IDENTITY;
  }

  // ── Pointer / wheel events ──────────────────────────────────────────────────

  // Active pointer tracking for drag and pinch.
  let dragActive = false;
  let lastDragX = 0;
  let lastDragY = 0;
  const activePointers = new Map<number, { x: number; y: number }>();
  let lastPinchDist = 0;

  // Tap vs drag discrimination.
  let pointerDownX = 0;
  let pointerDownY = 0;
  const DRAG_THRESHOLD = 8;

  function onPointerDown(e: PointerEvent): void {
    if (!svgContainer) return;
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    activePointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
    if (activePointers.size === 1) {
      dragActive = true;
      lastDragX = e.clientX;
      lastDragY = e.clientY;
      pointerDownX = e.clientX;
      pointerDownY = e.clientY;
    } else if (activePointers.size === 2) {
      dragActive = false;
      const pts = [...activePointers.values()];
      lastPinchDist = pointerDist(pts[0]!, pts[1]!);
    }
  }

  function onContainerClick(e: MouseEvent): void {
    const dx = e.clientX - pointerDownX;
    const dy = e.clientY - pointerDownY;
    if (Math.abs(dx) > DRAG_THRESHOLD || Math.abs(dy) > DRAG_THRESHOLD) return;
    if (!onSeatTap) return;
    const target = e.target as Element;
    const seatEl = target.closest('[data-seat-key]');
    if (!seatEl) return;
    const key = seatEl.getAttribute('data-seat-key') ?? '';
    const status = (seatEl.getAttribute('data-status') ?? 'available') as SeatStatusValue;
    if (key) onSeatTap(key, status);
  }

  /**
   * Roving-tabindex navigation (WID-R4).
   *
   * When focus is on a seat circle ([data-seat-key]):
   *   ArrowLeft/Right — move within the current row (does NOT wrap).
   *   ArrowUp/Down    — move to the same column index in the prev/next row.
   *   Home/End        — jump to the first/last seat in the current row.
   *
   * Navigation updates tabindex: the target seat gets tabindex="0" and receives
   * focus; the previously-focused seat gets tabindex="-1".  This maintains one
   * Tab stop per row (the last-focused seat in that row) so subsequent Tabs
   * continue to cycle through rows rather than individual seats.
   */
  function navigateSeat(current: Element, navKey: string): void {
    if (!svgContainer) return;

    const rowGroup = current.closest('[data-row-key]');
    if (!rowGroup) return;

    const rowSeats = Array.from(rowGroup.querySelectorAll<Element>('[data-seat-key]'));
    const seatIdx = rowSeats.indexOf(current);
    if (seatIdx === -1) return;

    let target: Element | null = null;

    if (navKey === 'ArrowRight') {
      target = seatIdx + 1 < rowSeats.length ? (rowSeats[seatIdx + 1] ?? null) : null;
    } else if (navKey === 'ArrowLeft') {
      target = seatIdx > 0 ? (rowSeats[seatIdx - 1] ?? null) : null;
    } else if (navKey === 'Home') {
      target = rowSeats[0] ?? null;
    } else if (navKey === 'End') {
      target = rowSeats[rowSeats.length - 1] ?? null;
    } else if (navKey === 'ArrowDown' || navKey === 'ArrowUp') {
      const allRows = Array.from(svgContainer.querySelectorAll<Element>('[data-row-key]'));
      const rowIdx = allRows.indexOf(rowGroup);
      const nextRowIdx = navKey === 'ArrowDown' ? rowIdx + 1 : rowIdx - 1;
      if (nextRowIdx >= 0 && nextRowIdx < allRows.length) {
        const nextRow = allRows[nextRowIdx]!;
        const nextRowSeats = Array.from(nextRow.querySelectorAll<Element>('[data-seat-key]'));
        target = nextRowSeats[Math.min(seatIdx, nextRowSeats.length - 1)] ?? null;
      }
    }

    if (target && target !== current) {
      current.setAttribute('tabindex', '-1');
      target.setAttribute('tabindex', '0');
      (target as HTMLElement).focus();
    }
  }

  function onContainerKeydown(e: KeyboardEvent): void {
    const target = e.target as Element;
    const seatEl = target.closest?.('[data-seat-key]') ?? null;

    if (seatEl) {
      if (e.key === 'Enter' || e.key === ' ') {
        if (!onSeatTap) return;
        e.preventDefault();
        const seatKey = seatEl.getAttribute('data-seat-key') ?? '';
        const status = (seatEl.getAttribute('data-status') ?? 'available') as SeatStatusValue;
        if (seatKey) onSeatTap(seatKey, status);
        return;
      }

      if (
        e.key === 'ArrowLeft' ||
        e.key === 'ArrowRight' ||
        e.key === 'ArrowUp' ||
        e.key === 'ArrowDown' ||
        e.key === 'Home' ||
        e.key === 'End'
      ) {
        e.preventDefault();
        navigateSeat(seatEl, e.key);
        return;
      }
    }
  }

  function onPointerMove(e: PointerEvent): void {
    activePointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
    if (activePointers.size === 1 && dragActive) {
      const dx = e.clientX - lastDragX;
      const dy = e.clientY - lastDragY;
      transform = pan(transform, dx, dy);
      lastDragX = e.clientX;
      lastDragY = e.clientY;
    } else if (activePointers.size === 2) {
      const pts = [...activePointers.values()];
      const p1 = pts[0]!;
      const p2 = pts[1]!;
      const dist = pointerDist(p1, p2);
      const mid = pointerMid(p1, p2);
      if (!svgContainer) return;
      const rect = svgContainer.getBoundingClientRect();
      const mx = mid.x - rect.left;
      const my = mid.y - rect.top;
      transform = pinchZoom(transform, lastPinchDist, dist, mx, my);
      lastPinchDist = dist;
    }
  }

  function onPointerUp(e: PointerEvent): void {
    activePointers.delete(e.pointerId);
    if (activePointers.size < 2) {
      dragActive = activePointers.size === 1;
      if (dragActive) {
        const pt = [...activePointers.values()][0]!;
        lastDragX = pt.x;
        lastDragY = pt.y;
      }
    }
  }

  function onWheel(e: WheelEvent): void {
    e.preventDefault();
    if (!svgContainer) return;
    const rect = svgContainer.getBoundingClientRect();
    const px = e.clientX - rect.left;
    const py = e.clientY - rect.top;
    transform = wheelZoom(transform, e.deltaY, px, py);
  }

  // ── Reactive effects ────────────────────────────────────────────────────────

  // Reload schema and restart poller when session changes.
  $effect(() => {
    const sessionId = session.id;
    const hasSchema = session.schema_url;

    void loadSchema(sessionId).then(() => {
      if (hasSchema) {
        startPoller(sessionId);
        // Auto-fit after schema loads.
        if (svgContainer) applyFit();
      }
    });

    return () => {
      poller?.stop();
    };
  });

  // Reapply fit when container size changes (ResizeObserver).
  $effect(() => {
    if (!svgContainer) return;
    const ro = new ResizeObserver(() => {
      if (!schemaLoading && svgHTML) applyFit();
    });
    ro.observe(svgContainer);
    return () => ro.disconnect();
  });

  onMount(() => {
    // Wheel events are passive by default; we need non-passive to prevent page scroll.
    if (svgContainer) {
      svgContainer.addEventListener('wheel', onWheel, { passive: false });
    }
  });

  onDestroy(() => {
    poller?.stop();
    if (svgContainer) {
      svgContainer.removeEventListener('wheel', onWheel);
    }
  });

  // Computed SVG transform attribute.
  const svgTransform = $derived(toSVGTransform(transform));

  // ── Selection highlights ────────────────────────────────────────────────────
  let prevSelectedKeys = $state<ReadonlySet<string>>(new Set());

  $effect(() => {
    if (!svgContainer || !svgHTML) return;
    applySelectionHighlights(svgContainer, selectedKeys, prevSelectedKeys);
    prevSelectedKeys = selectedKeys;
  });

  // ── Conflict highlight (WID-R2) ─────────────────────────────────────────────
  // Track previous conflict keys so we can clear the highlight when they change.
  let prevConflictKeys = $state<ReadonlySet<string>>(new Set());

  // When conflictKeys changes, apply or clear the seat conflict overlay.
  // Runs after the SVG has been inserted into the DOM (svgHTML is already set).
  $effect(() => {
    if (!svgContainer || schemaLoading) return;
    const keys = conflictKeys ?? (new Set<string>());
    if (keys.size > 0) {
      // Apply the error-red highlight to newly conflicting seats.
      applyConflictHighlight(svgContainer, keys);
    } else if (prevConflictKeys.size > 0) {
      // Conflicts were cleared (empty set passed) — restore real seat statuses.
      clearConflictHighlight(svgContainer, prevConflictKeys, catColorMap, seatStatuses);
    }
    prevConflictKeys = keys;
  });
</script>

<div class="seat-map-wrap" data-locale={locale}>
  <!-- ── Toolbar ── -->
  <div class="seat-map-toolbar" aria-label="Seat map controls">
    <button class="ctrl-btn" onclick={applyFit} title="Fit map to screen" aria-label="Fit seat map to screen">
      <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
        <path d="M1 5V2h3M12 1h3v3M15 11v3h-3M4 15H1v-3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
      </svg>
    </button>
    <button class="ctrl-btn" onclick={applyReset} title="Reset view" aria-label="Reset seat map view">
      <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
        <path d="M8 1v3l3-3-3-3v3a6 6 0 1 0 5.66 4H12a4 4 0 1 1-4-4z" fill="currentColor"/>
      </svg>
    </button>
  </div>

  <!-- ── Map container ── -->
  <!-- svelte-ignore a11y_no_noninteractive_tabindex a11y_no_noninteractive_element_interactions -->
  <div
    class="seat-map-container"
    bind:this={svgContainer}
    onpointerdown={onPointerDown}
    onpointermove={onPointerMove}
    onpointerup={onPointerUp}
    onpointercancel={onPointerUp}
    onclick={onContainerClick}
    onkeydown={onContainerKeydown}
    aria-label="Interactive seat map"
    role="application"
    tabindex="0"
  >
    {#if schemaLoading}
      <div class="seat-map-state" aria-live="polite">Loading seat map…</div>
    {:else if schemaError}
      <div class="seat-map-state seat-map-error" role="alert">{schemaError}</div>
    {:else if svgHTML}
      <!-- Wrap SVG in a transform group so zoom never triggers layout reflow.
           NOT aria-hidden: the SVG inside carries interactive, focusable seats
           (role="button"/tabindex) and an accessible group name; only the
           decorative decor layer inside the SVG is aria-hidden. -->
      <div class="seat-map-inner" style="transform: {svgTransform}">
        {@html svgHTML}
      </div>
    {/if}
  </div>
</div>

<style>
  .seat-map-wrap {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-height: 0;
    position: relative;
  }

  .seat-map-toolbar {
    display: flex;
    gap: 0.375rem;
    padding: 0.5rem 0.75rem;
    border-bottom: 1px solid var(--_border, #e5e7eb);
  }

  .ctrl-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    border: 1px solid var(--_border, #e5e7eb);
    border-radius: var(--_radius, 6px);
    background: transparent;
    cursor: pointer;
    color: inherit;
    font-family: inherit;
    transition: background 0.1s;
  }

  .ctrl-btn:hover {
    background: color-mix(in srgb, currentColor 8%, transparent);
  }

  .seat-map-container {
    flex: 1;
    overflow: hidden;
    position: relative;
    touch-action: none; /* let pointer events handle pan/zoom */
    user-select: none;
    cursor: grab;
    outline: none;
  }

  .seat-map-container:active {
    cursor: grabbing;
  }

  .seat-map-inner {
    position: absolute;
    top: 0;
    left: 0;
    transform-origin: 0 0;
    will-change: transform;
  }

  .seat-map-state {
    display: flex;
    align-items: center;
    justify-content: center;
    height: 100%;
    min-height: 120px;
    color: var(--_text-muted, #6b7280);
    font-size: 0.9rem;
  }

  .seat-map-error {
    color: #b91c1c;
    background: #fef2f2;
    border-radius: var(--_radius, 6px);
    margin: 1rem;
    padding: 1rem;
    height: auto;
  }
</style>
