import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/http";
import {
  isStaleIntakeError,
  actionsOf,
  escalateTextProblem,
  fixTextProblem,
  isFinal,
  isOpen,
  nextStepOf,
  operatorWait,
  panelOf,
  parseIntakeSearch,
  pollDelayMs,
  runningIntake,
  sourceViewOf,
  stateKey,
  suggestAdministrator,
  warningKey,
} from "@/lib/venuePlan/intakeState";

const all = { create: true, read: true };
const readOnly = { create: false, read: true };
const none = { create: false, read: false };

describe("the panel of each server state", () => {
  it("maps the seven states", () => {
    expect(panelOf("received")).toBe("waitingGenerator");
    expect(panelOf("generating")).toBe("waitingGenerator");
    expect(panelOf("operator")).toBe("waitingOperator");
    expect(panelOf("review")).toBe("review");
    expect(panelOf("approved")).toBe("saved");
    expect(panelOf("rejected")).toBe("rejected");
    expect(panelOf("cancelled")).toBe("cancelled");
    expect(panelOf("something_new")).toBe("unknown");
  });

  it("knows the final and the open states", () => {
    for (const s of ["approved", "rejected", "cancelled"]) {
      expect(isFinal(s)).toBe(true);
      expect(isOpen(s)).toBe(false);
    }
    for (const s of ["received", "generating", "review", "operator"]) {
      expect(isFinal(s)).toBe(false);
      expect(isOpen(s)).toBe(true);
    }
    expect(isOpen("something_new")).toBe(false);
  });
});

describe("the buttons", () => {
  it("review offers correct, fix, ask an administrator and cancel to a person with seating_plan.create", () => {
    expect(actionsOf("review", all)).toEqual({ view: true, approve: true, fix: true, escalate: true, cancel: true, sendAnother: false, nextStep: false });
  });

  it("a person who can only read sees the state and no button", () => {
    expect(actionsOf("review", readOnly)).toEqual({ view: true, approve: false, fix: false, escalate: false, cancel: false, sendAnother: false, nextStep: false });
    expect(actionsOf("approved", readOnly).nextStep).toBe(true);
    expect(actionsOf("approved", none).view).toBe(false);
  });

  it("waiting states can only be canceled", () => {
    for (const s of ["operator"]) {
      const a = actionsOf(s, all);
      expect(a.cancel).toBe(true);
      expect(a.approve).toBe(false);
      expect(a.fix).toBe(false);
      expect(a.escalate).toBe(false);
    }
    expect(actionsOf("generating", all).escalate).toBe(true);
  });

  it("a final state offers the next step and a new file, never a cancel", () => {
    expect(actionsOf("rejected", all)).toMatchObject({ sendAnother: true, cancel: false, approve: false });
    expect(actionsOf("cancelled", all)).toMatchObject({ sendAnother: true, cancel: false });
    expect(actionsOf("approved", all)).toMatchObject({ nextStep: true, sendAnother: false, cancel: false });
    expect(actionsOf("rejected", readOnly).sendAnother).toBe(false);
  });
});

describe("the hint to ask an administrator", () => {
  it("shows from the fifth draft on, in review only", () => {
    expect(suggestAdministrator("review", 4)).toBe(false);
    expect(suggestAdministrator("review", 5)).toBe(true);
    expect(suggestAdministrator("review", 9)).toBe(true);
    expect(suggestAdministrator("operator", 9)).toBe(false);
  });
});

describe("polling while the intake waits", () => {
  it("is fast for the generator, slow for an administrator", () => {
    expect(pollDelayMs("received")).toBe(3000);
    expect(pollDelayMs("generating")).toBe(3000);
    expect(pollDelayMs("operator")).toBe(30000);
  });

  it("stops on review and on the final states", () => {
    for (const s of ["review", "approved", "rejected", "cancelled", "something_new"]) expect(pollDelayMs(s)).toBe(false);
    expect(pollDelayMs(undefined)).toBe(false);
  });

  it("doubles after each failed read, up to a minute", () => {
    expect(pollDelayMs("generating", 1)).toBe(6000);
    expect(pollDelayMs("generating", 2)).toBe(12000);
    expect(pollDelayMs("generating", 9)).toBe(60000);
    expect(pollDelayMs("operator", 3)).toBe(60000);
  });
});

describe("what waits for an administrator", () => {
  it("tells an automatic hand-over from the person's own request", () => {
    expect(operatorWait("no_generator")).toBe("auto");
    expect(operatorWait("generator_failed")).toBe("auto");
    expect(operatorWait(null)).toBe("auto");
    expect(operatorWait("organizer_request")).toBe("request");
    expect(operatorWait("organizer_text")).toBe("request");
  });
});

describe("the running intake of a venue", () => {
  it("is the newest open one", () => {
    const list = [{ id: "a", state: "approved" }, { id: "b", state: "operator" }, { id: "c", state: "review" }];
    expect(runningIntake(list)?.id).toBe("b");
    expect(runningIntake([{ id: "a", state: "cancelled" }])).toBeNull();
    expect(runningIntake([])).toBeNull();
  });

  it("has a word for every state", () => {
    expect(stateKey("operator")).toBe("venuePlan.state.operator");
    expect(stateKey("x")).toBe("venuePlan.state.unknown");
  });
});

describe("how a source is shown", () => {
  it("prefers the PNG copy, then the file, then a download", () => {
    expect(sourceViewOf({ kind: "tiff", png_copy_path: "/p?format=png" })).toBe("png_copy");
    expect(sourceViewOf({ kind: "jpeg", png_copy_path: null })).toBe("file");
    expect(sourceViewOf({ kind: "png", png_copy_path: null })).toBe("file");
    expect(sourceViewOf({ kind: "pdf", png_copy_path: null })).toBe("download");
    expect(sourceViewOf({ kind: "heic", png_copy_path: null })).toBe("download");
    expect(sourceViewOf({ kind: "svg", png_copy_path: null })).toBe("file");
  });
});

describe("the next step after the plan is saved", () => {
  it("goes back to the date when the page was opened from it", () => {
    expect(nextStepOf(parseIntakeSearch({ event: "e1", session: "s1" }))).toEqual({ kind: "split", eventId: "e1", sessionId: "s1" });
  });

  it("offers a new event otherwise", () => {
    expect(nextStepOf(parseIntakeSearch({}))).toEqual({ kind: "createEvent" });
    expect(nextStepOf(parseIntakeSearch({ event: "e1" }))).toEqual({ kind: "createEvent" });
    expect(parseIntakeSearch({ event: 5, session: "" })).toEqual({});
  });
});

describe("the written texts", () => {
  it("a correction has 1 to 2000 characters", () => {
    expect(fixTextProblem("   ")).toBe("empty");
    expect(fixTextProblem("Row 5 has 12 seats")).toBeNull();
    expect(fixTextProblem("a".repeat(2000))).toBeNull();
    expect(fixTextProblem("a".repeat(2001))).toBe("tooLong");
  });

  it("the note to an administrator is optional and up to 1000", () => {
    expect(escalateTextProblem("")).toBeNull();
    expect(escalateTextProblem("a".repeat(1001))).toBe("tooLong");
  });

  it("warnings have a text, known or not", () => {
    expect(warningKey("legend_missing")).toBe("venuePlan.warning.legend_missing");
    expect(warningKey("brand_new")).toBe("venuePlan.warning.other");
  });
});

describe("isStaleIntakeError", () => {
  it("is true for the two 409 codes that mean the intake moved on", () => {
    expect(isStaleIntakeError(new ApiError(409, "venue_plan_intake.wrong_state", "x"))).toBe(true);
    expect(isStaleIntakeError(new ApiError(409, "venue_plan_intake.draft_stale", "x"))).toBe(true);
    expect(isStaleIntakeError(new ApiError(409, "venue_plan_intake.invalid_text", "x"))).toBe(false);
    expect(isStaleIntakeError(new Error("x"))).toBe(false);
  });
});
