import { describe, expect, it } from "vitest";
import { buildBundle, buildBundles, wireDay } from "@/lib/wizard/bundle";
import { newDraft, reduceDraft, type DraftAction, type WizardDraft } from "@/lib/wizard/draft";

const NOW = new Date("2026-10-16T10:00:00Z");
const opts = { orgId: "org-1" };

function draftOf(...actions: DraftAction[]): WizardDraft {
  return actions.reduce((d, a) => reduceDraft(d, a, NOW), newDraft("draft-1", NOW));
}

describe("buildBundle", () => {
  it("names the draft in the idempotency key", () => {
    expect(buildBundle(draftOf(), opts).externalRef).toBe("web:org-1:draft-1:1");
    expect(buildBundle(draftOf(), { ...opts, externalRef: "x" }).externalRef).toBe("x");
  });

  it("is an arena-source, unpublished bundle", () => {
    const b = buildBundle(draftOf(), opts);
    expect(b.source).toBe("arena");
    expect(b.publish).toBe(false);
  });

  it("carries the event answers as the bot does, trimmed", () => {
    const b = buildBundle(
      draftOf(
        { type: "event", field: "name", value: "  Swan Lake " },
        { type: "event", field: "description", value: "A ballet\n" },
        { type: "age", value: "12+" },
        { type: "promoter", id: "p-1", name: "Partner" },
      ),
      opts,
    );
    expect(b.action).toEqual({ actionName: "Swan Lake", description: "A ballet", descriptionFormat: "art", age: "12+", promoterId: "p-1" });
  });

  it("sends an empty promoter id when the organization promotes the event", () => {
    expect(buildBundle(draftOf(), opts).action.promoterId).toBe("");
  });

  it("addresses an existing venue by its arena id", () => {
    const b = buildBundle(
      draftOf({ type: "venueExisting", venue: { id: "v-1", name: "Hall", timezone: "Europe/Madrid", capacity: 300, country: "ES" } }),
      opts,
    );
    expect(b.venue).toEqual({ arenaVenueId: "v-1", timezone: "Europe/Madrid" });
    expect(b.categoryList[0].availability).toBe(300);
  });

  it("describes a new venue by name, city, country and zone", () => {
    const b = buildBundle(
      draftOf(
        { type: "venueMode", mode: "new" },
        { type: "venueNew", field: "newName", value: " Palau " },
        { type: "country", iso2: "es" },
        { type: "venueNew", field: "cityName", value: "Barcelona" },
        { type: "timezone", value: "Europe/Madrid" },
        { type: "venueNew", field: "capacity", value: "1 200" },
        { type: "currency", value: "eur", source: "auto" },
      ),
      opts,
    );
    expect(b.venue).toEqual({ venueName: "Palau", timezone: "Europe/Madrid", cityName: "Barcelona", countryName: "ES" });
    expect(b.actionEvent.currency).toBe("EUR");
    expect(b.categoryList[0].availability).toBe(1200);
  });

  it("leaves out what is not known yet", () => {
    const b = buildBundle(draftOf({ type: "venueMode", mode: "new" }), opts);
    expect(b.venue).toEqual({ venueName: "" });
    expect(b.actionEvent.currency).toBe("");
  });

  it("sends the first date with the venue's seats as its places", () => {
    const d = draftOf(
      { type: "venueExisting", venue: { id: "v-1", name: "Hall", timezone: "Europe/Madrid", capacity: 300, country: null } },
      { type: "dateDay", key: "d1", value: "2027-03-05" },
      { type: "dateField", key: "d1", field: "time", value: "1900" },
      { type: "catField", key: "d1", catKey: "d1-c1", field: "name", value: " Admission " },
      { type: "catField", key: "d1", catKey: "d1-c1", field: "price", value: "18,90" },
    );
    const b = buildBundle(d, opts);
    expect(b.actionEvent).toEqual({ day: "05.03.2027", time: "19:00", currency: "", sellEndTime: "2027-03-05T19:00:00+01:00" });
    expect(b.categoryList).toEqual([
      { categoryPriceName: "Admission", price: 18.9, availability: 300, nextCategoryIndex: -1, priceSchedule: [] },
    ]);
  });

  it("makes one bundle per date with its own key, and a second date may sell the same as the first", () => {
    const d = draftOf(
      { type: "dateDay", key: "d1", value: "2027-03-05" },
      { type: "dateField", key: "d1", field: "time", value: "19:00" },
      { type: "dateField", key: "d1", field: "capacity", value: "50" },
      { type: "catField", key: "d1", catKey: "d1-c1", field: "price", value: "10" },
      { type: "dateAdd", key: "d2", defaultName: "Admission" },
      { type: "dateDay", key: "d2", value: "2027-03-06" },
    );
    const [a, b] = buildBundles(d, opts);
    expect(a.externalRef).toBe("web:org-1:draft-1:1");
    expect(b.externalRef).toBe("web:org-1:draft-1:2");
    // The new date starts like the one before: its time, its places and its categories.
    expect(b.actionEvent.time).toBe("19:00");
    expect(b.categoryList).toEqual(a.categoryList);
    expect(b.actionEvent.day).toBe("06.03.2027");
  });

  it("sends an end time only when one is typed, as the bot's does not", () => {
    const base = [{ type: "dateField", key: "d1", field: "time", value: "19:00" }, { type: "dateDay", key: "d1", value: "2027-03-05" }] as const;
    expect(buildBundle(draftOf(...base), opts).actionEvent.endTime).toBeUndefined();
    expect(buildBundle(draftOf(...base, { type: "dateField", key: "d1", field: "endTime", value: "2145" }), opts).actionEvent.endTime).toBe("21:45");
  });

  it("leaves out the sales end and doors when the start is not a real time", () => {
    const d = draftOf({ type: "dateDay", key: "d1", value: "2027-03-05" }, { type: "dateField", key: "d1", field: "time", value: "later" });
    const b = buildBundle(d, opts);
    expect(b.actionEvent.sellEndTime).toBeUndefined();
    expect(b.actionEvent.doorsOpenTime).toBeUndefined();
  });
});

describe("wireDay", () => {
  it("is the bot's DD.MM.YYYY", () => {
    expect(wireDay("2027-01-05")).toBe("05.01.2027");
    expect(wireDay("")).toBe("");
  });
});
