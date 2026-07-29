/**
 * Small bridge between the API client and AuthProvider.
 *
 * The API layer cannot refresh React state itself, so a rejected mutation
 * announces that permissions may have changed. AuthProvider is the sole
 * subscriber in production and performs the authoritative /v1/me refetch.
 */
export type PermissionRefreshTrigger = "forbidden-mutation";

type Listener = (trigger: PermissionRefreshTrigger) => void;

const listeners = new Set<Listener>();

export function subscribeToPermissionRefresh(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function requestPermissionRefresh(trigger: PermissionRefreshTrigger): void {
  for (const listener of listeners) {
    listener(trigger);
  }
}

interface FocusEventTarget {
  addEventListener(type: "focus", listener: () => void): void;
  removeEventListener(type: "focus", listener: () => void): void;
}

/** Attach a browser-focus refetch without making server rendering depend on window. */
export function subscribeToWindowFocus(
  onFocus: () => void,
  target: FocusEventTarget | undefined =
    typeof window === "undefined" ? undefined : window,
): () => void {
  if (target === undefined) {
    return () => undefined;
  }
  target.addEventListener("focus", onFocus);
  return () => target.removeEventListener("focus", onFocus);
}
