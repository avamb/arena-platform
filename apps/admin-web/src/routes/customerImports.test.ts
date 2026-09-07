/**
 * Unit tests for the Customer Imports admin page (feature #520, W1-C7b).
 *
 * Covers only the pure exported helpers: validators, the create-body
 * builder, mapping-JSON parsing, the server-error mapper, and the
 * formatting helpers -- matching the repo convention of testing helper
 * functions rather than rendering the route component.
 */
import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/client";
import {
  CUSTOMER_IMPORT_LEGAL_BASES,
  CUSTOMER_IMPORT_SOURCE_LABELS,
  buildCreateCustomerImportBody,
  buildCustomerImportRowsPath,
  formatImportStatus,
  formatLegalBasis,
  formatSourceLabel,
  mapCustomerImportServerError,
  parseCustomerImportMapping,
  validateCustomerImportFileMediaId,
  validateCustomerImportOrgId,
} from "./customerImports";

const VALID_UUID = "550e8400-e29b-41d4-a716-446655440000";

// ---------------------------------------------------------------------------
// validateCustomerImportOrgId
// ---------------------------------------------------------------------------
describe("validateCustomerImportOrgId", () => {
  it("accepts an empty string (optional field)", () => {
    expect(validateCustomerImportOrgId("")).toBeNull();
    expect(validateCustomerImportOrgId("   ")).toBeNull();
  });
  it("accepts a valid UUID", () => {
    expect(validateCustomerImportOrgId(VALID_UUID)).toBeNull();
  });
  it("rejects a non-UUID value", () => {
    expect(validateCustomerImportOrgId("not-a-uuid")).not.toBeNull();
  });
});

// ---------------------------------------------------------------------------
// validateCustomerImportFileMediaId
// ---------------------------------------------------------------------------
describe("validateCustomerImportFileMediaId", () => {
  it("rejects an empty string", () => {
    expect(validateCustomerImportFileMediaId("")).not.toBeNull();
  });
  it("rejects a non-UUID value", () => {
    expect(validateCustomerImportFileMediaId("abhteam")).not.toBeNull();
  });
  it("accepts a valid UUID", () => {
    expect(validateCustomerImportFileMediaId(VALID_UUID)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// parseCustomerImportMapping
// ---------------------------------------------------------------------------
describe("parseCustomerImportMapping", () => {
  it("defaults an empty string to {}", () => {
    expect(parseCustomerImportMapping("")).toEqual({ value: {} });
    expect(parseCustomerImportMapping("   ")).toEqual({ value: {} });
  });
  it("parses a valid JSON object", () => {
    expect(parseCustomerImportMapping('{"frontends":{}}')).toEqual({
      value: { frontends: {} },
    });
  });
  it("rejects invalid JSON", () => {
    const result = parseCustomerImportMapping("{not json");
    expect(result.error).toBeDefined();
    expect(result.value).toBeUndefined();
  });
  it("rejects a JSON array", () => {
    const result = parseCustomerImportMapping("[1,2,3]");
    expect(result.error).toBeDefined();
  });
  it("rejects a JSON primitive", () => {
    const result = parseCustomerImportMapping("42");
    expect(result.error).toBeDefined();
  });
});

// ---------------------------------------------------------------------------
// buildCreateCustomerImportBody
// ---------------------------------------------------------------------------
describe("buildCreateCustomerImportBody", () => {
  it("builds a body without org_id when orgId is blank", () => {
    const result = buildCreateCustomerImportBody(
      "",
      "bil24_orders_json",
      VALID_UUID,
      "{}",
      "organizer_contract",
    );
    expect(result.error).toBeUndefined();
    if (result.error !== undefined) {
      throw new Error("expected success");
    }
    expect(result.body).toEqual({
      source_label: "bil24_orders_json",
      file_media_id: VALID_UUID,
      legal_basis: "organizer_contract",
      mapping: {},
    });
    expect(result.body.org_id).toBeUndefined();
  });

  it("includes org_id when provided", () => {
    const result = buildCreateCustomerImportBody(
      VALID_UUID,
      "wc_customers_csv",
      VALID_UUID,
      "{}",
      "explicit_consent",
    );
    if (result.error !== undefined) {
      throw new Error("expected success");
    }
    expect(result.body.org_id).toBe(VALID_UUID);
  });

  it("collects all field errors at once", () => {
    const result = buildCreateCustomerImportBody(
      "not-a-uuid",
      "bil24_orders_json",
      "",
      "{bad json",
      "organizer_contract",
    );
    expect(result.error).toBe(true);
    if (result.error === undefined) {
      throw new Error("expected failure");
    }
    expect(result.errors.orgId).toBeDefined();
    expect(result.errors.fileMediaId).toBeDefined();
    expect(result.errors.mapping).toBeDefined();
  });
});

// ---------------------------------------------------------------------------
// mapCustomerImportServerError
// ---------------------------------------------------------------------------
describe("mapCustomerImportServerError", () => {
  it("maps field-tagged details to the matching form field", () => {
    const err = new ApiError(400, {
      code: "validation.failed",
      message: "bad org",
      details: { field: "org_id" },
    });
    expect(mapCustomerImportServerError(err)).toEqual({ orgId: "bad org" });
  });

  it("maps known error codes without field details", () => {
    const err = new ApiError(404, {
      code: "customer_import.media_not_found",
      message: "media not found",
    });
    expect(mapCustomerImportServerError(err)).toEqual({
      fileMediaId: "media not found",
    });
  });

  it("maps permissions.denied to a form-level message", () => {
    const err = new ApiError(403, { code: "permissions.denied", message: "nope" });
    expect(mapCustomerImportServerError(err).form).toContain("superadmin.read");
  });

  it("falls back to a generic form error for unknown codes", () => {
    const err = new ApiError(500, { code: "boom", message: "kaboom" });
    expect(mapCustomerImportServerError(err).form).toBe("kaboom (boom)");
  });
});

// ---------------------------------------------------------------------------
// formatters
// ---------------------------------------------------------------------------
describe("formatImportStatus", () => {
  it("formats every known status", () => {
    expect(formatImportStatus("uploaded")).toBe("Uploaded");
    expect(formatImportStatus("dry_run_running")).toBe("Dry-run running");
    expect(formatImportStatus("dry_run_done")).toBe("Dry-run done");
    expect(formatImportStatus("applying")).toBe("Applying");
    expect(formatImportStatus("applied")).toBe("Applied");
    expect(formatImportStatus("failed")).toBe("Failed");
  });
});

describe("formatSourceLabel / formatLegalBasis", () => {
  it("formats every source label", () => {
    for (const label of CUSTOMER_IMPORT_SOURCE_LABELS) {
      expect(formatSourceLabel(label).length).toBeGreaterThan(0);
    }
  });
  it("formats every legal basis", () => {
    for (const basis of CUSTOMER_IMPORT_LEGAL_BASES) {
      expect(formatLegalBasis(basis).length).toBeGreaterThan(0);
    }
  });
});

// ---------------------------------------------------------------------------
// buildCustomerImportRowsPath
// ---------------------------------------------------------------------------
describe("buildCustomerImportRowsPath", () => {
  it("omits the action query param when action is empty", () => {
    expect(buildCustomerImportRowsPath(VALID_UUID, "")).toBe(
      `/v1/admin/customer-imports/${VALID_UUID}/rows`,
    );
  });
  it("includes the action query param when set", () => {
    expect(buildCustomerImportRowsPath(VALID_UUID, "skipped")).toBe(
      `/v1/admin/customer-imports/${VALID_UUID}/rows?action=skipped`,
    );
  });
});
