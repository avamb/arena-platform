/**
 * Accessibility primitives for the SuperAdmin shell (SAUI-13).
 *
 * Four small hooks compose the WCAG 2.2 AA keyboard contract our drawers
 * and dialogs require:
 *
 *   useEscapeClose(active, onClose)
 *     Listen for the Escape key while `active` is true and invoke
 *     `onClose`. Bound at the window so the operator does not need to
 *     focus the drawer to dismiss it.
 *
 *   useFocusOnMount(active, ref)
 *     When `active` flips from false to true, move keyboard focus to the
 *     element referenced by `ref`. Used to land the cursor on the close
 *     button when a drawer opens.
 *
 *   useFocusRestore(active)
 *     When `active` flips from false to true, record document.activeElement.
 *     When `active` flips back to false, restore focus to that element.
 *     This is the "where did I come from" guarantee for keyboard users so
 *     the dialog cannot strand them at the document body.
 *
 *   useFocusTrap(active, containerRef)
 *     Intercept Tab / Shift+Tab while `active` is true and cycle focus
 *     within the descendants of `containerRef`. Only used by true modal
 *     dialogs -- the inline drawers under /orders, /tickets, /refunds,
 *     /organizations are non-modal and intentionally do NOT trap focus.
 *
 * The hooks are SSR-safe: they short-circuit when `window` is undefined
 * and never read from the DOM until React calls the effect.
 */
import { useEffect, useRef, type RefObject } from "react";
import {
  FOCUSABLE_SELECTOR,
  isElementFocusable,
  isEscapeKey,
  isTabKey,
  nextTrapIndex,
} from "./focusTraversal";

/**
 * Bind a window-level Escape listener while `active` is true.
 *
 * The handler captures the event (preventDefault) so a nested input's
 * Escape does not race a parent component's listener. If the caller
 * needs the input's native Escape behaviour they should layer their own
 * onKeyDown that stops propagation before this hook sees it.
 */
export function useEscapeClose(active: boolean, onClose: () => void): void {
  useEffect(() => {
    if (!active) {
      return;
    }
    if (typeof window === "undefined") {
      return;
    }
    const handler = (event: KeyboardEvent): void => {
      if (isEscapeKey(event.key)) {
        event.preventDefault();
        onClose();
      }
    };
    window.addEventListener("keydown", handler);
    return () => {
      window.removeEventListener("keydown", handler);
    };
  }, [active, onClose]);
}

/**
 * Focus the element referenced by `ref` when `active` flips from false
 * to true. We use requestAnimationFrame so the ref is attached after
 * React has flushed the dialog's first commit.
 */
export function useFocusOnMount<T extends HTMLElement>(
  active: boolean,
  ref: RefObject<T | null>,
): void {
  useEffect(() => {
    if (!active) {
      return;
    }
    if (typeof window === "undefined") {
      return;
    }
    const id = window.requestAnimationFrame(() => {
      ref.current?.focus();
    });
    return () => {
      window.cancelAnimationFrame(id);
    };
  }, [active, ref]);
}

/**
 * Remember the active element on open and restore focus to it on close.
 *
 * If the saved element has since been removed from the DOM (the typical
 * "the row I clicked just re-rendered" case) the restore is a no-op and
 * the browser falls back to document.body. Callers that need a stronger
 * guarantee should pass a stable ref via `useFocusOnMount` to a known
 * neighbour after the dialog closes.
 */
export function useFocusRestore(active: boolean): void {
  const previousRef = useRef<HTMLElement | null>(null);
  useEffect(() => {
    if (typeof document === "undefined") {
      return;
    }
    if (active) {
      const previous = document.activeElement as HTMLElement | null;
      previousRef.current = previous ?? null;
      return;
    }
    const previous = previousRef.current;
    previousRef.current = null;
    if (previous !== null && typeof previous.focus === "function") {
      if (previous.isConnected !== false) {
        previous.focus();
      }
    }
  }, [active]);
}

/**
 * Trap Tab focus inside the container while `active` is true.
 *
 * The hook attaches a single keydown listener at the container element
 * and:
 *
 *   - rebuilds the focusable order on each keypress (cheap; modal
 *     contents are short and operator-facing latency is irrelevant);
 *   - calls preventDefault before delegating to focus() so the browser's
 *     default tab traversal cannot escape the modal;
 *   - falls back to focusing the container itself if the modal has no
 *     focusable children -- this is the WCAG-friendly outcome for a
 *     dialog that ships with all interactive children disabled.
 */
export function useFocusTrap<T extends HTMLElement>(
  active: boolean,
  containerRef: RefObject<T | null>,
): void {
  useEffect(() => {
    if (!active) {
      return;
    }
    if (typeof window === "undefined") {
      return;
    }
    const container = containerRef.current;
    if (container === null) {
      return;
    }
    const handler = (event: KeyboardEvent): void => {
      if (!isTabKey(event.key)) {
        return;
      }
      const all = Array.from(
        container.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR),
      ).filter((el) => isElementFocusable(el));
      if (all.length === 0) {
        event.preventDefault();
        container.focus();
        return;
      }
      const current = all.indexOf(document.activeElement as HTMLElement);
      const next = nextTrapIndex(all, current, event.shiftKey);
      if (next < 0) {
        return;
      }
      event.preventDefault();
      all[next].focus();
    };
    container.addEventListener("keydown", handler);
    return () => {
      container.removeEventListener("keydown", handler);
    };
  }, [active, containerRef]);
}
