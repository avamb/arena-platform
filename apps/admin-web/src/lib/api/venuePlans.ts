/**
 * Seating plans (spec 08_architecture/64, sections 3.3 and 3.4): the queue of plan
 * intakes an administrator works through, and the verification of seating plans.
 *
 *   GET  /v1/admin/plan-intakes?state=&org_id=&unclaimed=&limit=
 *   GET  /v1/admin/plan-intakes/{id}
 *   POST /v1/admin/plan-intakes/{id}/claim | drafts | return | approve | reject
 *   GET  /v1/admin/plan-intakes/{id}/sources/{source_id}   (X-Admin-Reason on a GET)
 *   GET  /v1/admin/plan-intakes/{id}/drafts/{n}/svg
 *   GET  /v1/admin/seating-plans?verification_state=&org_id=&country=&city_id=&q=&limit=&offset=
 *   POST /v1/admin/seating-plans/{id}/verify | needs-fix
 *
 * Every write and the source download carry X-Admin-Reason: reason.ts decides that
 * from the path, and the API client asks for it (ReasonPromptModal).
 *
 * Mock data: NONE. Everything here talks to the live backend.
 */
import type { components } from "@openapi/index";
import { config } from "@/lib/config";
import { ApiError, authedFetch, refresh } from "@/lib/api/client";
import { groupIssues } from "@/lib/admin/groupIssues";
import { clientChannelHeaders } from "@/lib/api/channel";
import {
  MISSING_REASON_CODE,
  clearActiveReason,
  getActiveReason,
  requiresAdminReason,
  resolveReasonFor,
} from "@/lib/api/reason";
import { clearSession, getAccessToken } from "@/lib/api/tokenStore";
import { requestPermissionRefresh } from "@/lib/auth/permissionRefresh";

type Schemas = components["schemas"];

export type VenuePlanIntake = Schemas["VenuePlanIntake"];
export type VenuePlanIntakeListItem = Schemas["VenuePlanIntakeListItem"];
export type VenuePlanDraft = Schemas["VenuePlanDraft"];
export type VenuePlanDraftHistoryItem = Schemas["VenuePlanDraftHistoryItem"];
export type VenuePlanIntakeSource = Schemas["VenuePlanIntakeSource"];
export type VenuePlanCorrection = Schemas["VenuePlanCorrection"];
export type SeatingPlanVerification = Schemas["SeatingPlanVerification"];
export type SeatingPlanSummary = Schemas["SeatingPlan"];

/** One row of GET /v1/admin/seating-plans (the schema is inline in openapi.yaml). */
export interface AdminSeatingPlanRow {
  readonly plan_id: string;
  readonly name: string;
  readonly plan_type: string;
  readonly visibility: string;
  readonly status: string;
  readonly verification_state?: string | null;
  readonly verification_note?: string | null;
  readonly owner_org_id: string;
  readonly owner_org_name: string;
  readonly venue_id: string;
  readonly venue_name: string;
  readonly city_id?: string | null;
  readonly city_name?: string | null;
  readonly country_iso2?: string | null;
  readonly current_version_id?: string | null;
  readonly version_number?: number | null;
  readonly capacity_seated: number;
  readonly capacity_standing: number;
  readonly created_at: string;
  readonly verified_at?: string | null;
}

// ---------------------------------------------------------------------------
// States
// ---------------------------------------------------------------------------

export const INTAKE_STATES = [
  "received",
  "generating",
  "review",
  "operator",
  "approved",
  "rejected",
  "cancelled",
] as const;
export type IntakeState = (typeof INTAKE_STATES)[number];

const STATE_LABELS: Readonly<Record<string, string>> = {
  received: "Received",
  generating: "Generating",
  review: "With the organizer",
  operator: "With an administrator",
  approved: "Approved",
  rejected: "Rejected",
  cancelled: "Cancelled",
};

export function intakeStateLabel(state: string): string {
  return STATE_LABELS[state] ?? state;
}

export function isFinalState(state: string): boolean {
  return state === "approved" || state === "rejected" || state === "cancelled";
}

export const VERIFICATION_STATES = ["unverified", "needs_fix", "verified"] as const;
export type VerificationState = (typeof VERIFICATION_STATES)[number];
/** The filter value for plans that have no state at all (imported from the old system). */
export const NO_STATE_FILTER = "none" as const;
/** The filter value for every state: the server gets no verification_state. */
export const ALL_STATES_FILTER = "all" as const;
export type VerificationFilter = VerificationState | typeof NO_STATE_FILTER | typeof ALL_STATES_FILTER;

/** The order of the status filter. */
export const FILTER_OPTIONS: readonly VerificationFilter[] = [ALL_STATES_FILTER, NO_STATE_FILTER, "unverified", "needs_fix", "verified"];

const VERIFICATION_LABELS: Readonly<Record<VerificationFilter, string>> = {
  all: "All",
  unverified: "Not verified",
  needs_fix: "Correction necessary",
  verified: "Verified",
  none: "No status",
};

export function verificationLabel(state: string | null | undefined): string {
  if (state === null || state === undefined || state === "") return VERIFICATION_LABELS.none;
  return VERIFICATION_LABELS[state as VerificationFilter] ?? state;
}

/** How many plans each state holds (counts of GET /v1/admin/seating-plans; every filter but the status applies). */
export interface VerificationCounts {
  readonly none: number;
  readonly unverified: number;
  readonly needs_fix: number;
  readonly verified: number;
}

/** "Not verified (3)"; without counts (not loaded yet) the bare label. "All" is the sum of the four. */
export function filterOptionLabel(f: VerificationFilter, counts: VerificationCounts | undefined): string {
  const base = VERIFICATION_LABELS[f];
  if (counts === undefined) return base;
  const n = f === ALL_STATES_FILTER ? counts.none + counts.unverified + counts.needs_fix + counts.verified : counts[f];
  return `${base} (${n})`;
}

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

/**
 * How long an intake has been waiting: "5 min", "3 h 20 min", "2 d 4 h". An
 * unreadable date is a dash; a date in the future counts as zero.
 */
export function waitingLabel(createdAt: string | null | undefined, now: number = Date.now()): string {
  if (createdAt === null || createdAt === undefined || createdAt === "") return "—";
  const t = new Date(createdAt).getTime();
  if (Number.isNaN(t)) return "—";
  const totalMin = Math.max(0, Math.floor((now - t) / 60_000));
  if (totalMin < 1) return "0 min";
  if (totalMin < 60) return `${totalMin} min`;
  const hours = Math.floor(totalMin / 60);
  if (hours < 24) {
    const min = totalMin % 60;
    return min === 0 ? `${hours} h` : `${hours} h ${min} min`;
  }
  const days = Math.floor(hours / 24);
  const restHours = hours % 24;
  return restHours === 0 ? `${days} d` : `${days} d ${restHours} h`;
}

export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return "—";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

export interface IntakeListFilter {
  readonly state: string;
  readonly unclaimed: boolean;
  readonly orgId?: string;
  readonly limit?: number;
}

export const INTAKE_PAGE_LIMIT = 100;

export function buildIntakeListPath(f: IntakeListFilter): string {
  const params = new URLSearchParams();
  if (f.state !== "") params.set("state", f.state);
  if (f.orgId !== undefined && f.orgId !== "") params.set("org_id", f.orgId);
  if (f.unclaimed) params.set("unclaimed", "true");
  params.set("limit", String(f.limit ?? INTAKE_PAGE_LIMIT));
  return `/v1/admin/plan-intakes?${params.toString()}`;
}

export const PLANS_PAGE_SIZE = 50;
export const PLANS_SEARCH_MAX = 100;

/** What the list of plans to verify shows. Every field is optional on the server. */
export interface PlanListFilter {
  readonly state: VerificationFilter;
  /** "" means every organization. */
  readonly orgId: string;
  /** ISO-2 code; "" means every country. */
  readonly country: string;
  /** "" means every city. */
  readonly cityId: string;
  /** A part of the venue name; "" means no search. */
  readonly q: string;
  readonly offset: number;
}

export const INITIAL_PLAN_FILTER: PlanListFilter = {
  state: ALL_STATES_FILTER,
  orgId: "",
  country: "",
  cityId: "",
  q: "",
  offset: 0,
};

/**
 * A change of any filter returns to the first page. A new country clears the city,
 * because a city belongs to one country. A change of the page alone is applyPage.
 */
export function applyFilterChange(
  prev: PlanListFilter,
  patch: Partial<Omit<PlanListFilter, "offset">>,
): PlanListFilter {
  const next: PlanListFilter = { ...prev, ...patch, offset: 0 };
  if (patch.country !== undefined && patch.country !== prev.country && patch.cityId === undefined) {
    return { ...next, cityId: "" };
  }
  return next;
}

export function applyPage(prev: PlanListFilter, offset: number): PlanListFilter {
  return { ...prev, offset: Math.max(0, offset) };
}

export function buildVerificationListPath(f: PlanListFilter, limit: number = PLANS_PAGE_SIZE): string {
  const params = new URLSearchParams();
  if (f.state !== ALL_STATES_FILTER) params.set("verification_state", f.state);
  if (f.orgId !== "") params.set("org_id", f.orgId);
  if (f.country !== "") params.set("country", f.country.toUpperCase());
  if (f.cityId !== "") params.set("city_id", f.cityId);
  const q = f.q.trim().slice(0, PLANS_SEARCH_MAX);
  if (q !== "") params.set("q", q);
  params.set("lang", "en");
  params.set("limit", String(limit));
  params.set("offset", String(f.offset));
  return `/v1/admin/seating-plans?${params.toString()}`;
}

export type IntakeAction = "claim" | "upload_draft" | "return" | "approve" | "reject";

export interface ActionGate {
  readonly enabled: boolean;
  /** Why the button is off; empty when it is on. */
  readonly why: string;
}

export function holdsClaim(
  intake: Pick<VenuePlanIntake, "operator_claimed" | "operator_id">,
  callerId: string | null,
): boolean {
  return (
    intake.operator_claimed === true &&
    callerId !== null &&
    intake.operator_id !== null &&
    intake.operator_id !== undefined &&
    intake.operator_id === callerId
  );
}

/**
 * Mirrors the server rules (venueplan.CanTransition + holdsClaim) so a button that
 * would answer 409 is off with a reason. The server stays the judge.
 */
export function gateFor(
  action: IntakeAction,
  intake: Pick<VenuePlanIntake, "state" | "operator_claimed" | "operator_id" | "current_draft">,
  callerId: string | null,
): ActionGate {
  if (isFinalState(intake.state)) {
    return { enabled: false, why: `The request is ${intakeStateLabel(intake.state).toLowerCase()}. No change is possible.` };
  }
  const mine = holdsClaim(intake, callerId);
  if (action === "claim") {
    return mine
      ? { enabled: false, why: "You hold this request." }
      : { enabled: true, why: "" };
  }
  if (!intake.operator_claimed) {
    return { enabled: false, why: "No administrator holds this request. Press Claim first." };
  }
  if (!mine) {
    return { enabled: false, why: "A different administrator holds this request. Use Take control to continue." };
  }
  const hasDraft = intake.current_draft !== null && intake.current_draft !== undefined;
  switch (action) {
    case "upload_draft":
      return intake.state === "operator"
        ? { enabled: true, why: "" }
        : { enabled: false, why: "You can upload a draft only when the request is with an administrator." };
    case "return":
      if (intake.state !== "operator") {
        return { enabled: false, why: "Only a request with an administrator can go back to the organizer." };
      }
      return hasDraft
        ? { enabled: true, why: "" }
        : { enabled: false, why: "Upload a draft first. The organizer must have a draft to examine." };
    case "approve":
      if (intake.state !== "operator" && intake.state !== "review") {
        return { enabled: false, why: "You can approve a request only when it is with the organizer or with an administrator." };
      }
      return hasDraft
        ? { enabled: true, why: "" }
        : { enabled: false, why: "There is no draft to approve." };
    case "reject":
      return intake.state === "operator"
        ? { enabled: true, why: "" }
        : { enabled: false, why: "You can reject only a request with an administrator." };
  }
}

export interface DraftValidationError {
  readonly code: string;
  readonly element?: string;
  readonly detail?: string;
}

/** The list of 422 venue_plan_intake.draft_validation_failed (details.errors); bad shapes are dropped. */
export function draftValidationErrors(err: ApiError): readonly DraftValidationError[] {
  const raw = err.details?.errors;
  if (!Array.isArray(raw)) return [];
  const out: DraftValidationError[] = [];
  for (const item of raw) {
    if (item === null || typeof item !== "object") continue;
    const o = item as Record<string, unknown>;
    if (typeof o.code !== "string") continue;
    out.push({
      code: o.code,
      element: typeof o.element === "string" ? o.element : undefined,
      detail: typeof o.detail === "string" ? o.detail : undefined,
    });
  }
  return out;
}

export function describeValidationError(e: DraftValidationError): string {
  const where = e.element !== undefined && e.element !== "" ? ` (${e.element})` : "";
  const what = e.detail !== undefined && e.detail !== "" ? `: ${e.detail}` : "";
  return `${e.code}${where}${what}`;
}

/** A message that says what the operator does next. */
export function intakeErrorMessage(err: ApiError): string {
  switch (err.code) {
    case "venue_plan_intake.not_claimed":
      return "No administrator holds this request. Press Claim, then try again.";
    case "venue_plan_intake.claimed_by_other":
      return "A different administrator holds this request. Press Take control to continue, or tell that administrator to finish.";
    case "venue_plan_intake.wrong_state":
      return "You cannot do this step with this status of the request. Reload the card to see the new status.";
    case "venue_plan_intake.draft_stale":
      return "The draft changed. Reload the card, examine the new draft, then approve it.";
    case "venue_plan_intake.draft_validation_failed": {
      const list = groupIssues(draftValidationErrors(err).map(describeValidationError));
      return list.length > 0
        ? `The geometry check found errors in the SVG. Correct these errors and upload the file again: ${list.join(", ")}.`
        : "The geometry check found errors in the SVG. Correct the file and upload it again.";
    }
    case "venue_plan_intake.unsupported_format":
      return "The file is not an SVG file. Select an .svg file.";
    case "venue_plan_intake.file_too_large":
      return "The SVG is more than 3 MB. Make a file with less data and upload it again.";
    case "venue_plan_intake.invalid_note":
      return "The note is too long. Write up to 2000 characters.";
    case "venue_plan_intake.invalid_reason":
      return "Write a cause for the organizer, 1 to 1000 characters.";
    case "venue_plan_intake.no_draft":
      return "The request has no draft. Upload a draft first.";
    case "venue_plan_intake.superadmin_only":
    case "permissions.denied":
      return "Only a platform administrator with access to seating plans can do this.";
    case "venue_plan_intake.not_found":
      return "We cannot find this request. Go back to the list.";
    case "superadmin.missing_reason":
    case "superadmin.reason_required":
      return "Write an audit reason and try again.";
    default:
      return `${err.message} (${err.code})`;
  }
}

/** A message for verify and needs-fix. */
export function verifyErrorMessage(err: ApiError): string {
  switch (err.code) {
    case "seating_plan.geometry_invalid": {
      const list = groupIssues(draftValidationErrors(err).map(describeValidationError));
      return list.length > 0
        ? `The geometry check found errors in the plan. Send it back with «Correction necessary», or correct it first: ${list.join(", ")}.`
        : "The geometry check found errors in the plan. Send it back with «Correction necessary», or correct it first.";
    }
    case "seating_plan.not_active":
      return "The plan is a draft or in the archive. You can verify only an active plan.";
    case "seating_plan.no_current_version":
      return "The plan has no version. Tell the owner to upload an SVG first.";
    case "seating_plan.not_found":
      return "We cannot find the plan. Reload the list.";
    case "seating_plan.verify_superadmin_only":
    case "permissions.denied":
      return "Only a platform administrator with the right to verify plans can do this.";
    case "seating_plan.invalid_note":
      return "Write a note of 1 to 1000 characters.";
    case "superadmin.missing_reason":
    case "superadmin.reason_required":
      return "Write an audit reason and try again.";
    default:
      return `${err.message} (${err.code})`;
  }
}

// ---------------------------------------------------------------------------
// Transport for what authedFetch cannot do: multipart bodies and binary answers.
// Same rules as the JSON client: bearer token, one refresh on 401, X-Admin-Reason
// from reason.ts, one retry when the server says the reason is missing.
// ---------------------------------------------------------------------------

const RAW_TIMEOUT_MS = 60_000;

interface RawRequest {
  readonly method: "GET" | "POST";
  readonly path: string;
  readonly body?: FormData;
  readonly accept: string;
}

function toApiError(status: number, text: string): ApiError {
  try {
    const parsed = JSON.parse(text) as { error?: { code?: string; message?: string; request_id?: string; trace_id?: string; details?: Record<string, unknown> } };
    if (parsed.error !== undefined && typeof parsed.error === "object" && parsed.error !== null) {
      return new ApiError(status, {
        code: parsed.error.code ?? `http.${status}`,
        message: parsed.error.message ?? `HTTP ${status}`,
        requestId: parsed.error.request_id,
        traceId: parsed.error.trace_id,
        details: parsed.error.details,
      });
    }
  } catch {
    // not JSON: fall through to the generic error
  }
  return new ApiError(status, { code: `http.${status}`, message: `HTTP ${status}` });
}

async function sendOnce(req: RawRequest, adminReason: string | undefined): Promise<Response> {
  const headers: Record<string, string> = { Accept: req.accept, ...clientChannelHeaders };
  const token = getAccessToken();
  if (token !== null) headers.Authorization = `Bearer ${token}`;
  if (adminReason !== undefined) {
    headers["X-Admin-Reason"] = adminReason;
  } else if (requiresAdminReason(req.path, req.method)) {
    try {
      headers["X-Admin-Reason"] = await resolveReasonFor(req.path);
    } catch (cause) {
      throw new ApiError(0, {
        code: "superadmin.reason_required",
        message: cause instanceof Error ? cause.message : "An audit reason is necessary for this request.",
      });
    }
  }
  const controller = new AbortController();
  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, RAW_TIMEOUT_MS);
  try {
    return await fetch(`${config.apiBaseUrl}${req.path}`, {
      method: req.method,
      headers,
      body: req.body,
      signal: controller.signal,
      credentials: "omit",
    });
  } catch (cause) {
    if (timedOut || (cause instanceof Error && cause.name === "AbortError")) {
      throw new ApiError(0, { code: "network.timeout", message: `Request timed out after ${RAW_TIMEOUT_MS / 1000}s` });
    }
    throw new ApiError(0, { code: "network.failure", message: cause instanceof Error ? cause.message : "Network request failed" });
  } finally {
    clearTimeout(timer);
  }
}

/** Sends the request and returns the response when it is 2xx; every other answer is an ApiError. */
export async function authedRaw(req: RawRequest): Promise<Response> {
  let res = await sendOnce(req, undefined);
  if (res.status === 401) {
    try {
      await refresh();
    } catch {
      clearSession();
      throw toApiError(401, await res.text());
    }
    res = await sendOnce(req, undefined);
  }
  if (res.ok) return res;
  let err = toApiError(res.status, await res.text());
  if (err.code === MISSING_REASON_CODE) {
    let reason = getActiveReason();
    if (reason === null) {
      clearActiveReason();
      try {
        reason = await resolveReasonFor(req.path);
      } catch (cause) {
        throw new ApiError(err.status, {
          code: "superadmin.reason_required",
          message: cause instanceof Error ? cause.message : "An audit reason is necessary for this request.",
        });
      }
    }
    const retry = await sendOnce(req, reason);
    if (retry.ok) return retry;
    err = toApiError(retry.status, await retry.text());
  }
  if (err.status === 403 && req.method !== "GET") {
    requestPermissionRefresh("forbidden-mutation");
  }
  throw err;
}

// ---------------------------------------------------------------------------
// Intake calls
// ---------------------------------------------------------------------------

const INTAKES = "/v1/admin/plan-intakes";

export async function listIntakes(f: IntakeListFilter): Promise<readonly VenuePlanIntakeListItem[]> {
  const res = await authedFetch<{ intakes: VenuePlanIntakeListItem[] }>({ method: "GET", path: buildIntakeListPath(f) });
  return res.intakes;
}

export async function getIntake(id: string): Promise<VenuePlanIntake> {
  const res = await authedFetch<{ intake: VenuePlanIntake }>({ method: "GET", path: `${INTAKES}/${encodeURIComponent(id)}` });
  return res.intake;
}

export async function claimIntake(id: string, opts: { force?: boolean } = {}): Promise<VenuePlanIntake> {
  const res = await authedFetch<{ intake: VenuePlanIntake }>({
    method: "POST",
    path: `${INTAKES}/${encodeURIComponent(id)}/claim`,
    body: opts.force === true ? { force: true } : {},
  });
  return res.intake;
}

/** Uploads a hand-drawn SVG as multipart: `svg` (the file) and an optional `note`. */
export async function uploadIntakeDraft(id: string, file: File, note: string): Promise<VenuePlanIntake> {
  const form = new FormData();
  form.append("svg", file, file.name);
  const trimmed = note.trim();
  if (trimmed !== "") form.append("note", trimmed);
  const res = await authedRaw({
    method: "POST",
    path: `${INTAKES}/${encodeURIComponent(id)}/drafts`,
    body: form,
    accept: "application/json",
  });
  const parsed = (await res.json()) as { intake: VenuePlanIntake };
  return parsed.intake;
}

export async function returnIntake(id: string, note: string): Promise<VenuePlanIntake> {
  const trimmed = note.trim();
  const res = await authedFetch<{ intake: VenuePlanIntake }>({
    method: "POST",
    path: `${INTAKES}/${encodeURIComponent(id)}/return`,
    body: trimmed === "" ? {} : { note: trimmed },
  });
  return res.intake;
}

export async function approveIntake(id: string, draft: number, name?: string): Promise<VenuePlanIntake> {
  const body: { draft: number; name?: string } = { draft };
  if (name !== undefined && name.trim() !== "") body.name = name.trim();
  const res = await authedFetch<{ intake: VenuePlanIntake }>({
    method: "POST",
    path: `${INTAKES}/${encodeURIComponent(id)}/approve`,
    body,
  });
  return res.intake;
}

export async function rejectIntake(id: string, reason: string): Promise<VenuePlanIntake> {
  const res = await authedFetch<{ intake: VenuePlanIntake }>({
    method: "POST",
    path: `${INTAKES}/${encodeURIComponent(id)}/reject`,
    body: { reason: reason.trim() },
  });
  return res.intake;
}

export interface DownloadedFile {
  readonly blob: Blob;
  readonly fileName: string;
}

/** Downloads one source file (an attachment). The server needs X-Admin-Reason and audits it. */
export async function downloadIntakeSource(
  intakeId: string,
  source: Pick<VenuePlanIntakeSource, "id" | "file_name">,
  format?: "png",
): Promise<DownloadedFile> {
  const q = format === "png" ? "?format=png" : "";
  const res = await authedRaw({
    method: "GET",
    path: `${INTAKES}/${encodeURIComponent(intakeId)}/sources/${encodeURIComponent(source.id)}${q}`,
    accept: "*/*",
  });
  return { blob: await res.blob(), fileName: format === "png" ? `${source.file_name}.png` : source.file_name };
}

/**
 * Reads the SVG of draft n through the API (the bearer token goes in the header) and
 * returns it as a Blob of type image/svg+xml, ready for URL.createObjectURL and an
 * <img>. The markup is never put into the page.
 */
export async function fetchDraftSvg(intakeId: string, n: number): Promise<Blob> {
  const res = await authedRaw({
    method: "GET",
    path: `${INTAKES}/${encodeURIComponent(intakeId)}/drafts/${n}/svg`,
    accept: "image/svg+xml",
  });
  const raw = await res.blob();
  return new Blob([raw], { type: "image/svg+xml" });
}

// ---------------------------------------------------------------------------
// Verification calls
// ---------------------------------------------------------------------------

export interface AdminSeatingPlanPage {
  readonly seating_plans: readonly AdminSeatingPlanRow[];
  readonly total: number;
  readonly limit: number;
  readonly offset: number;
  readonly counts: VerificationCounts;
}

/** ONE request per view of the list: the page, the total and the counts of every state. */
export function listAdminSeatingPlans(f: PlanListFilter): Promise<AdminSeatingPlanPage> {
  return authedFetch<AdminSeatingPlanPage>({ method: "GET", path: buildVerificationListPath(f) });
}

export function verifyPlan(planId: string): Promise<SeatingPlanVerification> {
  return authedFetch<SeatingPlanVerification>({
    method: "POST",
    path: `/v1/admin/seating-plans/${encodeURIComponent(planId)}/verify`,
  });
}

export function planNeedsFix(planId: string, note: string): Promise<SeatingPlanVerification> {
  return authedFetch<SeatingPlanVerification>({
    method: "POST",
    path: `/v1/admin/seating-plans/${encodeURIComponent(planId)}/needs-fix`,
    body: { note: note.trim() },
  });
}

interface PlanVersionLite {
  readonly id: string;
  readonly version_number: number;
  readonly svg_asset_media_id: string | null;
}

/**
 * The image of a plan's current version, the way routes/venueSeatingPlans.tsx finds
 * it: GET /v1/seating-plans/{id}/versions gives the version and its svg asset, then
 * GET /v1/media/{id} gives a signed URL. Null when the plan has no stored SVG.
 */
export async function fetchPlanPreviewUrl(planId: string, currentVersionId: string | null): Promise<string | null> {
  if (currentVersionId === null) return null;
  const list = await authedFetch<{ seating_plan_versions: PlanVersionLite[] }>({
    method: "GET",
    path: `/v1/seating-plans/${encodeURIComponent(planId)}/versions`,
  });
  const version = list.seating_plan_versions.find((v) => v.id === currentVersionId);
  const mediaId = version?.svg_asset_media_id ?? null;
  if (mediaId === null) return null;
  const media = await authedFetch<{ media_object: { signed_url?: string } }>({
    method: "GET",
    path: `/v1/media/${encodeURIComponent(mediaId)}`,
  });
  const raw = media.media_object.signed_url ?? "";
  if (raw === "") return null;
  return raw.startsWith("/") ? `${config.apiBaseUrl}${raw}` : raw;
}

// ---------------------------------------------------------------------------
// Rows and geo choices of the list
// ---------------------------------------------------------------------------

/** A row of the verification table. */
export interface VerificationRow {
  readonly planId: string;
  readonly name: string;
  readonly orgName: string;
  readonly venueName: string;
  readonly venueId: string;
  readonly cityName: string | null;
  readonly countryIso2: string | null;
  readonly seats: number | null;
  readonly standing: number;
  readonly createdAt: string;
  readonly state: string | null;
  readonly note: string | null;
  readonly currentVersionId: string | null;
}

export function rowFromAdminPlan(p: AdminSeatingPlanRow): VerificationRow {
  return {
    planId: p.plan_id,
    name: p.name,
    orgName: p.owner_org_name,
    venueName: p.venue_name,
    venueId: p.venue_id,
    cityName: p.city_name ?? null,
    countryIso2: p.country_iso2 ?? null,
    seats: p.capacity_seated ?? null,
    standing: p.capacity_standing ?? 0,
    createdAt: p.created_at,
    state: p.verification_state ?? null,
    note: p.verification_note ?? null,
    currentVersionId: p.current_version_id ?? null,
  };
}

/** "Prague, CZ"; a part that is missing is left out; a dash when both are missing. */
export function placeLabel(cityName: string | null, countryIso2: string | null): string {
  const parts = [cityName, countryIso2].filter((s): s is string => s !== null && s !== "");
  return parts.length === 0 ? "—" : parts.join(", ");
}

/** "1,200" or "1,200 + 300 standing"; a dash when the plan has no places at all. */
export function seatsLabel(seats: number | null, standing: number): string {
  if (seats === null && standing <= 0) return "—";
  const s = (seats ?? 0).toLocaleString("en-US");
  return standing > 0 ? `${s} + ${standing.toLocaleString("en-US")} standing` : s;
}

export interface GeoCountry {
  readonly iso2: string;
  readonly name: string;
}

export interface GeoCity {
  readonly id: string;
  readonly name: string;
  readonly country_iso2: string;
}

export interface GeoOption {
  readonly value: string;
  readonly label: string;
}

/** The countries of GET /v1/geo/countries as options: value is the ISO-2 code. */
export function countryChoices(countries: readonly GeoCountry[]): readonly GeoOption[] {
  return countries
    .map((c) => ({ value: c.iso2.toUpperCase(), label: `${c.name} (${c.iso2.toUpperCase()})` }))
    .sort((a, b) => a.label.localeCompare(b.label));
}

/** The cities of one country. With no country chosen there is no city to choose. */
export function cityChoices(cities: readonly GeoCity[], country: string): readonly GeoOption[] {
  if (country === "") return [];
  return cities
    .filter((c) => c.country_iso2.toUpperCase() === country.toUpperCase())
    .map((c) => ({ value: c.id, label: c.name }))
    .sort((a, b) => a.label.localeCompare(b.label));
}

