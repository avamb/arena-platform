/**
 * Onboarding applications (backlog APP-09, spec
 * 08_architecture/34_onboarding_applications_ru.md §3, §8 and appendix A).
 *
 * The operator-facing half of the organizer application flow, served by
 * apps/backend/internal/platform/httpserver/honboarding (all under /v1,
 * bearer JWT, platform_superadmin only):
 *
 *   GET  /v1/admin/onboarding/applications?status=&q=&limit=&offset=
 *   GET  /v1/admin/onboarding/applications/{id}
 *   POST /v1/admin/onboarding/applications/{id}/approve | reject |
 *        request-info | extend | resend | purge | recheck | notes
 *   GET|PUT /v1/admin/onboarding/settings
 *
 * Contract note: the generated schema types `OnboardingAdminListResponse.items`
 * as `Record<string, never>[]`, but the real JSON is FLAT — every row is the
 * application's own fields plus `would_approve` (Go: `ListItem` embeds
 * `Application`). `OnboardingListItem` below is the type of the real JSON.
 *
 * Mock data: NONE. Everything here talks to the live backend.
 */
import type { components } from "@openapi/index";
import { authedFetch } from "@/lib/api/client";

type Schemas = components["schemas"];

export type OnboardingApplication = Schemas["OnboardingAdminApplication"];
export type OnboardingCheck = Schemas["OnboardingCheck"];
export type OnboardingEvent = Schemas["OnboardingEvent"];
export type OnboardingNote = Schemas["OnboardingNote"];
export type OnboardingDetail = Schemas["OnboardingAdminDetail"];
export type OnboardingApproveResult = Schemas["OnboardingApproveResponse"];
export type OnboardingSettings = Schemas["OnboardingSettings"];
export type OnboardingSettingsRequest = Schemas["OnboardingSettingsRequest"];
export type OnboardingRejectRequest = Schemas["OnboardingRejectRequest"];
export type OnboardingRequestInfoRequest = Schemas["OnboardingRequestInfoRequest"];

/** One queue row as the server really sends it: the application, flat, plus the verdict. */
export type OnboardingListItem = OnboardingApplication & {
  readonly would_approve: boolean;
};

export interface OnboardingListResponse {
  readonly items: readonly OnboardingListItem[];
  readonly total: number;
  readonly counts: Readonly<Record<string, number>>;
}

// ---------------------------------------------------------------------------
// Statuses (spec §3)
// ---------------------------------------------------------------------------

export type OnboardingStatus =
  | "pending_approval"
  | "info_requested"
  | "draft"
  | "approved"
  | "rejected"
  | "expired";

export interface OnboardingTab {
  readonly status: OnboardingStatus;
  readonly label: string;
}

/** The six tabs, in queue order; Pending approval is the default. */
export const ONBOARDING_TABS: readonly OnboardingTab[] = [
  { status: "pending_approval", label: "Pending approval" },
  { status: "info_requested", label: "Info requested" },
  { status: "draft", label: "Draft" },
  { status: "approved", label: "Approved" },
  { status: "rejected", label: "Rejected" },
  { status: "expired", label: "Expired" },
];

export const DEFAULT_ONBOARDING_TAB: OnboardingStatus = "pending_approval";

export function statusLabel(status: string): string {
  return ONBOARDING_TABS.find((t) => t.status === status)?.label ?? status;
}

/** Count shown on a tab; a status the server did not report counts as 0. */
export function tabCount(
  counts: Readonly<Record<string, number>> | undefined,
  status: string,
): number {
  const n = counts?.[status];
  return typeof n === "number" && Number.isFinite(n) ? n : 0;
}

export function sourceLabel(source: string): string {
  switch (source) {
    case "site":
      return "Site";
    case "telegram":
      return "Telegram";
    case "operator":
      return "Operator";
    default:
      return source;
  }
}

// ---------------------------------------------------------------------------
// Which action is allowed in which status (mirrors lockForDecision in
// onboarding/admin.go; the server stays the judge, this only hides buttons
// that would answer 409 onboarding.wrong_state).
// ---------------------------------------------------------------------------

export type OnboardingAction =
  | "approve"
  | "request_info"
  | "reject"
  | "extend"
  | "resend"
  | "purge"
  | "recheck";

const ACTION_STATUSES: Readonly<Record<OnboardingAction, readonly string[]>> = {
  approve: ["pending_approval"],
  request_info: ["pending_approval", "info_requested"],
  reject: ["pending_approval", "info_requested", "draft"],
  extend: ["draft", "expired", "info_requested"],
  resend: ["draft", "pending_approval", "info_requested", "expired"],
  purge: ["draft", "pending_approval", "info_requested", "rejected", "expired"],
  recheck: [
    "draft",
    "pending_approval",
    "info_requested",
    "approved",
    "rejected",
    "expired",
  ],
};

/** Actions the card offers for an application. A purged one offers none. */
export function availableActions(app: {
  readonly status: string;
  readonly purged_at?: string | null;
}): readonly OnboardingAction[] {
  if (app.purged_at !== null && app.purged_at !== undefined) {
    return [];
  }
  return (Object.keys(ACTION_STATUSES) as OnboardingAction[]).filter((a) =>
    ACTION_STATUSES[a].includes(app.status),
  );
}

// ---------------------------------------------------------------------------
// List path
// ---------------------------------------------------------------------------

export const ONBOARDING_PAGE_SIZE = 25;

export function buildOnboardingListPath(
  status: string,
  rawSearch: string,
  limit: number,
  offset: number,
): string {
  const params = new URLSearchParams();
  if (status !== "") params.set("status", status);
  const q = rawSearch.trim();
  if (q !== "") params.set("q", q);
  params.set("limit", String(limit));
  params.set("offset", String(offset));
  return `/v1/admin/onboarding/applications?${params.toString()}`;
}

// ---------------------------------------------------------------------------
// API calls. State-changing ones carry X-Admin-Reason: reason.ts decides that
// from the path (see REASON_REQUIRED_ADMIN_MUTATION_REGEX), the API client
// prompts for it once per session.
// ---------------------------------------------------------------------------

const BASE = "/v1/admin/onboarding";

export function listOnboardingApplications(
  status: string,
  q: string,
  limit: number,
  offset: number,
): Promise<OnboardingListResponse> {
  return authedFetch<OnboardingListResponse>({
    method: "GET",
    path: buildOnboardingListPath(status, q, limit, offset),
  });
}

export function getOnboardingApplication(id: string): Promise<OnboardingDetail> {
  return authedFetch<OnboardingDetail>({
    method: "GET",
    path: `${BASE}/applications/${encodeURIComponent(id)}`,
  });
}

function decide<T>(id: string, action: string, body?: unknown): Promise<T> {
  return authedFetch<T>({
    method: "POST",
    path: `${BASE}/applications/${encodeURIComponent(id)}/${action}`,
    body,
  });
}

export function recheckOnboardingApplication(id: string): Promise<OnboardingDetail> {
  return decide<OnboardingDetail>(id, "recheck");
}

export function approveOnboardingApplication(id: string): Promise<OnboardingApproveResult> {
  return decide<OnboardingApproveResult>(id, "approve");
}

export function rejectOnboardingApplication(
  id: string,
  req: OnboardingRejectRequest,
): Promise<OnboardingDetail> {
  return decide<OnboardingDetail>(id, "reject", req);
}

export function requestOnboardingInfo(
  id: string,
  req: OnboardingRequestInfoRequest,
): Promise<OnboardingDetail> {
  return decide<OnboardingDetail>(id, "request-info", req);
}

export function extendOnboardingApplication(
  id: string,
  days?: number,
): Promise<OnboardingDetail> {
  return decide<OnboardingDetail>(id, "extend", days === undefined ? {} : { days });
}

export function resendOnboardingLink(id: string): Promise<OnboardingDetail> {
  return decide<OnboardingDetail>(id, "resend");
}

export function purgeOnboardingApplication(id: string): Promise<OnboardingDetail> {
  return decide<OnboardingDetail>(id, "purge");
}

export function addOnboardingNote(id: string, body: string): Promise<OnboardingNote> {
  return decide<OnboardingNote>(id, "notes", { body });
}

export function getOnboardingSettings(): Promise<OnboardingSettings> {
  return authedFetch<OnboardingSettings>({ method: "GET", path: `${BASE}/settings` });
}

export function updateOnboardingSettings(
  req: OnboardingSettingsRequest,
): Promise<OnboardingSettings> {
  return authedFetch<OnboardingSettings>({
    method: "PUT",
    path: `${BASE}/settings`,
    body: req,
  });
}

// ---------------------------------------------------------------------------
// The form, as the operator reads it (steps and field order from
// onboarding/schema.go, human labels from onboarding/labels.go "en").
// ---------------------------------------------------------------------------

export interface FormBlock {
  readonly id: string;
  readonly title: string;
  readonly fields: readonly string[];
}

export const FORM_BLOCKS: readonly FormBlock[] = [
  {
    id: "contact",
    title: "Contact",
    fields: ["first_name", "last_name", "email", "phone", "telegram_username"],
  },
  {
    id: "organization",
    title: "Organization",
    fields: [
      "org_name",
      "legal_name",
      "country",
      "legal_form",
      "tax_id",
      "tax_id_scheme",
      "registration_number",
      "address_line1",
      "address_postal_code",
      "address_city",
      "address_country",
      "website",
      "social_links",
    ],
  },
  {
    id: "events",
    title: "Events",
    fields: [
      "event_types",
      "seating",
      "events_per_year",
      "tickets_per_year",
      "avg_ticket_price",
      "currency",
      "first_event_date",
    ],
  },
  {
    id: "platform",
    title: "Platform and payments",
    fields: [
      "previous_system",
      "previous_system_name",
      "has_website",
      "website_platform",
      "wants_wp_plugin",
      "payment_provider",
      "has_payment_account",
      "notes",
    ],
  },
  {
    id: "consents",
    title: "Consents",
    fields: ["accept_terms", "accept_privacy", "confirm_authority", "marketing_opt_in"],
  },
];

export const FIELD_LABELS: Readonly<Record<string, string>> = {
  first_name: "First name",
  last_name: "Last name",
  email: "E-mail",
  phone: "Phone",
  telegram_username: "Telegram user name",
  org_name: "Name on posters and tickets",
  legal_name: "Legal name",
  country: "Country of the organization",
  legal_form: "Legal form",
  tax_id: "Tax number",
  tax_id_scheme: "Type of tax number",
  registration_number: "Registration number of the organization",
  address_line1: "Legal address",
  address_postal_code: "Postal code",
  address_city: "City",
  address_country: "Country of the address",
  website: "Site",
  social_links: "Links to social media",
  event_types: "Types of events",
  seating: "Seating",
  events_per_year: "Events for each year",
  tickets_per_year: "Tickets for each year",
  avg_ticket_price: "Average ticket price",
  currency: "Currency",
  first_event_date: "Date of the first event",
  previous_system: "System that sells the tickets now",
  previous_system_name: "Name of the system you use now",
  has_website: "Has a site",
  website_platform: "Software of the site",
  wants_wp_plugin: "Wants the WordPress plugin",
  payment_provider: "Payment provider",
  has_payment_account: "Has an account with this provider",
  notes: "Notes from the applicant",
  accept_terms: "Accepts the terms of service",
  accept_privacy: "Accepts the privacy policy",
  confirm_authority: "Confirms the right to act for the organization",
  marketing_opt_in: "Agrees to get news and tips (optional)",
};

/** Machine value -> readable text, per field. Unknown values print as they are. */
export const OPTION_LABELS: Readonly<Record<string, Readonly<Record<string, string>>>> = {
  tax_id_scheme: {
    vat: "VAT number",
    ico: "ID of the organization (IČO or equal)",
    ein: "EIN (USA)",
    other: "Other",
  },
  event_types: {
    concert: "Concerts",
    theatre: "Theater and shows",
    masterclass: "Master classes and workshops",
    festival: "Festivals",
    sport: "Sport",
    tour: "Tours and trips",
    other: "Other",
  },
  seating: {
    ga: "No seats (general admission)",
    seated: "Seats for each ticket",
    both: "Both",
  },
  events_per_year: { "1-5": "1 to 5", "6-20": "6 to 20", "21-100": "21 to 100", "100+": "More than 100" },
  tickets_per_year: {
    "<500": "Less than 500",
    "500-5k": "500 to 5,000",
    "5k-50k": "5,000 to 50,000",
    "50k+": "More than 50,000",
  },
  previous_system: { bil24: "Bil24", other: "A different system", none: "Does not sell tickets online" },
  website_platform: { wordpress: "WordPress", other: "A different software", none: "No site" },
  payment_provider: {
    stripe: "Stripe",
    flitt: "Flitt",
    other: "A different provider",
    undecided: "Not selected",
  },
};

/** Keys the operator may ask the applicant to fill in again: every field but the confirmed e-mail. */
export const REQUESTABLE_FIELDS: readonly string[] = FORM_BLOCKS.flatMap((b) => b.fields).filter(
  (k) => k !== "email",
);

export function fieldLabel(key: string): string {
  return FIELD_LABELS[key] ?? key;
}

/** True when an answer carries nothing worth printing. */
export function isBlank(value: unknown): boolean {
  if (value === null || value === undefined) return true;
  if (typeof value === "string") return value.trim() === "";
  if (Array.isArray(value)) return value.length === 0;
  return false;
}

/** One answer as plain text. Booleans read Yes/No, lists join, options use their label. */
export function formatAnswer(key: string, value: unknown): string {
  if (isBlank(value)) return "—";
  if (typeof value === "boolean") return value ? "Yes" : "No";
  const options = OPTION_LABELS[key];
  const one = (v: unknown): string => {
    const s = typeof v === "string" ? v : String(v);
    return options?.[s] ?? s;
  };
  if (Array.isArray(value)) return value.map(one).join(", ");
  if (typeof value === "object") return JSON.stringify(value);
  return one(value);
}

export interface AnswerRow {
  readonly key: string;
  readonly label: string;
  readonly text: string;
  readonly filled: boolean;
  readonly requested: boolean;
}

export interface AnswerGroup {
  readonly id: string;
  readonly title: string;
  readonly rows: readonly AnswerRow[];
}

/**
 * The answers grouped into the form's blocks, in the form's own order. A key
 * the server stores that this screen has no block for lands in a trailing
 * "Other" block, so a field added on the server is never silently invisible.
 */
export function groupAnswers(
  answers: Readonly<Record<string, unknown>> | null | undefined,
  requestedFields: readonly string[] | null | undefined = [],
): readonly AnswerGroup[] {
  const data = answers ?? {};
  const requested = new Set(requestedFields ?? []);
  const known = new Set<string>();
  const groups: AnswerGroup[] = FORM_BLOCKS.map((block) => ({
    id: block.id,
    title: block.title,
    rows: block.fields.map((key) => {
      known.add(key);
      const value = data[key];
      return {
        key,
        label: fieldLabel(key),
        text: formatAnswer(key, value),
        filled: !isBlank(value),
        requested: requested.has(key),
      };
    }),
  }));
  const extra = Object.keys(data)
    .filter((k) => !known.has(k) && !isBlank(data[k]))
    .sort();
  if (extra.length > 0) {
    groups.push({
      id: "other",
      title: "Other",
      rows: extra.map((key) => ({
        key,
        label: fieldLabel(key),
        text: formatAnswer(key, data[key]),
        filled: true,
        requested: requested.has(key),
      })),
    });
  }
  return groups;
}

// ---------------------------------------------------------------------------
// Checks and timeline
// ---------------------------------------------------------------------------

export const CHECK_LABELS: Readonly<Record<string, string>> = {
  email_confirmed: "E-mail is confirmed",
  complete: "Form is complete",
  consents: "Consents are given",
  country_allowed: "Country accepted",
  tax_id_format: "Tax number format",
  disposable_email: "Not a temporary e-mail",
  duplicate: "No second application or organization",
  payment_provider: "Payment provider is named",
};

export function checkLabel(key: string): string {
  return CHECK_LABELS[key] ?? key;
}

export const EVENT_LABELS: Readonly<Record<string, string>> = {
  created: "Draft started",
  email_confirmed: "E-mail is confirmed",
  resumed: "Applicant came back",
  revived: "Expired draft opened again",
  link_sent: "Continue link sent",
  submitted: "Sent for approval",
  resubmitted: "Sent again after the questions",
  info_requested: "We asked for information",
  extended: "Draft made longer",
  approved: "Approved",
  rejected: "Rejected",
  purged: "Personal data erased",
  reminder_sent: "We sent a reminder to the applicant",
  queue_reminder: "We sent a reminder to the operator about applications that wait",
};

export function eventLabel(kind: string): string {
  return EVENT_LABELS[kind] ?? kind;
}

/** Short who-did-it text for the timeline. */
export function actorText(e: { readonly actor_type: string; readonly actor_id: string }): string {
  switch (e.actor_type) {
    case "applicant":
      return "Applicant";
    case "system":
      return "System";
    case "operator":
      return e.actor_id !== "" ? `Operator ${e.actor_id.slice(0, 8)}` : "Operator";
    default:
      return e.actor_type;
  }
}

/** One-line detail of a timeline entry, "" when there is nothing to add. */
export function eventDetailText(e: {
  readonly kind: string;
  readonly detail?: Readonly<Record<string, unknown>> | null;
}): string {
  const d = e.detail ?? {};
  switch (e.kind) {
    case "rejected":
      return typeof d.reason === "string" ? `Cause: ${d.reason}` : "";
    case "info_requested":
      return Array.isArray(d.fields) ? `Fields: ${d.fields.map((f) => fieldLabel(String(f))).join(", ")}` : "";
    case "extended":
      return typeof d.days === "number" ? `${d.days} days` : "";
    case "approved":
      return [
        typeof d.org_slug === "string" ? `Organization ${d.org_slug}` : "",
        d.automatic === true ? "approved by the system" : "",
      ]
        .filter((s) => s !== "")
        .join(", ");
    case "created":
      return typeof d.source === "string" ? `Source: ${sourceLabel(d.source)}` : "";
    case "reminder_sent":
      return typeof d.number === "number" ? `Reminder number ${d.number}` : "";
    default:
      return "";
  }
}

// ---------------------------------------------------------------------------
// Dates
// ---------------------------------------------------------------------------

/** "just now", "5 min ago", "3 h ago", "2 d ago". Future or unparsable input prints as it is. */
export function relativeAge(iso: string | null | undefined, now: number = Date.now()): string {
  if (iso === null || iso === undefined || iso === "") return "—";
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return iso;
  const sec = Math.floor((now - t) / 1000);
  if (sec < 0) return "just now";
  if (sec < 60) return "just now";
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min} min ago`;
  const h = Math.floor(min / 60);
  if (h < 48) return `${h} h ago`;
  return `${Math.floor(h / 24)} d ago`;
}

// ---------------------------------------------------------------------------
// Dialog input checks (the server re-checks everything; these only save a round trip)
// ---------------------------------------------------------------------------

export interface RejectForm {
  readonly reason: string;
  readonly message: string;
}

export function validateReject(form: RejectForm): Record<string, string> {
  const errors: Record<string, string> = {};
  if (form.reason.trim() === "") errors.reason = "Give the cause of the rejection.";
  return errors;
}

export interface RequestInfoForm {
  readonly fields: readonly string[];
  readonly message: string;
}

export function validateRequestInfo(form: RequestInfoForm): Record<string, string> {
  const errors: Record<string, string> = {};
  if (form.fields.length === 0) errors.fields = "Select one field or more to ask about.";
  if (form.message.trim() === "") errors.message = "Write what the applicant must add or correct.";
  return errors;
}

export const MAX_EXTEND_DAYS = 1095;

/** Days as typed into the Extend dialog; "" means "the default draft lifetime". */
export function validateExtendDays(raw: string): { days?: number; error?: string } {
  const t = raw.trim();
  if (t === "") return {};
  if (!/^[0-9]+$/.test(t)) return { error: "Enter a number of days without decimals." };
  const n = Number(t);
  if (n < 1 || n > MAX_EXTEND_DAYS) return { error: `Days must be between 1 and ${MAX_EXTEND_DAYS}.` };
  return { days: n };
}

// ---------------------------------------------------------------------------
// Settings form (bounds mirror onboarding.UpdateSettings)
// ---------------------------------------------------------------------------

export const APPROVAL_MODES = ["manual", "auto_when_complete"] as const;
export type ApprovalMode = (typeof APPROVAL_MODES)[number];

export const APPROVAL_MODE_LABELS: Readonly<Record<ApprovalMode, string>> = {
  manual: "Manual. An operator decides on each application.",
  auto_when_complete: "Automatic when complete. The system approves without a person.",
};

export const AUTO_MODE_WARNING =
  "In this mode the system approves each application when all checks pass. It makes the organization, the owner account and a sales page. No person examines the application. Use this mode only after many applications where the decision of the system was the same as the decision of the operator.";

export interface SettingsForm {
  readonly approval_mode: string;
  readonly draft_ttl_days: string;
  readonly purge_after_days: string;
  readonly countries: string;
  readonly max_new_per_day: string;
  readonly terms_version: string;
  readonly privacy_version: string;
}

export function settingsToForm(s: OnboardingSettings): SettingsForm {
  return {
    approval_mode: s.approval_mode,
    draft_ttl_days: String(s.draft_ttl_days),
    purge_after_days: String(s.purge_after_days),
    countries: (s.countries ?? []).join(", "),
    max_new_per_day: String(s.max_new_per_day),
    terms_version: s.terms_version,
    privacy_version: s.privacy_version,
  };
}

const COUNTRY_RE = /^[A-Z]{2}$/;

/** "cz, de ES" -> ["CZ","DE","ES"]; blank means every country. `invalid` lists what is not an ISO alpha-2 code. */
export function parseCountries(raw: string): { codes: string[]; invalid: string[] } {
  const seen = new Set<string>();
  const codes: string[] = [];
  const invalid: string[] = [];
  for (const part of raw.split(/[\s,;]+/)) {
    const t = part.trim().toUpperCase();
    if (t === "") continue;
    if (!COUNTRY_RE.test(t)) {
      invalid.push(part.trim());
      continue;
    }
    if (!seen.has(t)) {
      seen.add(t);
      codes.push(t);
    }
  }
  return { codes, invalid };
}

function intInRange(raw: string, lo: number, hi: number): number | null {
  const t = raw.trim();
  if (!/^[0-9]+$/.test(t)) return null;
  const n = Number(t);
  return n >= lo && n <= hi ? n : null;
}

export interface SettingsCheck {
  readonly errors: Record<string, string>;
  /** Present only when `errors` is empty. */
  readonly request?: OnboardingSettingsRequest;
}

export function validateSettings(form: SettingsForm): SettingsCheck {
  const errors: Record<string, string> = {};
  if (!(APPROVAL_MODES as readonly string[]).includes(form.approval_mode)) {
    errors.approval_mode = "Select an approval mode.";
  }
  const ttl = intInRange(form.draft_ttl_days, 7, 1095);
  if (ttl === null) errors.draft_ttl_days = "Whole days from 7 to 1095.";
  const purge = intInRange(form.purge_after_days, 30, 3650);
  if (purge === null) errors.purge_after_days = "Whole days from 30 to 3650.";
  const perDay = intInRange(form.max_new_per_day, 1, 100000);
  if (perDay === null) errors.max_new_per_day = "A whole number from 1 to 100000.";
  const countries = parseCountries(form.countries);
  if (countries.invalid.length > 0) {
    errors.countries = `These are not ISO country codes of two letters: ${countries.invalid.join(", ")}.`;
  }
  const terms = form.terms_version.trim();
  if (terms === "" || terms.length > 40) errors.terms_version = "This field is necessary. Use 40 characters or less.";
  const privacy = form.privacy_version.trim();
  if (privacy === "" || privacy.length > 40) errors.privacy_version = "This field is necessary. Use 40 characters or less.";
  if (Object.keys(errors).length > 0 || ttl === null || purge === null || perDay === null) {
    return { errors };
  }
  return {
    errors,
    request: {
      approval_mode: form.approval_mode,
      draft_ttl_days: ttl,
      purge_after_days: purge,
      countries: countries.codes,
      max_new_per_day: perDay,
      terms_version: terms,
      privacy_version: privacy,
    },
  };
}

/** Field errors the server returned (`details.fields`), as a plain map. */
export function serverFieldErrors(details: Record<string, unknown> | undefined): Record<string, string> {
  const fields = details?.fields;
  if (fields === null || typeof fields !== "object") return {};
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(fields as Record<string, unknown>)) {
    if (typeof v === "string") out[k] = v;
  }
  return out;
}
