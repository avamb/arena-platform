/**
 * poll.ts - how the order status page waits for the payment to land. The buyer is
 * usually back from the provider before its webhook reaches us, so for the first
 * minutes "not paid yet" only means "still checking": the way back to the payment
 * page is offered later, never while the payment may be on its way.
 */

/** For this long the page says "Проверяем платёж…" and offers no way back to the payment page. */
export const CHECKING_MS = 3 * 60_000;
/** After this the polling stops and the page says the check took too long. */
export const POLL_DEADLINE_MS = 12 * 60_000;

export type StatusPhase = 'checking' | 'late' | 'timeout';

export function phaseFor(elapsedMs: number): StatusPhase {
  if (elapsedMs >= POLL_DEADLINE_MS) return 'timeout';
  return elapsedMs >= CHECKING_MS ? 'late' : 'checking';
}

/**
 * The pause before the next request, or null when the deadline has passed. 4 s while all is well
 * (10 s once the first minutes are over); after failed requests the pause grows to 15 s, so a
 * flaky connection is retried instead of given up on.
 */
export function nextDelayMs(elapsedMs: number, failures: number): number | null {
  if (elapsedMs >= POLL_DEADLINE_MS) return null;
  const base = elapsedMs >= CHECKING_MS ? 10_000 : 4_000;
  if (failures <= 0) return base;
  return Math.min(15_000, Math.max(base, 4_000 * (failures + 1)));
}

/** The cart is cleared only by the order this browser started (its token is the stored one), never by an old link to another. */
export function isOwnOrder(token: string, stored: string | null | undefined): boolean {
  return !!token && !!stored && token === stored;
}
