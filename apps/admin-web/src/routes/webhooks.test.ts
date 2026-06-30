import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/client";
import {
  KNOWN_EVENT_TYPES,
  arrayEq,
  formatDateTime,
  mapCreateServerError,
  validateCallbackUrl,
  validateSiteUrl,
} from "./webhooks";

/**
 * Feature #294 — S-3 Webhook subscribers admin UI.
 *
 * These tests exercise the pure helpers exported from webhooks.tsx
 * (validators, server-error mapper, deep-equality helper) without
 * mounting the React tree. The mounted behaviour is exercised by the
 * existing TanStack Router smoke tests once /webhooks is added to the
 * route tree.
 */
describe("Webhook subscribers UI helpers", () => {
  it("publishes the known event_type catalog", () => {
    expect(KNOWN_EVENT_TYPES).toContain("order_paid");
    expect(KNOWN_EVENT_TYPES).toContain("ticket_issued");
    expect(KNOWN_EVENT_TYPES).toContain("refund_succeeded");
    expect(KNOWN_EVENT_TYPES).toContain("v1.ticket.refunded");
    expect(KNOWN_EVENT_TYPES).toContain("v1.ticket.revoked");
    expect(KNOWN_EVENT_TYPES).toContain("v1.ticket.scanned");
    expect(KNOWN_EVENT_TYPES).toContain("v1.session.cancelled");
  });

  it("validates callback URLs (required, https/http only)", () => {
    expect(validateCallbackUrl("")).not.toBeNull();
    expect(validateCallbackUrl("   ")).not.toBeNull();
    expect(validateCallbackUrl("not-a-url")).not.toBeNull();
    expect(validateCallbackUrl("ftp://example.com")).not.toBeNull();
    expect(validateCallbackUrl("https://wp.example.com/wp-json/arena-events/v1/webhook")).toBeNull();
    expect(validateCallbackUrl("http://localhost:8080/hook")).toBeNull();
  });

  it("site URL is optional but rejects invalid values when supplied", () => {
    expect(validateSiteUrl("")).toBeNull();
    expect(validateSiteUrl("   ")).toBeNull();
    expect(validateSiteUrl("not-a-url")).not.toBeNull();
    expect(validateSiteUrl("ftp://example.com")).not.toBeNull();
    expect(validateSiteUrl("https://example.com")).toBeNull();
  });

  it("maps backend error envelopes to UI fields", () => {
    const conflict = new ApiError(409, {
      code: "conflict",
      message: "callback_url already registered",
    });
    expect(mapCreateServerError(conflict).callbackUrl).toMatch(/already exists/);

    const denied = new ApiError(403, {
      code: "permissions.denied",
      message: "missing",
    });
    expect(mapCreateServerError(denied).form).toMatch(/webhook\.subscriber\.manage/);

    const valErr = new ApiError(400, {
      code: "validation_error",
      message: "callback_url is required",
    });
    expect(mapCreateServerError(valErr).callbackUrl).toBe("callback_url is required");

    const unavail = new ApiError(503, {
      code: "service_unavailable",
      message: "service not available",
    });
    expect(mapCreateServerError(unavail).form).toMatch(/Retry shortly/);

    const unknown = new ApiError(500, {
      code: "internal_error",
      message: "kaboom",
    });
    expect(mapCreateServerError(unknown).form).toBe("kaboom (internal_error)");
  });

  it("shallow array equality recognises identical and mutated lists", () => {
    expect(arrayEq([], [])).toBe(true);
    expect(arrayEq(["a", "b"], ["a", "b"])).toBe(true);
    expect(arrayEq(["a", "b"], ["b", "a"])).toBe(false);
    expect(arrayEq(["a"], ["a", "b"])).toBe(false);
  });

  it("formats ISO timestamps to a compact YYYY-MM-DD HH:MM Z form", () => {
    expect(formatDateTime("2026-06-30T12:34:56Z")).toBe("2026-06-30 12:34Z");
    expect(formatDateTime("not-a-date")).toBe("not-a-date");
  });
});
