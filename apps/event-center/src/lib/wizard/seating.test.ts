import { describe, expect, it } from "vitest";
import type { SeatingPlan, SeatingPlanVersion } from "@/lib/api/types";
import { buildBundle, datePlaces, isSeatedDraft } from "@/lib/wizard/bundle";
import { dateClientErrors, mapDateErrors } from "@/lib/wizard/dateErrors";
import { effectiveTickets } from "@/lib/wizard/datesDraft";
import { newDraft, parseDraft, reduceDraft, type DraftAction, type WizardDraft } from "@/lib/wizard/draft";
import {
  MAX_SEAT_CATEGORIES,
  SEAT_COLORS,
  parseSeating,
  planChoiceOf,
  seatCatKey,
  seatExtraKey,
  seatingClientErrors,
  type PlanChoice,
} from "@/lib/wizard/seating";

const ORG = "00000000-0000-0000-0000-0000000000aa";
const NOW = new Date("2026-10-31T10:00:00Z");

function plan(over: Partial<SeatingPlan> = {}): SeatingPlan {
  return {
    id: "p1",
    venue_id: "v1",
    owner_org_id: ORG,
    name: "Main hall",
    plan_type: "mixed",
    visibility: "private",
    status: "active",
    current_version_id: "ver2",
    category_name_overrides: { "2": "Galerie" },
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    ...over,
  } as SeatingPlan;
}

function version(id: string): SeatingPlanVersion {
  return {
    id,
    seating_plan_id: "p1",
    version_number: id === "ver2" ? 2 : 1,
    geometry: {
      categories: [
        { index: 1, name: "Parter" },
        { index: 2, name: "Balcony" },
        { index: 3, name: "Standing", kind: "general_admission", capacity: 50 },
        { index: 4, name: "Legend" },
      ],
      sections: [
        {
          rows: [
            { seats: [{ category_index: 1 }, { category_index: 1 }] },
            { seats: [{ category_index: 2 }] },
          ],
        },
      ],
    },
  } as unknown as SeatingPlanVersion;
}

const church: PlanChoice = { versionId: "ver-church", name: "Church", seats: 100, categories: [{ index: 1, name: "Hall", places: 100, ga: false }] };

function seatedDraft(): WizardDraft {
  let d = newDraft("dr1", NOW, "Admission");
  const run = (a: DraftAction) => {
    d = reduceDraft(d, a, NOW);
  };
  run({ type: "venueExisting", venue: { id: "v1", name: "Church", timezone: "Europe/Madrid", capacity: 120, country: "ES" } });
  run({ type: "currency", value: "EUR", source: "typed" });
  run({ type: "dateDay", key: "d1", value: "2026-12-20" });
  run({ type: "dateField", key: "d1", field: "time", value: "19:00" });
  run({ type: "seatingPlan", plan: church });
  run({ type: "seatingPrice", index: 1, value: "30" });
  run({ type: "seatingExtraAdd", key: "x1" });
  run({ type: "seatingExtra", key: "x1", field: "name", value: " VIP " });
  run({ type: "seatingExtra", key: "x1", field: "price", value: "90" });
  return d;
}

describe("planChoiceOf", () => {
  it("reads the current version: seats per category, the zone's capacity, the display names, no empty category", () => {
    const c = planChoiceOf(plan(), [version("ver1"), version("ver2")], ORG);
    expect(c).toEqual({
      versionId: "ver2",
      name: "Main hall",
      seats: 53,
      categories: [
        { index: 1, name: "Parter", places: 2, ga: false },
        { index: 2, name: "Galerie", places: 1, ga: false },
        { index: 3, name: "Standing", places: 50, ga: true },
      ],
    });
  });

  it("offers only the organization's own active plans with a current version", () => {
    expect(planChoiceOf(plan({ owner_org_id: "other" }), [version("ver2")], ORG)).toBeNull();
    expect(planChoiceOf(plan({ status: "archived" } as Partial<SeatingPlan>), [version("ver2")], ORG)).toBeNull();
    expect(planChoiceOf(plan({ current_version_id: null } as Partial<SeatingPlan>), [version("ver2")], ORG)).toBeNull();
    expect(planChoiceOf(plan(), [version("ver1")], ORG)).toBeNull();
  });
});

describe("the seated draft", () => {
  it("builds a bundle with the plan version, the plan's categories and the extra ones", () => {
    const d = seatedDraft();
    expect(isSeatedDraft(d)).toBe(true);
    expect(datePlaces(d, d.dates[0])).toBe(100);
    const b = buildBundle(d, { orgId: ORG });
    expect(b.actionEvent.arenaSeatingPlanVersionId).toBe("ver-church");
    expect(b.venue.arenaVenueId).toBe("v1");
    expect(b.categoryList).toEqual([
      { categoryPriceName: "Hall", price: 30, availability: 0, nextCategoryIndex: -1, priceSchedule: [], placement: true, planCategoryIndex: 1 },
      {
        categoryPriceName: "VIP",
        price: 90,
        availability: 0,
        nextCategoryIndex: -1,
        priceSchedule: [],
        placement: true,
        planExtra: true,
        color: SEAT_COLORS[0],
      },
    ]);
  });

  it("a standing zone keeps the plan's capacity", () => {
    let d = seatedDraft();
    const zone = planChoiceOf(plan(), [version("ver2")], ORG);
    d = reduceDraft(d, { type: "seatingPlan", plan: zone }, NOW);
    const b = buildBundle(d, { orgId: ORG });
    expect(b.categoryList[2]).toMatchObject({ categoryPriceName: "Standing", availability: 50, placement: false, planCategoryIndex: 3 });
    // The extra category stays when another plan is chosen; the plan prices start empty.
    expect(b.categoryList[3]).toMatchObject({ categoryPriceName: "VIP", planExtra: true });
    expect(d.seating.categories.every((c) => c.price === "")).toBe(true);
  });

  it("free seating and another venue drop the plan", () => {
    const d = seatedDraft();
    expect(reduceDraft(d, { type: "seatingPlan", plan: null }, NOW).seating.versionId).toBe("");
    const other = reduceDraft(d, { type: "venueExisting", venue: { id: "v2", name: "Other", timezone: "Europe/Madrid", capacity: 10, country: "ES" } }, NOW);
    expect(other.seating.versionId).toBe("");
    expect(reduceDraft(d, { type: "venueMode", mode: "new" }, NOW).seating.versionId).toBe("");
    const same = reduceDraft(d, { type: "venueExisting", venue: { id: "v1", name: "Church", timezone: "Europe/Madrid", capacity: 120, country: "ES" } }, NOW);
    expect(same.seating.versionId).toBe("ver-church");
    // A free-seating bundle is the old one.
    const free = buildBundle(reduceDraft(d, { type: "seatingPlan", plan: null }, NOW), { orgId: ORG });
    expect(free.actionEvent.arenaSeatingPlanVersionId).toBeUndefined();
    expect(free.categoryList[0].planCategoryIndex).toBeUndefined();
  });

  it("checks the plan's prices and the extra categories, and nothing of the ticket editor", () => {
    let d = seatedDraft();
    expect(seatingClientErrors(d.seating).size).toBe(0);
    expect(dateClientErrors(d).size).toBe(0);
    d = reduceDraft(d, { type: "seatingPrice", index: 1, value: "abc" }, NOW);
    d = reduceDraft(d, { type: "seatingExtraAdd", key: "x2" }, NOW);
    d = reduceDraft(d, { type: "seatingExtra", key: "x2", field: "name", value: "hall" }, NOW);
    const errs = seatingClientErrors(d.seating);
    expect(errs.get(seatCatKey(1))).toBe("wizard.err.priceShape");
    expect(errs.get(seatExtraKey("x2", "name"))).toBe("wizard.err.catDup");
    expect(errs.get(seatExtraKey("x2", "price"))).toBe("wizard.err.priceRequired");
    // Every extra gets its own colour while there are free ones.
    expect(d.seating.extras[1].color).toBe(SEAT_COLORS[1]);
  });

  it("stops at fifteen categories", () => {
    let d = seatedDraft();
    for (let i = 0; i < 20; i += 1) d = reduceDraft(d, { type: "seatingExtraAdd", key: `k${i}` }, NOW);
    expect(d.seating.categories.length + d.seating.extras.length).toBe(MAX_SEAT_CATEGORIES);
  });

  it("puts the platform's category errors of a seated date under the date, not into hidden ticket fields", () => {
    const d = seatedDraft();
    const err = { code: "import.seating_category_duplicate", field: "/categoryList/1/categoryPriceName", message: "x" };
    const out = mapDateErrors(d.dates[0], effectiveTickets(d.dates, 0), [err], false, true);
    expect(out.general).toEqual([err]);
    expect(out.byKey.size).toBe(0);
  });

  it("survives a reload, and an older draft without a plan reads as free seating", () => {
    const d = seatedDraft();
    const back = parseDraft(JSON.parse(JSON.stringify(d)));
    expect(back?.seating).toEqual(d.seating);
    const old = JSON.parse(JSON.stringify(d)) as Record<string, unknown>;
    delete old.seating;
    expect(parseDraft(old)?.seating.versionId).toBe("");
    expect(parseSeating({ versionId: "x", planName: "y", seats: "many", categories: [], extras: [] }).versionId).toBe("");
  });
});
