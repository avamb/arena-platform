import { describe, expect, it } from "vitest";
import { eventCardLayout, isPastEvent, sectionForAnchor, sectionOf, type ItemId, type ItemRights } from "./eventActions";

const IDS: ItemId[] = [
  "edit", "sample", "orders", "csv", "guests", "funnel", "qr", "promo", "invitations", "links",
  "totals", "pageStats", "poster", "sessions", "salesSwitch", "materials", "emailNote", "referral", "status",
];
const ALL = Object.fromEntries(IDS.map((i) => [i, true])) as ItemRights;
const NONE = Object.fromEntries(IDS.map((i) => [i, false])) as ItemRights;

const ids = (l: ReturnType<typeof eventCardLayout>) => l.sections.map((s) => s.id);
const open = (l: ReturnType<typeof eventCardLayout>) => l.sections.filter((s) => s.defaultOpen).map((s) => s.id);

describe("eventCardLayout", () => {
  it("an event on sale: five sections, link first and the only one open", () => {
    const l = eventCardLayout({ status: "published", sales_state: "on_sale" }, ALL);
    expect(ids(l)).toEqual(["link", "sales", "edit", "promo", "more"]);
    expect(open(l)).toEqual(["link"]);
    expect(l.primary).toBe("qr");
    expect(l.sections.map((s) => s.items)).toEqual([
      ["qr"],
      ["orders", "csv", "guests", "funnel", "totals", "pageStats"],
      ["edit", "poster", "sessions", "salesSwitch", "materials", "sample", "emailNote"],
      ["promo", "invitations", "links", "referral"],
      ["status"],
    ]);
  });

  it("every item lives in exactly one section", () => {
    const l = eventCardLayout({ status: "published" }, ALL);
    expect(l.sections.flatMap((s) => s.items).sort()).toEqual([...IDS].sort());
    for (const s of l.sections) for (const i of s.items) expect(sectionOf(i)).toBe(s.id);
  });

  it("a draft puts More first and open (it holds Publish), the rest in the standard order, and keeps Edit as the main button", () => {
    const l = eventCardLayout({ status: "draft", sales_state: "draft" }, ALL);
    expect(ids(l)).toEqual(["more", "link", "sales", "edit", "promo"]);
    expect(open(l)).toEqual(["more"]);
    expect(l.primary).toBe("edit");
  });

  it("a draft with a past sales state is still a draft", () => {
    expect(ids(eventCardLayout({ status: "draft", sales_state: "archived" }, ALL))[0]).toBe("more");
  });

  it("orderBy keeps the order of the state before a status move; the status item stays while its flag is set", () => {
    const before = { status: "published", sales_state: "on_sale" };
    const after = { status: "archived", sales_state: "archived" };
    expect(ids(eventCardLayout(after, ALL, before))).toEqual(ids(eventCardLayout(before, ALL)));
    expect(ids(eventCardLayout(after, ALL))[0]).toBe("sales");
    // Nothing left to press, but the flag keeps the section with the status block.
    const l = eventCardLayout(after, { ...ALL, status: true }, before);
    expect(l.sections.find((s) => s.id === "more")?.items).toEqual(["status"]);
  });

  it("a draft whose person may not use the status block opens the next section", () => {
    const l = eventCardLayout({ status: "draft" }, { ...ALL, status: false });
    expect(ids(l)).toEqual(["link", "sales", "edit", "promo"]);
    expect(open(l)).toEqual(["link"]);
  });

  it.each([
    ["archived status", { status: "archived", sales_state: "archived" }],
    ["cancelled status", { status: "cancelled", sales_state: "on_sale" }],
    ["finished event", { status: "published", sales_state: "archived" }],
  ])("a past event (%s): sales and orders first and open, More last", (_name, event) => {
    expect(isPastEvent(event)).toBe(true);
    const l = eventCardLayout(event, ALL);
    expect(ids(l)).toEqual(["sales", "link", "edit", "promo", "more"]);
    expect(open(l)).toEqual(["sales"]);
    expect(l.primary).toBe("orders");
  });

  it("an item the person may not use is absent, and an empty section is not drawn", () => {
    // The agent: no orders, no export, no report, no link, no edit. The poster, the materials and the sample remain.
    const agent = eventCardLayout(
      { status: "published", sales_state: "on_sale" },
      { ...NONE, sample: true, poster: true, materials: true, sessions: true, salesSwitch: true, pageStats: true },
    );
    expect(agent.sections).toEqual([
      { id: "sales", items: ["pageStats"], defaultOpen: true },
      { id: "edit", items: ["poster", "sessions", "salesSwitch", "materials", "sample"], defaultOpen: false },
    ]);
    expect(agent.primary).toBe("sample");
    expect(eventCardLayout({ status: "published", sales_state: "on_sale" }, NONE).sections).toEqual([]);
    expect(eventCardLayout({ status: "draft" }, NONE).primary).toBeNull();

    // Manager without the sales link: the link section goes, the first section left opens.
    const noLink = eventCardLayout({ status: "published", sales_state: "on_sale" }, { ...ALL, qr: false });
    expect(ids(noLink)).toEqual(["sales", "edit", "promo", "more"]);
    expect(open(noLink)).toEqual(["sales"]);
    const noCsv = eventCardLayout({ status: "published", sales_state: "on_sale" }, { ...ALL, csv: false, guests: false });
    expect(noCsv.sections[1]?.items.slice(0, 2)).toEqual(["orders", "funnel"]);
    // Promotion with only Refer a friend still draws.
    const referralOnly = eventCardLayout({ status: "published" }, { ...NONE, referral: true });
    expect(referralOnly.sections).toEqual([{ id: "promo", items: ["referral"], defaultOpen: true }]);
  });
});

describe("sectionForAnchor", () => {
  it("finds the section of a section id, an item id and the old anchors", () => {
    expect(sectionForAnchor("#event-section-promo")).toBe("promo");
    expect(sectionForAnchor("#event-item-poster")).toBe("edit");
    expect(sectionForAnchor("#event-totals")).toBe("sales");
    expect(sectionForAnchor("#event-materials")).toBe("edit");
    expect(sectionForAnchor("#event-status-heading")).toBe("more");
    expect(sectionForAnchor("#email-note-title")).toBe("edit");
  });

  it("an unknown hash points nowhere", () => {
    expect(sectionForAnchor("")).toBeNull();
    expect(sectionForAnchor("#nothing")).toBeNull();
    expect(sectionForAnchor("#event-section-nothing")).toBeNull();
    expect(sectionForAnchor("#event-item-nothing")).toBeNull();
  });
});
