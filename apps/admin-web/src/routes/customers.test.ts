/**
 * Unit tests for feature #482 (W1-A4d) Customers directory validators and
 * server-error mapping. Logic-level only -- no DOM rendering, mirrors
 * networks.test.ts.
 */
import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/client";
import {
  mapServerError,
  validateCustomerQuery,
  validateOrgID,
} from "@/routes/customers";

describe("validateCustomerQuery", () => {
  it("accepts an empty string", () => {
    expect(validateCustomerQuery("")).toBeNull();
  });

  it("accepts a whitespace-only string", () => {
    expect(validateCustomerQuery("   ")).toBeNull();
  });

  it("accepts a query at exactly the 200 char cap", () => {
    expect(validateCustomerQuery("a".repeat(200))).toBeNull();
  });

  it("rejects a query one char over the 200 char cap", () => {
    expect(validateCustomerQuery("a".repeat(201))).toBe(
      "Search must be at most 200 characters",
    );
  });

  it("trims before measuring length, matching the backend's TrimSpace", () => {
    // 200 chars of payload plus surrounding whitespace must still pass —
    // the backend trims before checking len(q) > 200.
    expect(validateCustomerQuery(`  ${"a".repeat(200)}  `)).toBeNull();
  });
});

describe("validateOrgID", () => {
  it("rejects empty input", () => {
    expect(validateOrgID("")).toBe("Organization ID is required");
  });

  it("rejects a non-UUID string", () => {
    expect(validateOrgID("not-a-uuid")).toBe("Organization ID must be a UUID");
  });

  it("accepts a valid UUID", () => {
    expect(validateOrgID("0190a8b0-7d31-7a3c-9c4e-8c0c1d9d9c2a")).toBeNull();
  });

  it("accepts a valid uppercase UUID", () => {
    expect(validateOrgID("0190A8B0-7D31-7A3C-9C4E-8C0C1D9D9C2A")).toBeNull();
  });
});

describe("mapServerError", () => {
  function envelopeError(
    code: string,
    message = "msg",
    details?: Record<string, unknown>,
  ) {
    return new ApiError(400, { code, message, details });
  }

  it("maps customers.invalid_query onto the q field", () => {
    const r = mapServerError(
      envelopeError("customers.invalid_query", "q must be at most 200 characters"),
    );
    expect(r.q).toBe("q must be at most 200 characters");
    expect(r.form).toBeUndefined();
    expect(r.org).toBeUndefined();
  });

  it("maps customers.invalid_limit onto a form-level error", () => {
    const r = mapServerError(
      envelopeError("customers.invalid_limit", "limit must be a positive integer up to 200"),
    );
    expect(r.form).toBe("limit must be a positive integer up to 200");
    expect(r.q).toBeUndefined();
  });

  it("maps customers.invalid_offset onto a form-level error", () => {
    const r = mapServerError(
      envelopeError("customers.invalid_offset", "offset must be a non-negative integer"),
    );
    expect(r.form).toBe("offset must be a non-negative integer");
  });

  it("maps customers.not_found onto a form-level error", () => {
    const r = mapServerError(
      envelopeError("customers.not_found", "customer not found in this organization"),
    );
    expect(r.form).toBe("customer not found in this organization");
  });

  it("maps dependency.database_unavailable onto a form-level error", () => {
    const r = mapServerError(
      envelopeError("dependency.database_unavailable", "customers store not configured"),
    );
    expect(r.form).toBe("customers store not configured");
  });

  it("maps permissions.denied with operator-readable copy", () => {
    const r = mapServerError(envelopeError("permissions.denied", "forbidden"));
    expect(r.form).toMatch(/missing the required permission/i);
  });

  it("falls back to form-level for unknown codes", () => {
    const r = mapServerError(envelopeError("customers.internal", "boom"));
    expect(r.form).toContain("boom");
    expect(r.form).toContain("customers.internal");
  });
});
