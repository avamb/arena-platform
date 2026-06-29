import { useEffect, useState } from "react";
import { MIN_WIDTH_MD, minWidthQuery, type BreakpointName } from "./breakpoints";

/**
 * SSR-safe matchMedia hook.
 *
 * Returns `true` when the supplied media query currently matches. Server
 * (or non-DOM test) environments return the `defaultMatches` value so the
 * first render is deterministic; matchMedia is only consulted from inside
 * useEffect (i.e. after hydration).
 *
 * The hook intentionally subscribes via `addEventListener('change', ...)`
 * with an `addListener` fallback for older Safari (admin-web supports
 * Safari 14+; the fallback is cheap and keeps the test surface honest).
 */
export function useMediaQuery(query: string, defaultMatches = false): boolean {
  const [matches, setMatches] = useState<boolean>(defaultMatches);

  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
      return undefined;
    }
    const mql = window.matchMedia(query);
    const update = (): void => {
      setMatches(mql.matches);
    };
    update();
    if (typeof mql.addEventListener === "function") {
      mql.addEventListener("change", update);
      return () => {
        mql.removeEventListener("change", update);
      };
    }
    // Safari < 14 fallback
    mql.addListener(update);
    return () => {
      mql.removeListener(update);
    };
  }, [query]);

  return matches;
}

/**
 * True when the viewport is at or above the canonical desktop/mobile cut
 * (md, 768 px). Below this width admin-web renders the mobile shell.
 *
 * Pass `defaultMatches=true` on routes whose first paint should optimise
 * for the desktop layout (most admin surfaces); pass `false` on routes
 * known to be mobile-first.
 */
export function useIsDesktop(defaultMatches = true): boolean {
  return useMediaQuery(MIN_WIDTH_MD, defaultMatches);
}

/**
 * Generic breakpoint hook -- `true` when the viewport is at or above the
 * named breakpoint. SSR-safe.
 */
export function useBreakpoint(name: BreakpointName, defaultMatches = true): boolean {
  return useMediaQuery(minWidthQuery(name), defaultMatches);
}
