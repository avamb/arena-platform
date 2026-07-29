import { describe, expect, it, vi } from "vitest";
import {
  requestPermissionRefresh,
  subscribeToPermissionRefresh,
  subscribeToWindowFocus,
} from "@/lib/auth/permissionRefresh";

describe("permission refresh triggers", () => {
  it("notifies the provider after a forbidden mutation signal", () => {
    const listener = vi.fn();
    const unsubscribe = subscribeToPermissionRefresh(listener);

    requestPermissionRefresh("forbidden-mutation");

    expect(listener).toHaveBeenCalledWith("forbidden-mutation");
    unsubscribe();
  });

  it("subscribes to window focus and cleans up the listener", () => {
    let focusListener: (() => void) | undefined;
    const target = {
      addEventListener: vi.fn((_event: "focus", listener: () => void) => {
        focusListener = listener;
      }),
      removeEventListener: vi.fn(),
    };
    const onFocus = vi.fn();

    const unsubscribe = subscribeToWindowFocus(onFocus, target);
    focusListener?.();
    unsubscribe();

    expect(onFocus).toHaveBeenCalledOnce();
    expect(target.removeEventListener).toHaveBeenCalledWith("focus", onFocus);
  });
});
