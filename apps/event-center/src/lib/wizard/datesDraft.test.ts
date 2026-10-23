import { describe, expect, it } from "vitest";
import { MAX_CATEGORIES, MAX_DATES, effectiveTickets, isBlankDate, parseDates } from "@/lib/wizard/datesDraft";
import { isBlank, loadDraft, newDraft, reduceDraft, saveDraft, type DraftAction, type WizardDraft } from "@/lib/wizard/draft";

const NOW = new Date("2026-10-16T10:00:00Z");

function draftOf(...actions: DraftAction[]): WizardDraft {
  return actions.reduce((d, a) => reduceDraft(d, a, NOW), newDraft("draft-1", NOW, "Admission"));
}

describe("dates", () => {
  it("starts with one empty date and an unpriced first category", () => {
    const d = draftOf();
    expect(d.dates).toHaveLength(1);
    expect(d.dates[0].tickets.categories[0].name).toBe("Admission");
    expect(isBlankDate(d.dates[0])).toBe(true);
    expect(isBlank(d)).toBe(true);
  });

  it("a typed date makes the draft worth keeping", () => {
    expect(isBlank(draftOf({ type: "dateField", key: "d1", field: "time", value: "19:00" }))).toBe(false);
    expect(isBlank(draftOf({ type: "catField", key: "d1", catKey: "d1-c1", field: "price", value: "10" }))).toBe(false);
    expect(isBlank(draftOf({ type: "dateAdd", key: "d2", defaultName: "Admission" }))).toBe(false);
  });

  it("a new date starts like the one before, selling the same tickets", () => {
    const d = draftOf(
      { type: "dateField", key: "d1", field: "time", value: "19:00" },
      { type: "dateField", key: "d1", field: "capacity", value: "80" },
      { type: "catField", key: "d1", catKey: "d1-c1", field: "price", value: "12" },
      { type: "dateAdd", key: "d2", defaultName: "Admission" },
    );
    const second = d.dates[1];
    expect(second).toMatchObject({ key: "d2", day: "", time: "19:00", capacity: "80", sameTickets: true });
    expect(effectiveTickets(d.dates, 1).categories[0].price).toBe("12");
  });

  it("'own tickets' starts from what the date before sells, and edits only that date", () => {
    let d = draftOf(
      { type: "catField", key: "d1", catKey: "d1-c1", field: "price", value: "12" },
      { type: "dateAdd", key: "d2", defaultName: "Admission" },
      { type: "dateSame", key: "d2", same: false },
      { type: "catField", key: "d2", catKey: "d1-c1", field: "price", value: "15" },
    );
    expect(d.dates[1].sameTickets).toBe(false);
    expect(d.dates[0].tickets.categories[0].price).toBe("12");
    expect(d.dates[1].tickets.categories[0].price).toBe("15");
    d = reduceDraft(d, { type: "dateSame", key: "d2", same: true }, NOW);
    expect(effectiveTickets(d.dates, 1).categories[0].price).toBe("12");
  });

  it("the first date can never 'sell the same as before'", () => {
    const d = draftOf({ type: "dateSame", key: "d1", same: true });
    expect(d.dates[0].sameTickets).toBe(false);
  });

  it("follows 'same as before' back through a chain of dates", () => {
    const d = draftOf(
      { type: "catField", key: "d1", catKey: "d1-c1", field: "price", value: "9" },
      { type: "dateAdd", key: "d2", defaultName: "Admission" },
      { type: "dateAdd", key: "d3", defaultName: "Admission" },
    );
    expect(effectiveTickets(d.dates, 2).categories[0].price).toBe("9");
  });

  it("keeps at least one date and at most MAX_DATES", () => {
    let d = draftOf({ type: "dateRemove", key: "d1" });
    expect(d.dates).toHaveLength(1);
    for (let i = 0; i < MAX_DATES + 5; i += 1) d = reduceDraft(d, { type: "dateAdd", key: `n${i}`, defaultName: "A" }, NOW);
    expect(d.dates).toHaveLength(MAX_DATES);
    d = reduceDraft(d, { type: "dateRemove", key: "n0" }, NOW);
    expect(d.dates).toHaveLength(MAX_DATES - 1);
  });
});

describe("ticket modes", () => {
  it("several categories need two to start with, one category keeps the first", () => {
    let d = draftOf({ type: "catField", key: "d1", catKey: "d1-c1", field: "name", value: "Adults" }, { type: "ticketMode", key: "d1", mode: "parallel", catKey: "x" });
    expect(d.dates[0].tickets.categories.map((c) => c.name)).toEqual(["Adults", ""]);
    d = reduceDraft(d, { type: "ticketMode", key: "d1", mode: "single", catKey: "y" }, NOW);
    expect(d.dates[0].tickets.categories.map((c) => c.name)).toEqual(["Adults"]);
  });

  it("price changes belong to one category only", () => {
    let d = draftOf({ type: "stepAdd", key: "d1", stepKey: "s1" }, { type: "stepField", key: "d1", stepKey: "s1", field: "price", value: "20" });
    expect(d.dates[0].tickets.schedule).toEqual([{ key: "s1", from: "", price: "20" }]);
    d = reduceDraft(d, { type: "ticketMode", key: "d1", mode: "sequence", catKey: "z" }, NOW);
    expect(d.dates[0].tickets.schedule).toEqual([]);
    expect(reduceDraft(d, { type: "stepAdd", key: "d1", stepKey: "s2" }, NOW).dates[0].tickets.schedule).toEqual([]);
  });

  it("categories can be added up to the cap and removed down to two", () => {
    let d = draftOf({ type: "ticketMode", key: "d1", mode: "sequence", catKey: "m" });
    for (let i = 0; i < MAX_CATEGORIES + 3; i += 1) d = reduceDraft(d, { type: "catAdd", key: "d1", catKey: `c${i}` }, NOW);
    expect(d.dates[0].tickets.categories).toHaveLength(MAX_CATEGORIES);
    for (let i = 0; i < MAX_CATEGORIES; i += 1) d = reduceDraft(d, { type: "catRemove", key: "d1", catKey: d.dates[0].tickets.categories[0].key }, NOW);
    expect(d.dates[0].tickets.categories).toHaveLength(2);
  });
});

describe("a stored draft", () => {
  function memory(): Pick<Storage, "getItem" | "setItem" | "removeItem"> {
    const m = new Map<string, string>();
    return { getItem: (k) => m.get(k) ?? null, setItem: (k, v) => void m.set(k, v), removeItem: (k) => void m.delete(k) };
  }

  it("comes back with its dates and tickets", () => {
    const store = memory();
    const d = draftOf(
      { type: "dateDay", key: "d1", value: "2027-03-05" },
      { type: "ticketMode", key: "d1", mode: "sequence", catKey: "q" },
      { type: "catField", key: "d1", catKey: "d1-c1", field: "until", value: "2027-02-01" },
      { type: "dateAdd", key: "d2", defaultName: "Admission" },
    );
    saveDraft("org-1", d, store);
    expect(loadDraft("org-1", store)).toEqual(d);
  });

  it("a draft of an older version, or with broken dates, is dropped whole", () => {
    const store = memory();
    const d = draftOf({ type: "event", field: "name", value: "X" });
    saveDraft("org-1", d, store);
    const raw = JSON.parse(store.getItem("arena.events.wizard.org-1")!) as Record<string, unknown>;
    store.setItem("arena.events.wizard.org-1", JSON.stringify({ ...raw, version: 1 }));
    expect(loadDraft("org-1", store)).toBeNull();
    store.setItem("arena.events.wizard.org-1", JSON.stringify({ ...raw, dates: [{ key: "d1" }] }));
    expect(loadDraft("org-1", store)).toBeNull();
    store.setItem("arena.events.wizard.org-1", JSON.stringify({ ...raw, dates: [] }));
    expect(loadDraft("org-1", store)).toBeNull();
  });

  it("refuses a first date that sells 'the same as before'", () => {
    const d = draftOf();
    expect(parseDates([{ ...d.dates[0], sameTickets: true }])).toBeNull();
  });
});
