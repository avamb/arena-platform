/**
 * Unit tests for feature #490 (W1-A6e) org-scoped Orders page validators,
 * cancellability helper, and server-error mapping. Logic-level only -- no
 * DOM rendering, mirrors customers.test.ts.
 */
import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/client";
import {
  CANCELLABLE_STATUSES,
  ORDER_STATUSES,
  isCancellable,
  mapServerError,
  validateOrderQuery,
  validateOrgID,
} from "@/routes/orgOrders";

describe("validateOrderQuery", () => {
  it("accepts an empty string", () => {
    expect(validateOrderQuery("")).toBeNull();
  });

  it("accepts a whitespace-only string", () => {
    expect(validateOrderQuery("   ")).toBeNull();
  });

  it("accepts a query at exactly the 200 char cap", () => {
    expect(validateOrderQuery("a".repeat(200))).toBeNull();
  });

  it("rejects a query one char over the 200 char cap", () => {
    expect(validateOrderQuery("a".repeat(201))).toBe(
      "Search must be at most 200 characters",
    );
  });

  it("trims before measuring length, matching the backend's TrimSpace", () => {
    expect(validateOrderQuery(`  ${"a".repeat(200)}  `)).toBeNull();
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

describe("isCancellable", () => {
  it("allows pending_payment and manual_review", () => {
    for (const s of CANCELLABLE_STATUSES) {
      expect(isCancellable(s)).toBe(true);
    }
  });

  it("rejects paid, cancelled and expired", () => {
    expect(isCancellable("paid")).toBe(false);
    expect(isCancellable("cancelled")).toBe(false);
    expect(isCancellable("expired")).toBe(false);
  });

  it("every cancellable status is a known order status", () => {
    for (const s of CANCELLABLE_STATUSES) {
      expect(ORDER_STATUSES).toContain(s);
    }
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

  it("maps orders.invalid_query onto the q field", () => {
    const r = mapServerError(
      envelopeError("orders.invalid_query", "q must be at most 200 characters"),
    );
    expect(r.q).toBe("q must be at most 200 characters");
    expect(r.form).toBeUndefined();
    expect(r.org).toBeUndefined();
  });

  it("maps orders.invalid_limit onto a form-level error", () => {
    const r = mapServerError(
      envelopeError("orders.invalid_limit", "limit must be a positive integer up to 200"),
    );
    expect(r.form).toBe("limit must be a positive integer up to 200");
    expect(r.q).toBeUndefined();
  });

  it("maps orders.invalid_offset onto a form-level error", () => {
    const r = mapServerError(
      envelopeError("orders.invalid_offset", "offset must be a non-negative integer"),
    );
    expect(r.form).toBe("offset must be a non-negative integer");
  });

  it("maps orders.invalid_session_id onto a form-level error", () => {
    const r = mapServerError(
      envelopeError("orders.invalid_session_id", "session_id must be a UUID"),
    );
    expect(r.form).toBe("session_id must be a UUID");
  });

  it("maps orders.invalid_from and orders.invalid_to onto a form-level error", () => {
    expect(
      mapServerError(envelopeError("orders.invalid_from", "from must be RFC3339")).form,
    ).toBe("from must be RFC3339");
    expect(
      mapServerError(envelopeError("orders.invalid_to", "to must be RFC3339")).form,
    ).toBe("to must be RFC3339");
  });

  it("maps orders.not_found onto a form-level error", () => {
    const r = mapServerError(
      envelopeError("orders.not_found", "order not found in this organization"),
    );
    expect(r.form).toBe("order not found in this organization");
  });

  it("maps orders.invalid_transition with operator-readable copy", () => {
    const r = mapServerError(
      envelopeError("orders.invalid_transition", "cannot cancel a paid order"),
    );
    expect(r.form).toBe("This order can no longer be cancelled.");
  });

  it("maps dependency.database_unavailable onto a form-level error", () => {
    const r = mapServerError(
      envelopeError("dependency.database_unavailable", "orders store not configured"),
    );
    expect(r.form).toBe("orders store not configured");
  });

  it("maps permissions.denied with operator-readable copy", () => {
    const r = mapServerError(envelopeError("permissions.denied", "forbidden"));
    expect(r.form).toMatch(/missing the required permission/i);
  });

  it("falls back to form-level for unknown codes", () => {
    const r = mapServerError(envelopeError("orders.internal", "boom"));
    expect(r.form).toContain("boom");
    expect(r.form).toContain("orders.internal");
  });
});
