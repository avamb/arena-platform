import { describe, expect, it } from "vitest";
import { createCheckRunner } from "@/lib/wizard/checkRunner";
import { catSuffix, dateClientErrors, dateFieldKey, mapDateErrors } from "@/lib/wizard/dateErrors";
import { effectiveTickets } from "@/lib/wizard/datesDraft";
import { newDraft, reduceDraft, type DraftAction, type WizardDraft } from "@/lib/wizard/draft";
import type { CheckAnswer, CheckError } from "@/lib/wizard/validation";

const NOW = new Date("2026-10-16T10:00:00Z");
const err = (field: string, code = "x"): CheckError => ({ code, message: "m", field, status: 422 });

function draftOf(...actions: DraftAction[]): WizardDraft {
  return actions.reduce((d, a) => reduceDraft(d, a, NOW), newDraft("draft-1", NOW, "Admission"));
}

const withVenue: DraftAction = { type: "venueExisting", venue: { id: "v", name: "Hall", timezone: "Europe/Madrid", capacity: 100, country: null } };

describe("mapDateErrors", () => {
  it("maps a date's pointers to its fields", () => {
    const d = draftOf(withVenue);
    const date = d.dates[0];
    const out = mapDateErrors(date, effectiveTickets(d.dates, 0), [
      err("/actionEvent/day"),
      err("/actionEvent/time"),
      err("/actionEvent/sellEndTime"),
      err("/actionEvent/doorsOpenTime"),
      err("/categoryList/0/price"),
      err("/categoryList/0/categoryPriceName"),
      err("/categoryList/0/availability"),
      err("/categoryList"),
      err("/externalRef"),
    ]);
    const keys = [...out.byKey.keys()];
    expect(keys).toEqual(["d1.day", "d1.time", "d1.salesEnd", "d1.doors", "d1.c:d1-c1.price", "d1.c:d1-c1.name", "d1.capacity", "d1.tickets"]);
    expect(out.general.map((e) => e.field)).toEqual(["/externalRef"]);
  });

  it("maps a step's window and limit to the step in sequence mode and to the tickets elsewhere", () => {
    const seq = draftOf({ type: "ticketMode", key: "d1", mode: "sequence", catKey: "m" });
    const second = seq.dates[0].tickets.categories[1];
    const out = mapDateErrors(seq.dates[0], seq.dates[0].tickets, [err("/categoryList/1/sellEndTime"), err("/categoryList/0/sellLimit"), err("/categoryList/1/availability")]);
    expect([...out.byKey.keys()]).toEqual([dateFieldKey("d1", catSuffix(second.key, "until")), dateFieldKey("d1", catSuffix(seq.dates[0].tickets.categories[0].key, "limit")), "d1.capacity"]);
    const single = draftOf();
    const out2 = mapDateErrors(single.dates[0], single.dates[0].tickets, [err("/categoryList/0/sellEndTime"), err("/categoryList/0/priceSchedule"), err("/categoryList/7/price")]);
    expect([...out2.byKey.keys()]).toEqual(["d1.tickets"]);
  });

  it("shows category problems of a date that sells the same as before as one problem of its tickets", () => {
    const d = draftOf({ type: "dateAdd", key: "d2", defaultName: "Admission" });
    const out = mapDateErrors(d.dates[1], effectiveTickets(d.dates, 1), [err("/categoryList/0/price"), err("/actionEvent/day")], true);
    expect([...out.byKey.keys()]).toEqual(["d2.tickets", "d2.day"]);
  });
});

describe("dateClientErrors", () => {
  const filled = (...more: DraftAction[]) =>
    draftOf(withVenue, { type: "dateDay", key: "d1", value: "2027-03-05" }, { type: "dateField", key: "d1", field: "time", value: "19:00" }, { type: "catField", key: "d1", catKey: "d1-c1", field: "price", value: "10" }, ...more);

  it("is quiet for a complete date, and asks for a start time (the platform would read none as midnight)", () => {
    expect(dateClientErrors(filled()).size).toBe(0);
    expect(dateClientErrors(draftOf(withVenue)).get("d1.time")).toBe("wizard.err.timeRequired");
  });

  it("flags the shape of times and counts", () => {
    const e = dateClientErrors(
      filled(
        { type: "dateField", key: "d1", field: "time", value: "soon" },
        { type: "dateField", key: "d1", field: "endTime", value: "x" },
        { type: "dateField", key: "d1", field: "salesEnd", value: "soon" },
        { type: "dateField", key: "d1", field: "doors", value: "19:30" },
        { type: "dateField", key: "d1", field: "capacity", value: "lots" },
      ),
    );
    expect([...e.keys()].sort()).toEqual(["d1.capacity", "d1.endTime", "d1.salesEnd", "d1.time"].sort());
    // Doors after a start that cannot be read are not judged: the start is the problem.
    const doors = dateClientErrors(filled({ type: "dateField", key: "d1", field: "doors", value: "19:30" }));
    expect([...doors.keys()]).toEqual(["d1.doors"]);
  });

  it("rejects a start in the venue's skipped DST hour without rejecting nearby real times", () => {
    const onDay = (time: string) => filled(
      { type: "dateDay", key: "d1", value: "2027-03-28" },
      { type: "dateField", key: "d1", field: "time", value: time },
    );
    expect(dateClientErrors(onDay("02:30")).has("d1.time")).toBe(true);
    expect(dateClientErrors(onDay("01:30")).has("d1.time")).toBe(false);
    expect(dateClientErrors(onDay("03:30")).has("d1.time")).toBe(false);
  });

  it("needs the places of the date when the venue has no seat count", () => {
    const d = draftOf({ type: "venueMode", mode: "existing" });
    expect(dateClientErrors(d).get("d1.capacity")).toBe("wizard.err.placesRequired");
    expect(dateClientErrors(reduceDraft(d, { type: "dateField", key: "d1", field: "capacity", value: "40" }, NOW)).has("d1.capacity")).toBe(false);
  });

  it("asks for a price (0 is free), judges its shape, and refuses an empty or repeated name", () => {
    const d = draftOf(withVenue, { type: "catField", key: "d1", catKey: "d1-c1", field: "name", value: "" });
    expect(dateClientErrors(d).get("d1.c:d1-c1.name")).toBe("wizard.err.catName");
    expect(dateClientErrors(d).get("d1.c:d1-c1.price")).toBe("wizard.err.priceRequired");
    const bad = reduceDraft(d, { type: "catField", key: "d1", catKey: "d1-c1", field: "price", value: "ten" }, NOW);
    expect(dateClientErrors(bad).get("d1.c:d1-c1.price")).toBe("wizard.err.priceShape");
    const free = reduceDraft(d, { type: "catField", key: "d1", catKey: "d1-c1", field: "price", value: "0" }, NOW);
    expect(dateClientErrors(free).has("d1.c:d1-c1.price")).toBe(false);

    let two = draftOf(withVenue, { type: "ticketMode", key: "d1", mode: "parallel", catKey: "k" });
    const [a, b] = two.dates[0].tickets.categories;
    for (const [c, n] of [[a, "VIP"], [b, "vip"]] as const) {
      two = reduceDraft(two, { type: "catField", key: "d1", catKey: c.key, field: "name", value: n }, NOW);
      two = reduceDraft(two, { type: "catField", key: "d1", catKey: c.key, field: "price", value: "5" }, NOW);
      two = reduceDraft(two, { type: "catField", key: "d1", catKey: c.key, field: "places", value: "10" }, NOW);
    }
    const errs = dateClientErrors(two);
    expect(errs.get(`d1.c:${a.key}.name`)).toBe("wizard.err.catDup");
    expect(errs.get(`d1.c:${b.key}.name`)).toBe("wizard.err.catDup");
    expect(errs.has(`d1.c:${a.key}.places`)).toBe(false);
  });

  it("asks parallel categories for their places, not the date", () => {
    const d = draftOf(withVenue, { type: "ticketMode", key: "d1", mode: "parallel", catKey: "k" });
    const e = dateClientErrors(d);
    expect(e.has("d1.capacity")).toBe(false);
    expect([...e.keys()].filter((k) => k.endsWith(".places"))).toHaveLength(2);
  });

  it("asks a step that is not the last for a last day or a count, and keeps the days in order", () => {
    let d = draftOf(withVenue, { type: "ticketMode", key: "d1", mode: "sequence", catKey: "k" });
    const [a, b] = d.dates[0].tickets.categories;
    d = reduceDraft(d, { type: "catField", key: "d1", catKey: a.key, field: "name", value: "A" }, NOW);
    d = reduceDraft(d, { type: "catField", key: "d1", catKey: b.key, field: "name", value: "B" }, NOW);
    for (const c of [a, b]) d = reduceDraft(d, { type: "catField", key: "d1", catKey: c.key, field: "price", value: "1" }, NOW);
    expect(dateClientErrors(d).get(`d1.c:${a.key}.until`)).toBe("wizard.err.untilRequired");
    expect(dateClientErrors(d).has(`d1.c:${b.key}.until`)).toBe(false); // the last one has no end
    d = reduceDraft(d, { type: "catField", key: "d1", catKey: a.key, field: "limit", value: "30" }, NOW);
    expect(dateClientErrors(d).has(`d1.c:${a.key}.until`)).toBe(false);
    // Three steps: the second must not end before the first.
    d = reduceDraft(d, { type: "catAdd", key: "d1", catKey: "c3" }, NOW);
    const third = d.dates[0].tickets.categories[2];
    d = reduceDraft(d, { type: "catField", key: "d1", catKey: third.key, field: "name", value: "C" }, NOW);
    d = reduceDraft(d, { type: "catField", key: "d1", catKey: third.key, field: "price", value: "1" }, NOW);
    d = reduceDraft(d, { type: "catField", key: "d1", catKey: a.key, field: "until", value: "2027-02-10" }, NOW);
    d = reduceDraft(d, { type: "catField", key: "d1", catKey: b.key, field: "until", value: "2027-02-01" }, NOW);
    expect(dateClientErrors(d).get(`d1.c:${b.key}.until`)).toBe("wizard.err.untilOrder");
  });

  it("keeps price changes in order and complete", () => {
    let d = draftOf(withVenue, { type: "catField", key: "d1", catKey: "d1-c1", field: "price", value: "10" }, { type: "stepAdd", key: "d1", stepKey: "s1" }, { type: "stepAdd", key: "d1", stepKey: "s2" });
    expect(dateClientErrors(d).get("d1.s:s1.from")).toBe("wizard.err.stepFrom");
    expect(dateClientErrors(d).get("d1.s:s1.price")).toBe("wizard.err.priceRequired");
    d = reduceDraft(d, { type: "stepField", key: "d1", stepKey: "s1", field: "from", value: "2027-02-10" }, NOW);
    d = reduceDraft(d, { type: "stepField", key: "d1", stepKey: "s2", field: "from", value: "2027-02-10" }, NOW);
    expect(dateClientErrors(d).get("d1.s:s2.from")).toBe("wizard.err.stepOrder");
  });

  it("does not judge the categories of a date that sells the same as before", () => {
    const d = draftOf(withVenue, { type: "dateAdd", key: "d2", defaultName: "Admission" });
    expect([...dateClientErrors(d).keys()].some((k) => k.startsWith("d2.c:"))).toBe(false);
  });
});

describe("the check runner", () => {
  const answer = (valid: boolean): CheckAnswer => ({ valid, errors: [], warnings: [] });

  it("asks one answer per date, in order, and asks again only about a bundle it has not seen", async () => {
    const asked: unknown[] = [];
    const run = createCheckRunner(async (b) => {
      asked.push(b);
      return answer(true);
    });
    const a = { externalRef: "1", n: 1 };
    const b = { externalRef: "2", n: 1 };
    const first = await run(JSON.stringify([a, b]));
    expect(first.answers).toHaveLength(2);
    expect(asked).toEqual([a, b]);
    await run(JSON.stringify([a, { ...b, n: 2 }]));
    expect(asked).toEqual([a, b, { ...b, n: 2 }]);
  });

  it("asks the dates one after another, never together", async () => {
    let live = 0;
    let most = 0;
    const run = createCheckRunner(async () => {
      live += 1;
      most = Math.max(most, live);
      await Promise.resolve();
      live -= 1;
      return answer(true);
    });
    await run(JSON.stringify([{ n: 1 }, { n: 2 }, { n: 3 }]));
    expect(most).toBe(1);
  });

  it("fails the whole run when one date cannot be asked", async () => {
    const run = createCheckRunner(async (b) => {
      if ((b as { n: number }).n === 2) throw new Error("down");
      return answer(true);
    });
    await expect(run(JSON.stringify([{ n: 1 }, { n: 2 }]))).rejects.toThrow("down");
  });
});
