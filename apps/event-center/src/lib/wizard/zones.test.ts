import { describe, expect, it } from "vitest";
import { endOfInclusiveDayRfc3339, formatRfc3339, isKnownZone, startOfDayRfc3339, wallToEpoch, zoneOffsetMinutes } from "@/lib/wizard/zones";

describe("zones", () => {
  it("prints UTC as Z, like Go's time.RFC3339", () => {
    const e = wallToEpoch("2027-06-10", "20:00", "UTC");
    expect(e).not.toBeNull();
    expect(formatRfc3339(e!, "UTC")).toBe("2027-06-10T20:00:00Z");
    expect(formatRfc3339(e!, "")).toBe("2027-06-10T20:00:00Z");
    expect(formatRfc3339(e!, "Not/AZone")).toBe("2027-06-10T20:00:00Z");
  });

  it("follows the clock change of the venue's zone", () => {
    expect(formatRfc3339(wallToEpoch("2027-03-27", "19:00", "Europe/Prague")!, "Europe/Prague")).toBe("2027-03-27T19:00:00+01:00");
    expect(formatRfc3339(wallToEpoch("2027-03-28", "19:00", "Europe/Prague")!, "Europe/Prague")).toBe("2027-03-28T19:00:00+02:00");
    expect(formatRfc3339(wallToEpoch("2027-11-05", "19:00", "America/New_York")!, "America/New_York")).toBe("2027-11-05T19:00:00-04:00");
    expect(formatRfc3339(wallToEpoch("2027-11-10", "19:00", "America/New_York")!, "America/New_York")).toBe("2027-11-10T19:00:00-05:00");
  });

  it("adds minutes to an instant, not to the wall clock", () => {
    const start = wallToEpoch("2027-03-27", "23:00", "Europe/Prague")!;
    // Three hours later the clocks have moved: 23:00 + 3 h is 03:00 on the wall.
    expect(formatRfc3339(start + 3 * 60 * 60_000, "Europe/Prague")).toBe("2027-03-28T03:00:00+02:00");
  });

  it("starts a day, and ends an inclusive day at the next midnight", () => {
    expect(startOfDayRfc3339("2027-03-28", "Europe/Prague")).toBe("2027-03-28T00:00:00+01:00");
    expect(endOfInclusiveDayRfc3339("2027-03-27", "Europe/Prague")).toBe("2027-03-28T00:00:00+01:00");
    expect(endOfInclusiveDayRfc3339("2027-03-28", "Europe/Prague")).toBe("2027-03-29T00:00:00+02:00");
    expect(endOfInclusiveDayRfc3339("2027-12-31", "UTC")).toBe("2028-01-01T00:00:00Z");
  });

  it("refuses a day or a time that is not real", () => {
    expect(wallToEpoch("2027-02-30", "19:00", "UTC")).toBeNull();
    expect(wallToEpoch("2027-02-28", "25:00", "UTC")).toBeNull();
    expect(wallToEpoch("", "19:00", "UTC")).toBeNull();
    expect(startOfDayRfc3339("nope", "UTC")).toBe("");
  });

  it("knows a zone name when the browser does", () => {
    expect(isKnownZone("Europe/Madrid")).toBe(true);
    expect(isKnownZone("")).toBe(false);
    expect(isKnownZone("Mars/Olympus")).toBe(false);
    expect(zoneOffsetMinutes(Date.UTC(2027, 0, 1), "Asia/Kolkata")).toBe(330);
  });
});
