/**
 * Seat hold settings, the superadmin side (spec 08_architecture/67, section 7.2).
 *
 *   GET /v1/admin/hold-policy                              both profiles
 *   PUT /v1/admin/hold-policy/{profile}                    all fields of one profile
 *   PUT /v1/admin/events/{event_id}/hold-profile           {profile: "standard" | "high_demand" | null}
 *
 * Times travel in seconds (the poll interval in milliseconds); the screen shows minutes (the poll interval
 * in seconds). Every write carries X-Admin-Reason, typed in the screen's own field.
 *
 * TODO spec 67: these calls are written by hand because openapi.yaml does not describe the routes yet;
 * switch to the generated client when it does.
 *
 * Mock data: NONE.
 */
import { authedFetch } from "@/lib/api/client";

export type HoldProfileName = "standard" | "high_demand";
export const HOLD_PROFILE_NAMES: readonly HoldProfileName[] = ["standard", "high_demand"];

/** One profile as the server sends it and accepts it (without the last three fields). */
export interface HoldProfile {
  profile: string;
  select_seconds: number;
  payment_seconds: number;
  extend_seconds: number;
  extend_count: number;
  paying_seconds: number;
  max_seats_per_hold: number;
  max_active_per_device: number;
  max_active_per_ip: number;
  rehold_pause_seconds: number;
  poll_interval_ms: number;
  updated_at?: string;
  updated_by?: string | null;
}

export type HoldProfileRequest = Omit<HoldProfile, "profile" | "updated_at" | "updated_by">;

export interface HoldPolicyResponse {
  profiles: HoldProfile[];
}

const enc = encodeURIComponent;

export function getHoldPolicy(): Promise<HoldPolicyResponse> {
  return authedFetch({ method: "GET", path: "/v1/admin/hold-policy" });
}

export function putHoldPolicy(profile: string, body: HoldProfileRequest, reason: string): Promise<HoldProfile> {
  return authedFetch({ method: "PUT", path: `/v1/admin/hold-policy/${enc(profile)}`, body, adminReason: reason });
}

export function putEventHoldProfile(
  eventId: string,
  profile: HoldProfileName | null,
  reason: string,
): Promise<{ event_id: string; profile: HoldProfileName | null }> {
  return authedFetch({
    method: "PUT",
    path: `/v1/admin/events/${enc(eventId)}/hold-profile`,
    body: { profile },
    adminReason: reason,
  });
}

// ---------------------------------------------------------------------------
// The form: minutes on screen, seconds on the wire
// ---------------------------------------------------------------------------

/** What the person types: every field is text. Times are minutes, the poll interval is seconds. */
export interface HoldForm {
  select_minutes: string;
  payment_minutes: string;
  extend_minutes: string;
  extend_count: string;
  paying_minutes: string;
  max_seats_per_hold: string;
  max_active_per_device: string;
  max_active_per_ip: string;
  rehold_pause_minutes: string;
  poll_interval_seconds: string;
}

/** A number as short text: 600 s is "10", 150 s is "2.5". */
function trimNumber(n: number): string {
  return String(Math.round(n * 1000) / 1000);
}

export function secondsToMinutes(seconds: number): string {
  return trimNumber(seconds / 60);
}

export function millisToSeconds(ms: number): string {
  return trimNumber(ms / 1000);
}

export function formFromProfile(p: HoldProfileRequest): HoldForm {
  return {
    select_minutes: secondsToMinutes(p.select_seconds),
    payment_minutes: secondsToMinutes(p.payment_seconds),
    extend_minutes: secondsToMinutes(p.extend_seconds),
    extend_count: String(p.extend_count),
    paying_minutes: secondsToMinutes(p.paying_seconds),
    max_seats_per_hold: String(p.max_seats_per_hold),
    max_active_per_device: String(p.max_active_per_device),
    max_active_per_ip: String(p.max_active_per_ip),
    rehold_pause_minutes: secondsToMinutes(p.rehold_pause_seconds),
    poll_interval_seconds: millisToSeconds(p.poll_interval_ms),
  };
}

/** Parse a non-negative decimal ("2.5", "2,5"); null when it is not one. */
function parseNumber(raw: string): number | null {
  const s = raw.trim().replace(",", ".");
  if (!/^\d+(\.\d+)?$/.test(s)) return null;
  const n = Number(s);
  return Number.isFinite(n) ? n : null;
}

/** Field keys the checks name, so the screen can mark the field and say what is wrong. */
export type HoldField = keyof HoldForm | "reason";

export interface HoldFieldError {
  field: HoldField;
  /** Key of the screen's catalog (holdPolicyStrings.ts). */
  key: string;
  params?: Record<string, string | number>;
}

export type BuildResult =
  | { ok: true; request: HoldProfileRequest; reason: string }
  | { ok: false; errors: HoldFieldError[] };

interface Bound {
  min: number;
  max: number;
}

// The limits are the CHECKs of migration 0225, in the units of the form.
const MINUTE_LIMITS: Record<"select_minutes" | "payment_minutes", Bound> = {
  select_minutes: { min: 2, max: 30 },
  payment_minutes: { min: 5, max: 30 },
};

/**
 * Turn the form into the request: minutes become whole seconds, the poll interval becomes milliseconds.
 * The reason is required. A value that is not a number or is out of range is reported by field, nothing is
 * sent. The server checks the same limits again.
 */
export function buildHoldRequest(form: HoldForm, reasonRaw: string): BuildResult {
  const errors: HoldFieldError[] = [];
  const reason = reasonRaw.trim();
  if (reason === "") errors.push({ field: "reason", key: "holdPolicy.err.reason" });

  const minutes = (field: keyof HoldForm, bound?: Bound): number => {
    const n = parseNumber(form[field]);
    if (n === null) {
      errors.push({ field, key: "holdPolicy.err.number" });
      return 0;
    }
    const seconds = Math.round(n * 60);
    if (Math.abs(seconds - n * 60) > 1e-6) {
      errors.push({ field, key: "holdPolicy.err.wholeSeconds" });
    } else if (bound !== undefined && (n < bound.min || n > bound.max)) {
      errors.push({ field, key: "holdPolicy.err.range", params: { min: bound.min, max: bound.max } });
    }
    return seconds;
  };
  const count = (field: keyof HoldForm, min: number, max?: number): number => {
    const n = parseNumber(form[field]);
    if (n === null || !Number.isInteger(n)) {
      errors.push({ field, key: "holdPolicy.err.integer" });
      return 0;
    }
    if (n < min || (max !== undefined && n > max)) {
      errors.push({ field, key: "holdPolicy.err.range", params: { min, max: max ?? "∞" } });
    }
    return n;
  };

  const select_seconds = minutes("select_minutes", MINUTE_LIMITS.select_minutes);
  const payment_seconds = minutes("payment_minutes", MINUTE_LIMITS.payment_minutes);
  const extend_seconds = minutes("extend_minutes");
  const extend_count = count("extend_count", 0, 3);
  const paying_seconds = minutes("paying_minutes");
  const max_seats_per_hold = count("max_seats_per_hold", 1);
  const max_active_per_device = count("max_active_per_device", 1);
  const max_active_per_ip = count("max_active_per_ip", 1);
  const rehold_pause_seconds = minutes("rehold_pause_minutes");

  let poll_interval_ms = 0;
  const poll = parseNumber(form.poll_interval_seconds);
  if (poll === null) {
    errors.push({ field: "poll_interval_seconds", key: "holdPolicy.err.number" });
  } else {
    poll_interval_ms = Math.round(poll * 1000);
    if (Math.abs(poll_interval_ms - poll * 1000) > 1e-6) {
      errors.push({ field: "poll_interval_seconds", key: "holdPolicy.err.wholeMillis" });
    } else if (poll < 1 || poll > 10) {
      errors.push({ field: "poll_interval_seconds", key: "holdPolicy.err.range", params: { min: 1, max: 10 } });
    }
  }

  if (errors.length > 0) return { ok: false, errors };
  return {
    ok: true,
    reason,
    request: {
      select_seconds,
      payment_seconds,
      extend_seconds,
      extend_count,
      paying_seconds,
      max_seats_per_hold,
      max_active_per_device,
      max_active_per_ip,
      rehold_pause_seconds,
      poll_interval_ms,
    },
  };
}
