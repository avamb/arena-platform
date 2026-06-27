/**
 * Unit tests for the local filter / format helpers in the SuperAdmin
 * Organizations explorer (SAUI-06).
 *
 * The cross-tenant explorer ships with explicit guard rails around its
 * filter UX: filtering is LOCAL and MUST NOT silently drop fields. These
 * tests pin down:
 *
 *   - that the substring filter covers every claimed column,
 *   - that `includeDeleted` truly hides soft-deleted rows by default,
 *   - that the duration / count helpers stay user-honest for the
 *     edge cases the table is most likely to render.
 */
import { describe, it, expect } from "vitest";
import {
  filterRows,
  formatDurationSeconds,
  mapCreateOrgServerError,
  validateOrgCountry,
  validateOrgLocale,
  validateOrgName,
  validateOrgReservationTTL,
  validateOrgSlug,
  type AdminOrganization,
} from "./organizations";
import { ApiError } from "@/lib/api/client";

const baseOrg: AdminOrganization = {
  id: "00000000-0000-0000-0000-000000000001",
  name: "Acme Promotions",
  slug: "acme",
  country: "US",
  default_locale: "en-US",
  reservation_ttl_seconds: 600,
  created_at: "2024-01-01T00:00:00Z",
  updated_at: "2024-01-02T00:00:00Z",
  deleted_at: null,
};

const deletedOrg: AdminOrganization = {
  ...baseOrg,
  id: "00000000-0000-0000-0000-000000000002",
  name: "Closed Org",
  slug: "closed",
  country: "GB",
  default_locale: "en-GB",
  deleted_at: "2024-02-01T00:00:00Z",
};

const otherOrg: AdminOrganization = {
  ...baseOrg,
  id: "00000000-0000-0000-0000-000000000003",
  name: "Beta Tickets",
  slug: "beta",
  country: "DE",
  default_locale: "de-DE",
};

describe("filterRows", () => {
  const rows: readonly AdminOrganization[] = [baseOrg, deletedOrg, otherOrg];

  it("hides soft-deleted rows by default", () => {
    const result = filterRows(rows, "", false);
    expect(result.map((r) => r.slug)).toEqual(["acme", "beta"]);
  });

  it("includes soft-deleted rows when requested", () => {
    const result = filterRows(rows, "", true);
    expect(result.map((r) => r.slug)).toEqual(["acme", "closed", "beta"]);
  });

  it("matches by name (case-insensitive)", () => {
    expect(filterRows(rows, "acme", true).map((r) => r.slug)).toEqual(["acme"]);
    expect(filterRows(rows, "ACME", true).map((r) => r.slug)).toEqual(["acme"]);
  });

  it("matches by slug", () => {
    expect(filterRows(rows, "beta", false).map((r) => r.slug)).toEqual(["beta"]);
  });

  it("matches by country", () => {
    expect(filterRows(rows, "de", true).map((r) => r.slug)).toEqual([
      "beta", // country=DE, locale=de-DE
    ]);
  });

  it("matches by locale", () => {
    expect(filterRows(rows, "en-gb", true).map((r) => r.slug)).toEqual(["closed"]);
  });

  it("matches by uuid prefix", () => {
    expect(filterRows(rows, "000000000003", true).map((r) => r.slug)).toEqual(["beta"]);
  });

  it("returns no rows when nothing matches", () => {
    expect(filterRows(rows, "no-such-organization", true)).toEqual([]);
  });

  it("treats whitespace-only filter as empty", () => {
    expect(filterRows(rows, "   ", false).map((r) => r.slug)).toEqual(["acme", "beta"]);
  });
});

describe("formatDurationSeconds", () => {
  it("renders sub-minute durations as seconds", () => {
    expect(formatDurationSeconds(45)).toBe("45s");
  });

  it("renders sub-hour durations as minutes (+ remainder seconds)", () => {
    expect(formatDurationSeconds(600)).toBe("10m");
    expect(formatDurationSeconds(605)).toBe("10m 5s");
  });

  it("renders multi-hour durations as hours (+ remainder minutes)", () => {
    expect(formatDurationSeconds(3600)).toBe("1h");
    expect(formatDurationSeconds(3660)).toBe("1h 1m");
    expect(formatDurationSeconds(7200)).toBe("2h");
  });

  it("guards against non-positive / non-finite input", () => {
    expect(formatDurationSeconds(0)).toBe("—");
    expect(formatDurationSeconds(-1)).toBe("—");
    expect(formatDurationSeconds(Number.NaN)).toBe("—");
  });
});

describe("Create-organization form validators (feature #238)", () => {
  it("validateOrgName requires non-empty and caps at 200 chars", () => {
    expect(validateOrgName("")).not.toBeNull();
    expect(validateOrgName("   ")).not.toBeNull();
    expect(validateOrgName("Acme")).toBeNull();
    expect(validateOrgName("a".repeat(200))).toBeNull();
    expect(validateOrgName("a".repeat(201))).not.toBeNull();
  });

  it("validateOrgSlug enforces lowercase URL-safe identifier", () => {
    expect(validateOrgSlug("")).not.toBeNull();
    expect(validateOrgSlug("acme")).toBeNull();
    expect(validateOrgSlug("acme-events")).toBeNull();
    expect(validateOrgSlug("acme_events")).not.toBeNull();
    expect(validateOrgSlug("acme events")).not.toBeNull();
    // The validator lowercases internally so SCREAM-CASE is accepted as
    // input but enforced lowercase on submission.
    expect(validateOrgSlug("ACME")).toBeNull();
    expect(validateOrgSlug("-bad")).not.toBeNull();
    expect(validateOrgSlug("bad-")).not.toBeNull();
    expect(validateOrgSlug("a".repeat(101))).not.toBeNull();
  });

  it("validateOrgCountry tolerates blank and rejects malformed codes", () => {
    expect(validateOrgCountry("")).toBeNull();
    expect(validateOrgCountry("US")).toBeNull();
    expect(validateOrgCountry("GBR")).toBeNull();
    expect(validateOrgCountry("U")).not.toBeNull();
    expect(validateOrgCountry("USAA")).not.toBeNull();
    expect(validateOrgCountry("U1")).not.toBeNull();
  });

  it("validateOrgLocale tolerates blank and accepts BCP-47 tags", () => {
    expect(validateOrgLocale("")).toBeNull();
    expect(validateOrgLocale("en")).toBeNull();
    expect(validateOrgLocale("en-US")).toBeNull();
    expect(validateOrgLocale("de-DE")).toBeNull();
    expect(validateOrgLocale("english")).not.toBeNull();
    expect(validateOrgLocale("en_US")).not.toBeNull();
  });

  it("validateOrgReservationTTL accepts blank, requires positive int, caps at 86400", () => {
    expect(validateOrgReservationTTL("")).toBeNull();
    expect(validateOrgReservationTTL("1200")).toBeNull();
    expect(validateOrgReservationTTL("86400")).toBeNull();
    expect(validateOrgReservationTTL("86401")).not.toBeNull();
    expect(validateOrgReservationTTL("0")).not.toBeNull();
    expect(validateOrgReservationTTL("-1")).not.toBeNull();
    expect(validateOrgReservationTTL("1.5")).not.toBeNull();
    expect(validateOrgReservationTTL("abc")).not.toBeNull();
  });
});

describe("mapCreateOrgServerError (feature #238)", () => {
  function makeErr(code: string, message = "boom", details?: Record<string, unknown>): ApiError {
    return new ApiError(400, { code, message, details });
  }

  it("maps invalid_name to the name field", () => {
    const out = mapCreateOrgServerError(makeErr("admin_org.invalid_name", "bad"));
    expect(out.name).toBe("bad");
    expect(out.slug).toBeUndefined();
  });

  it("maps invalid_slug to the slug field", () => {
    const out = mapCreateOrgServerError(makeErr("admin_org.invalid_slug", "bad slug"));
    expect(out.slug).toBe("bad slug");
  });

  it("maps duplicate to the slug field (uniqueness is per slug)", () => {
    const out = mapCreateOrgServerError(makeErr("admin_org.duplicate", "already exists"));
    expect(out.slug).toBe("already exists");
  });

  it("maps body-shape errors to the form-level surface", () => {
    expect(mapCreateOrgServerError(makeErr("admin_org.empty_body", "x")).form).toBe("x");
    expect(mapCreateOrgServerError(makeErr("admin_org.invalid_body", "x")).form).toBe("x");
    expect(mapCreateOrgServerError(makeErr("admin_org.invalid_json", "x")).form).toBe("x");
  });

  it("maps permissions.denied to a friendly form-level message", () => {
    const out = mapCreateOrgServerError(makeErr("permissions.denied"));
    expect(out.form).toMatch(/org\.create/);
  });

  it("maps missing-reason errors to a form-level prompt", () => {
    expect(mapCreateOrgServerError(makeErr("superadmin.missing_reason")).form).toMatch(
      /audit reason/i,
    );
    expect(mapCreateOrgServerError(makeErr("superadmin.reason_required")).form).toMatch(
      /audit reason/i,
    );
  });

  it("honours details.field for forwards compatibility", () => {
    expect(
      mapCreateOrgServerError(makeErr("admin_org.unknown", "nope", { field: "name" })).name,
    ).toBe("nope");
    expect(
      mapCreateOrgServerError(makeErr("admin_org.unknown", "nope", { field: "slug" })).slug,
    ).toBe("nope");
  });

  it("falls back to a generic form-level message with the code suffix", () => {
    const out = mapCreateOrgServerError(makeErr("unexpected.code", "boom"));
    expect(out.form).toBe("boom (unexpected.code)");
  });
});
