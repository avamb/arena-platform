/**
 * selection.ts — Pure seat-selection logic for the Arena Tickets widget (WID-C).
 *
 * All functions are pure and side-effect-free so they can be unit-tested in a
 * Node.js environment without a DOM.
 *
 * Responsibilities:
 *  • Optimistic tap/keyboard seat selection (toggle semantics).
 *  • «Best available» N adjacent seat picker using a row-rank heuristic.
 *  • Single-seat-gap client-side hint detection.
 *
 * The `selectedKeys` set is treated as immutable — every mutating function
 * returns a new Set so callers can do reference-equality checks cheaply.
 */

import type { Geometry, SeatStatusValue } from '../types.js';

// ─── Types ────────────────────────────────────────────────────────────────────

/** Immutable set of currently-selected seat keys. */
export type SelectedKeys = ReadonlySet<string>;

// ─── Toggle selection ─────────────────────────────────────────────────────────

/**
 * Toggle seat `seatKey` in the current selection.
 *
 * Rules:
 *  - Only `available` seats (or already-selected seats being deselected) may
 *    be toggled.  Held / sold / blocked seats are silently ignored.
 *  - Deselecting a seat that is already in the set removes it.
 *  - Selecting a seat that is already in the set is a no-op (idempotent add).
 *
 * Returns a new Set (never mutates the input).
 */
export function toggleSeatSelection(
  selected: SelectedKeys,
  seatKey: string,
  status: SeatStatusValue | undefined,
): Set<string> {
  const next = new Set(selected);
  if (next.has(seatKey)) {
    // Deselect regardless of live status (user can always un-pick).
    next.delete(seatKey);
  } else {
    // Only select if the seat is currently available.
    const s = status ?? 'available';
    if (s === 'available') {
      next.add(seatKey);
    }
  }
  return next;
}

/**
 * Clear the full selection.  Returns a new empty Set.
 */
export function clearSelection(): Set<string> {
  return new Set();
}

// ─── Best available ───────────────────────────────────────────────────────────

/**
 * Pick up to `count` adjacent available seats in a specific price category
 * using a row-rank heuristic.
 *
 * Algorithm:
 *  1. Walk every section → row in geometry order.
 *  2. For each row, slide a window of `count` seats looking for the *leftmost*
 *     consecutive run of `count` available seats whose `category_index` matches
 *     the requested `categoryIndex`.
 *  3. Rank candidate rows by the number of available seats they have in the
 *     requested category (most-available first).  Tie-break is geometry order
 *     (first row encountered wins).
 *  4. Return the seat keys from the top-ranked row's first qualifying run.
 *
 * Returns an empty array when no row has `count` consecutive available seats
 * in the requested category.
 */
export function bestAvailableSeats(
  geometry: Geometry,
  seatStatuses: Record<string, SeatStatusValue>,
  categoryIndex: number,
  count: number,
): string[] {
  if (count <= 0) return [];

  interface RowCandidate {
    availableCount: number;
    run: string[]; // first qualifying consecutive run of exactly `count` keys
  }

  let best: RowCandidate | null = null;

  for (const section of geometry.sections) {
    for (const row of section.rows) {
      const seats = row.seats;
      let availableCount = 0;
      let run: string[] | null = null;
      let currentRun: string[] = [];

      for (const seat of seats) {
        const status = seatStatuses[seat.key] ?? 'available';
        const matches =
          seat.category_index === categoryIndex && status === 'available';

        if (matches) {
          currentRun.push(seat.key);
          availableCount++;
          // Capture the first qualifying run of exactly `count` seats.
          if (run === null && currentRun.length >= count) {
            run = currentRun.slice(0, count);
          }
        } else {
          currentRun = [];
        }
      }

      if (run === null) continue; // this row has no qualifying run

      if (
        best === null ||
        availableCount > best.availableCount
      ) {
        best = { availableCount, run };
      }
    }
  }

  return best?.run ?? [];
}

// ─── Single-seat-gap hint ─────────────────────────────────────────────────────

/**
 * Detect available seats that would be left as an isolated single-seat gap
 * given the current selection.
 *
 * An "isolated" seat is an available (and not selected) seat where both its
 * immediate neighbours in the same row are "occupied" — i.e. held, sold,
 * blocked, or selected by the current user.  Row edges count as occupied.
 *
 * Returns the seat keys of the isolated available seats so the UI can show a
 * warning badge.
 */
export function detectSingleSeatGaps(
  geometry: Geometry,
  seatStatuses: Record<string, SeatStatusValue>,
  selected: SelectedKeys,
): string[] {
  const gaps: string[] = [];

  for (const section of geometry.sections) {
    for (const row of section.rows) {
      const seats = row.seats;
      for (let i = 0; i < seats.length; i++) {
        const seat = seats[i];
        // Only candidate: available and not selected.
        const status = seatStatuses[seat.key] ?? 'available';
        if (status !== 'available' || selected.has(seat.key)) continue;

        const leftOccupied =
          i === 0 || _isSeatOccupied(seats[i - 1], seatStatuses, selected);
        const rightOccupied =
          i === seats.length - 1 ||
          _isSeatOccupied(seats[i + 1], seatStatuses, selected);

        if (leftOccupied && rightOccupied) {
          gaps.push(seat.key);
        }
      }
    }
  }

  return gaps;
}

/** @internal */
function _isSeatOccupied(
  seat: { key: string },
  seatStatuses: Record<string, SeatStatusValue>,
  selected: SelectedKeys,
): boolean {
  if (selected.has(seat.key)) return true;
  const status = seatStatuses[seat.key] ?? 'available';
  return status !== 'available';
}

// ─── GA zone quantity helpers ─────────────────────────────────────────────────

/** Minimum quantity for a GA zone stepper (never below 1). */
export const GA_MIN_QUANTITY = 1;

/** Maximum quantity allowed per GA zone line (client-side guard). */
export const GA_MAX_QUANTITY = 20;

/**
 * Clamp a GA quantity to [GA_MIN_QUANTITY, min(GA_MAX_QUANTITY, zoneCapacity)].
 */
export function clampGaQuantity(qty: number, zoneCapacity: number): number {
  const max = Math.min(GA_MAX_QUANTITY, zoneCapacity);
  return Math.max(GA_MIN_QUANTITY, Math.min(max, qty));
}

/**
 * Increment GA quantity respecting limits.
 */
export function incrementGaQuantity(qty: number, zoneCapacity: number): number {
  return clampGaQuantity(qty + 1, zoneCapacity);
}

/**
 * Decrement GA quantity respecting limits.
 */
export function decrementGaQuantity(qty: number, zoneCapacity: number): number {
  return clampGaQuantity(qty - 1, zoneCapacity);
}
