import { describe, expect, it } from "vitest";
import { buildBundle, buildBundles } from "@/lib/wizard/bundle";
import { dateClientErrors, dateFieldKey, extSuffix, mapDateErrors } from "@/lib/wizard/dateErrors";
import { isExternalDate, parseDates, sameTicketsAllowed } from "@/lib/wizard/datesDraft";
import { newDraft, parseDraft, reduceDraft, type DraftAction, type WizardDraft } from "@/lib/wizard/draft";
import { splitErrors } from "@/lib/wizard/validation";

const NOW = new Date("2026-10-16T10:00:00Z");
const opts = { orgId: "org-1" };

function draftOf(...actions: DraftAction[]): WizardDraft {
  return actions.reduce((d, a) => reduceDraft(d, a, NOW), newDraft("draft-1", NOW, "Admission"));
}

const base: DraftAction[] = [
  { type: "venueExisting", venue: { id: "v-1", name: "Hall", timezone: "Europe/Berlin", capacity: 300, country: "DE" } },
  { type: "currency", value: "eur", source: "auto" },
  { type: "dateDay", key: "d1", value: "2026-12-01" },
  { type: "dateField", key: "d1", field: "time", value: "19:00" },
];
const partner = (key: string): DraftAction[] => [
  { type: "dateSales", key, sales: "external" },
  { type: "dateExternal", key, field: "url", value: "https://www.eventim.de/event/1" },
  { type: "dateExternal", key, field: "seller", value: "Eventim" },
];

describe("a date sold by a partner box office", () => {
  it("sends externalSale and no categoryList", () => {
    const b = buildBundle(draftOf(...base, ...partner("d1")), opts);
    expect(b.actionEvent.externalSale).toEqual({ url: "https://www.eventim.de/event/1", sellerName: "Eventim" });
    expect("categoryList" in b).toBe(false);
  });

  it("sends the lowest price with a currency", () => {
    const d = draftOf(...base, ...partner("d1"), { type: "dateExternal", key: "d1", field: "price", value: "39,50" });
    expect(buildBundle(d, opts).actionEvent.externalSale).toEqual({
      url: "https://www.eventim.de/event/1",
      sellerName: "Eventim",
      priceFrom: 39.5,
      currency: "EUR",
    });
  });

  it("leaves a platform date as it was: no externalSale key, categories present", () => {
    const b = buildBundle(draftOf(...base), opts);
    expect("externalSale" in b.actionEvent).toBe(false);
    expect(b.categoryList).toHaveLength(1);
  });

  it("keeps the hall plan out of a partner date", () => {
    const d = draftOf(...base, ...partner("d1"));
    d.seating = { ...d.seating, versionId: "ver-1", seats: 10 } as WizardDraft["seating"];
    expect("arenaSeatingPlanVersionId" in buildBundle(d, opts).actionEvent).toBe(false);
  });

  it("judges only the box office of a partner date, not its places or tickets", () => {
    const empty = draftOf({ type: "dateSales", key: "d1", sales: "external" }, { type: "dateField", key: "d1", field: "time", value: "19:00" });
    expect([...dateClientErrors(empty).keys()].sort()).toEqual([dateFieldKey("d1", extSuffix("seller")), dateFieldKey("d1", extSuffix("url"))].sort());
    expect(dateClientErrors(draftOf(...base, ...partner("d1"))).size).toBe(0);
  });

  it("puts the server's box office errors on the fields of the date", () => {
    const d = draftOf(...base, ...partner("d1"));
    const split = splitErrors([
      { code: "import.invalid_external_sale", message: "x", field: "/actionEvent/externalSale/url", status: 422 },
      { code: "import.invalid_external_sale", message: "x", field: "/actionEvent/externalSale/sellerName", status: 422 },
    ]);
    expect(split.dated).toHaveLength(2);
    const mapped = mapDateErrors(d.dates[0], d.dates[0].tickets, split.dated);
    expect([...mapped.byKey.keys()].sort()).toEqual([dateFieldKey("d1", "ext:seller"), dateFieldKey("d1", "ext:url")].sort());
    expect(mapped.general).toHaveLength(0);
  });

  it("a tour: the next date starts as a partner date with the same name, never the same link", () => {
    const d = draftOf(...base, ...partner("d1"), { type: "dateAdd", key: "d2", defaultName: "Admission" });
    expect(d.dates[1].sales).toBe("external");
    expect(d.dates[1].external).toMatchObject({ url: "", seller: "Eventim" });
    expect(d.dates[1].sameTickets).toBe(false);
    expect(buildBundles(d, opts)).toHaveLength(2);
  });

  it("a date after a partner date cannot copy its tickets", () => {
    const d = draftOf(...base, ...partner("d1"), { type: "dateAdd", key: "d2", defaultName: "Admission" }, { type: "dateSales", key: "d2", sales: "platform" });
    expect(isExternalDate(d.dates, 0)).toBe(true);
    expect(sameTicketsAllowed(d.dates, 1)).toBe(false);
    expect(reduceDraft(d, { type: "dateSame", key: "d2", same: true }).dates[1].sameTickets).toBe(false);
  });

  it("a date that copied the first one keeps its tickets when the first goes to a partner", () => {
    const d0 = draftOf(...base, { type: "dateAdd", key: "d2", defaultName: "Admission" });
    expect(d0.dates[1].sameTickets).toBe(true);
    const d = reduceDraft(d0, { type: "dateSales", key: "d1", sales: "external" });
    expect(d.dates[1].sameTickets).toBe(false);
    expect(d.dates[1].tickets.categories).toHaveLength(1);
  });

  it("is stored with the draft and an older draft reads as a platform date", () => {
    const d = draftOf(...base, ...partner("d1"));
    expect(parseDraft(JSON.parse(JSON.stringify(d)))?.dates[0]).toMatchObject({ sales: "external", external: { seller: "Eventim" } });
    const old = JSON.parse(JSON.stringify(d.dates));
    for (const x of old) {
      delete x.sales;
      delete x.external;
    }
    expect(parseDates(old)?.[0]).toMatchObject({ sales: "platform", external: { url: "", seller: "", price: "", currency: "" } });
  });
});
