/**
 * Unit tests for the Payment Provider Configs CRUD module (feature #412 / AB-26).
 *
 * Covers:
 *  - Org-ID / provider / mode validators that mirror the backend contract
 *  - Public-config JSON validator
 *  - PROVIDER_REQUIRED_SECRETS catalogue (mirrors requiredSecretFields in Go)
 *  - Server-error mapper (all error codes the backend can emit)
 */
import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/client";
import {
  UUID_RE,
  PAYMENT_PROVIDERS,
  PAYMENT_MODES,
  PROVIDER_REQUIRED_SECRETS,
  validateOrgID,
  validateProvider,
  validateMode,
  validatePublicConfigJSON,
  mapServerError,
} from "./payments";

// ---------------------------------------------------------------------------
// UUID_RE
// ---------------------------------------------------------------------------
describe("UUID_RE", () => {
  it("matches a valid UUID", () => {
    expect(UUID_RE.test("550e8400-e29b-41d4-a716-446655440000")).toBe(true);
  });
  it("rejects non-UUID text", () => {
    expect(UUID_RE.test("abhteam")).toBe(false);
    expect(UUID_RE.test("")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// validateOrgID
// ---------------------------------------------------------------------------
describe("validateOrgID", () => {
  it("rejects empty string", () => {
    expect(validateOrgID("")).not.toBeNull();
  });
  it("rejects slug", () => {
    expect(validateOrgID("my-org")).not.toBeNull();
  });
  it("accepts valid UUID", () => {
    expect(validateOrgID("550e8400-e29b-41d4-a716-446655440000")).toBeNull();
  });
  it("accepts UUID with surrounding whitespace (trimmed)", () => {
    expect(validateOrgID("  550e8400-e29b-41d4-a716-446655440000  ")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// PAYMENT_PROVIDERS / validateProvider
// ---------------------------------------------------------------------------
describe("PAYMENT_PROVIDERS", () => {
  it("includes stripe, allpay, cloudpayments, yookassa, manual", () => {
    expect(PAYMENT_PROVIDERS).toContain("stripe");
    expect(PAYMENT_PROVIDERS).toContain("allpay");
    expect(PAYMENT_PROVIDERS).toContain("cloudpayments");
    expect(PAYMENT_PROVIDERS).toContain("yookassa");
    expect(PAYMENT_PROVIDERS).toContain("manual");
  });
});

describe("validateProvider", () => {
  it.each(["stripe", "allpay", "cloudpayments", "yookassa", "manual"])(
    "accepts %s",
    (p) => {
      expect(validateProvider(p)).toBeNull();
    },
  );
  it("rejects empty / unknown", () => {
    expect(validateProvider("")).not.toBeNull();
    expect(validateProvider("paypal")).not.toBeNull();
  });
});

// ---------------------------------------------------------------------------
// PAYMENT_MODES / validateMode
// ---------------------------------------------------------------------------
describe("PAYMENT_MODES", () => {
  it("enumerates test and live", () => {
    expect(PAYMENT_MODES).toContain("test");
    expect(PAYMENT_MODES).toContain("live");
    expect(PAYMENT_MODES).toHaveLength(2);
  });
});

describe("validateMode", () => {
  it("accepts test and live", () => {
    expect(validateMode("test")).toBeNull();
    expect(validateMode("live")).toBeNull();
  });
  it("rejects unknown", () => {
    expect(validateMode("sandbox")).not.toBeNull();
    expect(validateMode("")).not.toBeNull();
  });
});

// ---------------------------------------------------------------------------
// PROVIDER_REQUIRED_SECRETS
// ---------------------------------------------------------------------------
describe("PROVIDER_REQUIRED_SECRETS", () => {
  it("stripe requires api_key and webhook_secret", () => {
    expect(PROVIDER_REQUIRED_SECRETS.stripe).toContain("api_key");
    expect(PROVIDER_REQUIRED_SECRETS.stripe).toContain("webhook_secret");
  });
  it("allpay requires merchant_id and secret_key", () => {
    expect(PROVIDER_REQUIRED_SECRETS.allpay).toContain("merchant_id");
    expect(PROVIDER_REQUIRED_SECRETS.allpay).toContain("secret_key");
  });
  it("cloudpayments requires public_id and api_secret", () => {
    expect(PROVIDER_REQUIRED_SECRETS.cloudpayments).toContain("public_id");
    expect(PROVIDER_REQUIRED_SECRETS.cloudpayments).toContain("api_secret");
  });
  it("yookassa requires shop_id and secret_key", () => {
    expect(PROVIDER_REQUIRED_SECRETS.yookassa).toContain("shop_id");
    expect(PROVIDER_REQUIRED_SECRETS.yookassa).toContain("secret_key");
  });
  it("manual has no required secrets", () => {
    expect(PROVIDER_REQUIRED_SECRETS.manual).toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------
// validatePublicConfigJSON
// ---------------------------------------------------------------------------
describe("validatePublicConfigJSON", () => {
  it("allows empty string (optional)", () => {
    expect(validatePublicConfigJSON("")).toBeNull();
  });
  it("accepts a valid JSON object", () => {
    expect(validatePublicConfigJSON('{"webhook_url": "https://x.com"}')).toBeNull();
  });
  it("rejects arrays", () => {
    expect(validatePublicConfigJSON("[1,2,3]")).not.toBeNull();
  });
  it("rejects scalar number", () => {
    expect(validatePublicConfigJSON("42")).not.toBeNull();
  });
  it("rejects invalid JSON", () => {
    expect(validatePublicConfigJSON("{bad")).not.toBeNull();
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

  it("maps payment_config.invalid_provider to provider field", () => {
    const err = makeError("payment_config.invalid_provider", "bad provider");
    expect(mapServerError(err).provider).toBe("bad provider");
  });

  it("maps payment_config.unsupported_provider to provider field", () => {
    const err = makeError("payment_config.unsupported_provider", "not supported");
    expect(mapServerError(err).provider).toBe("not supported");
  });

  it("maps payment_config.invalid_mode to mode field", () => {
    const err = makeError("payment_config.invalid_mode", "bad mode");
    expect(mapServerError(err).mode).toBe("bad mode");
  });

  it("maps payment_config.invalid_public_config to public_config field", () => {
    const err = makeError("payment_config.invalid_public_config", "bad config");
    expect(mapServerError(err).public_config).toBe("bad config");
  });

  it("maps payment_config.invalid_secrets to secrets field", () => {
    const err = makeError("payment_config.invalid_secrets", "bad secrets");
    expect(mapServerError(err).secrets).toBe("bad secrets");
  });

  it("maps payment_config.duplicate to form-level error", () => {
    const err = makeError("payment_config.duplicate", "already exists");
    expect(mapServerError(err).form).toBe("already exists");
  });

  it("maps payment_config.not_found to form-level error", () => {
    const err = makeError("payment_config.not_found", "not found");
    expect(mapServerError(err).form).toBe("not found");
  });

  it("maps payment_config.empty_body / invalid_body to form-level error", () => {
    expect(typeof mapServerError(makeError("payment_config.empty_body", "empty")).form).toBe("string");
    expect(mapServerError(makeError("payment_config.invalid_body", "invalid body")).form).toBe("invalid body");
  });

  it("maps permissions.denied to a readable form-level message", () => {
    const err = makeError("permissions.denied");
    expect(mapServerError(err).form).toContain("payment_config.write");
  });

  it("maps unknown code with provider field detail to provider", () => {
    const err = makeError("unknown.code", "bad provider", { field: "provider" });
    expect(mapServerError(err).provider).toBe("bad provider");
  });

  it("maps unknown code with mode field to mode", () => {
    const err = makeError("unknown.code", "bad mode", { field: "mode" });
    expect(mapServerError(err).mode).toBe("bad mode");
  });

  it("maps unknown code with no field to form-level error", () => {
    const err = makeError("totally.unknown", "something broke");
    expect(mapServerError(err).form).toContain("something broke");
  });
});
