// @vitest-environment jsdom
/**
 * A date with ONE category and a layout of three slots: the slots without a category become NEW categories, they go
 * into the tray, and "Apply" sends ONE request with `new_tiers` and `new_tier_key`. After the answer the palette
 * shows the created categories. The API module is mocked; nothing reaches a server before Apply.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api/endpoints", () => ({
  fetchSessionSchemaAdmin: vi.fn(),
  fetchSessionSeatsAdmin: vi.fn(),
  listTiers: vi.fn(),
  listSeatBatches: vi.fn(),
  listLayoutAlgorithms: vi.fn(),
  suggestSeatLayout: vi.fn(),
  createSeatBatch: vi.fn(),
  revertSeatBatch: vi.fn(),
}));

import {
  createSeatBatch,
  fetchSessionSchemaAdmin,
  fetchSessionSeatsAdmin,
  listLayoutAlgorithms,
  listSeatBatches,
  listTiers,
  suggestSeatLayout,
} from "@/lib/api/endpoints";
import { SeatManager } from "@/components/seatManager/SeatManager";
import { I18nProvider } from "@/lib/i18n";
import { smallHall } from "@/lib/seatManager/hall.fixture";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const roots: Root[] = [];
const realRect = HTMLElement.prototype.getBoundingClientRect;
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const byId = (id: string) => document.querySelector(`[data-testid="${id}"]`) as HTMLElement | null;
const allById = (id: string) => [...document.querySelectorAll(`[data-testid="${id}"]`)] as HTMLElement[];

async function until(cond: () => boolean, what: string) {
  for (let i = 0; i < 60; i++) {
    if (cond()) return;
    await act(async () => {
      await sleep(50);
    });
  }
  throw new Error(`timeout: ${what}`);
}

async function click(el: HTMLElement | null) {
  if (el === null) throw new Error("no element to click");
  await act(async () => {
    el.click();
  });
}

async function typeInto(el: HTMLElement | null, value: string) {
  if (el === null) throw new Error("no input");
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    setter.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

const tier = (id: string, name: string, price: number, order: number, color: string | null = null) => ({
  id,
  session_id: "s1",
  name,
  pricing_mode: "fixed",
  price_amount: price,
  current_price: price,
  currency: "EUR",
  sort_order: order,
  is_open: true,
  color,
  seat_count: 3,
  ga_unit_count: 0,
});

const seatKeys = ["A|1|1", "A|1|2", "A|1|3", "A|1|4", "A|2|1", "A|2|2", "A|2|3", "A|2|4", "B|1|1", "B|1|2", "B|1|3"];
const proposal = {
  session_id: "s1",
  algorithm: "demo",
  params: {},
  currency: "EUR",
  layout: {
    slots: [
      { key: "c1", price_amount: 5000, share: 0.4 },
      { key: "c2", price_amount: 3000, share: 0.3 },
      { key: "c3", price_amount: 1500, share: 0.3 },
    ],
    assignments: Object.fromEntries(seatKeys.map((k, i) => [k, i < 4 ? "c1" : i < 8 ? "c2" : "c3"])),
    frozen_seat_keys: [],
  },
  metrics: {
    sold_seats: 0,
    held_seats: 0,
    closed_seats: 0,
    revenue_sold_minor: 0,
    revenue_held_minor: 0,
    revenue_remaining_minor: 29500,
    revenue_estimate_minor: 29500,
    realistic_revenue_minor: 24000,
    by_slot: [
      { slot_key: "c1", price_amount: 5000, seats: 4, open_seats: 4, revenue_minor: 20000, share: 0.36 },
      { slot_key: "c2", price_amount: 3000, seats: 4, open_seats: 4, revenue_minor: 12000, share: 0.36 },
      { slot_key: "c3", price_amount: 1500, seats: 3, open_seats: 3, revenue_minor: 4500, share: 0.28 },
    ],
  },
};

let tiers = [tier("t1", "Standard", 3000, 0)];

beforeEach(() => {
  HTMLElement.prototype.getBoundingClientRect = function () {
    return { left: 0, top: 0, right: 800, bottom: 600, width: 800, height: 600, x: 0, y: 0, toJSON: () => ({}) } as DOMRect;
  };
  sessionStorage.clear();
  tiers = [tier("t1", "Standard", 3000, 0)];
  vi.mocked(fetchSessionSchemaAdmin).mockResolvedValue({ geometry: smallHall() } as never);
  vi.mocked(fetchSessionSeatsAdmin).mockResolvedValue({
    seats: seatKeys.map((k) => ({ kind: "seat", seat_key: k, tier_id: "t1", status: "available" })),
  } as never);
  vi.mocked(listTiers).mockImplementation(async () => ({ tiers }) as never);
  vi.mocked(listSeatBatches).mockResolvedValue({ items: [] } as never);
  vi.mocked(listLayoutAlgorithms).mockResolvedValue({
    currency: "EUR",
    algorithms: [{ id: "demo", label_key: "layoutgen.algorithm.demo", required_annotations: [], params: [] }],
  } as never);
  vi.mocked(suggestSeatLayout).mockResolvedValue(proposal as never);
  vi.mocked(createSeatBatch).mockImplementation(async () => {
    tiers = [tier("t1", "Standard", 3000, 0), tier("r1", "Category 1", 5000, 1, "#4f46e5"), tier("r2", "Category 3", 1500, 2, "#059669")];
    return {
      id: "b1",
      session_id: "s1",
      kind: "paint",
      changes: new Array(8).fill({}),
      skipped: [],
      created_tiers: [
        { key: "n1", tier_id: "r1", name: "Category 1", price_amount: 5000, color: "#4f46e5" },
        { key: "n2", tier_id: "r2", name: "Category 3", price_amount: 1500, color: "#059669" },
      ],
    } as never;
  });
});
afterEach(() => {
  HTMLElement.prototype.getBoundingClientRect = realRect;
  for (const r of roots.splice(0)) act(() => r.unmount());
  document.body.innerHTML = "";
  vi.clearAllMocks();
});

async function mount(props: { initialTab?: "layout"; canCreateTiers?: boolean } = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  roots.push(root);
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <I18nProvider locale="en">
          <SeatManager orgId="o1" eventId="e1" sessionId="s1" {...props} />
        </I18nProvider>
      </QueryClientProvider>,
    );
  });
  await until(() => byId("seat-manager") !== null, "the manager");
}

describe("categories from the allocator", () => {
  it("opens on the layout tab when asked, shows the preview table and maps open slots to a new category", async () => {
    await mount({ initialTab: "layout" });
    expect(byId("seat-tab-layout")!.getAttribute("aria-selected")).toBe("true");
    await until(() => byId("layout-proposal") !== null, "the proposal");

    // The preview table: caption, three rows, a total row, and the realistic line.
    const table = byId("layout-table")!;
    expect(table.querySelector("caption")).not.toBeNull();
    expect(allById("layout-table-row")).toHaveLength(3);
    expect(byId("layout-table-total")!.textContent).toContain("11");
    expect(byId("layout-realistic")!.textContent).toContain("Realistic estimate");

    // Standard (3000) takes the 3000 slot; the other two are new categories with the proposal's name and price.
    const selects = allById("layout-map") as unknown as HTMLSelectElement[];
    expect(selects.map((s) => s.value)).toEqual(["@new", "t1", "@new"]);
    const names = allById("layout-new-name") as unknown as HTMLInputElement[];
    expect(names.map((n) => n.value)).toEqual(["Category 1", "Category 3"]);
    expect((allById("layout-new-price") as unknown as HTMLInputElement[]).map((n) => n.value)).toEqual(["50", "15"]);
  });

  it("refuses a name a category has, then transfers, then ONE request creates them and the palette shows them", async () => {
    await mount({ initialTab: "layout" });
    await until(() => byId("layout-proposal") !== null, "the proposal");

    const transfer = byId("layout-transfer") as HTMLButtonElement;
    await typeInto(allById("layout-new-name")[0]!, "standard");
    expect(transfer.disabled).toBe(true);
    expect(document.body.textContent).toContain("A category already has this name");
    await typeInto(allById("layout-new-name")[0]!, "Category 1");
    expect(transfer.disabled).toBe(false);

    await click(transfer);
    expect(createSeatBatch).not.toHaveBeenCalled(); // nothing reaches the server before Apply
    expect(byId("seat-tab-changes")!.getAttribute("aria-selected")).toBe("true");
    expect(allById("seat-pending-tier")).toHaveLength(2);
    expect(byId("seat-pending-tiers")!.textContent).toContain("new, not created yet");
    // The tray is stored with its categories.
    expect(sessionStorage.getItem("arena.seats.tray.s1")).toContain("pendingTiers");

    await click(byId("seat-apply"));
    await until(() => vi.mocked(createSeatBatch).mock.calls.length > 0, "the request");
    expect(createSeatBatch).toHaveBeenCalledTimes(1);
    const body = vi.mocked(createSeatBatch).mock.calls[0]![2] as unknown as {
      kind: string;
      new_tiers: { key: string; name: string; price_amount: number; color: string }[];
      changes: { seat_key: string; tier_id?: string; new_tier_key?: string }[];
    };
    expect(body.kind).toBe("paint");
    expect(body.new_tiers.map((x) => [x.key, x.name, x.price_amount])).toEqual([
      ["n1", "Category 1", 5000],
      ["n2", "Category 3", 1500],
    ]);
    // 4 seats of c1 and 3 of c3 changed (the 4 seats of c2 already are Standard).
    expect(body.changes).toHaveLength(7);
    expect(body.changes.every((c) => c.new_tier_key === "n1" || c.new_tier_key === "n2")).toBe(true);

    // After the answer the pending categories are gone and the palette shows the real ones.
    await until(() => byId("seat-pending-tiers") === null, "the pending list is replaced");
    await click(byId("seat-tab-brush"));
    await until(() => allById("seat-brush").length === 3, "three categories in the palette");
    expect(allById("seat-brush").map((b) => b.getAttribute("data-tier-name"))).toEqual(["Standard", "Category 1", "Category 3"]);
    expect(allById("seat-brush").every((b) => b.getAttribute("data-pending") === null)).toBe(true);
    expect(sessionStorage.getItem("arena.seats.tray.s1")).toBeNull();
  });

  it("on a one-category date the first proposal asks for several categories without touching the form", async () => {
    vi.mocked(listLayoutAlgorithms).mockResolvedValue({
      currency: "EUR",
      algorithms: [
        {
          id: "demo",
          label_key: "layoutgen.algorithm.demo",
          required_annotations: [],
          params: [
            { name: "categories", type: "integer", required: true, min: 1, max: 15, default: 1, unit: "count", label_key: "k" },
            { name: "price_max", type: "integer", required: false, min: 0, max: 100000000, default: null, unit: "minor_currency", label_key: "k" },
            { name: "prices", type: "integer_list", required: false, min: 0, max: 100000000, min_items: 1, max_items: 15, default: null, unit: "minor_currency", label_key: "k" },
          ],
        },
      ],
    } as never);
    await mount({ initialTab: "layout" });
    await until(() => vi.mocked(suggestSeatLayout).mock.calls.length > 0, "the first request");
    const body = vi.mocked(suggestSeatLayout).mock.calls[0]![2] as unknown as { params: Record<string, unknown> };
    expect(body.params.categories).toBe(3);
    expect(body.params.price_max).toBe(3000);
    expect(body.params.prices).toBeUndefined();
  });

  it("without the right to create categories offers only existing ones", async () => {
    await mount({ initialTab: "layout", canCreateTiers: false });
    await until(() => byId("layout-proposal") !== null, "the proposal");
    expect(allById("layout-new-name")).toHaveLength(0);
    expect(byId("layout-no-create")).not.toBeNull();
    expect((allById("layout-map")[0] as unknown as HTMLSelectElement).querySelector('option[value="@new"]')).toBeNull();
  });
});

describe("+ category of the brush panel", () => {
  it("adds a category of the tray, usable as a brush and removable while pending", async () => {
    await mount();
    await click(byId("seat-add-tier"));
    await click(byId("seat-add-tier-submit"));
    expect(document.body.textContent).toContain("Type a name");
    await typeInto(byId("seat-add-tier-name"), "Balcony");
    await typeInto(byId("seat-add-tier-price"), "12,5");
    await click(byId("seat-add-tier-submit"));

    const brushes = allById("seat-brush");
    expect(brushes).toHaveLength(2);
    const pending = brushes.find((b) => b.getAttribute("data-pending") === "1")!;
    expect(pending.getAttribute("data-tier-name")).toBe("Balcony");
    expect(pending.getAttribute("aria-pressed")).toBe("true");
    expect(pending.textContent).toContain("12.50");

    // A category no seat has is not sent: the Changes tab says so.
    await click(byId("seat-tab-changes"));
    expect(byId("seat-pending-unused")).not.toBeNull();

    await click(byId("seat-pending-remove-change"));
    expect(allById("seat-pending-tier")).toHaveLength(0);
    expect(sessionStorage.getItem("arena.seats.tray.s1")).toBeNull();
  });
});
