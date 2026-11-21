import { describe, expect, it } from "vitest";
import { NO_PLAN, approvedKey, noPlanView, reduceNoPlan, templateToOffer } from "@/lib/venuePlan/wizardFlow";

describe("the no-plan section of the wizard", () => {
  it("asks first: upload or continue", () => {
    expect(noPlanView(NO_PLAN, false)).toBe("ask");
  });

  it("offers a verified template BEFORE any upload", () => {
    expect(noPlanView(NO_PLAN, true)).toBe("template");
  });

  it("shows only a loading hint while the templates are read, never the upload choice", () => {
    expect(noPlanView(NO_PLAN, false, true)).toBe("wait");
    // The read ends with a template: the offer comes first.
    expect(noPlanView(NO_PLAN, true, false)).toBe("template");
    // The read ends with no template, or failed (the same for the view): the upload choice.
    expect(noPlanView(NO_PLAN, false, false)).toBe("ask");
  });

  it("declining the template opens the upload form", () => {
    const s = reduceNoPlan(NO_PLAN, "declineTemplate");
    expect(noPlanView(s, true)).toBe("upload");
    // Closing the form does not bring the offer back.
    expect(noPlanView(reduceNoPlan(s, "closeUpload"), true)).toBe("ask");
  });

  it("'Upload a seating plan' opens the form and counts as declining the template", () => {
    const s = reduceNoPlan(NO_PLAN, "openUpload");
    expect(s.uploadOpen).toBe(true);
    expect(s.declinedTemplate).toBe(true);
  });

  it("'Continue without a plan' folds the section, and 'Upload' can open it again", () => {
    const s = reduceNoPlan(NO_PLAN, "continue");
    expect(noPlanView(s, false)).toBe("continued");
    expect(noPlanView(reduceNoPlan(s, "openUpload"), false)).toBe("upload");
  });

  it("offers the first template that is not the organization's own", () => {
    expect(templateToOffer([{ own: true, id: "a" }, { own: false, id: "b" }])?.id).toBe("b");
    expect(templateToOffer([{ own: true }])).toBeNull();
    expect(templateToOffer(undefined)).toBeNull();
    expect(templateToOffer([])).toBeNull();
  });

  it("an approved intake changes the key the wizard watches", () => {
    const before = approvedKey([{ id: "a", state: "review" }]);
    const after = approvedKey([{ id: "a", state: "approved" }]);
    expect(before).toBe("");
    expect(after).toBe("a");
    expect(approvedKey([{ id: "b", state: "approved" }, { id: "a", state: "approved" }])).toBe("a,b");
  });
});
