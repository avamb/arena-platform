/**
 * The host page hides its own "Buy tickets" buttons while the buyer is in the
 * checkout sheet (owner decision 2026-11-24, Lampyris sales page). It reads
 * the open sheet from ONE attribute on <arena-tickets>: `data-cart-open`.
 *
 * Source-level assertions (`?raw`), the idiom of widget_hosted_card.test.ts.
 */

import { describe, it, expect } from 'vitest';
import widgetSource from './ArenaTickets.svelte?raw';

describe('data-cart-open on the host element', () => {
  it('mirrors the open checkout sheet and every stage after seat selection on the host', () => {
    // An inline payment step (embedded Stripe) closes the sheet first: the stage keeps the attribute.
    expect(widgetSource).toMatch(/if \(cartSheetOpen \|\| stage !== 'selecting'\) host\.setAttribute\('data-cart-open', ''\);/);
    expect(widgetSource).toMatch(/else host\.removeAttribute\('data-cart-open'\);/);
  });

  it('is driven by the same state that renders the sheet', () => {
    expect(widgetSource).toContain('{#if cartSheetOpen && selectedSession}');
  });
});
