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
import { filterRows, formatDurationSeconds, type AdminOrganization } from "./organizations";

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
