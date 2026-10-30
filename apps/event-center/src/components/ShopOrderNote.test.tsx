// @vitest-environment jsdom
/**
 * A-10 and SA-26: the internal note of an order. It is saved by the button or by Ctrl+Enter (not by leaving the
 * field), shows who wrote it and when, is erased by an empty text, refuses a text that is too long, asks before the
 * page is left with an unsaved text, stops with the other person's text when a colleague saved a note first, and is
 * read only (or absent) for a person who cannot manage the shop.
 */
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRoute, createRouter } from "@tanstack/react-router";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api/shopApi", () => ({ saveShopOrderNote: vi.fn(), fetchShopOrder: vi.fn() }));

import { ShopOrderNote } from "@/components/ShopOrderNote";
import { fetchShopOrder, saveShopOrderNote } from "@/lib/api/shopApi";
import type { ShopOrder } from "@/lib/api/types";
import { I18nProvider } from "@/lib/i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
window.scrollTo = () => undefined;

const ORG = "11111111-1111-4111-8111-111111111111";
const ID = "22222222-2222-4222-8222-222222222222";
const roots: Root[] = [];
let client: QueryClient;
let router: ReturnType<typeof createRouter>;

function order(note: { text: string; by: string; at: string | null } = { text: "", by: "", at: null }): ShopOrder {
  return { id: ID, org_id: ORG, number: 1042, status: "paid", staff_note: note } as unknown as ShopOrder;
}

const DANA = { text: "Ring twice", by: "Dana Levi", at: "2026-10-29T15:30:00Z" };

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  vi.mocked(saveShopOrderNote).mockReset();
  vi.mocked(fetchShopOrder).mockReset();
  // By default nobody else touched the note: the order read before the save is the one on the screen.
  vi.mocked(fetchShopOrder).mockImplementation(async () => client.getQueryData<ShopOrder>(["shopOrder", ORG, ID]) as ShopOrder);
});
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** The card: the order comes from its query, so a saved note reaches the field as it does in the app. */
function Card({ first, canManage }: { first: ShopOrder; canManage: boolean }) {
  const q = useQuery({ queryKey: ["shopOrder", ORG, ID], queryFn: () => Promise.resolve(first), initialData: first, staleTime: Infinity });
  return <ShopOrderNote orgId={ORG} order={q.data} canManage={canManage} country="ES" />;
}

async function mount(o: ShopOrder, canManage = true): Promise<HTMLElement> {
  const rootRoute = createRootRoute();
  const card = createRoute({ getParentRoute: () => rootRoute, path: "/", component: () => <Card first={o} canManage={canManage} /> });
  const other = createRoute({ getParentRoute: () => rootRoute, path: "/other", component: () => <p>other</p> });
  router = createRouter({ routeTree: rootRoute.addChildren([card, other]), history: createMemoryHistory({ initialEntries: ["/"] }) });
  const el = document.createElement("div");
  document.body.appendChild(el);
  const root = createRoot(el);
  roots.push(root);
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <I18nProvider locale="en">
          <RouterProvider router={router} />
        </I18nProvider>
      </QueryClientProvider>,
    );
    await sleep(50);
  });
  return el;
}

const area = (el: HTMLElement) => el.querySelector('[data-testid="shop-order-note-input"]') as HTMLTextAreaElement;
const saveButton = (el: HTMLElement) => el.querySelector('[data-testid="shop-order-note-save"]') as HTMLButtonElement;
const byId = (el: HTMLElement, id: string) => el.querySelector('[data-testid="' + id + '"]') as HTMLElement;

async function typeInto(field: HTMLTextAreaElement, text: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")?.set?.call(field, text);
    field.dispatchEvent(new Event("input", { bubbles: true }));
    await sleep(0);
  });
}

async function press(button: HTMLElement) {
  await act(async () => {
    button.click();
    await sleep(30);
  });
}

describe("ShopOrderNote", () => {
  it("does not save by leaving the field, saves by the button, and then names the author and the time", async () => {
    const saved = order({ text: "Called, deliver after 18:00", by: "Dana Levi", at: "2026-10-29T15:30:00Z" });
    vi.mocked(saveShopOrderNote).mockResolvedValue(saved);
    const el = await mount(order());
    expect(saveButton(el).disabled).toBe(true);
    await typeInto(area(el), "  Called, deliver after 18:00  ");
    await act(async () => {
      area(el).focus();
      area(el).blur();
      await sleep(20);
    });
    expect(saveNotCalled()).toBe(true);
    expect(saveButton(el).disabled).toBe(false);
    await press(saveButton(el));
    expect(saveShopOrderNote).toHaveBeenCalledWith(ORG, ID, "Called, deliver after 18:00");
    expect(byId(el, "shop-order-note-state").textContent).toContain("Note saved.");
    expect(byId(el, "shop-order-note-by").textContent).toMatch(/^Dana Levi, /);
    // The card's own query now holds the order with its note.
    expect(client.getQueryData<ShopOrder>(["shopOrder", ORG, ID])?.staff_note.text).toBe("Called, deliver after 18:00");
  });

  it("saves with Ctrl+Enter", async () => {
    vi.mocked(saveShopOrderNote).mockResolvedValue(order({ text: "Back door", by: "Dana Levi", at: "2026-10-29T15:30:00Z" }));
    const el = await mount(order());
    await typeInto(area(el), "Back door");
    await act(async () => {
      area(el).dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true }));
      await sleep(30);
    });
    expect(saveShopOrderNote).toHaveBeenCalledWith(ORG, ID, "Back door");
    // A plain Enter is a new line, not a save.
    vi.mocked(saveShopOrderNote).mockClear();
    await typeInto(area(el), "Back door\nline two");
    await act(async () => {
      area(el).dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
      await sleep(30);
    });
    expect(saveShopOrderNote).not.toHaveBeenCalled();
  });

  it("shows who wrote a saved note when the card opens", async () => {
    const el = await mount(order(DANA));
    expect(area(el).value).toBe("Ring twice");
    expect(byId(el, "shop-order-note-by").textContent).toContain("Dana Levi");
  });

  it("sends nothing when the text did not change", async () => {
    const el = await mount(order(DANA));
    await typeInto(area(el), "Ring twice  ");
    expect(saveButton(el).disabled).toBe(true);
    await press(saveButton(el));
    expect(saveShopOrderNote).not.toHaveBeenCalled();
  });

  it("erases the note with an empty text", async () => {
    vi.mocked(saveShopOrderNote).mockResolvedValue(order());
    const el = await mount(order(DANA));
    await typeInto(area(el), "");
    await press(saveButton(el));
    expect(saveShopOrderNote).toHaveBeenCalledWith(ORG, ID, "");
    expect(byId(el, "shop-order-note-state").textContent).toContain("Note removed.");
  });

  it("refuses a text that is too long before asking the server", async () => {
    const el = await mount(order());
    await typeInto(area(el), "x".repeat(2001));
    expect(saveButton(el).disabled).toBe(true);
    expect(byId(el, "shop-order-note-input-error").textContent).toContain("2000");
    expect(saveShopOrderNote).not.toHaveBeenCalled();
  });

  it("asks before the page is left with a text that is not saved, and not otherwise", async () => {
    const clean = await mount(order(DANA));
    await act(async () => {
      void router.navigate({ to: "/other" });
      await sleep(50);
    });
    expect(router.state.location.pathname).toBe("/other");
    for (const r of roots.splice(0)) act(() => r.unmount());
    document.body.innerHTML = "";
    expect(clean).toBeTruthy();

    const el = await mount(order(DANA));
    await typeInto(area(el), "Ring three times");
    await act(async () => {
      void router.navigate({ to: "/other" });
      await sleep(50);
    });
    expect(router.state.location.pathname).toBe("/");
    expect(byId(el, "unsaved-dialog").textContent).toContain("There are unsaved changes. Leave?");
  });

  it("stops with the colleague's text when a note was saved after the one the person started from (SA-26)", async () => {
    const theirs = order({ text: "Leave at the door", by: "Noa Cohen", at: "2026-10-29T16:00:00Z" });
    vi.mocked(fetchShopOrder).mockResolvedValue(theirs);
    vi.mocked(saveShopOrderNote).mockResolvedValue(order({ text: "Ring three times", by: "Dana Levi", at: "2026-10-29T16:05:00Z" }));
    const el = await mount(order(DANA));
    await typeInto(area(el), "Ring three times");
    await press(saveButton(el));
    expect(saveShopOrderNote).not.toHaveBeenCalled();
    const warn = byId(el, "shop-order-note-changed");
    expect(warn.textContent).toContain("Noa Cohen changed this note");
    expect(warn.textContent).toContain("Leave at the door");
    // The typed text is still there, nothing was lost.
    expect(area(el).value).toBe("Ring three times");
    await press(byId(el, "shop-order-note-overwrite"));
    expect(saveShopOrderNote).toHaveBeenCalledWith(ORG, ID, "Ring three times");
    expect(byId(el, "shop-order-note-changed")).toBeNull();
  });

  it("takes the colleague's note when the person chooses to keep it", async () => {
    vi.mocked(fetchShopOrder).mockResolvedValue(order({ text: "Leave at the door", by: "Noa Cohen", at: "2026-10-29T16:00:00Z" }));
    const el = await mount(order(DANA));
    await typeInto(area(el), "Ring three times");
    await press(saveButton(el));
    await press(byId(el, "shop-order-note-use-theirs"));
    expect(saveShopOrderNote).not.toHaveBeenCalled();
    expect(area(el).value).toBe("Leave at the door");
    expect(byId(el, "shop-order-note-changed")).toBeNull();
  });

  it("is plain text for a reader, and absent when there is no note", async () => {
    const read = await mount(order(DANA), false);
    expect(read.querySelector("textarea")).toBeNull();
    expect(read.querySelector('[data-testid="shop-order-note-text"]')?.textContent).toBe("Ring twice");
    for (const r of roots.splice(0)) act(() => r.unmount());
    document.body.innerHTML = "";
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const none = await mount(order(), false);
    expect(none.querySelector('[data-testid="shop-order-staff-note"]')).toBeNull();
  });
});

function saveNotCalled(): boolean {
  return vi.mocked(saveShopOrderNote).mock.calls.length === 0;
}
