// @vitest-environment jsdom
/**
 * "Menu" and "Promotion" of the bar (spec 62 §12.1): Menu opens every section
 * as a headed group, then the theme switch, the language and Sign out;
 * Promotion opens its own group alone; Escape and "Close" shut them.
 */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRouter } from "@tanstack/react-router";
import { I18nProvider } from "@/lib/i18n";
import type { OrgAccess } from "@/lib/auth";
import { TabBar } from "@/components/AppNav";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
// The router scrolls to the top on a load, which jsdom does not implement.
window.scrollTo = () => undefined;

const roots: Root[] = [];

afterEach(() => {
  while (roots.length > 0) {
    const r = roots.pop() as Root;
    act(() => r.unmount());
  }
  document.body.innerHTML = "";
  localStorage.clear();
  document.documentElement.removeAttribute("data-theme");
});

const ALL = [
  "import.bil24_session",
  "order.read",
  "refund.read",
  "promo.read",
  "complimentary.read",
  "promoter.read",
  "venue.read",
  "sales_link.read",
  "membership.read",
  "billing.read",
  "org.update",
  "payment_config.read",
];

async function mountBar(perms: string[], orgCount = 2): Promise<HTMLElement> {
  const set = new Set(perms);
  const access: OrgAccess = { data: undefined, isLoading: false, error: null, can: (p) => set.has(p) };
  const root = createRootRoute({
    component: () => <TabBar orgId="org1" access={access} shopMode="full" orgCount={orgCount} onLogout={() => undefined} />,
  });
  const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: ["/"] }) });
  await act(async () => {
    await router.load();
  });
  const el = document.createElement("div");
  document.body.appendChild(el);
  const r = createRoot(el);
  roots.push(r);
  await act(async () => {
    r.render(
      <QueryClientProvider client={new QueryClient()}>
        <I18nProvider locale="ru">
          <RouterProvider router={router} />
        </I18nProvider>
      </QueryClientProvider>,
    );
  });
  return el;
}

function more(el: HTMLElement): HTMLButtonElement {
  return el.querySelector('[data-testid="nav-more"]') as HTMLButtonElement;
}

function groupsOf(el: HTMLElement): [string, string[]][] {
  return [...el.querySelectorAll('[data-testid="more-sheet"] section.more-group')].map((s) => [
    s.querySelector("h3")?.textContent ?? "",
    [...s.querySelectorAll("[data-testid]")].map((a) => a.getAttribute("data-testid") ?? "").filter((id) => !id.startsWith("more-group")),
  ]);
}

describe("Menu", () => {
  afterEach(() => vi.unstubAllEnvs());

  it("lists the sections as headed groups in the order of the bot's menu", async () => {
    vi.stubEnv("VITE_FEATURE_WIZARD", "1");
    const el = await mountBar([...ALL, "shop.read"]);
    expect(el.querySelector('[data-testid="more-sheet"]')).toBeNull();
    act(() => more(el).click());
    expect(more(el).getAttribute("aria-expanded")).toBe("true");
    expect(groupsOf(el)).toEqual([
      ["Новое мероприятие", ["nav-new-event"]],
      ["Мои мероприятия", ["menu-events", "nav-summary"]],
      ["Заказы и возвраты", ["menu-orders", "nav-refunds"]],
      ["Продвижение", ["nav-promo", "nav-invitations", "nav-links", "nav-growth"]],
      ["Вход на мероприятие", ["nav-scanner", "nav-guest-list"]],
      ["Магазин", ["nav-shop"]],
      [
        "Настройки",
        ["nav-org-data", "nav-payments", "nav-team", "nav-promoters", "nav-venues", "nav-notifications", "nav-billing", "nav-switch-org"],
      ],
      ["Помощь", ["nav-help"]],
    ]);
    expect(el.querySelector('[data-testid="theme-switch"]')).not.toBeNull();
    expect(el.querySelector('[data-testid="logout"]')?.textContent).toBe("Выйти");
    // Focus moves into the sheet.
    expect(document.activeElement?.closest('[data-testid="more-sheet"]')).not.toBeNull();
  });

  it("shows a manager without org.update and billing.read only their items", async () => {
    const manager = ALL.filter((p) => p !== "org.update" && p !== "billing.read" && p !== "payment_config.read" && p !== "import.bil24_session");
    const el = await mountBar(manager, 1);
    act(() => more(el).click());
    const groups = groupsOf(el);
    expect(groups.map(([title]) => title)).toEqual(["Мои мероприятия", "Заказы и возвраты", "Продвижение", "Вход на мероприятие", "Настройки", "Помощь"]);
    expect(groups.find(([title]) => title === "Настройки")?.[1]).toEqual(["nav-team", "nav-promoters", "nav-venues", "nav-notifications"]);
  });

  it("leaves out the Orders group for a role without order.read and refund.read", async () => {
    const el = await mountBar(["promo.read"], 1);
    act(() => more(el).click());
    expect(groupsOf(el).map(([title]) => title)).toEqual(["Мои мероприятия", "Продвижение", "Вход на мероприятие", "Настройки", "Помощь"]);
    expect(el.querySelector('[data-testid="nav-refunds"]')).toBeNull();
  });

  it("opens the Promotion sheet with the promotion items alone", async () => {
    const el = await mountBar(ALL);
    act(() => (el.querySelector('[data-testid="nav-promotion"]') as HTMLButtonElement).click());
    expect(el.querySelector('[data-testid="more-sheet"]')).toBeNull();
    const ids = [...el.querySelectorAll('[data-testid="promotion-sheet"] .more-list [data-testid]')].map((a) => a.getAttribute("data-testid"));
    expect(ids).toEqual(["nav-promo", "nav-invitations", "nav-links", "nav-growth"]);
    expect(el.querySelector('[data-testid="logout"]')).toBeNull();
    act(() => (el.querySelector('[data-testid="promotion-close"]') as HTMLButtonElement).click());
    expect(el.querySelector('[data-testid="promotion-sheet"]')).toBeNull();
  });

  it("closes with Escape and with Close", async () => {
    const el = await mountBar(ALL);
    act(() => more(el).click());
    act(() => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    });
    expect(el.querySelector('[data-testid="more-sheet"]')).toBeNull();
    act(() => more(el).click());
    act(() => (el.querySelector('[data-testid="more-close"]') as HTMLButtonElement).click());
    expect(el.querySelector('[data-testid="more-sheet"]')).toBeNull();
  });

  it("switches the theme and keeps the choice", async () => {
    const el = await mountBar(ALL, 1);
    act(() => more(el).click());
    expect(el.querySelector('[data-testid="nav-switch-org"]')).toBeNull();
    act(() => (el.querySelector('[data-testid="theme-dark"]') as HTMLButtonElement).click());
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(el.querySelector('[data-testid="theme-dark"]')?.getAttribute("aria-pressed")).toBe("true");
    act(() => (el.querySelector('[data-testid="theme-system"]') as HTMLButtonElement).click());
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
  });
});
