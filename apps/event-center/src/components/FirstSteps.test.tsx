// @vitest-environment jsdom
/**
 * The "First steps" card of the events list: four open steps, partly done,
 * complete (no card), a failed read (no card) and the two-step manager.
 */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRouter } from "@tanstack/react-router";
import { I18nProvider } from "@/lib/i18n";
import type { GettingStarted } from "@/lib/api/types";
import { FirstSteps, firstStepsShown } from "@/components/FirstSteps";

// A plain function, not vi.fn: a spy that returns a rejected promise leaves an unhandled rejection of its own.
const read: { answer: () => Promise<GettingStarted> } = { answer: () => Promise.reject(new Error("no answer set")) };
const fetchGettingStarted = {
  resolves: (v: GettingStarted) => void (read.answer = () => Promise.resolve(v)),
  fails: () => void (read.answer = async () => {
    throw new Error("boom");
  }),
};
vi.mock("@/lib/api/endpoints", () => ({ fetchGettingStarted: () => read.answer() }));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
window.scrollTo = () => undefined;

const roots: Root[] = [];
type Key = GettingStarted["steps"][number]["key"];

const state = (done: Partial<Record<Key, boolean>>, keys: Key[] = ["org_data", "payments", "event", "sales_link"]): GettingStarted => {
  const steps = keys.map((key) => ({ key, done: done[key] === true }));
  return { complete: steps.every((s) => s.done), steps };
};

async function mountCard(): Promise<HTMLElement> {
  const root = createRootRoute({ component: () => <FirstSteps orgId="org1" /> });
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
        <I18nProvider locale="en">
          <RouterProvider router={router} />
        </I18nProvider>
      </QueryClientProvider>,
    );
  });
  // The read answers on a later turn of the event loop: let it land.
  for (let i = 0; i < 3; i++) {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  }
  return el;
}

const card = (el: HTMLElement) => el.querySelector('[data-testid="first-steps"]');
const linkOf = (el: HTMLElement, key: Key) => el.querySelector(`a[data-testid="first-step-${key}"]`) as HTMLAnchorElement | null;


afterEach(() => {
  while (roots.length > 0) {
    const r = roots.pop() as Root;
    act(() => r.unmount());
  }
  document.body.innerHTML = "";
});

describe("firstStepsShown", () => {
  it("needs a read that worked, steps, and not all done", () => {
    expect(firstStepsShown(undefined)).toBe(false);
    expect(firstStepsShown({ complete: true, steps: [] })).toBe(false);
    expect(firstStepsShown({ complete: false, steps: [] })).toBe(false);
    expect(firstStepsShown(state({ org_data: true, payments: true, event: true, sales_link: true }))).toBe(false);
    expect(firstStepsShown(state({}))).toBe(true);
  });
});

describe("FirstSteps", () => {
  it("lists four open steps as links to their screens", async () => {
    fetchGettingStarted.resolves(state({}));
    const el = await mountCard();
    expect(card(el)?.querySelector("h2")?.textContent).toBe("First steps");
    expect(el.querySelector('[data-testid="first-steps-progress"]')?.textContent).toBe("0 of 4 done");
    expect(linkOf(el, "org_data")?.textContent).toBe("Add the organization data");
    expect(linkOf(el, "org_data")?.getAttribute("href")).toBe("/o/org1/org-data");
    expect(linkOf(el, "payments")?.textContent).toBe("Connect payments");
    expect(linkOf(el, "payments")?.getAttribute("href")).toBe("/o/org1/org-data#payments");
    expect(linkOf(el, "event")?.textContent).toBe("Add the first event");
    expect(linkOf(el, "event")?.getAttribute("href")).toBe("/o/org1/events/new");
    expect(linkOf(el, "sales_link")?.textContent).toBe("Get a sales link");
    expect(linkOf(el, "sales_link")?.getAttribute("href")).toBe("/o/org1/links/new");
  });

  it("shows a done step as a check mark and plain text", async () => {
    fetchGettingStarted.resolves(state({ org_data: true, event: true }));
    const el = await mountCard();
    expect(el.querySelector('[data-testid="first-steps-progress"]')?.textContent).toBe("2 of 4 done");
    const row = el.querySelector('[data-testid="first-step-row-org_data"]') as HTMLElement;
    expect(row.getAttribute("data-done")).toBe("true");
    expect(row.textContent).toContain("✓");
    expect(row.textContent).toContain("Add the organization data");
    expect(row.querySelector("a")).toBeNull();
    expect(linkOf(el, "org_data")).toBeNull();
    expect(linkOf(el, "event")).toBeNull();
    expect(linkOf(el, "payments")).not.toBeNull();
    expect(linkOf(el, "sales_link")).not.toBeNull();
  });

  it("is gone when every step is done", async () => {
    fetchGettingStarted.resolves(state({ org_data: true, payments: true, event: true, sales_link: true }));
    const el = await mountCard();
    expect(card(el)).toBeNull();
  });

  it("shows nothing when the read fails", async () => {
    fetchGettingStarted.fails();
    const el = await mountCard();
    expect(card(el)).toBeNull();
    expect(el.textContent).toBe("");
  });

  it("shows only the steps the server lists, for a manager with two", async () => {
    fetchGettingStarted.resolves(state({ event: true }, ["event", "sales_link"]));
    const el = await mountCard();
    expect(el.querySelector('[data-testid="first-steps-progress"]')?.textContent).toBe("1 of 2 done");
    expect(el.querySelectorAll("li")).toHaveLength(2);
    expect(linkOf(el, "org_data")).toBeNull();
    expect(linkOf(el, "payments")).toBeNull();
    expect(linkOf(el, "sales_link")).not.toBeNull();
  });
});
