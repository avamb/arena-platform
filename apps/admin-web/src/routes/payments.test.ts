/**
 * Unit tests for the Payment Provider Configs CRUD module (feature #412 / AB-26).
 *
 * Covers:
 *  - Org-ID / provider / mode validators that mirror the backend contract
 *  - Public-config JSON validator
 *  - PROVIDER_REQUIRED_SECRETS catalogue (mirrors requiredSecretFields in Go)
 *  - Server-error mapper (all error codes the backend can emit)
 *  - Per-config Stripe webhook URL + the four checkout.session.* events the
 *    organizer must subscribe that endpoint to
 */
import { describe, expect, it } from "vitest";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
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
  buildStripeWebhookUrl,
  buildStripeConfigWebhookUrl,
  STRIPE_WEBHOOK_EVENTS,
  STRIPE_CHECKOUT_SESSION_EVENTS,
  StripeWebhookConfigUrlsView,
  STRIPE_API_KEY_PERMISSIONS,
  StripeApiKeyPermissionsView,
  normalizeVerificationStatus,
  PaymentConfigFormDialog,
  type PaymentConfig,
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

// ---------------------------------------------------------------------------
// buildStripeWebhookUrl (AB-34)
// ---------------------------------------------------------------------------
describe("buildStripeWebhookUrl", () => {
  it("appends the webhook path to a plain base URL", () => {
    const url = buildStripeWebhookUrl("https://api.arenasoldout.com");
    expect(url).toBe("https://api.arenasoldout.com/v1/payment-intents/webhook");
  });

  it("strips trailing slashes from the base before appending", () => {
    const url = buildStripeWebhookUrl("https://api.arenasoldout.com/");
    expect(url).toBe("https://api.arenasoldout.com/v1/payment-intents/webhook");
  });

  it("handles multiple trailing slashes", () => {
    const url = buildStripeWebhookUrl("https://api.example.com//");
    expect(url).toBe("https://api.example.com/v1/payment-intents/webhook");
  });

  it("works with a localhost development base", () => {
    const url = buildStripeWebhookUrl("http://localhost:18080");
    expect(url).toBe("http://localhost:18080/v1/payment-intents/webhook");
  });

  it("works with a path-prefixed base URL", () => {
    const url = buildStripeWebhookUrl("https://api.example.com/arena");
    expect(url).toBe("https://api.example.com/arena/v1/payment-intents/webhook");
  });
});

// ---------------------------------------------------------------------------
// STRIPE_WEBHOOK_EVENTS (AB-34)
// ---------------------------------------------------------------------------
describe("STRIPE_WEBHOOK_EVENTS", () => {
  it("includes all events the Go handler consumes", () => {
    expect(STRIPE_WEBHOOK_EVENTS).toContain("payment_intent.succeeded");
    expect(STRIPE_WEBHOOK_EVENTS).toContain("payment_intent.payment_failed");
    expect(STRIPE_WEBHOOK_EVENTS).toContain("payment_intent.requires_action");
    expect(STRIPE_WEBHOOK_EVENTS).toContain("payment_intent.processing");
    expect(STRIPE_WEBHOOK_EVENTS).toContain("payment_intent.amount_capturable");
    expect(STRIPE_WEBHOOK_EVENTS).toContain("payment_intent.manual_review");
  });

  it("includes the hosted Checkout Session events", () => {
    // The widget's Stripe-hosted redirect flow reports the payment through
    // checkout.session.* rather than payment_intent.* alone.
    expect(STRIPE_WEBHOOK_EVENTS).toContain("checkout.session.completed");
    expect(STRIPE_WEBHOOK_EVENTS).toContain("checkout.session.expired");
    expect(STRIPE_WEBHOOK_EVENTS).toContain(
      "checkout.session.async_payment_succeeded",
    );
    expect(STRIPE_WEBHOOK_EVENTS).toContain(
      "checkout.session.async_payment_failed",
    );
  });

  it("does not include mock provider aliases", () => {
    // Mock aliases are test-only shorthands that must not be registered in Stripe
    for (const evt of STRIPE_WEBHOOK_EVENTS) {
      expect(evt).not.toMatch(/^mock\./);
    }
  });

  it("only includes payment_intent.* / checkout.session.* events (Stripe naming convention)", () => {
    for (const evt of STRIPE_WEBHOOK_EVENTS) {
      expect(evt).toMatch(/^(payment_intent|checkout\.session)\./);
    }
  });
});

// ---------------------------------------------------------------------------
// Per-config webhook endpoint: POST /v1/payment-intents/webhook/{config_id}
// ---------------------------------------------------------------------------
describe("buildStripeConfigWebhookUrl", () => {
  const CONFIG_ID = "550e8400-e29b-41d4-a716-446655440000";

  it("appends the config id to the webhook path", () => {
    expect(
      buildStripeConfigWebhookUrl("https://api.arenasoldout.com", CONFIG_ID),
    ).toBe(
      `https://api.arenasoldout.com/v1/payment-intents/webhook/${CONFIG_ID}`,
    );
  });

  it("strips trailing slashes from the base before appending", () => {
    expect(
      buildStripeConfigWebhookUrl("https://api.example.com//", CONFIG_ID),
    ).toBe(`https://api.example.com/v1/payment-intents/webhook/${CONFIG_ID}`);
  });

  it("works with a path-prefixed base URL", () => {
    expect(
      buildStripeConfigWebhookUrl("https://api.example.com/arena", CONFIG_ID),
    ).toBe(
      `https://api.example.com/arena/v1/payment-intents/webhook/${CONFIG_ID}`,
    );
  });

  it("returns null for a config that has no id yet (never a partial URL)", () => {
    expect(buildStripeConfigWebhookUrl("https://api.example.com", "")).toBeNull();
    expect(
      buildStripeConfigWebhookUrl("https://api.example.com", "   "),
    ).toBeNull();
  });
});

describe("STRIPE_CHECKOUT_SESSION_EVENTS", () => {
  it("is exactly the four hosted-Checkout events the per-config endpoint needs", () => {
    expect([...STRIPE_CHECKOUT_SESSION_EVENTS]).toEqual([
      "checkout.session.completed",
      "checkout.session.expired",
      "checkout.session.async_payment_succeeded",
      "checkout.session.async_payment_failed",
    ]);
  });

  it("names no payment_intent.* event (those belong to the shared endpoint)", () => {
    for (const evt of STRIPE_CHECKOUT_SESSION_EVENTS) {
      expect(evt).toMatch(/^checkout\.session\./);
    }
  });

  it("is a subset of the events the handler understands", () => {
    for (const evt of STRIPE_CHECKOUT_SESSION_EVENTS) {
      expect(STRIPE_WEBHOOK_EVENTS).toContain(evt);
    }
  });
});

// ---------------------------------------------------------------------------
// StripeWebhookConfigUrlsView — rendered with renderToStaticMarkup because the
// admin-web Vitest environment is Node-only (venueSeatingPlans precedent).
// ---------------------------------------------------------------------------
describe("StripeWebhookConfigUrlsView", () => {
  const API_BASE = "https://api.arenasoldout.com";
  const SAVED_ID = "550e8400-e29b-41d4-a716-446655440000";

  function makeConfig(overrides: Partial<PaymentConfig> = {}): PaymentConfig {
    return {
      id: SAVED_ID,
      org_id: "11111111-2222-3333-4444-555555555555",
      provider: "stripe",
      mode: "live",
      provider_account_id: null,
      public_config: null,
      secret_fields_set: ["api_key", "webhook_secret"],
      status: "configured",
      missing_required_fields: [],
      is_active: true,
      created_at: "2026-09-01T00:00:00Z",
      updated_at: "2026-09-01T00:00:00Z",
      ...overrides,
    };
  }

  function render(stripeConfigs: readonly PaymentConfig[]): string {
    return renderToStaticMarkup(
      createElement(StripeWebhookConfigUrlsView, {
        apiBaseUrl: API_BASE,
        stripeConfigs,
      }),
    );
  }

  it("renders the saved config's full per-config webhook URL", () => {
    const html = render([makeConfig()]);
    expect(html).toContain(
      `${API_BASE}/v1/payment-intents/webhook/${SAVED_ID}`,
    );
  });

  it("names all four checkout.session.* events as the required set", () => {
    const html = render([makeConfig()]);
    for (const evt of STRIPE_CHECKOUT_SESSION_EVENTS) {
      expect(html).toContain(evt);
    }
    expect(html).toContain("only");
  });

  it("does not name a payment_intent.* event in the per-config instructions", () => {
    expect(render([makeConfig()])).not.toContain("payment_intent.");
  });

  it("shows a hint instead of a URL for a config that is not saved yet", () => {
    const html = render([makeConfig({ id: "" })]);
    expect(html).toContain("stripe-webhook-config-url-unsaved");
    expect(html).toContain("has not been saved yet");
    // No partial / broken URL is rendered at all.
    expect(html).not.toContain("/v1/payment-intents/webhook");
  });

  it("shows the save-first hint when the organization has no Stripe config", () => {
    const html = render([]);
    expect(html).toContain("stripe-webhook-config-url-none");
    expect(html).toContain("once it is saved");
    expect(html).not.toContain("/v1/payment-intents/webhook/");
  });

  it("renders one URL per saved Stripe config (test and live)", () => {
    const other = "660e8400-e29b-41d4-a716-446655440001";
    const html = render([
      makeConfig({ mode: "test" }),
      makeConfig({ id: other, mode: "live" }),
    ]);
    expect(html).toContain(`/v1/payment-intents/webhook/${SAVED_ID}`);
    expect(html).toContain(`/v1/payment-intents/webhook/${other}`);
  });
});

// ---------------------------------------------------------------------------
// Verification state (2026-09-21)
// ---------------------------------------------------------------------------
//
// A live organization's Stripe config read "configured" all day while its
// api_key held a string that was not a key: every sale died with a 503 on
// the pay button and this screen said nothing. `status` is a shape check on
// the form; only `verification_status` reports what the provider said.

describe("normalizeVerificationStatus", () => {
  it("passes through the two states the provider actually answered", () => {
    expect(normalizeVerificationStatus("ok")).toBe("ok");
    expect(normalizeVerificationStatus("failed")).toBe("failed");
  });

  it("treats anything it does not recognise as unverified", () => {
    // An older backend omits the field; a newer one may grow a state this
    // build has never heard of. Neither may fall through to the green light.
    expect(normalizeVerificationStatus(undefined)).toBe("unverified");
    expect(normalizeVerificationStatus(null)).toBe("unverified");
    expect(normalizeVerificationStatus("")).toBe("unverified");
    expect(normalizeVerificationStatus("unverified")).toBe("unverified");
    expect(normalizeVerificationStatus("pending_review")).toBe("unverified");
    expect(normalizeVerificationStatus("OK")).toBe("unverified");
    expect(normalizeVerificationStatus("configured")).toBe("unverified");
  });
});

// ---------------------------------------------------------------------------
// Stored secrets are locked against a password manager (2026-09-21)
// ---------------------------------------------------------------------------
//
// The dialog reads as a login form to Chrome — an e-mail in "Provider account
// ID", a type="password" input directly beneath it — so it filled api_key
// with a saved password. An autofill dispatches a real, trusted input event,
// so watching for changes cannot tell it from typing: the field has to be
// read-only until the operator asks to replace it.

describe("PaymentConfigFormDialog secrets", () => {
  const ORG = "11111111-2222-3333-4444-555555555555";

  function storedConfig(): PaymentConfig {
    return {
      id: "550e8400-e29b-41d4-a716-446655440000",
      org_id: ORG,
      provider: "stripe",
      mode: "test",
      provider_account_id: "andreev@example.com",
      public_config: {},
      secret_fields_set: ["api_key", "webhook_secret"],
      status: "configured",
      missing_required_fields: [],
      is_active: true,
      created_at: "2026-09-21T00:00:00Z",
      updated_at: "2026-09-21T00:00:00Z",
      verification_status: "ok",
      verified_at: "2026-09-21T12:00:00Z",
      verification_error: null,
    };
  }

  function renderDialog(mode: Parameters<typeof PaymentConfigFormDialog>[0]["mode"]) {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    return renderToStaticMarkup(
      createElement(
        QueryClientProvider,
        { client },
        createElement(PaymentConfigFormDialog, {
          mode,
          orgID: ORG,
          onClose: () => {},
        }),
      ),
    );
  }

  it("renders at all", () => {
    // Guards a real defect caught before release: `dirty` called
    // isSecretLocked, which reads a const declared further down, so every
    // render threw a temporal-dead-zone ReferenceError. tsc does not see it.
    expect(() => renderDialog({ kind: "edit", config: storedConfig() })).not.toThrow();
    expect(() => renderDialog({ kind: "create" })).not.toThrow();
  });

  it("locks an already-stored secret and offers Replace", () => {
    const html = renderDialog({ kind: "edit", config: storedConfig() });
    expect(html).toContain('data-testid="payments-form-replace-api_key"');
    expect(html).toContain("press Replace to change");
    // readOnly is what makes the field not an autofill target in the first
    // place; React serialises it as the `readonly` attribute.
    const apiKeyInput = html.slice(html.indexOf('id="payment-secret-api_key"'));
    expect(apiKeyInput.slice(0, apiKeyInput.indexOf(">"))).toContain("readonly");
  });

  it("asks the browser not to treat secret fields as a login", () => {
    const html = renderDialog({ kind: "edit", config: storedConfig() });
    // Matched case-insensitively: react-dom/server emits the React prop
    // spelling (autoComplete), and HTML attribute names are case-insensitive
    // so the browser reads it either way.
    const lower = html.toLowerCase();
    // "new-password" rather than "off": Chrome ignores "off" on a form it
    // has decided is a login.
    expect(lower).toContain('autocomplete="new-password"');
    expect(lower).toContain('autocomplete="off"');
  });

  it("leaves a not-yet-stored secret editable on create", () => {
    const html = renderDialog({ kind: "create" });
    expect(html).not.toContain('data-testid="payments-form-replace-api_key"');
    const apiKeyInput = html.slice(html.indexOf('id="payment-secret-api_key"'));
    expect(apiKeyInput.slice(0, apiKeyInput.indexOf(">"))).not.toContain("readonly");
  });

  // The whole point of the hint is that it sits where the key is pasted.
  // Defining the component but forgetting to mount it would leave the
  // operator asking the same question again.
  it("offers the API-key hint beside the secret fields of a Stripe config", () => {
    const html = renderDialog({ kind: "create" });
    expect(html).toContain('data-testid="stripe-api-key-help-panel"');
  });
});

// ---------------------------------------------------------------------------
// StripeApiKeyPermissionsView — the answer to "what do I tick in Stripe?",
// kept in the admin so nobody has to ask.
// ---------------------------------------------------------------------------
describe("StripeApiKeyPermissionsView", () => {
  function render(): string {
    return renderToStaticMarkup(createElement(StripeApiKeyPermissionsView));
  }

  it("names every permission with its access level and endpoint", () => {
    const html = render();
    for (const p of STRIPE_API_KEY_PERMISSIONS) {
      expect(html).toContain(`${p.resource} — ${p.access}`);
      expect(html).toContain(p.endpoint);
    }
  });

  // These two are what a restricted key must carry TODAY: one creates the
  // hosted page, the other is what the Connection check calls. A key
  // missing Balance sells fine and still shows red, which is exactly the
  // confusion this list exists to prevent.
  it("marks the two permissions the current flow depends on as required", () => {
    const required = STRIPE_API_KEY_PERMISSIONS.filter(
      (p) => p.need === "required",
    );
    expect(required.map((p) => p.resource).sort()).toEqual([
      "Balance",
      "Checkout Sessions",
    ]);
    expect(
      required.find((p) => p.resource === "Checkout Sessions")?.endpoint,
    ).toBe("POST /v1/checkout/sessions");
    expect(required.find((p) => p.resource === "Balance")?.endpoint).toBe(
      "GET /v1/balance",
    );
  });

  it("says plainly that the rest can stay off, and that mode must match", () => {
    const html = render();
    expect(html).toContain("can stay");
    expect(html).toContain("None");
    expect(html).toContain("mode has to match");
    // The key's scope and the signing secret are unrelated; conflating them
    // is how an operator ends up granting Webhook Endpoints access.
    expect(html).toContain("webhook_secret");
  });
});
