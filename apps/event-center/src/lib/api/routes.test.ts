import { describe, expect, it } from "vitest";
import { API, buildPath } from "@/lib/api/routes";

describe("buildPath", () => {
  it("fills a placeholder with a digit in its name ({iso2})", () => {
    // The first version only knew letters and underscores and sent the literal "{iso2}" (6-1a).
    expect(buildPath(API.geoCurrency, { iso2: "ES" })).toBe("/v1/geo/countries/ES/currency");
  });
  it("encodes values and adds only filled query keys", () => {
    expect(buildPath(API.events, { org_id: "a/b" }, { lang: "ru", x: undefined, y: "" })).toBe("/v1/organizations/a%2Fb/events?lang=ru");
  });
  it("refuses a missing value", () => {
    expect(() => buildPath(API.geoTimezones, {})).toThrow(/iso2/);
  });
});
