import { describe, expect, it } from "vitest";
import type { SessionChangeImpact } from "@/lib/api/types";
import { translate } from "@/lib/i18n";
import {
  MAX_NOTICE_CHARS,
  cancelBody,
  changeButtons,
  emptyMessage,
  impactQuery,
  impactToDryRun,
  instantToWall,
  isContactBlocked,
  moveBody,
  noticeToSend,
  noticeTooLong,
  proposeMove,
  resetToSuggested,
  sameInstant,
  typeMessage,
  validTime,
  withSuggested,
  zonedToInstant,
} from "@/lib/sessionChange";

const can = (...perms: string[]) => (p: string) => perms.includes(p);
const t = (key: Parameters<typeof translate>[1], params?: Parameters<typeof translate>[2]) => translate("en", key, params);

function impact(over: Partial<SessionChangeImpact> = {}): SessionChangeImpact {
  return {
    session_id: "s1",
    kinds: ["date"],
    orders: 2,
    tickets: 3,
    arena_orders: 2,
    site_orders: 0,
    no_address: 0,
    blocked: "",
    contact: { source: "promoter", name: "P", email: "p@example.test", phone: "", phone_hidden: false, complete: true, target_kind: "promoter", target_id: "x", target_name: "P" },
    default_message: "We moved the show.",
    current: { start_at: "2027-01-10T19:00:00Z", end_at: "2027-01-10T21:00:00Z", venue_id: "v", venue_name: "Hall", timezone: "Europe/Madrid", status: "scheduled" },
    proposed: { start_at: "2027-01-17T19:00:00Z", end_at: "2027-01-17T21:00:00Z", venue_id: "v", venue_name: "Hall", timezone: "Europe/Madrid", status: "scheduled" },
    ...over,
  };
}

describe("changeButtons", () => {
  it("shows nothing without session.update", () => {
    expect(changeButtons("scheduled", can("session.read"))).toEqual({ move: false, cancel: false });
  });
  it("offers both on a scheduled session, only a move on a draft, nothing after the end", () => {
    const yes = can("session.update");
    expect(changeButtons("scheduled", yes)).toEqual({ move: true, cancel: true });
    expect(changeButtons("draft", yes)).toEqual({ move: true, cancel: false });
    expect(changeButtons("cancelled", yes)).toEqual({ move: false, cancel: false });
    expect(changeButtons("completed", yes)).toEqual({ move: false, cancel: false });
    expect(changeButtons(undefined, yes)).toEqual({ move: false, cancel: false });
  });
});

describe("wall clock and instant", () => {
  it("reads a Madrid wall clock in winter and in summer", () => {
    expect(zonedToInstant("2027-01-17", "20:30", "Europe/Madrid")).toBe("2027-01-17T19:30:00Z");
    expect(zonedToInstant("2027-07-17", "20:30", "Europe/Madrid")).toBe("2027-07-17T18:30:00Z");
  });
  it("handles a zone ahead of UTC across midnight", () => {
    expect(zonedToInstant("2027-03-01", "00:30", "Asia/Jerusalem")).toBe("2027-02-28T22:30:00Z");
  });
  it("refuses a time that does not exist when the clocks go forward", () => {
    // 2027-03-28 02:30 does not exist in Madrid.
    expect(zonedToInstant("2027-03-28", "02:30", "Europe/Madrid")).toBeNull();
    expect(zonedToInstant("2027-03-28", "03:30", "Europe/Madrid")).toBe("2027-03-28T01:30:00Z");
  });
  it("reads an hour that happens twice as its first occurrence", () => {
    // 2027-10-31 02:30 happens twice in Madrid (the first one is still summer time).
    expect(zonedToInstant("2027-10-31", "02:30", "Europe/Madrid")).toBe("2027-10-31T00:30:00Z");
  });
  it("refuses malformed input and an unknown zone", () => {
    expect(zonedToInstant("2027-13-01", "20:30", "Europe/Madrid")).toBeNull();
    expect(zonedToInstant("2027-01-17", "25:00", "Europe/Madrid")).toBeNull();
    expect(zonedToInstant("2027-01-17", "20:30", "Nowhere/Land")).toBeNull();
    expect(validTime("9:30")).toBe(false);
    expect(validTime("09:30")).toBe(true);
  });
  it("round-trips an instant through the wall clock", () => {
    const wall = instantToWall("2027-07-17T18:30:00Z", "Europe/Madrid");
    expect(wall).toEqual({ day: "2027-07-17", time: "20:30" });
    expect(zonedToInstant(wall.day, wall.time, "Europe/Madrid")).toBe("2027-07-17T18:30:00Z");
    expect(instantToWall("2027-07-17T18:30:00Z", "Nowhere/Land")).toEqual({ day: "2027-07-17", time: "18:30" });
  });
  it("compares instants, not strings", () => {
    expect(sameInstant("2027-01-17T19:30:00Z", "2027-01-17T20:30:00+01:00")).toBe(true);
    expect(sameInstant("2027-01-17T19:30:00Z", "2027-01-17T19:31:00Z")).toBe(false);
  });
});

describe("proposeMove", () => {
  const base = {
    currentStartAt: "2027-01-10T19:00:00Z",
    currentVenueId: "v1",
    day: "2027-01-10",
    time: "20:00",
    scheduleTouched: false,
    venueId: "",
    zone: "Europe/Madrid",
  };
  it("changes nothing until the person touches the schedule or the venue", () => {
    expect(proposeMove(base)).toEqual({ problem: "none", startAt: null, venueId: null, changed: false });
  });
  it("proposes the new instant in the venue zone", () => {
    expect(proposeMove({ ...base, day: "2027-01-17", scheduleTouched: true })).toMatchObject({
      startAt: "2027-01-17T19:00:00Z",
      changed: true,
    });
  });
  it("treats the same wall clock as no change", () => {
    expect(proposeMove({ ...base, scheduleTouched: true }).changed).toBe(false);
  });
  it("keeps the instant when only the venue changes", () => {
    expect(proposeMove({ ...base, venueId: "v2" })).toEqual({ problem: "none", startAt: null, venueId: "v2", changed: true });
    expect(proposeMove({ ...base, venueId: "v1" }).changed).toBe(false);
  });
  it("reports a bad or missing time and a time lost to a clock change", () => {
    expect(proposeMove({ ...base, scheduleTouched: true, time: "" }).problem).toBe("invalidTime");
    expect(proposeMove({ ...base, scheduleTouched: true, time: "7pm" }).problem).toBe("invalidTime");
    const gap = proposeMove({ ...base, scheduleTouched: true, day: "2027-03-28", time: "02:30" });
    expect(gap.problem).toBe("gapTime");
    expect(gap.changed).toBe(false);
  });
});

describe("request shapes", () => {
  it("sends only what changes in the dry run query", () => {
    expect(impactQuery({ startAt: "2027-01-17T19:00:00Z", venueId: null, locale: "ru" })).toEqual({ locale: "ru", start_at: "2027-01-17T19:00:00Z" });
    expect(impactQuery({ cancel: true, locale: "en" })).toEqual({ locale: "en", status: "cancelled" });
  });
  it("builds the PATCH of a move with the notice only when buyers get a letter", () => {
    const p = { problem: "none" as const, startAt: "2027-01-17T19:00:00Z", venueId: "v2", changed: true };
    expect(moveBody(p, "Sorry.")).toEqual({ start_at: "2027-01-17T19:00:00Z", venue_id: "v2", notice: { message: "Sorry." } });
    expect(moveBody(p, null)).toEqual({ start_at: "2027-01-17T19:00:00Z", venue_id: "v2" });
    expect(moveBody({ ...p, venueId: null }, "")).toEqual({ start_at: "2027-01-17T19:00:00Z", notice: { message: "" } });
  });
  it("builds the PATCH of a cancellation as status cancelled, never a delete", () => {
    expect(cancelBody("Sorry.")).toEqual({ status: "cancelled", notice: { message: "Sorry." } });
    expect(cancelBody(null)).toEqual({ status: "cancelled" });
  });
  it("sends a message only to a session that has buyers", () => {
    expect(noticeToSend(null, "x")).toBeNull();
    expect(noticeToSend({ orders: 0 }, "x")).toBeNull();
    expect(noticeToSend({ orders: 1 }, "x")).toBe("x");
  });
  it("counts the message the way the API does", () => {
    expect(noticeTooLong("a".repeat(MAX_NOTICE_CHARS))).toBe(false);
    expect(noticeTooLong("a".repeat(MAX_NOTICE_CHARS + 1))).toBe(true);
    // 1000 emoji are 2000 UTF-16 units but 1000 code points.
    expect(noticeTooLong("😀".repeat(MAX_NOTICE_CHARS))).toBe(false);
  });
});

describe("the suggested message", () => {
  it("fills an untouched field and never overwrites a typed one", () => {
    const first = withSuggested(emptyMessage, "Suggested");
    expect(first).toEqual({ text: "Suggested", touched: false });
    expect(withSuggested(first, "Other")).toEqual({ text: "Other", touched: false });
    const typed = typeMessage("Mine");
    expect(withSuggested(typed, "Other")).toEqual({ text: "Mine", touched: true });
    expect(resetToSuggested("Other")).toEqual({ text: "Other", touched: false });
  });
});

describe("impactToDryRun", () => {
  it("shows the numbers of a move and no block", () => {
    const r = impactToDryRun(impact(), "move", t);
    expect(r.blocked).toBeUndefined();
    expect(r.figures?.find((f) => f.label === "Orders")?.value).toBe(2);
    expect(r.figures?.find((f) => f.label === "Buyers write to")?.value).toBe("p@example.test");
    expect(r.warnings).toBeUndefined();
  });
  it("blocks on a missing contact and says to add it", () => {
    const i = impact({ blocked: "contact_missing" });
    expect(isContactBlocked(i)).toBe(true);
    expect(impactToDryRun(i, "move", t).blocked).toContain("contact e-mail");
  });
  it("blocks on a partner site and says nothing was changed", () => {
    const r = impactToDryRun(impact({ blocked: "site_route_unsupported", site_orders: 2 }), "cancel", t);
    expect(r.blocked).toContain("2 orders were sold through a partner site");
    expect(r.blocked).toContain("Nothing was changed");
  });
  it("says again that a cancellation refunds nothing", () => {
    const r = impactToDryRun(impact({ kinds: ["cancelled"], no_address: 1 }), "cancel", t);
    expect(r.warnings?.some((w) => w.includes("does NOT refund"))).toBe(true);
    expect(r.warnings?.some((w) => w.includes("1 orders have no buyer e-mail"))).toBe(true);
  });
  it("says no letters go out without buyers or without a visible change", () => {
    expect(impactToDryRun(impact({ orders: 0, tickets: 0, arena_orders: 0 }), "move", t).summary).toContain("Nobody has bought");
    expect(impactToDryRun(impact({ kinds: [] }), "move", t).summary).toContain("no change");
  });
});
