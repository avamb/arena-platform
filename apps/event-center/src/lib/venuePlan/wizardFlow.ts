/**
 * What the wizard's seating section shows for a venue that has no usable plan
 * (spec 64 sections 2.1 and 2.5), as plain functions. The section is a few
 * buttons; the rules of those buttons live here so a test can walk them without
 * a browser.
 *
 * Order for the person: a verified template of the venue is offered FIRST (the
 * copy is free and ready). They may decline it and upload their own files. Or
 * they continue without a plan (free seating).
 */
export interface NoPlanState {
  /** The upload form is open. */
  uploadOpen: boolean;
  /** "Continue without a plan" was pressed. */
  continued: boolean;
  /** "Upload my own seating plan" was pressed on the template offer. */
  declinedTemplate: boolean;
}

export const NO_PLAN: NoPlanState = { uploadOpen: false, continued: false, declinedTemplate: false };

export type NoPlanAction = "openUpload" | "closeUpload" | "continue" | "declineTemplate";

export function reduceNoPlan(s: NoPlanState, a: NoPlanAction): NoPlanState {
  switch (a) {
    case "openUpload":
      // Uploading one's own plan means the verified one is not wanted.
      return { uploadOpen: true, continued: false, declinedTemplate: true };
    case "declineTemplate":
      return { uploadOpen: true, continued: false, declinedTemplate: true };
    case "closeUpload":
      return { ...s, uploadOpen: false };
    case "continue":
      return { ...s, uploadOpen: false, continued: true };
  }
}

export type NoPlanView = "wait" | "template" | "upload" | "continued" | "ask";

/**
 * `templatesLoading`: the read of the venue's verified plans is still running. Then the person
 * sees only a loading hint: the template offer must come before any upload, and it is not
 * known yet. A FAILED read is not loading: the view falls back to the upload choice.
 */
export function noPlanView(s: NoPlanState, hasTemplate: boolean, templatesLoading = false): NoPlanView {
  if (templatesLoading && !s.uploadOpen && !s.declinedTemplate) return "wait";
  if (s.uploadOpen) return "upload";
  if (hasTemplate && !s.declinedTemplate) return "template";
  return s.continued ? "continued" : "ask";
}

/** The template to offer: the first verified plan that is not already the organization's own. */
export function templateToOffer<T extends { own: boolean }>(templates: readonly T[] | undefined): T | null {
  return templates?.find((x) => !x.own) ?? null;
}

/** Ids of the approved intakes, as one string: a change of it means a plan was just saved. */
export function approvedKey(items: readonly { id: string; state: string }[]): string {
  return items
    .filter((i) => i.state === "approved")
    .map((i) => i.id)
    .sort()
    .join(",");
}
