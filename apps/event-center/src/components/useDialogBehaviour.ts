/**
 * What every modal dialog needs: Escape closes it, Tab stays inside it, and
 * the control that opened it gets the focus back (same rules as StepUpDialog).
 */
import { useCallback, useEffect, useRef, type KeyboardEvent as ReactKeyboardEvent, type RefObject } from "react";
import { trapFocusIndex } from "@/lib/stepUp";

const FOCUSABLE = 'button:not([disabled]), input:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])';

export function useDialogBehaviour(
  box: RefObject<HTMLElement>,
  initialFocus: RefObject<HTMLElement>,
  onEscape: () => void,
): { onKeyDown: (e: ReactKeyboardEvent<HTMLElement>) => void } {
  const escape = useRef(onEscape);
  escape.current = onEscape;

  useEffect(() => {
    const before = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    initialFocus.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") escape.current();
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      if (before !== null && before.isConnected) before.focus();
    };
  }, [initialFocus]);

  const onKeyDown = useCallback(
    (e: ReactKeyboardEvent<HTMLElement>) => {
      if (e.key !== "Tab" || box.current === null) return;
      const items = Array.from(box.current.querySelectorAll<HTMLElement>(FOCUSABLE));
      const next = trapFocusIndex(items.length, items.indexOf(document.activeElement as HTMLElement), e.shiftKey);
      if (next < 0) return;
      e.preventDefault();
      items[next].focus();
    },
    [box],
  );
  return { onKeyDown };
}
