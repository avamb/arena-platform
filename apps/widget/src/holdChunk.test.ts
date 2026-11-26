/** The seat hold at the click (spec 67) stays out of the main bundle: only a dynamic import reaches it. */

import { describe, expect, it } from 'vitest';
import arenaTicketsSource from './ArenaTickets.svelte?raw';
import seatMapSource from './components/SeatMapView.svelte?raw';
import holdBasicsSource from './lib/holdBasics.ts?raw';

// A static value import of one of these modules pulls the hold chunk back in.
const LAZY = /^\s*import\s+(?!type\b)[^;]*from\s+'[./]*(lib\/)?(holds|holdI18n|holdDraw|holdWidget|holdRuntime)\.js'/m;
const LAZY_COMPONENT = /^\s*import\s+HoldClock\s+from/m;

describe('hold chunk', () => {
  it('ArenaTickets loads the hold runtime by dynamic import only', () => {
    expect(arenaTicketsSource).toContain("import('./lib/holdRuntime.js')");
    expect(arenaTicketsSource).not.toMatch(LAZY);
    expect(arenaTicketsSource).not.toMatch(LAZY_COMPONENT);
  });

  it('SeatMapView takes the hold drawing as a prop, never by import', () => {
    expect(seatMapSource).not.toMatch(LAZY);
  });

  it('holdBasics, which stays in the main bundle, imports none of the lazy modules', () => {
    expect(holdBasicsSource).not.toMatch(/from\s+'\.\/(holds|holdI18n|holdDraw|holdWidget|holdRuntime)\.js'/);
  });
});
