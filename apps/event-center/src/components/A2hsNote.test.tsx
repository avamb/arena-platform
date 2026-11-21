// @vitest-environment jsdom
/**
 * "Add to home screen" is for a phone. On a wide screen (the width where the
 * tab bar moves under the header, 900 px) it never shows. The new-version
 * banner reuses its frame and must still show everywhere.
 */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@/lib/i18n";
import { A2hsNote, WIDE_QUERY } from "@/components/AppNav";
import { NewVersionBanner } from "@/components/NewVersionBanner";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | null = null;
let host: HTMLDivElement | null = null;

/** A matchMedia that knows only the wide query; every other query is false. */
function screenWidth(wide: boolean) {
  const listeners = new Set<() => void>();
  let isWide = wide;
  window.matchMedia = ((query: string) => ({
    // Live, like a real MediaQueryList.
    get matches() {
      return query === WIDE_QUERY ? isWide : false;
    },
    media: query,
    addEventListener: (_: string, fn: () => void) => listeners.add(fn),
    removeEventListener: (_: string, fn: () => void) => listeners.delete(fn),
  })) as unknown as typeof window.matchMedia;
  return {
    resize(next: boolean) {
      isWide = next;
      listeners.forEach((fn) => fn());
    },
  };
}

function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root?.render(<I18nProvider locale="en">{node}</I18nProvider>));
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  localStorage.clear();
  vi.restoreAllMocks();
});

describe("A2hsNote", () => {
  it("shows on a phone", () => {
    screenWidth(false);
    mount(<A2hsNote />);
    expect(document.querySelector('[data-testid="a2hs"]')).not.toBeNull();
  });

  it("does not show on a wide screen", () => {
    screenWidth(true);
    mount(<A2hsNote />);
    expect(document.querySelector('[data-testid="a2hs"]')).toBeNull();
  });

  it("follows a resize", () => {
    const screen = screenWidth(false);
    mount(<A2hsNote />);
    expect(document.querySelector('[data-testid="a2hs"]')).not.toBeNull();
    act(() => screen.resize(true));
    expect(document.querySelector('[data-testid="a2hs"]')).toBeNull();
    act(() => screen.resize(false));
    expect(document.querySelector('[data-testid="a2hs"]')).not.toBeNull();
  });

  it("keeps the new-version banner on a wide screen", async () => {
    screenWidth(true);
    mount(<NewVersionBanner enabled running="a1" fetchDeployed={async () => "b2"} reload={() => undefined} />);
    await act(async () => {
      await Promise.resolve();
    });
    expect(document.querySelector('[data-testid="new-version"]')).not.toBeNull();
  });
});
