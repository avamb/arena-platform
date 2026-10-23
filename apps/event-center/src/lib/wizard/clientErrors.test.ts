import { describe, expect, it } from "vitest";
import { clientErrors } from "@/lib/wizard/clientErrors";
import { newDraft, reduceDraft, type DraftAction } from "@/lib/wizard/draft";

const NOW = new Date("2026-10-16T10:00:00Z");
const run = (...a: DraftAction[]) => clientErrors(a.reduce((d, x) => reduceDraft(d, x, NOW), newDraft("d", NOW)));

describe("clientErrors", () => {
  it("has nothing to say about a blank draft (the server reports what is missing)", () => {
    expect(run()).toEqual({});
  });
  it("refuses a name over 200 characters and a description over 4000", () => {
    expect(run({ type: "event", field: "name", value: "x".repeat(201) }).name).toBe("wizard.err.nameLong");
    expect(run({ type: "event", field: "name", value: "x".repeat(200) }).name).toBeUndefined();
    expect(run({ type: "event", field: "description", value: "y".repeat(4001) }).description).toBe("wizard.err.descriptionLong");
  });
  it("counts characters, not UTF-16 units", () => {
    expect(run({ type: "event", field: "name", value: "🎭".repeat(200) }).name).toBeUndefined();
  });
  it("wants a whole number of seats for a NEW venue only", () => {
    expect(run({ type: "venueMode", mode: "new" }).venueCapacity).toBe("wizard.err.capacity");
    expect(run({ type: "venueMode", mode: "new" }, { type: "venueNew", field: "capacity", value: "300" }).venueCapacity).toBeUndefined();
    expect(run({ type: "venueMode", mode: "existing" }).venueCapacity).toBeUndefined();
  });
  it("wants three letters for a typed currency", () => {
    expect(run({ type: "currency", value: "EURO", source: "typed" }).currency).toBe("wizard.err.currencyShape");
    expect(run({ type: "currency", value: "eur", source: "typed" }).currency).toBeUndefined();
  });
});
