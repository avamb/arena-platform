/**
 * Unit tests for the network detail (SAUI-08) helpers.
 *
 * These cover the pure helpers that don't depend on React render:
 *  - validateUuid: covers v4-style UUIDs and rejects malformed input
 *  - formatDate: ISO -> human "YYYY-MM-DD HH:mm"
 *
 * Full DOM/integration coverage is deferred to the e2e smoke (out of
 * scope under YOLO mode). The pure helpers are still worth pinning so
 * accidental regressions on the UUID regex or date formatter surface
 * during type-check + vitest runs.
 */
import { describe, expect, test } from "vitest";
import { formatDate, validateUuid } from "./networkDetail";

describe("validateUuid", () => {
  test("accepts a canonical lowercase UUID", () => {
    expect(
      validateUuid("0190a8b0-7d31-7a3c-9c4e-8c0c1d9d9c2a"),
    ).toBeNull();
  });

  test("accepts an uppercase UUID (case-insensitive)", () => {
    expect(
      validateUuid("0190A8B0-7D31-7A3C-9C4E-8C0C1D9D9C2A"),
    ).toBeNull();
  });

  test("trims whitespace before validating", () => {
    expect(
      validateUuid("  0190a8b0-7d31-7a3c-9c4e-8c0c1d9d9c2a  "),
    ).toBeNull();
  });

  test("rejects empty string", () => {
    expect(validateUuid("")).toBe("value is required");
  });

  test("rejects whitespace-only as empty", () => {
    expect(validateUuid("   ")).toBe("value is required");
  });

  test("rejects malformed string", () => {
    expect(validateUuid("not-a-uuid")).toMatch(/must be a valid UUID/);
  });

  test("rejects UUID with too few segments", () => {
    expect(validateUuid("0190a8b0-7d31-7a3c-9c4e")).toMatch(
      /must be a valid UUID/,
    );
  });

  test("uses the provided label in the error", () => {
    expect(validateUuid("", "user_id")).toBe("user_id is required");
    expect(validateUuid("xx", "organization_id")).toMatch(
      /^organization_id must be a valid UUID/,
    );
  });
});

describe("formatDate", () => {
  test("formats a canonical ISO timestamp to YYYY-MM-DD HH:mm", () => {
    expect(formatDate("2026-06-27T11:45:33Z")).toBe("2026-06-27 11:45");
  });

  test("preserves the original string for unparseable input", () => {
    expect(formatDate("garbage")).toBe("garbage");
  });

  test("normalises timezone offset to UTC", () => {
    // 12:00 UTC+02:00 == 10:00 UTC.
    expect(formatDate("2026-06-27T12:00:00+02:00")).toBe("2026-06-27 10:00");
  });
});
