import { describe, expect, it } from "vitest";
import { NO_DATES, datesBackwards, datesQuery, dayEndIso, dayStartIso, hasDates, ticketLetterKey, ticketLetterTone } from "@/lib/orderCards";

describe("the date filters", () => {
  it("turn a typed day into the first and the last instant of it", () => {
    const start = new Date(dayStartIso("2026-11-07") ?? "");
    const end = new Date(dayEndIso("2026-11-07") ?? "");
    expect([start.getFullYear(), start.getMonth(), start.getDate(), start.getHours(), start.getMinutes()]).toEqual([2026, 10, 7, 0, 0]);
    expect([end.getDate(), end.getHours(), end.getMinutes(), end.getSeconds()]).toEqual([7, 23, 59, 59]);
    expect(end.getTime()).toBeGreaterThan(start.getTime());
  });
  it("drop a blank or an impossible day", () => {
    expect(dayStartIso("")).toBeUndefined();
    expect(dayStartIso("07.11.2026")).toBeUndefined();
    expect(dayEndIso("2026-02-31")).toBeUndefined();
  });
  it("send only the ranges the person filled in", () => {
    expect(datesQuery(NO_DATES)).toEqual({ from: undefined, to: undefined, sessionFrom: undefined, sessionTo: undefined });
    const q = datesQuery({ ...NO_DATES, sessionFrom: "2026-12-01" });
    expect(q.sessionFrom).toBeDefined();
    expect(q.from).toBeUndefined();
    expect(hasDates(NO_DATES)).toBe(false);
    expect(hasDates({ ...NO_DATES, orderTo: "2026-12-01" })).toBe(true);
  });
  it("spot a range that runs backwards", () => {
    expect(datesBackwards({ ...NO_DATES, orderFrom: "2026-12-02", orderTo: "2026-12-01" })).toBe(true);
    expect(datesBackwards({ ...NO_DATES, sessionFrom: "2026-12-01", sessionTo: "2026-12-01" })).toBe(false);
    expect(datesBackwards({ ...NO_DATES, sessionFrom: "2026-12-01" })).toBe(false);
  });
});

describe("the letter of a ticket", () => {
  it("names every state a delivery job can be in", () => {
    expect(ticketLetterKey("sent")).toBe("delivery.sentShort");
    expect(ticketLetterKey("processing")).toBe("delivery.pending");
    expect(ticketLetterKey("failed")).toBe("delivery.failed");
    expect(ticketLetterKey("disabled")).toBe("delivery.skipped");
    expect(ticketLetterKey("none")).toBe("delivery.none");
    expect(ticketLetterKey("surprise")).toBe("delivery.none");
    // The provider's word about a letter (also one the selling site sent).
    expect(ticketLetterKey("delivered")).toBe("delivery.deliveredShort");
    expect(ticketLetterKey("opened")).toBe("delivery.openedShort");
    expect(ticketLetterKey("delayed")).toBe("delivery.delayedShort");
    expect(ticketLetterKey("sending")).toBe("delivery.pending");
    expect([ticketLetterTone("delivered"), ticketLetterTone("opened"), ticketLetterTone("delayed")]).toEqual(["ok", "ok", "wait"]);
  });
  it("gives every state a tone", () => {
    expect([ticketLetterTone("sent"), ticketLetterTone("pending"), ticketLetterTone("failed"), ticketLetterTone("none")]).toEqual(["ok", "wait", "bad", "neutral"]);
  });
});
