import { describe, expect, it } from "vitest";
import { buildBundle, stubDay, STUB_TIME } from "@/lib/wizard/bundle";
import { newDraft, reduceDraft, type DraftAction, type WizardDraft } from "@/lib/wizard/draft";

const NOW = new Date("2026-10-16T10:00:00Z");
const opts = { now: NOW, orgId: "org-1" };

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
    expect(b.action).toEqual({ actionName: "Swan Lake", description: "A ballet", age: "12+", promoterId: "p-1" });
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

  it("stands in for the missing date and category with a marked stub a year ahead", () => {
    const b = buildBundle(draftOf(), opts);
    expect(b.actionEvent.day).toBe("16.10.2027");
    expect(b.actionEvent.time).toBe(STUB_TIME);
    expect(b.categoryList).toEqual([{ categoryPriceName: "Category 1", price: 1, availability: 1 }]);
  });
});

describe("stubDay", () => {
  it("is the bot's DD.MM.YYYY, one year after today", () => {
    expect(stubDay(new Date("2026-01-05T00:00:00Z"))).toBe("05.01.2027");
    expect(stubDay(new Date("2028-12-31T23:59:00Z"))).toBe("31.12.2029");
  });
});
