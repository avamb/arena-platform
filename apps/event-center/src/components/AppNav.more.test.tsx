// @vitest-environment jsdom
/**
 * "More" of the bottom bar (spec 62 §12.1): one press opens every other
 * section, Promo codes first, then the theme switch, the language and Sign
 * out; Escape and "Close" shut it.
 */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it } from "vitest";
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
  "order.read",
  "promo.read",
  "complimentary.read",
  "promoter.read",
  "sales_link.read",
  "membership.read",
  "billing.read",
  "org.update",
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

describe("More", () => {
  it("lists the sections in the agreed order, Promo codes first", async () => {
    const el = await mountBar([...ALL, "shop.read"]);
    expect(el.querySelector('[data-testid="more-sheet"]')).toBeNull();
    act(() => more(el).click());
    expect(more(el).getAttribute("aria-expanded")).toBe("true");
    const ids = [...el.querySelectorAll(".more-list [data-testid]")].map((a) => a.getAttribute("data-testid"));
    expect(ids).toEqual([
      "nav-promo",
      "nav-invitations",
      "nav-promoters",
      "nav-links",
      "nav-growth",
      "nav-notifications",
      "nav-team",
      "nav-shop",
      "nav-billing",
      "nav-org-data",
      "nav-switch-org",
    ]);
    expect(el.querySelector('[data-testid="theme-switch"]')).not.toBeNull();
    expect(el.querySelector('[data-testid="logout"]')?.textContent).toBe("Выйти");
    // Focus moves into the sheet.
    expect(document.activeElement?.closest('[data-testid="more-sheet"]')).not.toBeNull();
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
