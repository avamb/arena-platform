import { describe, expect, it } from "vitest";
import {
  PRESETS,
  clockText,
  doorsAfterStart,
  doorsBody,
  draftFor,
  draftInstant,
  instantToWall,
  parseClock,
  presetInstant,
  salesEndBody,
  wallToInstant,
  zoneOffsetMinutes,
} from "@/lib/saleTimes";

const MADRID = "Europe/Madrid";

describe("parseClock", () => {
  it("takes real clock times only", () => {
    expect(parseClock("18:30")).toBe(18 * 60 + 30);
    expect(parseClock("9:05")).toBe(9 * 60 + 5);
    expect(parseClock(" 00:00 ")).toBe(0);
    for (const bad of ["", "24:00", "12:60", "1830", "18:3", "ab:cd", "18:30:15"]) expect(parseClock(bad)).toBeNull();
  });
  it("prints back with two digits", () => {
    expect(clockText(9 * 60 + 5)).toBe("09:05");
  });
});

describe("the venue's wall clock", () => {
  it("knows the offset of Madrid in summer and winter", () => {
    expect(zoneOffsetMinutes(Date.UTC(2026, 6, 1, 12), MADRID)).toBe(120);
    expect(zoneOffsetMinutes(Date.UTC(2026, 0, 1, 12), MADRID)).toBe(60);
  });

  it("turns a venue time into an RFC 3339 instant", () => {
    expect(wallToInstant("2026-11-20", "19:30", MADRID)).toBe("2026-11-20T18:30:00Z");
    expect(wallToInstant("2026-07-20", "19:30", MADRID)).toBe("2026-07-20T17:30:00Z");
    expect(wallToInstant("2026-07-20", "19:30", "UTC")).toBe("2026-07-20T19:30:00Z");
    expect(wallToInstant("2026-07-20", "19:30", "America/New_York")).toBe("2026-07-20T23:30:00Z");
  });

  it("round-trips with instantToWall", () => {
    const iso = wallToInstant("2026-12-31", "23:45", MADRID);
    expect(iso).not.toBeNull();
    expect(instantToWall(iso as string, MADRID)).toEqual({ day: "2026-12-31", time: "23:45" });
    // 23:30 UTC is already the next day in Madrid.
    expect(instantToWall("2026-06-30T23:30:00Z", MADRID)).toEqual({ day: "2026-07-01", time: "01:30" });
  });

  it("refuses a time that does not exist when the clocks go forward", () => {
    // Madrid, 2026-03-29: 02:00 jumps to 03:00.
    expect(wallToInstant("2026-03-29", "02:30", MADRID)).toBeNull();
    expect(wallToInstant("2026-03-29", "03:30", MADRID)).toBe("2026-03-29T01:30:00Z");
  });

  it("takes the first of two when the clocks go back", () => {
    // Madrid, 2026-10-25: 03:00 goes back to 02:00, so 02:30 happens twice.
    const iso = wallToInstant("2026-10-25", "02:30", MADRID);
    expect(iso).toBe("2026-10-25T00:30:00Z");
    expect(instantToWall(iso as string, MADRID)).toEqual({ day: "2026-10-25", time: "02:30" });
  });

  it("takes the first occurrence in western zones and half-hour clock changes", () => {
    expect(wallToInstant("2026-11-01", "01:30", "America/New_York")).toBe("2026-11-01T05:30:00Z");
    expect(wallToInstant("2026-04-05", "01:45", "Australia/Lord_Howe")).toBe("2026-04-04T14:45:00Z");
  });

  it("refuses bad input and an unknown zone", () => {
    expect(wallToInstant("2026-02-30", "10:00", MADRID)).toBeNull();
    expect(wallToInstant("2026-02-20", "25:00", MADRID)).toBeNull();
    expect(wallToInstant("2026-02-20", "10:00", "Mars/Olympus")).toBeNull();
    expect(instantToWall("not a date", MADRID)).toBeNull();
  });
});

describe("drafts", () => {
  const start = "2026-11-20T19:00:00Z"; // 20:00 in Madrid

  it("starts empty on the session's day when nothing is set", () => {
    expect(draftFor(null, start, MADRID)).toEqual({ anotherDay: false, day: "2026-11-20", time: "" });
  });

  it("starts on the stored time, and opens the day picker for another day", () => {
    expect(draftFor(start, start, MADRID)).toEqual({ anotherDay: false, day: "2026-11-20", time: "20:00" });
    expect(draftFor("2026-11-19T19:00:00Z", start, MADRID)).toEqual({ anotherDay: true, day: "2026-11-19", time: "20:00" });
  });

  it("means the session's day unless another day is asked for", () => {
    expect(draftInstant({ anotherDay: false, day: "2030-01-01", time: "18:00" }, start, MADRID)).toEqual({
      ok: true,
      iso: "2026-11-20T17:00:00Z",
    });
    expect(draftInstant({ anotherDay: true, day: "2026-11-18", time: "18:00" }, start, MADRID)).toEqual({
      ok: true,
      iso: "2026-11-18T17:00:00Z",
    });
  });

  it("names the problem", () => {
    expect(draftInstant({ anotherDay: false, day: null, time: "" }, start, MADRID)).toEqual({ ok: false, problem: "time" });
    expect(draftInstant({ anotherDay: true, day: null, time: "18:00" }, start, MADRID)).toEqual({ ok: false, problem: "day" });
    expect(draftInstant({ anotherDay: true, day: "2026-03-29", time: "02:30" }, start, MADRID)).toEqual({ ok: false, problem: "gap" });
  });
});

describe("presets and bodies", () => {
  const start = "2026-11-20T19:00:00Z";

  it("counts back from the start", () => {
    expect(presetInstant(start, 0)).toBe(start);
    expect(presetInstant(start, 60)).toBe("2026-11-20T18:00:00Z");
    expect(presetInstant(start, 24 * 60)).toBe("2026-11-19T19:00:00Z");
    expect(PRESETS.doors.map((p) => p.key)).toEqual(["before30m", "before1h"]);
    expect(PRESETS.salesEnd.map((p) => p.key)).toEqual(["atStart", "before1h", "before1d"]);
  });

  it("refuses doors after the start, and only those", () => {
    expect(doorsAfterStart("2026-11-20T19:00:01Z", start)).toBe(true);
    expect(doorsAfterStart(start, start)).toBe(false);
    expect(doorsAfterStart("2026-11-20T18:00:00Z", start)).toBe(false);
  });

  it("builds a body that names one time, and null clears the doors", () => {
    expect(salesEndBody("2026-11-20T18:00:00Z")).toEqual({ sales_end_at: "2026-11-20T18:00:00Z" });
    expect(doorsBody("2026-11-20T18:00:00Z")).toEqual({ doors_open_at: "2026-11-20T18:00:00Z" });
    expect(JSON.stringify(doorsBody(null))).toBe('{"doors_open_at":null}');
    expect(Object.keys(salesEndBody("2026-11-20T18:00:00Z"))).toEqual(["sales_end_at"]);
  });
});
