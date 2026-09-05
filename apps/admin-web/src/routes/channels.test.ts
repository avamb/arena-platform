/**
 * Unit tests for the Sales Channels CRUD module (feature #412 / AB-26).
 *
 * Covers:
 *  - Org-ID validation (org picker UUID escape hatch)
 *  - Channel field validators that mirror the backend contract
 *  - Server-error mapper (all error codes the backend can emit)
 */
import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/client";
import {
  UUID_RE,
  PAYMENT_MODES,
  PROVIDERS,
  validateChannelOrgID,
  validateChannelName,
  validatePaymentMode,
  validateProvider,
  validateProviderAccountID,
  validateFeePercent,
  validateReservationTTL,
  validateSettingsJSON,
  mapServerError,
  providerFieldsFromSettings,
  settingsFromProviderFields,
  validateStatementDescriptor,
  formatGatewayTimestamp,
  gatewayRotateButtonLabel,
} from "./channels";

// ---------------------------------------------------------------------------
// UUID_RE
// ---------------------------------------------------------------------------
describe("UUID_RE", () => {
  it("matches a valid lowercase UUID v4", () => {
    expect(UUID_RE.test("550e8400-e29b-41d4-a716-446655440000")).toBe(true);
  });
  it("matches uppercase UUID", () => {
    expect(UUID_RE.test("550E8400-E29B-41D4-A716-446655440000")).toBe(true);
  });
  it("rejects empty string", () => {
    expect(UUID_RE.test("")).toBe(false);
  });
  it("rejects slug / non-UUID text", () => {
    expect(UUID_RE.test("abhteam")).toBe(false);
    expect(UUID_RE.test("my-org")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// validateChannelOrgID
// ---------------------------------------------------------------------------
describe("validateChannelOrgID", () => {
  it("rejects empty string", () => {
    expect(validateChannelOrgID("")).not.toBeNull();
  });
  it("rejects plain slug", () => {
    expect(validateChannelOrgID("abhteam")).not.toBeNull();
  });
  it("accepts a valid UUID", () => {
    expect(validateChannelOrgID("550e8400-e29b-41d4-a716-446655440000")).toBeNull();
  });
  it("accepts UUID with leading/trailing whitespace (trimmed)", () => {
    expect(validateChannelOrgID("  550e8400-e29b-41d4-a716-446655440000  ")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// validateChannelName
// ---------------------------------------------------------------------------
describe("validateChannelName", () => {
  it("rejects empty string", () => {
    expect(validateChannelName("")).not.toBeNull();
  });
  it("rejects whitespace-only", () => {
    expect(validateChannelName("   ")).not.toBeNull();
  });
  it("accepts a simple name", () => {
    expect(validateChannelName("Main channel")).toBeNull();
  });
  it("rejects name longer than 200 chars", () => {
    expect(validateChannelName("a".repeat(201))).not.toBeNull();
  });
  it("accepts exactly 200 chars", () => {
    expect(validateChannelName("a".repeat(200))).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// PAYMENT_MODES / validatePaymentMode
// ---------------------------------------------------------------------------
describe("PAYMENT_MODES", () => {
  it("enumerates direct_merchant and merchant_of_record", () => {
    expect(PAYMENT_MODES).toContain("direct_merchant");
    expect(PAYMENT_MODES).toContain("merchant_of_record");
    expect(PAYMENT_MODES).toHaveLength(2);
  });
});

describe("validatePaymentMode", () => {
  it("accepts direct_merchant", () => {
    expect(validatePaymentMode("direct_merchant")).toBeNull();
  });
  it("accepts merchant_of_record", () => {
    expect(validatePaymentMode("merchant_of_record")).toBeNull();
  });
  it("rejects unknown mode", () => {
    expect(validatePaymentMode("unknown")).not.toBeNull();
    expect(validatePaymentMode("")).not.toBeNull();
  });
});

// ---------------------------------------------------------------------------
// PROVIDERS / validateProvider
// ---------------------------------------------------------------------------
describe("PROVIDERS", () => {
  it("enumerates stripe and allpay", () => {
    expect(PROVIDERS).toContain("stripe");
    expect(PROVIDERS).toContain("allpay");
  });
});

describe("validateProvider", () => {
  it.each(["stripe", "allpay"])("accepts %s", (p) => {
    expect(validateProvider(p)).toBeNull();
  });
  it("rejects empty / unknown", () => {
    expect(validateProvider("")).not.toBeNull();
    expect(validateProvider("paypal")).not.toBeNull();
  });
});

// ---------------------------------------------------------------------------
// validateProviderAccountID
// ---------------------------------------------------------------------------
describe("validateProviderAccountID", () => {
  it("direct_merchant requires a non-empty account id", () => {
    expect(validateProviderAccountID("direct_merchant", "")).not.toBeNull();
    expect(validateProviderAccountID("direct_merchant", "acct_123")).toBeNull();
  });
  it("merchant_of_record allows empty account id", () => {
    expect(validateProviderAccountID("merchant_of_record", "")).toBeNull();
    expect(validateProviderAccountID("merchant_of_record", "acct_123")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// validateFeePercent
// ---------------------------------------------------------------------------
describe("validateFeePercent", () => {
  it("rejects empty string", () => {
    expect(validateFeePercent("")).not.toBeNull();
  });
  it("accepts zero", () => {
    expect(validateFeePercent("0")).toBeNull();
  });
  it("accepts a typical value like 2.50", () => {
    expect(validateFeePercent("2.50")).toBeNull();
  });
  it("rejects more than 2 decimal places", () => {
    expect(validateFeePercent("2.505")).not.toBeNull();
  });
  it("rejects values above 100", () => {
    expect(validateFeePercent("100.01")).not.toBeNull();
  });
  it("accepts exactly 100", () => {
    expect(validateFeePercent("100")).toBeNull();
  });
  it("rejects negative", () => {
    expect(validateFeePercent("-1")).not.toBeNull();
  });
});

// ---------------------------------------------------------------------------
// validateReservationTTL
// ---------------------------------------------------------------------------
describe("validateReservationTTL", () => {
  it("allows empty (optional field)", () => {
    expect(validateReservationTTL("")).toBeNull();
  });
  it("accepts a reasonable TTL in seconds", () => {
    expect(validateReservationTTL("900")).toBeNull();
  });
  it("rejects zero", () => {
    expect(validateReservationTTL("0")).not.toBeNull();
  });
  it("rejects negative", () => {
    expect(validateReservationTTL("-60")).not.toBeNull();
  });
  it("rejects fractional", () => {
    expect(validateReservationTTL("60.5")).not.toBeNull();
  });
  it("rejects values above 86400", () => {
    expect(validateReservationTTL("86401")).not.toBeNull();
  });
  it("accepts exactly 86400", () => {
    expect(validateReservationTTL("86400")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// validateSettingsJSON
// ---------------------------------------------------------------------------
describe("validateSettingsJSON", () => {
  it("allows empty string (optional)", () => {
    expect(validateSettingsJSON("")).toBeNull();
  });
  it("accepts a valid JSON object", () => {
    expect(validateSettingsJSON('{"key": "value"}')).toBeNull();
  });
  it("rejects arrays", () => {
    expect(validateSettingsJSON("[1,2,3]")).not.toBeNull();
  });
  it("rejects scalar strings", () => {
    expect(validateSettingsJSON('"hello"')).not.toBeNull();
  });
  it("rejects invalid JSON", () => {
    expect(validateSettingsJSON("{bad json")).not.toBeNull();
  });
});

// ---------------------------------------------------------------------------
// mapServerError
// ---------------------------------------------------------------------------
describe("mapServerError", () => {
  function makeError(
    code: string,
    message = "err",
    details?: Record<string, unknown>,
  ): ApiError {
    return new ApiError(400, { code, message, details });
  }

  it("maps channel.invalid_name to name field", () => {
    const err = makeError("channel.invalid_name", "bad name");
    expect(mapServerError(err).name).toBe("bad name");
  });

  it("maps channel.duplicate to name field", () => {
    const err = makeError("channel.duplicate", "already exists");
    expect(mapServerError(err).name).toBe("already exists");
  });

  it("maps channel.invalid_config with provider field to provider", () => {
    const err = makeError("channel.invalid_config", "bad provider", {
      field: "provider",
    });
    expect(mapServerError(err).provider).toBe("bad provider");
  });

  it("maps channel.invalid_config with provider_account_id in message", () => {
    const err = makeError(
      "channel.invalid_config",
      "provider_account_id is required",
    );
    expect(mapServerError(err).provider_account_id).toBe(
      "provider_account_id is required",
    );
  });

  it("maps channel.invalid_settings to settings field", () => {
    const err = makeError("channel.invalid_settings", "bad settings");
    expect(mapServerError(err).settings).toBe("bad settings");
  });

  it("maps channel.not_found to form-level error", () => {
    const err = makeError("channel.not_found", "not found");
    expect(mapServerError(err).form).toBe("not found");
  });

  it("maps permissions.denied to a readable form-level message", () => {
    const err = makeError("permissions.denied");
    expect(mapServerError(err).form).toContain("permission");
  });

  it("maps unknown code with field detail to matching field", () => {
    const err = makeError("unknown.code", "bad name detail", { field: "name" });
    expect(mapServerError(err).name).toBe("bad name detail");
  });

  it("maps unknown code with no field to form-level error", () => {
    const err = makeError("totally.unknown", "something wrong");
    expect(mapServerError(err).form).toContain("something wrong");
  });
});

// ---------------------------------------------------------------------------
// providerFieldsFromSettings
// ---------------------------------------------------------------------------
describe("providerFieldsFromSettings", () => {
  it("extracts stripe statement_descriptor", () => {
    const result = providerFieldsFromSettings("stripe", {
      statement_descriptor: "Arena Tel Aviv",
    });
    expect(result.stripeStatementDescriptor).toBe("Arena Tel Aviv");
    expect(result.allpayTerminalId).toBe("");
    expect(result.advancedJSON).toBe("");
  });

  it("extracts allpay terminal_id", () => {
    const result = providerFieldsFromSettings("allpay", {
      terminal_id: "TID-001",
    });
    expect(result.allpayTerminalId).toBe("TID-001");
    expect(result.stripeStatementDescriptor).toBe("");
    expect(result.advancedJSON).toBe("");
  });

  it("puts unknown keys into advancedJSON", () => {
    const result = providerFieldsFromSettings("stripe", {
      statement_descriptor: "Arena",
      feature_flag_waitlist: true,
    });
    expect(result.stripeStatementDescriptor).toBe("Arena");
    const advanced = JSON.parse(result.advancedJSON) as Record<string, unknown>;
    expect(advanced["feature_flag_waitlist"]).toBe(true);
    expect("statement_descriptor" in advanced).toBe(false);
  });

  it("returns empty fields for null settings", () => {
    const result = providerFieldsFromSettings("stripe", null);
    expect(result.stripeStatementDescriptor).toBe("");
    expect(result.advancedJSON).toBe("");
  });
});

// ---------------------------------------------------------------------------
// settingsFromProviderFields
// ---------------------------------------------------------------------------
describe("settingsFromProviderFields", () => {
  it("builds stripe settings object", () => {
    const result = settingsFromProviderFields("stripe", "Arena Venue", "", "");
    expect(result["statement_descriptor"]).toBe("Arena Venue");
  });

  it("builds allpay settings object", () => {
    const result = settingsFromProviderFields("allpay", "", "TID-001", "");
    expect(result["terminal_id"]).toBe("TID-001");
  });

  it("merges advanced JSON with provider fields", () => {
    const result = settingsFromProviderFields(
      "stripe",
      "Venue",
      "",
      '{"feature_flag":true}',
    );
    expect(result["statement_descriptor"]).toBe("Venue");
    expect(result["feature_flag"]).toBe(true);
  });

  it("provider fields override advanced JSON keys", () => {
    // structured field wins over advanced JSON for the same key
    const result = settingsFromProviderFields(
      "stripe",
      "Structured",
      "",
      '{"statement_descriptor":"Advanced"}',
    );
    expect(result["statement_descriptor"]).toBe("Structured");
  });

  it("returns empty object when nothing is set", () => {
    const result = settingsFromProviderFields("stripe", "", "", "");
    expect(Object.keys(result).length).toBe(0);
  });

  it("provider switch: stripe fields ignored for allpay", () => {
    // When provider=allpay, stripeStatementDescriptor should not be included
    const result = settingsFromProviderFields("allpay", "some-stripe-value", "", "");
    expect("statement_descriptor" in result).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// validateStatementDescriptor
// ---------------------------------------------------------------------------
describe("validateStatementDescriptor", () => {
  it("accepts empty string (optional)", () => {
    expect(validateStatementDescriptor("")).toBeNull();
  });
  it("accepts valid descriptor", () => {
    expect(validateStatementDescriptor("Arena Tel Aviv")).toBeNull();
  });
  it("rejects descriptor over 22 characters", () => {
    expect(validateStatementDescriptor("A".repeat(23))).not.toBeNull();
  });
  it("rejects descriptor with forbidden characters", () => {
    expect(validateStatementDescriptor("Arena <test>")).not.toBeNull();
  });
});

// ---------------------------------------------------------------------------
// Bil24-compatible gateway credential section (feature #474)
// ---------------------------------------------------------------------------
describe("formatGatewayTimestamp", () => {
  it("renders a full UTC date+time for a valid RFC3339 timestamp", () => {
    expect(formatGatewayTimestamp("2026-09-05T12:34:56Z")).toBe(
      "2026-09-05 12:34:56 UTC",
    );
  });
  it("normalises a non-UTC offset to UTC", () => {
    expect(formatGatewayTimestamp("2026-09-05T14:34:56+02:00")).toBe(
      "2026-09-05 12:34:56 UTC",
    );
  });
  it("returns the raw string unchanged for an invalid/empty timestamp", () => {
    expect(formatGatewayTimestamp("not-a-date")).toBe("not-a-date");
    expect(formatGatewayTimestamp("")).toBe("");
  });
});

describe("gatewayRotateButtonLabel", () => {
  it("reads 'Issue token' when the gateway was never provisioned", () => {
    expect(gatewayRotateButtonLabel(false)).toBe("Issue token");
  });
  it("reads 'Issue token' while the summary is still loading (undefined)", () => {
    expect(gatewayRotateButtonLabel(undefined)).toBe("Issue token");
  });
  it("reads 'Rotate token' once a credential already exists", () => {
    expect(gatewayRotateButtonLabel(true)).toBe("Rotate token");
  });
});
