import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/http";
import type { BotTeamMember } from "@/lib/api/types";
import { canRemoveRow, isRevokable, looksLikeEmail, memberState, retryMinutes } from "@/lib/team";

const base: BotTeamMember = {
  user_id: "u1",
  email: "a@example.test",
  role: "manager",
  membership_role: "organizer",
  telegram_linked: false,
  invitation_pending: false,
  joined_at: "2026-10-01T10:00:00Z",
};

describe("memberState", () => {
  it("follows the invitation when there is one", () => {
    expect(memberState({ ...base, invitation_state: "waiting" })).toBe("waiting");
    expect(memberState({ ...base, invitation_state: "expired" })).toBe("expired");
    expect(memberState({ ...base, invitation_state: "accepted" })).toBe("accepted");
  });
  it("says telegram for a linked person without an invitation, else added", () => {
    expect(memberState({ ...base, telegram_linked: true })).toBe("telegram");
    expect(memberState(base)).toBe("added");
  });
});

describe("isRevokable", () => {
  const tg = { ...base, invitation_id: "i1", invitation_channel: "telegram" };
  it("is true for a waiting or expired Telegram invitation", () => {
    expect(isRevokable({ ...tg, invitation_state: "waiting" })).toBe(true);
    expect(isRevokable({ ...tg, invitation_state: "expired" })).toBe(true);
  });
  it("is false for an accepted one, a web one and a member with no invitation", () => {
    expect(isRevokable({ ...tg, invitation_state: "accepted" })).toBe(false);
    expect(isRevokable({ ...tg, invitation_channel: "web", invitation_state: "waiting" })).toBe(false);
    expect(isRevokable(base)).toBe(false);
  });
});

describe("canRemoveRow", () => {
  it("never offers your own row", () => {
    expect(canRemoveRow(base, "u1")).toBe(false);
    expect(canRemoveRow(base, "u2")).toBe(true);
    expect(canRemoveRow(base, undefined)).toBe(false);
  });
});

describe("retryMinutes", () => {
  it("rounds the seconds up to whole minutes, at least one", () => {
    expect(retryMinutes(new ApiError(429, "bot.invitation_resend_too_soon", "x", undefined, { retry_after_seconds: 599 }))).toBe(10);
    expect(retryMinutes(new ApiError(429, "bot.invitation_resend_too_soon", "x", undefined, { retry_after_seconds: 61 }))).toBe(2);
    expect(retryMinutes(new ApiError(429, "bot.invitation_resend_too_soon", "x", undefined, { retry_after_seconds: 3 }))).toBe(1);
  });
  it("falls back to the full interval", () => {
    expect(retryMinutes(new ApiError(429, "bot.invitation_resend_too_soon", "x"))).toBe(10);
    expect(retryMinutes(new Error("boom"))).toBe(10);
  });
});

describe("looksLikeEmail", () => {
  it("accepts an address and refuses text", () => {
    expect(looksLikeEmail(" a@b.co ")).toBe(true);
    expect(looksLikeEmail("a@b")).toBe(false);
    expect(looksLikeEmail("a b@c.de")).toBe(false);
    expect(looksLikeEmail("")).toBe(false);
  });
});
