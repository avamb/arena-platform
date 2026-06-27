/**
 * Unit tests for the shared SuperAdmin support console helpers
 * (SAUI-10). These helpers underpin /orders, /tickets and /refunds, so
 * a regression here breaks all three routes at once -- the test suite
 * pins down the contracts that the routes rely on.
 */
import { describe, it, expect } from "vitest";
import {
  buildSupportQuery,
  canGoNext,
  canGoPrev,
  clampLimit,
  clampOffset,
  currentPage,
  formatDateTime,
  formatMoneyMinor,
  isValidUuid,
  readSupportFiltersFromLocation,
  shortUuid,
  SUPPORT_MAX_LIMIT,
} from "./supportConsole";

const VALID_UUID = "11111111-2222-3333-4444-555555555555";

describe("isValidUuid", () => {
  it("accepts a canonical 36-char hyphenated UUID", () => {
    expect(isValidUuid(VALID_UUID)).toBe(true);
  });
  it("tolerates surrounding whitespace", () => {
    expect(isValidUuid(`  ${VALID_UUID}  `)).toBe(true);
  });
  it("accepts uppercase", () => {
    expect(isValidUuid(VALID_UUID.toUpperCase())).toBe(true);
  });
  it("rejects empty / missing input", () => {
    expect(isValidUuid("")).toBe(false);
    expect(isValidUuid("   ")).toBe(false);
  });
  it("rejects non-UUID strings", () => {
    expect(isValidUuid("not-a-uuid")).toBe(false);
    expect(isValidUuid("11111111-2222-3333-4444")).toBe(false);
    expect(isValidUuid(`${VALID_UUID}xx`)).toBe(false);
  });
});

describe("clampLimit", () => {
  it("returns default when undefined / NaN", () => {
    expect(clampLimit(undefined)).toBe(50);
    expect(clampLimit(Number.NaN)).toBe(50);
  });
  it("returns default when zero or negative", () => {
    expect(clampLimit(0)).toBe(50);
    expect(clampLimit(-1)).toBe(50);
  });
  it("caps at backend max of 200", () => {
    expect(clampLimit(SUPPORT_MAX_LIMIT)).toBe(SUPPORT_MAX_LIMIT);
    expect(clampLimit(SUPPORT_MAX_LIMIT + 1)).toBe(SUPPORT_MAX_LIMIT);
    expect(clampLimit(10_000)).toBe(SUPPORT_MAX_LIMIT);
  });
  it("floors fractional input", () => {
    expect(clampLimit(25.7)).toBe(25);
  });
});

describe("clampOffset", () => {
  it("returns 0 for undefined / NaN / negative", () => {
    expect(clampOffset(undefined)).toBe(0);
    expect(clampOffset(Number.NaN)).toBe(0);
    expect(clampOffset(-100)).toBe(0);
  });
  it("preserves non-negative integers", () => {
    expect(clampOffset(0)).toBe(0);
    expect(clampOffset(50)).toBe(50);
    expect(clampOffset(12345)).toBe(12345);
  });
});

describe("buildSupportQuery", () => {
  it("emits limit and offset even when other filters are empty", () => {
    const q = buildSupportQuery(
      { orgId: "", statusValue: "", limit: 50, offset: 0 },
      "state",
    );
    expect(q).toBe("limit=50&offset=0");
  });

  it("includes a valid org_id and the chosen status key", () => {
    const q = buildSupportQuery(
      { orgId: VALID_UUID, statusValue: "completed", limit: 25, offset: 100 },
      "state",
    );
    expect(q).toBe(
      `org_id=${encodeURIComponent(VALID_UUID)}&state=completed&limit=25&offset=100`,
    );
  });

  it("drops malformed org_id rather than sending a 400-bound value", () => {
    const q = buildSupportQuery(
      { orgId: "not-a-uuid", statusValue: "active", limit: 50, offset: 0 },
      "status",
    );
    expect(q).toBe("status=active&limit=50&offset=0");
  });

  it("URL-encodes status values that contain special characters", () => {
    const q = buildSupportQuery(
      { orgId: "", statusValue: "in progress", limit: 50, offset: 0 },
      "state",
    );
    expect(q).toBe("state=in%20progress&limit=50&offset=0");
  });

  it("uses the per-entity status key (state vs. status)", () => {
    const stateQ = buildSupportQuery(
      { orgId: "", statusValue: "succeeded", limit: 50, offset: 0 },
      "state",
    );
    const statusQ = buildSupportQuery(
      { orgId: "", statusValue: "active", limit: 50, offset: 0 },
      "status",
    );
    expect(stateQ).toContain("state=succeeded");
    expect(stateQ).not.toContain("status=");
    expect(statusQ).toContain("status=active");
    expect(statusQ).not.toContain("state=");
  });

  it("clamps an over-large limit to the backend maximum", () => {
    const q = buildSupportQuery(
      { orgId: "", statusValue: "", limit: 9999, offset: 0 },
      "state",
    );
    expect(q).toContain(`limit=${SUPPORT_MAX_LIMIT}`);
  });
});

describe("readSupportFiltersFromLocation", () => {
  it("parses org_id and the configured status key", () => {
    const f = readSupportFiltersFromLocation(
      `?org_id=${VALID_UUID}&state=completed&limit=100&offset=200`,
      "state",
    );
    expect(f.orgId).toBe(VALID_UUID);
    expect(f.statusValue).toBe("completed");
    expect(f.limit).toBe(100);
    expect(f.offset).toBe(200);
  });

  it("ignores the wrong status key for the entity", () => {
    const f = readSupportFiltersFromLocation(
      "?state=succeeded&status=active",
      "status",
    );
    expect(f.statusValue).toBe("active");
  });

  it("falls back to defaults when params are absent", () => {
    const f = readSupportFiltersFromLocation("", "state");
    expect(f).toEqual({ orgId: "", statusValue: "", limit: 50, offset: 0 });
  });

  it("clamps malformed limit / offset values", () => {
    const f = readSupportFiltersFromLocation(
      "?limit=abc&offset=-5",
      "state",
    );
    expect(f.limit).toBe(50);
    expect(f.offset).toBe(0);
  });

  it("accepts a query string without a leading '?'", () => {
    const f = readSupportFiltersFromLocation("limit=25", "state");
    expect(f.limit).toBe(25);
  });
});

describe("pagination helpers", () => {
  it("currentPage returns 1 at offset 0", () => {
    expect(currentPage(0, 50)).toBe(1);
  });
  it("currentPage handles partial pages and zero limit safely", () => {
    expect(currentPage(50, 50)).toBe(2);
    expect(currentPage(150, 50)).toBe(4);
    expect(currentPage(10, 0)).toBe(11); // safe: limit is forced to 1
  });
  it("canGoPrev requires offset > 0", () => {
    expect(canGoPrev(0)).toBe(false);
    expect(canGoPrev(1)).toBe(true);
  });
  it("canGoNext only when page is full", () => {
    expect(canGoNext(50, 50)).toBe(true);
    expect(canGoNext(49, 50)).toBe(false);
    expect(canGoNext(0, 50)).toBe(false);
  });
});

describe("formatMoneyMinor", () => {
  it("renders an amount and currency code", () => {
    expect(formatMoneyMinor(12345, "RUB")).toBe("123.45 RUB");
    expect(formatMoneyMinor(0, "USD")).toBe("0.00 USD");
  });
  it("uppercases the currency code", () => {
    expect(formatMoneyMinor(100, "eur")).toBe("1.00 EUR");
  });
  it("renders em-dash when amount is missing", () => {
    expect(formatMoneyMinor(null, "USD")).toBe("—");
    expect(formatMoneyMinor(undefined, "USD")).toBe("—");
    expect(formatMoneyMinor(Number.NaN, "USD")).toBe("—");
  });
  it("renders em-dash when currency is missing", () => {
    expect(formatMoneyMinor(100, null)).toBe("—");
    expect(formatMoneyMinor(100, "")).toBe("—");
  });
});

describe("formatDateTime", () => {
  it("renders ISO timestamps as 'YYYY-MM-DD HH:MMZ'", () => {
    expect(formatDateTime("2024-05-01T12:34:56Z")).toBe("2024-05-01 12:34Z");
  });
  it("returns em-dash when value is missing", () => {
    expect(formatDateTime(null)).toBe("—");
    expect(formatDateTime(undefined)).toBe("—");
    expect(formatDateTime("")).toBe("—");
  });
  it("returns input untouched when not parseable", () => {
    expect(formatDateTime("not-a-date")).toBe("not-a-date");
  });
});

describe("shortUuid", () => {
  it("returns first 8 chars and ellipsis for canonical UUIDs", () => {
    expect(shortUuid(VALID_UUID)).toBe("11111111…");
  });
  it("returns short input unchanged", () => {
    expect(shortUuid("abc")).toBe("abc");
    expect(shortUuid("12345678")).toBe("12345678");
  });
});
