/**
 * The seating plan intake as the screen sees it (spec 64 sections 2.2 to 2.4):
 * which panel shows for a server state, which buttons a person gets, when the
 * screen asks the server again. Pure: no React, no fetch.
 *
 * The server has seven states. `received` and `generating` both mean "a
 * generator is on it" (wave 2 writes them), `operator` means an administrator
 * works on the plan. The person never decides these states, so the screen
 * only reads them.
 */
import { ApiError } from "@/lib/api/http";
import type { MessageKey } from "@/lib/i18n/en";

/** The query key of one intake (the intake page reads it, the review refreshes it). */
export const planIntakeKey = (orgId: string, intakeId: string) => ["venuePlan", "intake", orgId, intakeId] as const;

/**
 * The server says the intake moved on (another state, or a newer draft) than the screen shows.
 * The screen must show the error AND read the intake again.
 */
export function isStaleIntakeError(error: unknown): boolean {
  return error instanceof ApiError && (error.code === "venue_plan_intake.wrong_state" || error.code === "venue_plan_intake.draft_stale");
}

export type IntakeState = "received" | "generating" | "review" | "operator" | "approved" | "rejected" | "cancelled";

const STATES: readonly IntakeState[] = ["received", "generating", "review", "operator", "approved", "rejected", "cancelled"];

export function parseState(state: string): IntakeState | null {
  return (STATES as readonly string[]).includes(state) ? (state as IntakeState) : null;
}

/** approved, rejected and cancelled: nothing changes after them. */
export function isFinal(state: string): boolean {
  return state === "approved" || state === "rejected" || state === "cancelled";
}

/** An intake that is still going: the venue shows it as "running". */
export function isOpen(state: string): boolean {
  return parseState(state) !== null && !isFinal(state);
}

export type Panel = "waitingGenerator" | "waitingOperator" | "review" | "saved" | "rejected" | "cancelled" | "unknown";

export function panelOf(state: string): Panel {
  switch (parseState(state)) {
    case "received":
    case "generating":
      return "waitingGenerator";
    case "operator":
      return "waitingOperator";
    case "review":
      return "review";
    case "approved":
      return "saved";
    case "rejected":
      return "rejected";
    case "cancelled":
      return "cancelled";
    default:
      return "unknown";
  }
}

export interface Permissions {
  /** seating_plan.create: upload, correct, ask an administrator, cancel, approve. */
  create: boolean;
  /** seating_plan.read: view. */
  read: boolean;
}

export interface IntakeActions {
  view: boolean;
  /** "Correct": approve the current draft. */
  approve: boolean;
  /** "Fix it": send the written correction. */
  fix: boolean;
  /** "Ask an administrator". */
  escalate: boolean;
  /** Cancel the intake. */
  cancel: boolean;
  /** "Send another file": a new upload after a rejection or a cancel. */
  sendAnother: boolean;
  /** "Split seats by price" or "Create an event at this venue": after the plan is saved. */
  nextStep: boolean;
}

/** The buttons of a state for a person with these permissions. The server checks every one again. */
export function actionsOf(state: string, perms: Permissions): IntakeActions {
  const s = parseState(state);
  const open = s !== null && !isFinal(s);
  return {
    view: perms.read,
    approve: perms.create && s === "review",
    fix: perms.create && s === "review",
    escalate: perms.create && (s === "review" || s === "received" || s === "generating"),
    cancel: perms.create && open,
    sendAnother: perms.create && (s === "rejected" || s === "cancelled"),
    nextStep: perms.read && s === "approved",
  };
}

/** After this many drafts without approval the screen offers an administrator (spec 64 section 2.3). */
export const ADMIN_HINT_ROUNDS = 5;

export function suggestAdministrator(state: string, reviewRounds: number): boolean {
  return parseState(state) === "review" && reviewRounds >= ADMIN_HINT_ROUNDS;
}

const FAST_POLL_MS = 3_000;
const SLOW_POLL_MS = 30_000;
const MAX_POLL_MS = 60_000;

/**
 * How long to wait before reading the intake again, or false to stop (the value `refetchInterval` takes). A
 * generator answers in minutes (3 s), an administrator in hours (30 s). A
 * state that waits for the person (review) and the final states never poll.
 * Every failed read in a row doubles the wait, up to a minute.
 */
export function pollDelayMs(state: string | undefined, failures = 0): number | false {
  if (state === undefined) return false;
  const s = parseState(state);
  if (s === null || isFinal(s) || s === "review") return false;
  const base = s === "operator" ? SLOW_POLL_MS : FAST_POLL_MS;
  return Math.min(MAX_POLL_MS, base * 2 ** Math.min(Math.max(failures, 0), 5));
}

/** Why an intake waits for an administrator, as the person reads it. */
export function operatorWait(reason: string | null): "auto" | "request" {
  return reason === "organizer_request" || reason === "organizer_text" || reason === "answers" ? "request" : "auto";
}

/** The newest intake that is still going, from a list that is newest first. */
export function runningIntake<T extends { state: string }>(items: readonly T[]): T | null {
  return items.find((i) => isOpen(i.state)) ?? null;
}

export function stateKey(state: string): MessageKey {
  switch (parseState(state)) {
    case "received":
      return "venuePlan.state.received";
    case "generating":
      return "venuePlan.state.generating";
    case "review":
      return "venuePlan.state.review";
    case "operator":
      return "venuePlan.state.operator";
    case "approved":
      return "venuePlan.state.approved";
    case "rejected":
      return "venuePlan.state.rejected";
    case "cancelled":
      return "venuePlan.state.cancelled";
    default:
      return "venuePlan.state.unknown";
  }
}

/** How a source file is shown: its PNG copy, the file itself, or only a download. */
export type SourceView = "png_copy" | "file" | "download";

const SHOWN_AS_IS = ["jpeg", "png", "gif", "webp", "bmp", "svg"];

export function sourceViewOf(s: { kind: string; png_copy_path: string | null }): SourceView {
  if (s.png_copy_path !== null && s.png_copy_path !== "") return "png_copy";
  return SHOWN_AS_IS.includes(s.kind) ? "file" : "download";
}

// ─── The address: ?event=<id>&session=<id> ───────────────────────────────────

export interface IntakeSearch {
  event?: string;
  session?: string;
}

/** The page may be opened from a date: then the next step goes back to that date. */
export function parseIntakeSearch(raw: Record<string, unknown>): IntakeSearch {
  const out: IntakeSearch = {};
  if (typeof raw.event === "string" && raw.event !== "") out.event = raw.event;
  if (typeof raw.session === "string" && raw.session !== "") out.session = raw.session;
  return out;
}

export type NextStep = { kind: "split"; eventId: string; sessionId: string } | { kind: "createEvent" };

export function nextStepOf(search: IntakeSearch): NextStep {
  if (search.event !== undefined && search.session !== undefined) {
    return { kind: "split", eventId: search.event, sessionId: search.session };
  }
  return { kind: "createEvent" };
}

// ─── The texts a person writes ───────────────────────────────────────────────

export const FIX_TEXT_MAX = 2000;
export const ESCALATE_TEXT_MAX = 1000;

/** "Write what to fix": 1 to 2000 characters. */
export function fixTextProblem(text: string): "empty" | "tooLong" | null {
  const n = [...text.trim()].length;
  if (n === 0) return "empty";
  return n > FIX_TEXT_MAX ? "tooLong" : null;
}

/** The note to an administrator is optional: up to 1000 characters. */
export function escalateTextProblem(text: string): "tooLong" | null {
  return [...text.trim()].length > ESCALATE_TEXT_MAX ? "tooLong" : null;
}

/** The warning codes the importer gives, as message keys. An unknown code gets the general text. */
export function warningKey(code: string): MessageKey {
  return code === "legend_missing" ? "venuePlan.warning.legend_missing" : "venuePlan.warning.other";
}
