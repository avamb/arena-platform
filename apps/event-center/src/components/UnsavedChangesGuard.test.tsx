// @vitest-environment jsdom
/**
 * A-02: the guard of the product card. While the form is dirty, closing the tab asks the browser to confirm and a
 * move to another screen asks first ("There are unsaved changes. Leave?"). A clean form is never stopped, and a
 * move the form makes itself (after a create or a delete) is not asked about.
 */
import { act, type MutableRefObject } from "react";
import { createRoot, type Root } from "react-dom/client";
import { RouterProvider, createBrowserHistory, createMemoryHistory, createRootRoute, createRoute, createRouter } from "@tanstack/react-router";
import { afterEach, describe, expect, it } from "vitest";
import { UnsavedChangesGuard } from "@/components/UnsavedChangesGuard";
import { I18nProvider } from "@/lib/i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const roots: Root[] = [];
// jsdom has no scrolling, and the router restores the scroll position after every move.
window.scrollTo = () => undefined;
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** `browser`: the real history, the only one that listens for the tab being closed. */
async function mount(dirty: boolean, allowLeave?: MutableRefObject<boolean>, browser = false, extra: { onOpenChange?: (open: boolean) => void; onSaveAndLeave?: () => Promise<boolean> } = {}) {
  const rootRoute = createRootRoute();
  const card = createRoute({ getParentRoute: () => rootRoute, path: "/", component: () => <UnsavedChangesGuard dirty={dirty} allowLeave={allowLeave} {...extra} /> });
  const other = createRoute({ getParentRoute: () => rootRoute, path: "/other", component: () => <p data-testid="other">other</p> });
  const router = createRouter({ routeTree: rootRoute.addChildren([card, other]), history: browser ? createBrowserHistory() : createMemoryHistory({ initialEntries: ["/"] }) });
  const el = document.createElement("div");
  document.body.appendChild(el);
  const root = createRoot(el);
  roots.push(root);
  await act(async () => {
    root.render(
      <I18nProvider locale="en">
        <RouterProvider router={router} />
      </I18nProvider>,
    );
    await sleep(20);
  });
  return { router, el };
}

/** Starts a move and gives the router time to answer. A blocked move stays pending until the person decides, so it is not awaited. */
async function go(router: Awaited<ReturnType<typeof mount>>["router"], to: string) {
  await act(async () => {
    void router.navigate({ to });
    await sleep(50);
  });
}

describe("UnsavedChangesGuard", () => {
  it("lets a clean form leave without a question", async () => {
    const { router, el } = await mount(false);
    await go(router, "/other");
    expect(router.state.location.pathname).toBe("/other");
    expect(el.querySelector('[data-testid="unsaved-dialog"]')).toBeNull();
  });

  it("asks before a dirty form leaves, and Stay keeps the person where they are", async () => {
    const { router, el } = await mount(true);
    await go(router, "/other");
    expect(router.state.location.pathname).toBe("/");
    const dialog = el.querySelector('[data-testid="unsaved-dialog"]');
    expect(dialog?.textContent).toContain("There are unsaved changes. Leave?");
    await act(async () => {
      (el.querySelector('[data-testid="unsaved-stay"]') as HTMLElement).click();
      await sleep(20);
    });
    expect(el.querySelector('[data-testid="unsaved-dialog"]')).toBeNull();
    expect(router.state.location.pathname).toBe("/");
  });

  it("leaves when the person says Leave", async () => {
    const { router, el } = await mount(true);
    await go(router, "/other");
    await act(async () => {
      (el.querySelector('[data-testid="unsaved-leave"]') as HTMLElement).click();
      await sleep(20);
    });
    expect(router.state.location.pathname).toBe("/other");
  });

  it("does not ask about a move the form makes itself", async () => {
    const allow = { current: true };
    const { router, el } = await mount(true, allow);
    await go(router, "/other");
    expect(router.state.location.pathname).toBe("/other");
    expect(el.querySelector('[data-testid="unsaved-dialog"]')).toBeNull();
  });

  it("asks the browser to confirm closing the tab only while the form is dirty", async () => {
    await mount(true, undefined, true);
    const dirty = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(dirty);
    expect(dirty.defaultPrevented).toBe(true);
    for (const r of roots.splice(0)) act(() => r.unmount());
    document.body.innerHTML = "";
    await mount(false, undefined, true);
    const clean = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(clean);
    expect(clean.defaultPrevented).toBe(false);
  });

  it("has two actions without a way to save, three with one (M-09)", async () => {
    const plain = await mount(true);
    await go(plain.router, "/other");
    expect(plain.el.querySelectorAll('[data-testid="unsaved-dialog"] button')).toHaveLength(2);
    expect(plain.el.querySelector('[data-testid="unsaved-save-leave"]')).toBeNull();
    for (const r of roots.splice(0)) act(() => r.unmount());
    document.body.innerHTML = "";

    const withSave = await mount(true, undefined, false, { onSaveAndLeave: () => Promise.resolve(true) });
    await go(withSave.router, "/other");
    const buttons = Array.from(withSave.el.querySelectorAll('[data-testid="unsaved-dialog"] button')).map((b) => b.getAttribute("data-testid"));
    expect(buttons).toEqual(["unsaved-stay", "unsaved-save-leave", "unsaved-leave"]);
    expect(withSave.el.querySelector('[data-testid="unsaved-save-leave"]')?.textContent).toBe("Save and leave");
  });

  it("tells the screen while the question is open, so it can hide the Save bar", async () => {
    const seen: boolean[] = [];
    const { router, el } = await mount(true, undefined, false, { onOpenChange: (o) => seen.push(o) });
    expect(seen.at(-1)).toBe(false);
    await go(router, "/other");
    expect(seen.at(-1)).toBe(true);
    await act(async () => {
      (el.querySelector('[data-testid="unsaved-stay"]') as HTMLElement).click();
      await sleep(20);
    });
    expect(seen.at(-1)).toBe(false);
  });

  it("saves, then leaves when the save went through", async () => {
    const allow = { current: false };
    let saved = 0;
    const { router, el } = await mount(true, allow, false, {
      onSaveAndLeave: () => {
        saved += 1;
        return Promise.resolve(true);
      },
    });
    await go(router, "/other");
    await act(async () => {
      (el.querySelector('[data-testid="unsaved-save-leave"]') as HTMLElement).click();
      await sleep(30);
    });
    expect(saved).toBe(1);
    expect(router.state.location.pathname).toBe("/other");
  });

  it("stays on the card when the save did not go through", async () => {
    const { router, el } = await mount(true, undefined, false, { onSaveAndLeave: () => Promise.resolve(false) });
    await go(router, "/other");
    await act(async () => {
      (el.querySelector('[data-testid="unsaved-save-leave"]') as HTMLElement).click();
      await sleep(30);
    });
    expect(router.state.location.pathname).toBe("/");
    expect(el.querySelector('[data-testid="unsaved-dialog"]')).toBeNull();
  });
});
