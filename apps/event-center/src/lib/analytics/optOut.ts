/**
 * The person's choice to stay out of the product analytics (spec 50, decision 4).
 *
 * "Help improve the service" is ON until the person turns it off. The choice
 * lives in this browser (localStorage) until the user record has a field for
 * it, so it follows the browser, not the account. Every storage access is
 * guarded: a blocked storage means "not opted out" (the default) and a choice
 * that lasts until the page reloads.
 */
import { useSyncExternalStore } from "react";

export const OPT_OUT_KEY = "arena_analytics_opt_out";

let memory = false;
const listeners = new Set<() => void>();

export function isOptedOut(): boolean {
  let stored = false;
  try {
    stored = localStorage.getItem(OPT_OUT_KEY) === "1";
  } catch {
    // Storage blocked: the in-memory choice below.
  }
  return memory || stored;
}

export function setOptedOut(value: boolean): void {
  try {
    if (value) localStorage.setItem(OPT_OUT_KEY, "1");
    else localStorage.removeItem(OPT_OUT_KEY);
    memory = false; // the storage holds the choice
  } catch {
    // The choice then lasts until the page reloads. A failed write can never lose an opt-out.
    memory = value;
  }
  for (const fn of [...listeners]) fn();
}

function subscribe(fn: () => void): () => void {
  listeners.add(fn);
  // Another tab changed the choice.
  const onStorage = (e: StorageEvent) => {
    if (e.key === OPT_OUT_KEY || e.key === null) fn();
  };
  if (typeof window !== "undefined") window.addEventListener("storage", onStorage);
  return () => {
    listeners.delete(fn);
    if (typeof window !== "undefined") window.removeEventListener("storage", onStorage);
  };
}

/** `[optedOut, setOptedOut]` for a screen. */
export function useAnalyticsOptOut(): readonly [boolean, (value: boolean) => void] {
  const out = useSyncExternalStore(subscribe, isOptedOut, () => false);
  return [out, setOptedOut];
}
