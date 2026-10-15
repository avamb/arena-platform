/**
 * 4-5: the rules of the team screen, kept apart from the component so they
 * can be tested without a browser. They mirror the bot's Team screen
 * (eventbot/team.go, team_invitations.go).
 */
import { ApiError } from "@/lib/api/http";
import type { BotTeamMember } from "@/lib/api/types";
import type { MessageKey } from "@/lib/i18n/en";

/** The three languages a letter can be written in. */
export const INVITE_LOCALES = ["en", "ru", "es"] as const;

/** The state a row shows: one word, never colour only. */
export type MemberState = "waiting" | "accepted" | "expired" | "telegram" | "added";

export function memberState(m: BotTeamMember): MemberState {
  if (m.invitation_state === "waiting" || m.invitation_state === "expired" || m.invitation_state === "accepted") {
    return m.invitation_state;
  }
  return m.telegram_linked ? "telegram" : "added";
}

export function memberStateKey(s: MemberState): MessageKey {
  return `team.state.${s}` as MessageKey;
}

/**
 * Whether a row offers "Send again" and "Revoke". Only a Telegram invitation
 * that is still waiting or has expired: a web invitation has no letter to
 * send again, and a lost link is the person's own "forgot password".
 */
export function isRevokable(m: BotTeamMember): boolean {
  return (
    m.invitation_id !== undefined &&
    m.invitation_channel === "telegram" &&
    (m.invitation_state === "waiting" || m.invitation_state === "expired")
  );
}

/**
 * Whether a row offers "Remove". Never your own row: the screen is only open
 * to a person who can manage the team, so the organization always keeps at
 * least the owner who is looking at it (the server has no last-owner rule on
 * this route, so the screen is the guard).
 */
export function canRemoveRow(m: BotTeamMember, selfUserId: string | undefined): boolean {
  return selfUserId !== undefined && m.user_id !== selfUserId;
}

/** Whole minutes to wait, at least 1, from the 429 of a resend (`retry_after_seconds` in the details). */
export function retryMinutes(err: unknown): number {
  if (err instanceof ApiError) {
    const secs = err.details?.retry_after_seconds;
    if (typeof secs === "number" && Number.isFinite(secs) && secs > 0) return Math.max(1, Math.ceil(secs / 60));
  }
  return 10;
}

/** The e-mail shape the server accepts, checked before a request is sent. */
export function looksLikeEmail(raw: string): boolean {
  const v = raw.trim();
  return v.length <= 254 && /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(v);
}

/** The localized word for the channel of an invitation. */
export function channelKey(channel: string | undefined): MessageKey | null {
  if (channel === "web") return "team.channel.web";
  if (channel === "telegram") return "team.channel.telegram";
  return null;
}
