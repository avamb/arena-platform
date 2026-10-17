/**
 * What the poster said becomes SUGGESTIONS: sanitised, compared with the current
 * form, never applied silently, never inventing a venue, a currency or a capacity.
 */
import { describe, expect, it } from "vitest";
import {
  NO_SELECTION,
  buildActions,
  buildPlan,
  isRowOn,
  parseFacts,
  priceBasis,
  venueBasis,
  venueCandidatesFor,
  type Plan,
  type PosterFacts,
  type Selection,
} from "@/lib/wizard/posterHints";
import { newDraft, reduceDraft, type DraftAction, type VenueChoice, type WizardDraft } from "@/lib/wizard/draft";

const NOW = new Date("2026-10-17T10:00:00Z");
const fresh = (): WizardDraft => newDraft("d", NOW, "Admission");
const edit = (d: WizardDraft, ...actions: DraftAction[]) => actions.reduce((x, a) => reduceDraft(x, a, NOW), d);
const NONE: PosterFacts = { name: "", date: "", time: "", venueName: "", city: "", age: "", description: "", categories: [] };
const facts = (over: Partial<PosterFacts>): PosterFacts => ({ ...NONE, ...over });
const ctx = (draft: WizardDraft, venues: VenueChoice[] | null = []) => ({ draft, venues, defaultCategoryName: "Admission" });
const hall: VenueChoice = { id: "v1", name: "E2E Hall", timezone: "Europe/Madrid", capacity: 500, country: "ES" };
const rowKeys = (p: Plan) => p.rows.map((r) => r.key);

describe("parseFacts: the model's answer is untrusted", () => {
  const ok = {
    facts: {
      name: " Swan Lake ",
      date: "2027-03-05",
      time: "19:30",
      venue_name: "Grand Theatre",
      city: "Madrid",
      age: "12+",
      description: "Ballet",
      categories: [{ name: "Stalls", price_minor: 3490, currency: "EUR" }],
    },
  };
  it("reads a good answer and trims text", () => {
    expect(parseFacts(ok)).toEqual({
      name: "Swan Lake",
      date: "2027-03-05",
      time: "19:30",
      venueName: "Grand Theatre",
      city: "Madrid",
      age: "12+",
      description: "Ballet",
      categories: [{ name: "Stalls", priceMinor: 3490, currency: "EUR" }],
    });
  });
  it("is null for anything that is not the expected shape", () => {
    for (const bad of [null, undefined, "x", 3, {}, { facts: null }, { facts: "x" }]) expect(parseFacts(bad)).toBeNull();
  });
  it("drops values the wizard cannot use instead of guessing", () => {
    const f = parseFacts({
      facts: {
        name: 42,
        date: "2027-02-30",
        time: "25:00",
        venue_name: null,
        city: "x".repeat(500),
        age: "21+",
        description: { html: "<b>" },
        categories: [
          { name: "A", price_minor: -5, currency: "EUR" },
          { name: "B", price_minor: 1.5, currency: "EUR" },
          { name: "C", price_minor: "10", currency: "EUR" },
          { name: "D", price_minor: 1000, currency: "euro" },
          null,
          "x",
        ],
      },
    });
    expect(f).toEqual({
      name: "",
      date: "",
      time: "",
      venueName: "",
      city: "",
      age: "",
      description: "",
      categories: [{ name: "D", priceMinor: 1000, currency: "" }],
    });
  });
  it("never offers NR or an unknown age; a date needs a real calendar day", () => {
    expect(parseFacts({ facts: { age: "NR" } })?.age).toBe("");
    expect(parseFacts({ facts: { date: "2027-13-01" } })?.date).toBe("");
    expect(parseFacts({ facts: { date: "05.03.2027" } })?.date).toBe("");
  });
  it("keeps markup as plain characters (the screen renders text, never HTML)", () => {
    const f = parseFacts({ facts: { name: "<img src=x onerror=alert(1)>" } });
    expect(f?.name).toBe("<img src=x onerror=alert(1)>");
  });
  it("strips control characters and line breaks from one-line values", () => {
    expect(parseFacts({ facts: { name: "A\u0000B\nC" } })?.name).toBe("AB C");
  });
});

describe("buildPlan: fill, replace, skip", () => {
  it("an empty form gets every usable value, all on by default", () => {
    const p = buildPlan(facts({ name: "Swan Lake", description: "Ballet", age: "12+", date: "2027-03-05", time: "19:30" }), ctx(fresh()));
    expect(rowKeys(p)).toEqual(["name", "description", "age", "date", "time"]);
    expect(p.rows.every((r) => !r.conflict && isRowOn(r, undefined))).toBe(true);
  });
  it("typed text is NOT replaced by default: it is a conflict that needs its own tick", () => {
    const d = edit(fresh(), { type: "event", field: "name", value: "My own name" });
    const p = buildPlan(facts({ name: "Swan Lake" }), ctx(d));
    const row = p.rows[0];
    expect(row.conflict).toBe(true);
    expect(row.current).toBe("My own name");
    expect(isRowOn(row, undefined)).toBe(false);
    expect(buildActions(p, NO_SELECTION, d).actions).toEqual([]);
  });
  it("a value already in the form is not suggested again", () => {
    const d = edit(fresh(), { type: "event", field: "name", value: "Swan Lake" }, { type: "dateField", key: "d1", field: "time", value: "1930" });
    const p = buildPlan(facts({ name: "Swan Lake", time: "19:30" }), ctx(d));
    expect(p.rows).toEqual([]);
    expect(p.nothingNew).toBe(true);
  });
  it("a tick made while the field was empty does not replace text typed afterwards", () => {
    const empty = fresh();
    const plan0 = buildPlan(facts({ name: "Swan Lake" }), ctx(empty));
    const ticked: Selection = { ...NO_SELECTION, rows: new Map([["name", { on: true, basis: plan0.rows[0].current }]]) };
    expect(buildActions(plan0, ticked, empty).actions).toHaveLength(1);
    const typedLater = edit(empty, { type: "event", field: "name", value: "Typed after the review" });
    const plan1 = buildPlan(facts({ name: "Swan Lake" }), ctx(typedLater));
    expect(buildActions(plan1, ticked, typedLater).actions).toEqual([]);
  });
  it("an explicit tick on a conflict replaces, and only that field", () => {
    const d = edit(fresh(), { type: "event", field: "name", value: "Old" }, { type: "event", field: "description", value: "Old text" });
    const p = buildPlan(facts({ name: "New", description: "New text" }), ctx(d));
    const sel: Selection = { ...NO_SELECTION, rows: new Map([["name", { on: true, basis: "Old" }]]) };
    expect(buildActions(p, sel, d).actions).toEqual([{ type: "event", field: "name", value: "New" }]);
  });
  it("the poster's one date goes to the FIRST date only; other dates are left alone", () => {
    let d = edit(fresh(), { type: "dateAdd", key: "d2", defaultName: "Admission" }, { type: "dateDay", key: "d2", value: "2027-05-01" });
    d = edit(d, { type: "dateField", key: "d2", field: "time", value: "21:00" });
    const p = buildPlan(facts({ date: "2027-03-05", time: "19:30" }), ctx(d));
    const acts = buildActions(p, NO_SELECTION, d).actions;
    expect(acts).toEqual([
      { type: "dateDay", key: "d1", value: "2027-03-05" },
      { type: "dateField", key: "d1", field: "time", value: "19:30" },
    ]);
    const after = edit(d, ...acts);
    expect(after.dates[1].day).toBe("2027-05-01");
    expect(after.dates[1].time).toBe("21:00");
  });
});

describe("the venue is offered, never invented", () => {
  it("never creates or assigns a venue from a name: candidates are not preselected", () => {
    const d = fresh();
    const p = buildPlan(facts({ venueName: "e2e hall", city: "Madrid" }), ctx(d, [hall, { ...hall, id: "v2", name: "Other place" }]));
    expect(p.venueCandidates.map((c) => c.id)).toEqual(["v1"]);
    // No new-venue hint beside candidates, and nothing is applied without a deliberate choice.
    expect(rowKeys(p)).toEqual([]);
    expect(buildActions(p, NO_SELECTION, d).actions).toEqual([]);
    const sel: Selection = { ...NO_SELECTION, venue: { id: "v1", basis: venueBasis(d) } };
    expect(buildActions(p, sel, d).actions).toEqual([{ type: "venueExisting", venue: hall }]);
  });
  it("a choice made for another venue state no longer counts", () => {
    const d = fresh();
    const p = buildPlan(facts({ venueName: "E2E Hall" }), ctx(d, [hall]));
    const sel: Selection = { ...NO_SELECTION, venue: { id: "v1", basis: venueBasis(d) } };
    const changed = edit(d, { type: "venueMode", mode: "new" });
    expect(buildActions(p, sel, changed).actions).toEqual([]);
  });
  it("no safe match: the name is a hint for a NEW venue; the city needs a country first", () => {
    const d = fresh();
    const p = buildPlan(facts({ venueName: "Grand Theatre", city: "Madrid" }), ctx(d, [hall]));
    expect(p.venueCandidates).toEqual([]);
    expect(rowKeys(p)).toEqual(["venueNew"]);
    expect(p.notes).toContainEqual({ code: "city", value: "Madrid" });
    expect(buildActions(p, NO_SELECTION, d).actions).toEqual([
      { type: "venueMode", mode: "new" },
      { type: "venueNew", field: "newName", value: "Grand Theatre" },
    ]);
  });
  it("the city is a row only when the new venue already has a country", () => {
    const d = edit(fresh(), { type: "venueMode", mode: "new" }, { type: "country", iso2: "ES" });
    const p = buildPlan(facts({ venueName: "Grand Theatre", city: "Madrid" }), ctx(d, [hall]));
    expect(rowKeys(p)).toEqual(["venueNew", "city"]);
  });
  it("an already chosen existing venue is not overwritten by a name hint", () => {
    const d = edit(fresh(), { type: "venueExisting", venue: hall });
    const p = buildPlan(facts({ venueName: "Grand Theatre" }), ctx(d, [hall]));
    expect(rowKeys(p)).toEqual([]);
    expect(p.notes).toContainEqual({ code: "venueName", value: "Grand Theatre" });
  });
  it("the venue that is already chosen needs no candidates", () => {
    const d = edit(fresh(), { type: "venueExisting", venue: hall });
    const p = buildPlan(facts({ venueName: "E2E Hall" }), ctx(d, [hall]));
    expect(p.venueCandidates).toEqual([]);
    expect(p.nothingNew).toBe(true);
  });
  it("a person who cannot list venues gets name hints only", () => {
    const p = buildPlan(facts({ venueName: "Grand Theatre" }), ctx(fresh(), null));
    expect(rowKeys(p)).toEqual(["venueNew"]);
  });
  it("matches ignoring case and accents, and a very short name is never a candidate by containment", () => {
    const v: VenueChoice[] = [
      { ...hall, id: "a", name: "Teatro Zarzuela" },
      { ...hall, id: "b", name: "Sala" },
    ];
    expect(venueCandidatesFor("TEATRO ZÁRZUELA", v).map((x) => x.id)).toEqual(["a"]);
    expect(venueCandidatesFor("Gran Sala Principal", v).map((x) => x.id)).toEqual(["b"]);
    expect(venueCandidatesFor("Sa", v)).toEqual([]);
    expect(venueCandidatesFor("", v)).toEqual([]);
  });
});

describe("prices need a category and a currency, and never overwrite the ticket setup", () => {
  const priced = (c: PosterFacts["categories"]) => facts({ categories: c });
  const stalls = { name: "Stalls", priceMinor: 3490, currency: "EUR" };
  const withEur = () => edit(fresh(), { type: "currency", value: "EUR", source: "typed" });

  it("offers a price only as an explicit choice; nothing is applied by default", () => {
    const d = withEur();
    const p = buildPlan(priced([stalls]), ctx(d));
    expect(p.prices).toHaveLength(1);
    expect(p.prices[0]).toMatchObject({ currency: "EUR", setsCurrency: false, priceText: "34.90", conflict: false });
    expect(buildActions(p, NO_SELECTION, d).actions).toEqual([]);
  });
  it("a chosen price sets the category name and price only: no capacity, no sale time, no mode", () => {
    const d = withEur();
    const p = buildPlan(priced([stalls]), ctx(d));
    const sel: Selection = { ...NO_SELECTION, price: { index: 0, basis: priceBasis(d) } };
    const { actions } = buildActions(p, sel, d);
    expect(actions.map((a) => a.type)).toEqual(["catField", "catField"]);
    const after = edit(d, ...actions);
    const cat = after.dates[0].tickets.categories[0];
    expect(cat.name).toBe("Stalls");
    expect(cat.price).toBe("34.90");
    expect(cat.places).toBe("");
    expect(after.dates[0].tickets.mode).toBe("single");
    expect(after.dates[0].capacity).toBe("");
    expect(after.dates[0].salesEnd).toBe("");
  });
  it("with no currency in the form the poster's currency is offered as a visible part of the choice", () => {
    const d = fresh();
    const p = buildPlan(priced([stalls]), ctx(d));
    expect(p.prices[0]).toMatchObject({ currency: "EUR", setsCurrency: true });
    const sel: Selection = { ...NO_SELECTION, price: { index: 0, basis: priceBasis(d) } };
    const after = edit(d, ...buildActions(p, sel, d).actions);
    expect(after.currency).toBe("EUR");
    expect(after.currencySource).toBe("typed");
  });
  it("a different currency is not applied and not overwritten: it is explained", () => {
    const d = edit(fresh(), { type: "currency", value: "USD", source: "typed" });
    const p = buildPlan(priced([stalls]), ctx(d));
    expect(p.prices).toEqual([]);
    expect(p.notes).toContainEqual({ code: "priceCurrencyDiffers", poster: "EUR", form: "USD" });
  });
  it("a poster price with no currency and a form with none is NOT guessed", () => {
    const p = buildPlan(priced([{ name: "", priceMinor: 1500, currency: "" }]), ctx(fresh()));
    expect(p.prices).toEqual([]);
    expect(p.notes).toContainEqual({ code: "priceNoCurrency" });
  });
  it("a poster price with no currency uses the form's known currency", () => {
    const p = buildPlan(priced([{ name: "", priceMinor: 1500, currency: "" }]), ctx(withEur()));
    expect(p.prices[0]).toMatchObject({ currency: "EUR", setsCurrency: false, priceText: "15.00" });
  });
  it("a customised ticket setup is never touched", () => {
    for (const setup of [
      [{ type: "ticketMode", key: "d1", mode: "parallel", catKey: "d1-c2" }],
      [{ type: "ticketMode", key: "d1", mode: "sequence", catKey: "d1-c3" }],
      [{ type: "stepAdd", key: "d1", stepKey: "s1" }],
    ] as DraftAction[][]) {
      const d = edit(withEur(), ...setup);
      const p = buildPlan(priced([stalls]), ctx(d));
      expect(p.prices).toEqual([]);
      expect(p.notes).toContainEqual({ code: "priceSetup" });
    }
  });
  it("a typed price is a replacement that must be chosen on purpose; an equal price is not offered", () => {
    const d = edit(withEur(), { type: "catField", key: "d1", catKey: "d1-c1", field: "price", value: "20" });
    expect(buildPlan(priced([stalls]), ctx(d)).prices[0].conflict).toBe(true);
    const same = edit(withEur(), { type: "catField", key: "d1", catKey: "d1-c1", field: "price", value: "34,90" }, { type: "catField", key: "d1", catKey: "d1-c1", field: "name", value: "Stalls" });
    expect(buildPlan(priced([stalls]), ctx(same)).prices).toEqual([]);
  });
  it("a price choice made before the price was typed does not replace it", () => {
    const d = withEur();
    const p = buildPlan(priced([stalls]), ctx(d));
    const sel: Selection = { ...NO_SELECTION, price: { index: 0, basis: priceBasis(d) } };
    const typedLater = edit(d, { type: "catField", key: "d1", catKey: "d1-c1", field: "price", value: "10" });
    expect(buildActions(buildPlan(priced([stalls]), ctx(typedLater)), sel, typedLater).actions).toEqual([]);
    expect(p.prices).toHaveLength(1);
  });
  it("several poster categories: one is chosen on purpose and the rest are named as not added", () => {
    const p = buildPlan(priced([stalls, { name: "VIP", priceMinor: 9000, currency: "EUR" }]), ctx(withEur()));
    expect(p.prices.map((x) => x.name)).toEqual(["Stalls", "VIP"]);
    expect(p.notes).toContainEqual({ code: "moreCategories", count: 1 });
  });
});
