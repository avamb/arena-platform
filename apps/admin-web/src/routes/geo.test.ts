import { describe, expect, it } from "vitest";
import { GEO_SLUG_RE, validateGeoSlug } from "@/routes/geo";

describe("Geo Registry validation", () => {
  it.each(["prague", "new-york", "a1"])("accepts registry slug %s", (slug) => {
    expect(validateGeoSlug(slug)).toBeNull();
  });

  it.each(["", "New York", "new_york", "-city", "city-"])("rejects invalid registry slug %s", (slug) => {
    expect(validateGeoSlug(slug)).not.toBeNull();
  });

  it("uses a conservative lower-case slug pattern", () => {
    expect(GEO_SLUG_RE.test("prague-1")).toBe(true);
    expect(GEO_SLUG_RE.test("Prague")).toBe(false);
  });
});
