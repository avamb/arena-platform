// @vitest-environment jsdom
/** "What is here?": opens on a press, closes on Escape and gives the focus back, lists the lines the role sees. */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ScreenHelp } from "@/components/ScreenHelp";
import { I18nProvider } from "@/lib/i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const state = vi.hoisted(() => ({ denied: [] as string[] }));

vi.mock("@/screens/OrgLayout", () => ({
  useOrg: () => ({ orgId: "o1", orgName: "Org", access: { can: (p: string) => !state.denied.includes(p) } }),
}));
vi.mock("@/lib/auth", () => ({
  useMe: () => ({ data: { organization_memberships: [{ org_id: "o1", modules: [], shop_mode: null }] } }),
  organizationsOf: (m: unknown[]) => m,
}));

const roots: Root[] = [];
beforeEach(() => {
  state.denied = [];
  vi.stubEnv("VITE_FEATURE_WIZARD", "1");
});
afterEach(() => {
  vi.unstubAllEnvs();
  for (const r of roots.splice(0)) act(() => r.unmount());
  document.body.innerHTML = "";
});

function mount(locale: "en" | "ru" = "en") {
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  roots.push(root);
  act(() => {
    root.render(
      <I18nProvider locale={locale}>
        <div className="screen-head">
          <h1>Events</h1>
          <ScreenHelp screenId="events" />
        </div>
        <button type="button" data-testid="elsewhere">
          elsewhere
        </button>
      </I18nProvider>,
    );
  });
}

const q = (id: string) => document.querySelector<HTMLElement>(`[data-testid="${id}"]`)!;
const has = (id: string) => document.querySelector(`[data-testid="${id}"]`) !== null;
const press = (el: HTMLElement) => act(() => el.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const lineIds = () => [...document.querySelectorAll("[data-testid='screen-help-line']")].map((e) => e.getAttribute("data-line-id"));

describe("ScreenHelp", () => {
  it("is a button beside the heading and starts closed", () => {
    mount();
    const b = q("screen-help-events");
    expect(b.tagName).toBe("BUTTON");
    expect(b.textContent).toBe("What is here?");
    expect(b.getAttribute("aria-expanded")).toBe("false");
    expect(has("screen-help-text")).toBe(false);
  });

  it("opens with the lines of the screen and the way back, and closes on a second press", () => {
    mount();
    press(q("screen-help-events"));
    expect(q("screen-help-events").getAttribute("aria-expanded")).toBe("true");
    expect(q("screen-help-events").getAttribute("aria-controls")).toBe(q("screen-help-text").id);
    expect(lineIds()).toEqual(["nav-new-event", "menu-events", "nav-summary"]);
    expect(q("screen-help-back").textContent).toBe("To come back here: Events.");
    press(q("screen-help-events"));
    expect(has("screen-help-text")).toBe(false);
  });

  it("closes on Escape and the focus returns to the button", () => {
    mount();
    press(q("screen-help-events"));
    act(() => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(has("screen-help-text")).toBe(false);
    expect(document.activeElement).toBe(q("screen-help-events"));
  });

  it("closes on a press outside but not inside its text", () => {
    mount();
    press(q("screen-help-events"));
    act(() => q("screen-help-text").dispatchEvent(new MouseEvent("mousedown", { bubbles: true })));
    expect(has("screen-help-text")).toBe(true);
    act(() => q("elsewhere").dispatchEvent(new MouseEvent("mousedown", { bubbles: true })));
    expect(has("screen-help-text")).toBe(false);
  });

  it("does not describe an item the role does not have", () => {
    state.denied = ["import.bil24_session"];
    mount();
    press(q("screen-help-events"));
    expect(lineIds()).toEqual(["menu-events", "nav-summary"]);
  });

  it("speaks the language of the page", () => {
    mount("ru");
    expect(q("screen-help-events").textContent).toBe("Что здесь?");
    press(q("screen-help-events"));
    expect(q("screen-help-back").textContent).toBe("Как сюда вернуться: Мероприятия.");
  });
});
