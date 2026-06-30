import { describe, expect, it } from "vitest";
import { mobileFormStyles } from "./login";
import { __test as resetTest } from "./passwordReset";
import { __test as inviteTest } from "./acceptInvite";

/**
 * Feature #296 — M-3: Auth & onboarding flows on mobile.
 *
 * These tests assert the shared mobile-fitness contract for
 * /login, /password-reset and /accept-invite (Wave M-8 gate):
 *   - touch-target minimum is >= 44 CSS px,
 *   - font-size >= 16 (prevents iOS auto-zoom on focus),
 *   - layout fits 360 CSS px width (width 100%, maxWidth 360,
 *     box-sizing: border-box, no fixed pixel widths above the
 *     viewport),
 *   - error alert is position: sticky top: 0 so it cannot be
 *     hidden by the on-screen keyboard,
 *   - the zod schemas exported from password-reset / accept-invite
 *     enforce the documented length bounds.
 */
describe("Wave M-3 — auth mobile fitness contract", () => {
  it("login mobile styles meet the 44 px / 16 fontSize / 360 px contract", () => {
    const { pageStyle, inputStyle, submitStyle, alertStyle, linkStyle } = mobileFormStyles;
    // 360 px-safe layout
    expect(pageStyle.width).toBe("100%");
    expect(pageStyle.maxWidth).toBe(360);
    expect(pageStyle.boxSizing).toBe("border-box");
    // Touch targets >= 44 CSS px
    expect(inputStyle.minHeight).toBeGreaterThanOrEqual(44);
    expect(submitStyle.minHeight).toBeGreaterThanOrEqual(44);
    expect(linkStyle.minHeight).toBeGreaterThanOrEqual(44);
    // Input fontSize >= 16 prevents iOS auto-zoom
    expect(inputStyle.fontSize).toBeGreaterThanOrEqual(16);
    expect(submitStyle.fontSize).toBeGreaterThanOrEqual(16);
    // Input is full width and box-sizing: border-box so it cannot
    // overflow the 360 px viewport
    expect(inputStyle.width).toBe("100%");
    expect(inputStyle.boxSizing).toBe("border-box");
    expect(submitStyle.width).toBe("100%");
    // Error toast sticks to the top so the on-screen keyboard can
    // never hide it
    expect(alertStyle.position).toBe("sticky");
    expect(alertStyle.top).toBe(0);
    expect(alertStyle.width).toBe("100%");
    expect(alertStyle.boxSizing).toBe("border-box");
  });

  it("password-reset request schema requires a valid email", () => {
    expect(resetTest.requestSchema.safeParse({ email: "" }).success).toBe(false);
    expect(resetTest.requestSchema.safeParse({ email: "not-an-email" }).success).toBe(false);
    const ok = resetTest.requestSchema.safeParse({ email: "Op@Example.com" });
    expect(ok.success).toBe(true);
    if (ok.success) {
      // Email is normalised: trimmed + lower-cased
      expect(ok.data.email).toBe("op@example.com");
    }
  });

  it("password-reset confirm schema enforces 8..72 character bounds", () => {
    expect(resetTest.confirmSchema.safeParse({ password: "short" }).success).toBe(false);
    expect(resetTest.confirmSchema.safeParse({ password: "x".repeat(73) }).success).toBe(false);
    expect(resetTest.confirmSchema.safeParse({ password: "x".repeat(8) }).success).toBe(true);
    expect(resetTest.confirmSchema.safeParse({ password: "x".repeat(72) }).success).toBe(true);
  });

  it("accept-invite schema enforces 8..72 character bounds", () => {
    expect(inviteTest.inviteSchema.safeParse({ password: "" }).success).toBe(false);
    expect(inviteTest.inviteSchema.safeParse({ password: "1234567" }).success).toBe(false);
    expect(inviteTest.inviteSchema.safeParse({ password: "1".repeat(73) }).success).toBe(false);
    expect(inviteTest.inviteSchema.safeParse({ password: "valid-password-1" }).success).toBe(true);
  });
});
