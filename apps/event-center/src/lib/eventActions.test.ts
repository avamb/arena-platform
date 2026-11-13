import { describe, expect, it } from "vitest";
import { eventCardLayout, isPastEvent, type ActionRights } from "./eventActions";

const ALL: ActionRights = { edit: true, sample: true, orders: true, csv: true, guests: true, funnel: true, qr: true };
const NONE: ActionRights = { edit: false, sample: false, orders: false, csv: false, guests: false, funnel: false, qr: false };

const ids = (l: ReturnType<typeof eventCardLayout>) => l.groups.map((g) => g.id);

describe("eventCardLayout", () => {
  it("an event on sale: link for buyers first, then sales and orders, then edit", () => {
    const l = eventCardLayout({ status: "published", sales_state: "on_sale" }, ALL);
    expect(ids(l)).toEqual(["link", "sales", "edit"]);
    expect(l.statusFirst).toBe(false);
    expect(l.primary).toBe("qr");
    expect(l.groups.map((g) => g.actions)).toEqual([["qr"], ["orders", "csv", "guests", "funnel"], ["edit", "sample"]]);
  });

  it("a draft puts the status block (publish) first and keeps Edit as the main button", () => {
    const l = eventCardLayout({ status: "draft", sales_state: "draft" }, ALL);
    expect(l.statusFirst).toBe(true);
    expect(ids(l)).toEqual(["link", "sales", "edit"]);
    expect(l.primary).toBe("edit");
  });

  it("a draft with a past sales state is still a draft", () => {
    expect(eventCardLayout({ status: "draft", sales_state: "archived" }, ALL).statusFirst).toBe(true);
  });

  it.each([
    ["archived status", { status: "archived", sales_state: "archived" }],
    ["cancelled status", { status: "cancelled", sales_state: "on_sale" }],
    ["finished event", { status: "published", sales_state: "archived" }],
  ])("a past event (%s): sales and orders first, the status block last", (_name, event) => {
    expect(isPastEvent(event)).toBe(true);
    const l = eventCardLayout(event, ALL);
    expect(ids(l)).toEqual(["sales", "link", "edit"]);
    expect(l.statusFirst).toBe(false);
    expect(l.primary).toBe("orders");
  });

  it("a button the person may not use is absent, and an empty group is not drawn", () => {
    // The agent: no orders, no export, no report, no link, no edit. Only the sample ticket remains.
    const agent = eventCardLayout({ status: "published", sales_state: "on_sale" }, { ...NONE, sample: true });
    expect(agent.groups).toEqual([{ id: "edit", actions: ["sample"] }]);
    expect(eventCardLayout({ status: "published", sales_state: "on_sale" }, NONE).groups).toEqual([]);
    expect(eventCardLayout({ status: "draft" }, NONE).primary).toBeNull();

    // Manager without the sales link: the link group goes, the rest keeps its order.
    const noLink = eventCardLayout({ status: "published", sales_state: "on_sale" }, { ...ALL, qr: false });
    expect(ids(noLink)).toEqual(["sales", "edit"]);
    const noCsv = eventCardLayout({ status: "published", sales_state: "on_sale" }, { ...ALL, csv: false, guests: false });
    expect(noCsv.groups[1]?.actions).toEqual(["orders", "funnel"]);
  });
});
