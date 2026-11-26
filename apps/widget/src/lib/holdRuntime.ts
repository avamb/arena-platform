/**
 * holdRuntime.ts — the seat hold at the click (spec 67) as ONE lazily loaded
 * chunk: the controller, its routes and its words. The widget loads it with a
 * dynamic `import()` only when a session with a seat plan is chosen, so a GA
 * page never downloads it. Import only types from here statically.
 */

export { startWidgetHold } from './holdWidget.js';
export { getHoldStrings, holdErrorText } from './holdI18n.js';
export { applyHoldStyles, createPulser } from './holdDraw.js';
export { default as HoldClock } from '../components/HoldClock.svelte';
