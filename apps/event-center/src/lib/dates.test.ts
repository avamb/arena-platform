import { describe, expect, it } from "vitest";
import {
  addDays,
  addMonths,
  boundsEmpty,
  clampMonth,
  dateBounds,
  dateCandidates,
  dateWords,
  displayDate,
  inBounds,
  isIsoDay,
  mondayIndex,
  monthView,
  quickPicks,
  todayInZone,
} from "@/lib/dates";

// The same clock and the same cases as TestDateCandidates in the bot
// (eventbot/wizard_dates_test.go): the two channels must read a date alike.
const TODAY = "2026-10-01";

describe("dateCandidates (the bot's table)", () => {
  const cases: [string, string[]][] = [
    // What the first organizer typed on 2026-10-01: a comma, a trailing dot.
    ["20.10,2026", ["2026-10-20"]],
    ["20.10.2026.", ["2026-10-20"]],
    ["20 10 2026", ["2026-10-20"]],
    ["20/10/26", ["2026-10-20"]],
    // Day and month that can be swapped are both offered, day first.
    ["04.05.2026", ["2026-05-04", "2026-04-05"]],
    ["040526", ["2026-05-04", "2026-04-05"]],
    ["4-5-2026", ["2026-05-04", "2026-04-05"]],
    ["05.05.2026", ["2026-05-05"]],
    ["31.12.2026", ["2026-12-31"]],
    // No year: the next such day.
    ["20.10", ["2026-10-20"]],
    ["5 апреля", ["2027-04-05"]],
    ["20 октября", ["2026-10-20"]],
    ["20 окт.", ["2026-10-20"]],
    ["October 20", ["2026-10-20"]],
    ["20 October 2027", ["2027-10-20"]],
    ["2026-10-20", ["2026-10-20"]],
    ["сегодня", ["2026-10-01"]],
    ["Завтра", ["2026-10-02"]],
    // Spanish: "de" between the parts, accented "mañana", its own month words.
    ["20 de octubre", ["2026-10-20"]],
    ["20 de octubre de 2027", ["2027-10-20"]],
    ["5 de enero", ["2027-01-05"]],
    ["3 abr", ["2027-04-03"]],
    ["15 de diciembre", ["2026-12-15"]],
    ["10 de agosto de 2027", ["2027-08-10"]],
    ["hoy", ["2026-10-01"]],
    ["Mañana", ["2026-10-02"]],
    ["pasado mañana", ["2026-10-03"]],
    // Not dates.
    ["31.02.2026", []],
    ["13.13.2026", []],
    ["banana", []],
    ["", []],
    ["50", []],
  ];
  for (const [input, want] of cases) {
    it(`reads ${JSON.stringify(input)}`, () => {
      expect(dateCandidates(input, TODAY)).toEqual(want);
    });
  }

  it("reads a few more shapes", () => {
    expect(dateCandidates("послезавтра", TODAY)).toEqual(["2026-10-03"]);
    expect(dateCandidates("29.02", TODAY)).toEqual([]); // like the bot: no year means this year or the next, and neither has a 29 February
    expect(dateCandidates("29.02.2028", TODAY)).toEqual(["2028-02-29"]);
    expect(dateCandidates("Oct 20th, 2026", TODAY)).toEqual(["2026-10-20"]);
    expect(dateCandidates("   20.10.2026  ", TODAY)).toEqual(["2026-10-20"]);
  });

  it("a date earlier than today without a year is next year's", () => {
    expect(dateCandidates("30.09", TODAY)).toEqual(["2027-09-30"]);
    // 01.10 is also 10 January (month.day), which is the next such day in 2027.
    expect(dateCandidates("01.10", TODAY)).toEqual(["2026-10-01", "2027-01-10"]);
  });

  it("returns nothing for a clock that is not a day", () => {
    expect(dateCandidates("20.10.2026", "not a day")).toEqual([]);
  });
});

describe("dateWords", () => {
  it("spells a day in each language", () => {
    expect(dateWords("ru", "2026-10-20")).toBe("вторник, 20 октября 2026");
    expect(dateWords("en", "2026-10-20")).toBe("Tuesday, 20 October 2026");
    expect(dateWords("es", "2026-10-20")).toBe("martes, 20 de octubre de 2026");
  });
  it("gives back text that is not a day", () => {
    expect(dateWords("en", "soon")).toBe("soon");
  });
});

describe("ISO helpers", () => {
  it("checks real days", () => {
    expect(isIsoDay("2026-02-28")).toBe(true);
    expect(isIsoDay("2026-02-30")).toBe(false);
    expect(isIsoDay("26-02-28")).toBe(false);
  });
  it("adds days and months across edges", () => {
    expect(addDays("2026-12-31", 1)).toBe("2027-01-01");
    expect(addDays("2026-03-01", -1)).toBe("2026-02-28");
    expect(addMonths("2026-12", 1)).toBe("2027-01");
    expect(addMonths("2026-01", -1)).toBe("2025-12");
  });
  it("counts Monday as 0", () => {
    expect(mondayIndex("2026-10-19")).toBe(0);
    expect(mondayIndex("2026-10-25")).toBe(6);
  });
  it("prints the short form", () => {
    expect(displayDate("2026-10-20")).toBe("20.10.2026");
  });
});

describe("today in the venue's zone", () => {
  it("is a day later in the east and earlier in the west of the same instant", () => {
    const instant = new Date("2026-10-14T22:30:00Z");
    expect(todayInZone("UTC", instant)).toBe("2026-10-14");
    expect(todayInZone("Asia/Jerusalem", instant)).toBe("2026-10-15"); // +3 in October
    expect(todayInZone("America/Los_Angeles", instant)).toBe("2026-10-14");
  });
  it("falls back to UTC for a missing or unknown zone", () => {
    const instant = new Date("2026-10-14T22:30:00Z");
    expect(todayInZone(undefined, instant)).toBe("2026-10-14");
    expect(todayInZone("Nowhere/Land", instant)).toBe("2026-10-14");
  });
});

describe("bounds", () => {
  it("never starts before today", () => {
    expect(dateBounds(TODAY, "2026-09-01", "2026-12-31")).toEqual({ min: TODAY, max: "2026-12-31" });
    expect(dateBounds(TODAY, "2026-10-10")).toEqual({ min: "2026-10-10", max: "" });
    expect(dateBounds(TODAY)).toEqual({ min: TODAY, max: "" });
  });
  it("judges days and empty ranges", () => {
    const b = dateBounds(TODAY, undefined, "2026-10-05");
    expect(inBounds("2026-10-01", b)).toBe(true);
    expect(inBounds("2026-10-05", b)).toBe(true);
    expect(inBounds("2026-10-06", b)).toBe(false);
    expect(inBounds("2026-09-30", b)).toBe(false);
    expect(boundsEmpty(dateBounds(TODAY, "2026-10-09", "2026-10-05"))).toBe(true);
    expect(boundsEmpty(b)).toBe(false);
  });
  it("keeps the shown month inside the range", () => {
    const b = dateBounds(TODAY, undefined, "2026-12-15");
    expect(clampMonth("2026-05", b)).toBe("2026-10");
    expect(clampMonth("2027-03", b)).toBe("2026-12");
    expect(clampMonth("2026-11", b)).toBe("2026-11");
  });
});

describe("monthView", () => {
  const b = dateBounds(TODAY, undefined, "2026-11-10");

  it("pads the first week and marks dead days", () => {
    const v = monthView("2026-10", b);
    // 1 October 2026 is a Thursday: three padding cells first.
    expect(v.weeks[0].slice(0, 3)).toEqual([null, null, null]);
    expect(v.weeks[0][3]).toEqual({ iso: "2026-10-01", day: 1, enabled: true });
    expect(v.weeks.every((w) => w.length === 7)).toBe(true);
    expect(v.weeks.flat().filter((c) => c !== null)).toHaveLength(31);
  });

  it("marks days before today and after the last day as dead", () => {
    const past = monthView("2026-10", dateBounds("2026-10-10", undefined, "2026-10-20"));
    const byDay = new Map(past.weeks.flat().filter((c) => c !== null).map((c) => [c!.day, c!.enabled]));
    expect(byDay.get(9)).toBe(false);
    expect(byDay.get(10)).toBe(true);
    expect(byDay.get(20)).toBe(true);
    expect(byDay.get(21)).toBe(false);
  });

  it("the arrows are dead where the neighbouring month has nothing to pick", () => {
    expect(monthView("2026-10", b).prevMonth).toBe("");
    expect(monthView("2026-10", b).nextMonth).toBe("2026-11");
    expect(monthView("2026-11", b).prevMonth).toBe("2026-10");
    expect(monthView("2026-11", b).nextMonth).toBe("");
  });

  it("an open-ended range always has a next month", () => {
    expect(monthView("2026-10", dateBounds(TODAY)).nextMonth).toBe("2026-11");
  });

  it("starts a Monday-first week for a month that begins on Monday", () => {
    const v = monthView("2026-06", dateBounds("2026-06-01"));
    expect(v.weeks[0][0]).toEqual({ iso: "2026-06-01", day: 1, enabled: true });
  });
});

describe("quickPicks", () => {
  it("offers only shortcuts inside the range", () => {
    expect(quickPicks(TODAY, dateBounds(TODAY)).map((q) => q.key)).toEqual(["today", "tomorrow", "week", "month"]);
    expect(quickPicks(TODAY, dateBounds(TODAY, undefined, "2026-10-03")).map((q) => q.key)).toEqual(["today", "tomorrow"]);
    expect(quickPicks(TODAY, dateBounds(TODAY, "2026-10-05")).map((q) => q.key)).toEqual(["week", "month"]);
    expect(quickPicks(TODAY, dateBounds(TODAY))[2].iso).toBe("2026-10-08");
  });
});
