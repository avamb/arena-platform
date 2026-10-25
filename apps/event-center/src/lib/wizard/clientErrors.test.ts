// @vitest-environment jsdom
// The description is ART (spec 44, RT-11): its length is the VISIBLE text, which reads HTML and needs a DOM.
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
    expect(run({ type: "event", field: "description", value: "<p>" + "y".repeat(4001) + "</p>" }).description).toBe("wizard.err.descriptionLong");
    expect(run({ type: "event", field: "description", value: "<p>" + "y".repeat(4000) + "</p>" }).description).toBeUndefined();
  });
  it("counts the VISIBLE text of the description, not the HTML (a description of 4000 letters in many marks passes)", () => {
    // 10 bold words of 400 letters: 4000 letters and 9 spaces are 4009 characters of text, over the limit...
    const word = (n: number) => "<strong>" + "w".repeat(n) + "</strong>";
    expect(run({ type: "event", field: "description", value: "<p>" + word(399) + " " + word(399) + "</p>" }).description).toBeUndefined();
    expect(run({ type: "event", field: "description", value: "<p>" + [1, 2, 3, 4, 5, 6, 7, 8, 9, 10].map(() => word(400)).join(" ") + "</p>" }).description).toBe(
      "wizard.err.descriptionLong",
    );
    // ...and the tags alone, however many, are not text: 3500 letters in 3500 alternating marks are 3500 characters but far
    // more than the 16000 characters the server stores, so the markup limit speaks, not the visible one.
    const many = "<p>" + Array.from({ length: 1750 }, () => "<strong>a</strong><em>b</em>").join("") + "</p>";
    expect(run({ type: "event", field: "description", value: many }).description).toBe("wizard.err.descriptionMarkup");
  });
  it("refuses a description with more than 20 links", () => {
    const link = (n: number) => '<a href="https://example.com/' + n + '">l' + n + "</a>";
    const links = (k: number) => "<p>" + Array.from({ length: k }, (_, i) => link(i)).join(" ") + "</p>";
    expect(run({ type: "event", field: "description", value: links(20) }).description).toBeUndefined();
    expect(run({ type: "event", field: "description", value: links(21) }).description).toBe("wizard.err.descriptionMarkup");
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
