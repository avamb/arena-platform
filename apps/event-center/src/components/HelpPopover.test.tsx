// @vitest-environment jsdom
/** The round "?" help: opens on press, closes on Escape, on an outside press and when another one opens. */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it } from "vitest";
import { HelpPopover } from "@/components/HelpPopover";
import { CATALOGS, I18nProvider, LOCALES } from "@/lib/i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const roots: Root[] = [];
afterEach(() => {
  for (const r of roots.splice(0)) act(() => r.unmount());
  document.body.innerHTML = "";
});

function mount() {
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  roots.push(root);
  act(() => {
    root.render(
      <I18nProvider locale="en">
        <h2>
          Sales <HelpPopover textKey="help.eventSales" testId="h1" />
        </h2>
        <h2>
          Letters <HelpPopover textKey="help.letterText" testId="h2" />
        </h2>
        <button type="button" data-testid="elsewhere">
          elsewhere
        </button>
      </I18nProvider>,
    );
  });
}

const q = (id: string) => document.querySelector<HTMLElement>(`[data-testid="${id}"]`)!;
const shown = (id: string) => document.querySelector(`[data-testid="${id}"]`) !== null;
const press = (el: HTMLElement) => act(() => el.dispatchEvent(new MouseEvent("click", { bubbles: true })));

describe("HelpPopover", () => {
  it("is a button that names what it does and starts closed", () => {
    mount();
    const b = q("h1-button");
    expect(b.tagName).toBe("BUTTON");
    expect(b.getAttribute("aria-expanded")).toBe("false");
    expect(b.getAttribute("aria-label")).toBe("Show an explanation");
    expect(b.getAttribute("aria-controls")).toBeNull();
    expect(shown("h1-text")).toBe(false);
    press(b);
    expect(b.getAttribute("aria-controls")).toBe(q("h1-text").id);
    expect(q("h1-text").getAttribute("role")).toBe("note");
  });

  it("opens on a press and closes on a second press", () => {
    mount();
    press(q("h1-button"));
    expect(q("h1-button").getAttribute("aria-expanded")).toBe("true");
    expect(shown("h1-text")).toBe(true);
    expect(q("h1-text").textContent).toContain("Counted from the orders");
    press(q("h1-button"));
    expect(shown("h1-text")).toBe(false);
  });

  it("closes on Escape and gives the focus back to the button", () => {
    mount();
    press(q("h1-button"));
    act(() => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(shown("h1-text")).toBe(false);
    expect(document.activeElement).toBe(q("h1-button"));
  });

  it("closes on a press outside, but not on a press inside its text", () => {
    mount();
    press(q("h1-button"));
    act(() => q("h1-text").dispatchEvent(new MouseEvent("mousedown", { bubbles: true })));
    expect(shown("h1-text")).toBe(true);
    act(() => q("elsewhere").dispatchEvent(new MouseEvent("mousedown", { bubbles: true })));
    expect(shown("h1-text")).toBe(false);
  });

  it("keeps one popover open at a time", () => {
    mount();
    press(q("h1-button"));
    press(q("h2-button"));
    expect(shown("h1-text")).toBe(false);
    expect(shown("h2-text")).toBe(true);
  });
});

describe("help texts", () => {
  it("exist in every language and never name the old systems", () => {
    const keys = Object.keys(CATALOGS.en).filter((k) => k.startsWith("help."));
    expect(keys.length).toBeGreaterThan(10);
    for (const locale of LOCALES) {
      for (const k of keys) {
        const text = (CATALOGS[locale] as Record<string, string>)[k];
        expect(text, `${locale} ${k}`).toBeTruthy();
        expect(text).not.toMatch(/bil24|wordpress/i);
      }
    }
  });
});
