import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { CATALOGS, LOCALES } from "@/lib/i18n";
import { en, type MessageKey } from "@/lib/i18n/en";
import { eventCardLayout, type ActionId, type ActionRights } from "@/lib/eventActions";
import { EVENT_STATUSES, statusActions, type StatusAction } from "@/lib/eventStatus";
import { navGroups } from "@/lib/navMenu";
import { ACTION_HELP, ITEM_HELP, STATUS_HELP, helpLineIds, screenHelp, type NavContext } from "@/lib/screenHelp";

const ACTIONS: ActionId[] = ["edit", "sample", "orders", "csv", "guests", "funnel", "qr"];
const STATUS: StatusAction[] = ["publish", "off", "archive", "delete"];

function person(denied: string[] = []): NavContext {
  return { access: { can: (p) => !denied.includes(p) }, shopMode: "full", orgCount: 2 };
}

const owner = person();
const manager = person(["org.update", "billing.read", "membership.read"]);
const noOrders = person(["order.read"]);
const nobody = person(["order.read", "refund.read"]);

function rights(can: (p: string) => boolean): ActionRights {
  const sales = can("order.read");
  const csv = sales && can("session.read");
  return {
    edit: can("event.update"),
    sample: can("session.read"),
    orders: sales,
    csv,
    guests: csv,
    funnel: can("report.read"),
    qr: can("sales_link.manage"),
  };
}

function card(p: NavContext, status = "published") {
  return { event: { status }, rights: rights(p.access.can), status: { canPublish: p.access.can("event.publish"), canDelete: p.access.can("event.delete") } };
}

describe("screen help: every item has a line in every language", () => {
  beforeEach(() => vi.stubEnv("VITE_FEATURE_WIZARD", "1"));
  afterEach(() => vi.unstubAllEnvs());

  const texts = (key: MessageKey): string[] => LOCALES.map((l) => CATALOGS[l][key]);

  it("every item of navMenu.ts has a description", () => {
    const entries = navGroups(owner.access, "full", 2).flatMap((g) => g.items);
    expect(entries.length).toBeGreaterThan(15);
    for (const e of entries) {
      const key = ITEM_HELP[e.id];
      expect(key, `no help line for the menu item ${e.id}`).toBeDefined();
      for (const l of LOCALES) expect(CATALOGS[l][key as MessageKey]?.trim().length, `${l} ${key}`).toBeGreaterThan(0);
    }
    // No stale line either.
    expect(Object.keys(ITEM_HELP).sort()).toEqual(entries.map((e) => e.id).sort());
  });

  it("every action of eventActions.ts and every status button has a description and a name", () => {
    const seen = new Set<ActionId>();
    const all = Object.fromEntries(ACTIONS.map((a) => [a, true])) as ActionRights;
    for (const status of [...EVENT_STATUSES, "published"]) {
      for (const g of eventCardLayout({ status }, all).groups) g.actions.forEach((a) => seen.add(a));
    }
    expect([...seen].sort()).toEqual([...ACTIONS].sort());
    for (const a of ACTIONS) for (const k of [ACTION_HELP[a].text, ACTION_HELP[a].label]) expect(texts(k).every((x) => x.trim() !== ""), `${a} ${k}`).toBe(true);

    const buttons = new Set(EVENT_STATUSES.flatMap((s) => statusActions(s, { canPublish: true, canDelete: true })));
    expect([...buttons].sort()).toEqual([...STATUS].sort());
    for (const a of STATUS) for (const k of [STATUS_HELP[a].text, STATUS_HELP[a].label]) expect(texts(k).every((x) => x.trim() !== ""), `${a} ${k}`).toBe(true);
  });

  it("the texts keep the house rules: English source, no semicolon, the glossary", () => {
    const keys = (Object.keys(en) as MessageKey[]).filter((k) => k.startsWith("screenHelp."));
    expect(keys.length).toBeGreaterThan(30);
    for (const k of keys) {
      expect(en[k].includes(";"), k).toBe(false);
      expect(/\b(customer|email|letter|tier|hall|photo|promoter)s?\b/i.test(en[k]), k).toBe(false);
    }
  });
});

describe("screen help: lines follow what the person sees", () => {
  beforeEach(() => vi.stubEnv("VITE_FEATURE_WIZARD", "1"));
  afterEach(() => vi.unstubAllEnvs());

  it("events list: the owner and the manager read the same lines, in the order of the menu", () => {
    for (const p of [owner, manager]) {
      const h = screenHelp("events", p);
      expect(h && helpLineIds(h)).toEqual(["nav-new-event", "menu-events", "nav-summary"]);
      expect(h?.back).toEqual(["nav.events"]);
    }
  });

  it("a role that cannot create events does not read about the Add event button", () => {
    const h = screenHelp("events", person(["import.bil24_session"]));
    expect(h && helpLineIds(h)).toEqual(["menu-events", "nav-summary"]);
  });

  it("orders: the list is for order.read, the refunds screen reads only its own line, no rights means no help", () => {
    expect(helpLineIds(screenHelp("orders", owner)!)).toEqual(["menu-orders", "menu-refunds"]);
    expect(screenHelp("orders", noOrders)).toBeNull();
    expect(helpLineIds(screenHelp("refunds", noOrders)!)).toEqual(["menu-refunds"]);
    expect(screenHelp("orders", nobody)).toBeNull();
    expect(screenHelp("refunds", nobody)).toBeNull();
    expect(screenHelp("refunds", noOrders)?.back).toEqual(["nav.refunds"]);
    expect(screenHelp("refunds", owner)?.back).toEqual(["nav.more", "nav.section.orders", "nav.refunds"]);
  });

  it("settings screens: the way back is Menu, Settings, the item", () => {
    expect(screenHelp("team", owner)?.back).toEqual(["nav.more", "nav.section.settings", "nav.team"]);
    expect(screenHelp("team", manager)).toBeNull();
    expect(helpLineIds(screenHelp("orgData", owner)!)).toEqual(["nav-org-data", "nav-payments"]);
    expect(screenHelp("billing", manager)).toBeNull();
    expect(screenHelp("notifications", manager)?.back).toEqual(["nav.more", "nav.section.settings", "nav.notifications"]);
  });

  it("promotion screens list the entry points the person sees, in the order of the sheet", () => {
    const h = screenHelp("links", owner);
    expect(h && helpLineIds(h)).toEqual(["nav-promo", "nav-invitations", "nav-links", "nav-growth"]);
    expect(h?.back).toEqual(["nav.promotion", "nav.links"]);
    const some = screenHelp("links", person(["promo.read", "complimentary.read"]));
    expect(some && helpLineIds(some)).toEqual(["nav-links", "nav-growth"]);
  });

  it("shop home needs a shop, and entrance hides the guest list from a role without order.read", () => {
    expect(screenHelp("shop", { ...owner, shopMode: "none" })).toBeNull();
    expect(helpLineIds(screenHelp("shop", owner)!)).toEqual(["nav-shop"]);
    expect(helpLineIds(screenHelp("entrance", noOrders)!)).toEqual(["nav-scanner"]);
    expect(helpLineIds(screenHelp("entrance", owner)!)).toEqual(["nav-scanner", "nav-guest-list"]);
  });

  it("event card: groups and buttons in the order of the card, the status block last", () => {
    const h = screenHelp("event", owner, card(owner))!;
    expect(h.sections.map((s) => s.id)).toEqual(["link", "sales", "edit", "more"]);
    expect(helpLineIds(h)).toEqual([
      "action-qr",
      "action-orders",
      "action-csv",
      "action-guests",
      "action-funnel",
      "action-edit",
      "action-sample",
      "status-off",
      "status-archive",
      "status-delete",
    ]);
    expect(h.back).toEqual(["nav.events", "screenHelp.step.selectEvent"]);
  });

  it("event card of a draft: the status block comes first, an event that is over shows the sales first", () => {
    const draft = screenHelp("event", owner, card(owner, "draft"))!;
    expect(draft.sections[0].id).toBe("more");
    expect(draft.sections[0].lines.map((l) => l.id)).toEqual(["status-publish", "status-delete"]);
    const done = screenHelp("event", owner, card(owner, "archived"))!;
    expect(done.sections.map((s) => s.id)).toEqual(["sales", "link", "edit", "more"]);
  });

  it("event card: a role without order.read has no orders, CSV and guests lines", () => {
    const h = screenHelp("event", noOrders, card(noOrders))!;
    const ids = helpLineIds(h);
    expect(ids).not.toContain("action-orders");
    expect(ids).not.toContain("action-csv");
    expect(ids).not.toContain("action-guests");
    expect(ids).toContain("action-funnel");
  });

  it("event card: lines equal the buttons of the layout and the status block, one for one", () => {
    for (const p of [owner, manager, noOrders]) {
      for (const status of EVENT_STATUSES) {
        const c = card(p, status);
        const layout = eventCardLayout(c.event, c.rights);
        const expected = [
          ...layout.groups.flatMap((g) => g.actions.map((a) => `action-${a}`)),
          ...statusActions(status, c.status).map((a) => `status-${a}`),
        ].sort();
        const h = screenHelp("event", p, c);
        expect(h ? helpLineIds(h).sort() : []).toEqual(expected);
      }
    }
  });
});
