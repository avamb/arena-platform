// @vitest-environment jsdom
/**
 * A-10: the internal note of an order. It saves when the person leaves the field, shows who wrote it and when, is
 * erased by an empty text, refuses a text that is too long, and is read only (or absent) for a person who cannot
 * manage the shop.
 */
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api/shopApi", () => ({ saveShopOrderNote: vi.fn() }));

import { ShopOrderNote } from "@/components/ShopOrderNote";
import { saveShopOrderNote } from "@/lib/api/shopApi";
import type { ShopOrder } from "@/lib/api/types";
import { I18nProvider } from "@/lib/i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ORG = "11111111-1111-4111-8111-111111111111";
const ID = "22222222-2222-4222-8222-222222222222";
const roots: Root[] = [];
let client: QueryClient;

function order(note: { text: string; by: string; at: string | null } = { text: "", by: "", at: null }): ShopOrder {
  return { id: ID, org_id: ORG, number: 1042, status: "paid", staff_note: note } as unknown as ShopOrder;
}

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  vi.mocked(saveShopOrderNote).mockReset();
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
  const el = document.createElement("div");
  document.body.appendChild(el);
  const root = createRoot(el);
  roots.push(root);
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <I18nProvider locale="en">
          <Card first={o} canManage={canManage} />
        </I18nProvider>
      </QueryClientProvider>,
    );
    await sleep(0);
  });
  return el;
}

const area = (el: HTMLElement) => el.querySelector('[data-testid="shop-order-note-input"]') as HTMLTextAreaElement;

async function typeInto(field: HTMLTextAreaElement, text: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")?.set?.call(field, text);
    field.dispatchEvent(new Event("input", { bubbles: true }));
    await sleep(0);
  });
}

async function leave(field: HTMLElement) {
  await act(async () => {
    field.focus();
    field.blur();
    await sleep(20);
  });
}

describe("ShopOrderNote", () => {
  it("saves when the person leaves the field and then names the author and the time", async () => {
    const saved = order({ text: "Called, deliver after 18:00", by: "Dana Levi", at: "2026-10-29T15:30:00Z" });
    vi.mocked(saveShopOrderNote).mockResolvedValue(saved);
    const el = await mount(order());
    await typeInto(area(el), "  Called, deliver after 18:00  ");
    expect(saveShopOrderNote).not.toHaveBeenCalled();
    await leave(area(el));
    expect(saveShopOrderNote).toHaveBeenCalledWith(ORG, ID, "Called, deliver after 18:00");
    expect(el.querySelector('[data-testid="shop-order-note-state"]')?.textContent).toContain("Note saved.");
    expect(el.querySelector('[data-testid="shop-order-note-by"]')?.textContent).toMatch(/^Dana Levi, /);
    // The card's own query now holds the order with its note.
    expect(client.getQueryData<ShopOrder>(["shopOrder", ORG, ID])?.staff_note.text).toBe("Called, deliver after 18:00");
  });

  it("shows who wrote a saved note when the card opens", async () => {
    const el = await mount(order({ text: "Ring twice", by: "Dana Levi", at: "2026-10-29T15:30:00Z" }));
    expect(area(el).value).toBe("Ring twice");
    expect(el.querySelector('[data-testid="shop-order-note-by"]')?.textContent).toContain("Dana Levi");
  });

  it("sends nothing when the text did not change", async () => {
    const el = await mount(order({ text: "Ring twice", by: "Dana Levi", at: "2026-10-29T15:30:00Z" }));
    await typeInto(area(el), "Ring twice  ");
    await leave(area(el));
    expect(saveShopOrderNote).not.toHaveBeenCalled();
  });

  it("erases the note with an empty text", async () => {
    vi.mocked(saveShopOrderNote).mockResolvedValue(order());
    const el = await mount(order({ text: "Ring twice", by: "Dana Levi", at: "2026-10-29T15:30:00Z" }));
    await typeInto(area(el), "");
    await leave(area(el));
    expect(saveShopOrderNote).toHaveBeenCalledWith(ORG, ID, "");
    expect(el.querySelector('[data-testid="shop-order-note-state"]')?.textContent).toContain("Note removed.");
  });

  it("refuses a text that is too long before asking the server", async () => {
    const el = await mount(order());
    await typeInto(area(el), "x".repeat(2001));
    await leave(area(el));
    expect(saveShopOrderNote).not.toHaveBeenCalled();
    expect(el.querySelector('[data-testid="shop-order-note-input-error"]')?.textContent).toContain("2000");
  });

  it("is plain text for a reader, and absent when there is no note", async () => {
    const read = await mount(order({ text: "Ring twice", by: "Dana Levi", at: "2026-10-29T15:30:00Z" }), false);
    expect(read.querySelector("textarea")).toBeNull();
    expect(read.querySelector('[data-testid="shop-order-note-text"]')?.textContent).toBe("Ring twice");
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const none = await mount(order(), false);
    expect(none.querySelector('[data-testid="shop-order-staff-note"]')).toBeNull();
  });
});
