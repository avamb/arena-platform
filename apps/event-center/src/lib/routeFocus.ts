/**
 * Moves the keyboard and screen-reader focus to the new screen's heading
 * after a navigation (spec section 9, review finding L6). Without it the
 * focus stays on the link that was pressed, often a link that no longer
 * exists, and a screen reader does not announce that the screen changed.
 *
 * The heading may appear only after the screen's data has loaded, so the
 * hook looks for it for up to two seconds and then falls back to <main>.
 * It never takes the focus away from a field the person is typing in, and
 * it does nothing on the first page load.
 */
import { useEffect, useRef } from "react";
import { useRouterState } from "@tanstack/react-router";

const LOOK_EVERY_MS = 50;
const LOOK_FOR_MS = 2_000;

function typing(el: Element | null): boolean {
  return el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement || el instanceof HTMLSelectElement;
}

function focusTarget(el: HTMLElement): void {
  if (!el.hasAttribute("tabindex")) el.setAttribute("tabindex", "-1");
  el.focus();
}

export function useFocusOnRouteChange(): void {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const first = useRef(true);
  useEffect(() => {
    if (first.current) {
      first.current = false;
      return;
    }
    let waited = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const look = () => {
      if (typing(document.activeElement)) return;
      const heading = document.querySelector<HTMLElement>("#main h1");
      if (heading !== null) {
        focusTarget(heading);
        return;
      }
      waited += LOOK_EVERY_MS;
      if (waited >= LOOK_FOR_MS) {
        const main = document.getElementById("main");
        if (main !== null) focusTarget(main);
        return;
      }
      timer = setTimeout(look, LOOK_EVERY_MS);
    };
    timer = setTimeout(look, 0);
    return () => clearTimeout(timer);
  }, [pathname]);
}
