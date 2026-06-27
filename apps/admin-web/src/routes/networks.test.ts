/**
 * Unit tests for SAUI-07 Operator Networks slug/name validation and
 * server-error mapping. Logic-level only -- no DOM rendering, so these
 * run in the same node-environment vitest config the rest of the suite
 * uses.
 */
import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/client";
import {
  OPERATOR_NETWORK_SLUG_RE,
  mapServerError,
  validateNetworkName,
  validateNetworkSlug,
} from "@/routes/networks";

describe("validateNetworkSlug", () => {
  it("rejects empty input", () => {
    expect(validateNetworkSlug("")).toBe("Slug is required");
  });

  it.each([
    ["lowercase-alnum-hyphen", null],
    ["a", null],
    ["abc", null],
    ["abc123", null],
    ["a-b-c", null],
    ["a1-b2-c3", null],
    ["a".repeat(64), null],
  ])("accepts %j", (slug, expected) => {
    expect(validateNetworkSlug(slug)).toBe(expected);
  });

  it.each([
    ["UPPER", "Slug must be lowercase letters, digits, or hyphens; start and end with [a-z0-9]"],
    ["-leading-hyphen", "Slug must be lowercase letters, digits, or hyphens; start and end with [a-z0-9]"],
    ["trailing-hyphen-", "Slug must be lowercase letters, digits, or hyphens; start and end with [a-z0-9]"],
    ["has space", "Slug must be lowercase letters, digits, or hyphens; start and end with [a-z0-9]"],
    ["under_score", "Slug must be lowercase letters, digits, or hyphens; start and end with [a-z0-9]"],
    ["dot.notation", "Slug must be lowercase letters, digits, or hyphens; start and end with [a-z0-9]"],
  ])("rejects %j", (slug, expected) => {
    expect(validateNetworkSlug(slug)).toBe(expected);
  });

  it("rejects slug over 64 chars", () => {
    expect(validateNetworkSlug("a".repeat(65))).toBe(
      "Slug must be at most 64 characters",
    );
  });

  it("regex matches backend operatorNetworkSlugRE", () => {
    // The regex must be exactly `^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`
    // — same as the Go side in apps/backend/internal/platform/httpserver/networks.go.
    expect(OPERATOR_NETWORK_SLUG_RE.source).toBe(
      "^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$",
    );
  });
});

describe("validateNetworkName", () => {
  it("rejects empty / whitespace-only name", () => {
    expect(validateNetworkName("")).toBe("Name is required");
    expect(validateNetworkName("   ")).toBe("Name is required");
  });

  it("accepts a normal name", () => {
    expect(validateNetworkName("Acme Network")).toBeNull();
  });

  it("rejects names over 200 chars", () => {
    expect(validateNetworkName("a".repeat(201))).toBe(
      "Name must be at most 200 characters",
    );
  });
});

describe("mapServerError", () => {
  function envelopeError(code: string, message = "msg", details?: Record<string, unknown>) {
    return new ApiError(400, {
      code,
      message,
      details,
    });
  }

  it("maps operator_network.invalid_name onto the name field", () => {
    const r = mapServerError(envelopeError("operator_network.invalid_name", "bad name"));
    expect(r.name).toBe("bad name");
    expect(r.slug).toBeUndefined();
    expect(r.form).toBeUndefined();
  });

  it("maps operator_network.invalid_slug onto the slug field", () => {
    const r = mapServerError(
      envelopeError("operator_network.invalid_slug", "bad slug", { field: "slug" }),
    );
    expect(r.slug).toBe("bad slug");
    expect(r.name).toBeUndefined();
    expect(r.form).toBeUndefined();
  });

  it("maps duplicate_slug onto the slug field", () => {
    const r = mapServerError(
      envelopeError(
        "operator_network.duplicate_slug",
        "an active operator network with that slug already exists",
      ),
    );
    expect(r.slug).toBe(
      "an active operator network with that slug already exists",
    );
  });

  it("maps no_changes onto a form-level error", () => {
    const r = mapServerError(
      envelopeError("operator_network.no_changes", "at least one of name or slug"),
    );
    expect(r.form).toBe("at least one of name or slug");
    expect(r.name).toBeUndefined();
    expect(r.slug).toBeUndefined();
  });

  it("maps not_found onto a form-level error", () => {
    const r = mapServerError(
      envelopeError("operator_network.not_found", "not found or already archived"),
    );
    expect(r.form).toBe("not found or already archived");
  });

  it("maps permissions.denied with operator-readable copy", () => {
    const r = mapServerError(envelopeError("permissions.denied", "forbidden"));
    expect(r.form).toMatch(/missing the required permission/i);
  });

  it("falls back to form-level for unknown codes", () => {
    const r = mapServerError(envelopeError("operator_network.insert_failed", "boom"));
    expect(r.form).toContain("boom");
    expect(r.form).toContain("operator_network.insert_failed");
  });

  it("respects details.field for unknown codes", () => {
    const r = mapServerError(
      new ApiError(400, {
        code: "some.future_code",
        message: "future field error",
        details: { field: "name" },
      }),
    );
    expect(r.name).toBe("future field error");
    expect(r.slug).toBeUndefined();
    expect(r.form).toBeUndefined();
  });
});
