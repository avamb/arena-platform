/**
 * Unit tests for the Venues admin form (feature #259 / V-3).
 *
 * The venues route ships with structured-address validation, a country
 * normaliser, request-body builders, and an extended server-error
 * mapper. Node-environment vitest tests pin down the pure helpers so a
 * regression in client-side validation surfaces before the DOM does.
 */
import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/client";
import {
  ISO_COUNTRY_RE,
  VENUE_STATUSES,
  buildCreateVenueBody,
  buildUpdateVenueBody,
  isVenueStatus,
  listIanaTimezones,
  mapServerError,
  normalizeCountry,
  validateVenueAddressLine,
  validateVenueContactEmail,
  validateVenueContactPhone,
  validateVenueCountry,
  validateVenueGeoLat,
  validateVenueGeoLng,
  validateVenueGeoPair,
  validateVenuePostalCode,
  validateVenueTimezone,
  validateVenueWebsiteUrl,
  type VenueFormState,
} from "@/routes/venues";

const baseState: VenueFormState = {
  name: "Hall A",
  cityID: "",
  addressLine1: "",
  addressLine2: "",
  postalCode: "",
  country: "",
  geoLat: "",
  geoLng: "",
  timezone: "",
  phone: "",
  email: "",
  website: "",
  status: "active",
  capacity: "",
};

describe("VENUE_STATUSES", () => {
  it("enumerates the three OpenAPI statuses in order", () => {
    expect(VENUE_STATUSES).toEqual(["active", "draft", "archived"]);
  });

  it("isVenueStatus rejects unknown values", () => {
    expect(isVenueStatus("active")).toBe(true);
    expect(isVenueStatus("draft")).toBe(true);
    expect(isVenueStatus("archived")).toBe(true);
    expect(isVenueStatus("ACTIVE")).toBe(false);
    expect(isVenueStatus("")).toBe(false);
    expect(isVenueStatus("deleted")).toBe(false);
  });
});

describe("normalizeCountry / validateVenueCountry", () => {
  it("normalizes whitespace + lowercase to canonical alpha-2", () => {
    expect(normalizeCountry(" ru ")).toBe("RU");
    expect(normalizeCountry("us")).toBe("US");
    expect(normalizeCountry("")).toBe("");
  });

  it("ISO_COUNTRY_RE matches exactly two uppercase letters", () => {
    expect(ISO_COUNTRY_RE.test("RU")).toBe(true);
    expect(ISO_COUNTRY_RE.test("US")).toBe(true);
    expect(ISO_COUNTRY_RE.test("ru")).toBe(false);
    expect(ISO_COUNTRY_RE.test("RUS")).toBe(false);
    expect(ISO_COUNTRY_RE.test("R1")).toBe(false);
  });

  it("treats empty input as valid (optional field)", () => {
    expect(validateVenueCountry("")).toBeNull();
    expect(validateVenueCountry("  ")).toBeNull();
  });

  it.each(["RU", "us", " gb "])("accepts %j", (raw) => {
    expect(validateVenueCountry(raw)).toBeNull();
  });

  it.each(["R", "RUS", "12", "R1"])("rejects %j", (raw) => {
    expect(validateVenueCountry(raw)).toMatch(/ISO-3166-1/);
  });
});

describe("validateVenuePostalCode", () => {
  it("accepts empty + short inputs", () => {
    expect(validateVenuePostalCode("")).toBeNull();
    expect(validateVenuePostalCode("101000")).toBeNull();
    expect(validateVenuePostalCode("SW1A 1AA")).toBeNull();
  });
  it("rejects >32 chars", () => {
    expect(validateVenuePostalCode("x".repeat(33))).toMatch(/at most 32/);
  });
});

describe("validateVenueAddressLine", () => {
  it("accepts blanks", () => {
    expect(validateVenueAddressLine("", 1)).toBeNull();
    expect(validateVenueAddressLine("", 2)).toBeNull();
  });
  it("accepts up to 200 chars", () => {
    expect(validateVenueAddressLine("x".repeat(200), 1)).toBeNull();
  });
  it("rejects >200 with the right index in message", () => {
    expect(validateVenueAddressLine("x".repeat(201), 2)).toMatch(/line 2/);
  });
});

describe("validateVenueGeoLat / Lng / Pair", () => {
  it("validates lat range", () => {
    expect(validateVenueGeoLat("")).toBeNull();
    expect(validateVenueGeoLat("0")).toBeNull();
    expect(validateVenueGeoLat("90")).toBeNull();
    expect(validateVenueGeoLat("-90")).toBeNull();
    expect(validateVenueGeoLat("90.1")).toMatch(/between/);
    expect(validateVenueGeoLat("not-a-number")).toMatch(/number/);
  });
  it("validates lng range", () => {
    expect(validateVenueGeoLng("180")).toBeNull();
    expect(validateVenueGeoLng("-180")).toBeNull();
    expect(validateVenueGeoLng("180.5")).toMatch(/between/);
  });
  it("requires both halves of the coordinate pair", () => {
    expect(validateVenueGeoPair("", "")).toBeNull();
    expect(validateVenueGeoPair("55.7", "37.6")).toBeNull();
    expect(validateVenueGeoPair("55.7", "")).toMatch(/together/);
    expect(validateVenueGeoPair("", "37.6")).toMatch(/together/);
  });
});

describe("validateVenueTimezone", () => {
  it("accepts blank", () => {
    expect(validateVenueTimezone("")).toBeNull();
  });
  it.each(["UTC", "Europe/Moscow", "America/Argentina/Buenos_Aires"])(
    "accepts %j",
    (tz) => {
      expect(validateVenueTimezone(tz)).toBeNull();
    },
  );
  it.each(["not a tz", "Europe\\Moscow", "Europe/Moscow/Extra/Too/Deep"])(
    "rejects %j",
    (tz) => {
      expect(validateVenueTimezone(tz)).toMatch(/IANA/);
    },
  );
});

describe("validateVenueContactEmail", () => {
  it("accepts blank", () => {
    expect(validateVenueContactEmail("")).toBeNull();
  });
  it("accepts valid emails", () => {
    expect(validateVenueContactEmail("ops@example.com")).toBeNull();
  });
  it("rejects malformed", () => {
    expect(validateVenueContactEmail("bad-email")).toMatch(/valid email/);
    expect(validateVenueContactEmail("a@b")).toMatch(/valid email/);
  });
});

describe("validateVenueContactPhone", () => {
  it("accepts blank, +, digits, spaces, parens, hyphens", () => {
    expect(validateVenueContactPhone("")).toBeNull();
    expect(validateVenueContactPhone("+7 (495) 123-45-67")).toBeNull();
  });
  it("rejects letters", () => {
    expect(validateVenueContactPhone("CALL ME")).toMatch(/digits/);
  });
});

describe("validateVenueWebsiteUrl", () => {
  it("accepts blank + http(s) URLs", () => {
    expect(validateVenueWebsiteUrl("")).toBeNull();
    expect(validateVenueWebsiteUrl("https://example.com")).toBeNull();
    expect(validateVenueWebsiteUrl("http://localhost:8080/v")).toBeNull();
  });
  it("rejects junk and non-http schemes", () => {
    expect(validateVenueWebsiteUrl("not-a-url")).toMatch(/valid URL/);
    expect(validateVenueWebsiteUrl("ftp://example.com")).toMatch(/http or https/);
  });
});

describe("listIanaTimezones", () => {
  it("returns a non-empty list", () => {
    const list = listIanaTimezones();
    expect(list.length).toBeGreaterThan(0);
    // The engine list always contains Europe/Moscow on modern ICU; the
    // fallback ships it explicitly. Asserting on this rather than "UTC"
    // because the engine normalises "UTC" to "Etc/UTC".
    expect(list).toContain("Europe/Moscow");
  });
});

describe("buildCreateVenueBody", () => {
  it("emits name + city_id + status only when other optionals are blank", () => {
    const body = buildCreateVenueBody({
      ...baseState,
      name: "  Hall A  ",
      cityID: "  ",
    });
    expect(body).toEqual({ name: "Hall A", city_id: "", status: "active" });
  });

  it("emits all structured fields when filled", () => {
    const body = buildCreateVenueBody({
      ...baseState,
      addressLine1: "Tverskaya 1",
      addressLine2: "Floor 2",
      postalCode: "101000",
      country: "ru",
      geoLat: "55.7558",
      geoLng: "37.6173",
      timezone: "Europe/Moscow",
      phone: "+7 495 1234567",
      email: "ops@arena.example",
      website: "https://arena.example",
      status: "draft",
      capacity: "1500",
    });
    expect(body).toMatchObject({
      name: "Hall A",
      city_id: "",
      status: "draft",
      address_line1: "Tverskaya 1",
      address_line2: "Floor 2",
      postal_code: "101000",
      country: "RU",
      geo_lat: 55.7558,
      geo_lng: 37.6173,
      timezone: "Europe/Moscow",
      contact_phone: "+7 495 1234567",
      contact_email: "ops@arena.example",
      website_url: "https://arena.example",
      capacity_default: 1500,
    });
  });

  it("uppercases country when sending", () => {
    const body = buildCreateVenueBody({ ...baseState, country: " us " });
    expect(body.country).toBe("US");
  });
});

describe("buildUpdateVenueBody", () => {
  const prev: VenueFormState = {
    ...baseState,
    name: "Hall A",
    addressLine1: "Old line",
    country: "RU",
    geoLat: "55",
    geoLng: "37",
    timezone: "Europe/Moscow",
    status: "active",
    capacity: "100",
  };

  it("returns {} when nothing changed", () => {
    expect(buildUpdateVenueBody(prev, prev)).toEqual({});
  });

  it("sends null when clearing an optional string field", () => {
    const body = buildUpdateVenueBody({ ...prev, addressLine1: "" }, prev);
    expect(body).toEqual({ address_line1: null });
  });

  it("sends null when clearing geo coordinates", () => {
    const body = buildUpdateVenueBody(
      { ...prev, geoLat: "", geoLng: "" },
      prev,
    );
    expect(body).toEqual({ geo_lat: null, geo_lng: null });
  });

  it("sends null when clearing country", () => {
    const body = buildUpdateVenueBody({ ...prev, country: "" }, prev);
    expect(body).toEqual({ country: null });
  });

  it("updates status when changed", () => {
    expect(
      buildUpdateVenueBody({ ...prev, status: "archived" }, prev),
    ).toEqual({ status: "archived" });
  });

  it("sends capacity_default as number", () => {
    expect(buildUpdateVenueBody({ ...prev, capacity: "250" }, prev)).toEqual({
      capacity_default: 250,
    });
  });

  it("sends capacity_default=null when cleared", () => {
    expect(buildUpdateVenueBody({ ...prev, capacity: "" }, prev)).toEqual({
      capacity_default: null,
    });
  });
});

describe("mapServerError", () => {
  function envelope(
    code: string,
    msg = "boom",
    details?: Record<string, unknown>,
  ): ApiError {
    return new ApiError(422, { code, message: msg, details });
  }

  it.each([
    ["venue.invalid_name", "name"],
    ["venue.duplicate", "name"],
    ["venue.invalid_city_id", "city_id"],
    ["venue.invalid_country", "country"],
    ["venue.invalid_postal_code", "postal_code"],
    ["venue.invalid_geo_lat", "geo_lat"],
    ["venue.invalid_geo_lng", "geo_lng"],
    ["venue.invalid_timezone", "timezone"],
    ["venue.invalid_email", "contact_email"],
    ["venue.invalid_contact_email", "contact_email"],
    ["venue.invalid_phone", "contact_phone"],
    ["venue.invalid_website", "website_url"],
    ["venue.invalid_website_url", "website_url"],
    ["venue.invalid_status", "status"],
  ])("routes %s -> %s", (code, field) => {
    const out = mapServerError(envelope(code));
    expect((out as Record<string, string>)[field]).toBe("boom");
  });

  it("routes invalid_address_line via details.field", () => {
    expect(
      mapServerError(
        envelope("venue.invalid_address_line", "bad", { field: "address_line2" }),
      ),
    ).toEqual({ address_line2: "bad" });
    expect(
      mapServerError(envelope("venue.invalid_address_line", "bad")),
    ).toEqual({ address_line1: "bad" });
  });

  it("routes generic codes via details.field", () => {
    const out = mapServerError(
      envelope("validation.failed", "bad zip", { field: "postal_code" }),
    );
    expect(out).toEqual({ postal_code: "bad zip" });
  });

  it("falls back to form-level error when field is unknown", () => {
    const out = mapServerError(envelope("something.unexpected", "bad"));
    expect(out.form).toBe("bad (something.unexpected)");
  });

  it("maps permissions.denied + reason gates to form-level", () => {
    expect(mapServerError(envelope("permissions.denied", "x")).form).toMatch(
      /permission/,
    );
    expect(
      mapServerError(envelope("superadmin.missing_reason", "x")).form,
    ).toMatch(/audit reason/);
  });
});
