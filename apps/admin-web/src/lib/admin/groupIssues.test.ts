import { describe, expect, it } from "vitest";
import { groupIssues } from "./groupIssues";

describe("groupIssues", () => {
  it("returns an empty list for no issues", () => {
    expect(groupIssues([])).toEqual([]);
  });

  it("leaves a single item without a count", () => {
    expect(groupIssues(["a: one"])).toEqual(["a: one"]);
  });

  it("collapses equal texts with a count and keeps the first-appearance order", () => {
    const x = "price_category_unlabelled: a price category has no label";
    const input = [x, "b: two", x, "c: three", x, "b: two"];
    expect(groupIssues(input)).toEqual([`${x} (3 times)`, "b: two (2 times)", "c: three"]);
  });
});
