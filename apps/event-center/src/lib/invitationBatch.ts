/**
 * Pure rules of the CSV upload of staff invitations (spec 46, sections 3.5 and
 * 3.6, task INV-06b): what a file may be before it is sent, the example file,
 * the request id that survives a retry, how a refusal is read back into the
 * check table, and what the billing preview says.
 *
 * vitest runs without a DOM, so everything a click would exercise lives here
 * and is tested in invitationBatch.test.ts. Nothing here logs: the file holds
 * guests' contact details.
 */
import { ApiError } from "@/lib/api/http";
import type { InvitationBatchReport, InvitationBatchRowIssue } from "@/lib/api/types";
import type { MessageKey } from "@/lib/i18n";
import { newRequestId, type Delivery } from "@/lib/staffInvitations";

/** The server's own limits of one file (409/413/422 above them). */
export const MAX_BATCH_BYTES = 512 * 1024;
export const MAX_BATCH_GUESTS = 100;
export const MAX_BATCH_PLACES = 250;

/** How a guest WITHOUT an e-mail in the file is served. A guest with an e-mail always gets the letter. */
export const BATCH_DELIVERIES: readonly Delivery[] = ["pdf_only", "my_email", "guest_email"];
export const DEFAULT_BATCH_DELIVERY: Delivery = "pdf_only";

/** How many problem rows the table shows at once; the rest is counted. */
export const MAX_SHOWN_ISSUES = 100;

// ─── The file ────────────────────────────────────────────────────────────────

export type FileProblem = "empty" | "tooLarge" | "spreadsheet";

const SPREADSHEET = /\.(xlsx|xlsm|xls|ods|numbers)$/i;

/** What stops a file before it is sent. The server checks the content, so only the cheap facts are checked here. */
export function fileProblem(file: { name: string; size: number }): FileProblem | null {
  if (SPREADSHEET.test(file.name)) return "spreadsheet";
  if (file.size === 0) return "empty";
  if (file.size > MAX_BATCH_BYTES) return "tooLarge";
  return null;
}

/** The columns of the file and what each is for. `required` is the one every file needs. */
export const CSV_COLUMNS = [
  { name: "name", required: true },
  { name: "guest", required: false },
  { name: "email", required: false },
  { name: "phone", required: false },
  { name: "locale", required: false },
  { name: "note", required: false },
  { name: "seat_key", required: false },
  { name: "sector", required: false },
  { name: "row", required: false },
  { name: "seat", required: false },
  { name: "category", required: false },
  { name: "qty", required: false },
] as const;

export type CsvColumn = (typeof CSV_COLUMNS)[number]["name"];

/**
 * The example file: two guests, one with two seats of the plan, one with
 * general-admission places. A byte order mark first, so Excel opens it as UTF-8.
 * The values are samples: the person replaces them with their own sectors,
 * rows and categories.
 */
export function exampleCsv(): string {
  const rows: string[][] = [
    ["guest", "name", "email", "phone", "locale", "note", "sector", "row", "seat", "category", "qty"],
    ["1", "Ana López", "ana@example.com", "", "es", "Press", "Parter", "1", "1", "", ""],
    ["1", "Ana López", "", "", "", "", "Parter", "1", "2", "", ""],
    ["2", "Carlos Ruiz", "", "+34 600 000 000", "es", "Sponsor", "", "", "", "General admission", "2"],
  ];
  return "﻿" + rows.map((r) => r.join(",")).join("\r\n") + "\r\n";
}

export const EXAMPLE_FILENAME = "invitations-example.csv";

// ─── The request id of one file ──────────────────────────────────────────────

export interface BatchAttempt {
  readonly id: string;
  /** Which pick of a file, and with which delivery, the id was first used for. */
  readonly key: string;
}

/**
 * The server ties a request id to the file AND the delivery. A pick is one
 * choosing of a file (choosing the same file again is a new pick, because the
 * person may have edited it meanwhile), so the id lives for one pick and one
 * delivery: a retry reuses it, a new file or another delivery gets a new one.
 */
export function batchAttemptFor(prev: BatchAttempt | null, pick: number, delivery: Delivery, make: () => string = newRequestId): BatchAttempt {
  const key = `${pick}|${delivery}`;
  if (prev !== null && prev.key === key) return prev;
  return { id: make(), key };
}

// ─── Reading an answer ───────────────────────────────────────────────────────

function isIssue(x: unknown): x is InvitationBatchRowIssue {
  if (x === null || typeof x !== "object") return false;
  const i = x as Record<string, unknown>;
  return typeof i.row === "number" && typeof i.code === "string";
}

/** True for an answer shaped like a report (a dry run), false for a batch. */
export function isReport(x: unknown): x is InvitationBatchReport {
  if (x === null || typeof x !== "object") return false;
  const r = x as Record<string, unknown>;
  return Array.isArray(r.errors) && r.errors.every(isIssue) && typeof r.guests === "number" && typeof r.places === "number";
}

/** The report a refusal carries (`details.report` of 422 `invitation_batch.invalid` and of the 409 refusals), or null. */
export function reportOfError(err: unknown): InvitationBatchReport | null {
  if (!(err instanceof ApiError)) return null;
  const report = err.details?.["report"];
  return isReport(report) ? report : null;
}

/** The line of the file a `invitation_batch.invalid_csv` points at, or null. */
export function csvErrorLine(err: unknown): number | null {
  if (!(err instanceof ApiError) || err.code !== "invitation_batch.invalid_csv") return null;
  const line = err.details?.["line"];
  return typeof line === "number" && Number.isFinite(line) && line > 0 ? line : null;
}

/** True when a refusal is "split the file": too many guests, places or rows, or a file over the size limit. */
export function isTooBig(err: unknown): boolean {
  if (!(err instanceof ApiError)) return false;
  return (
    err.code === "invitation_batch.too_many_rows" ||
    err.code === "invitation_batch.too_many_places" ||
    err.code === "invitation_batch.file_too_large" ||
    err.code === "http.payload_too_large" ||
    err.status === 413
  );
}

/**
 * The error as the screen shows it. A body over the router's 1 MiB limit answers the general
 * `http.payload_too_large`, which means "split the file" here and nothing specific elsewhere.
 */
export function batchError(err: unknown): unknown {
  if (err instanceof ApiError && isTooBig(err) && err.code !== "invitation_batch.file_too_large" && !err.code.startsWith("invitation_batch.")) {
    return new ApiError(err.status, "invitation_batch.file_too_large", err.message, err.requestId, err.details);
  }
  return err;
}

/** Problems of the file as a whole (row 0) and of its rows, the rows in file order. */
export function splitIssues(report: Pick<InvitationBatchReport, "errors">): { file: InvitationBatchRowIssue[]; rows: InvitationBatchRowIssue[] } {
  const file = report.errors.filter((e) => e.row <= 0);
  const rows = report.errors.filter((e) => e.row > 0).sort((a, b) => a.row - b.row);
  return { file, rows };
}

/** The first `cap` problem rows and how many are left out. */
export function shownIssues<T>(rows: readonly T[], cap: number = MAX_SHOWN_ISSUES): { shown: T[]; hidden: number } {
  return { shown: rows.slice(0, cap), hidden: Math.max(0, rows.length - cap) };
}

/** True when the report allows the real upload: it is valid, has no problem, and hands out at least one place. */
export function canIssue(report: InvitationBatchReport | null): boolean {
  return report !== null && report.valid && report.errors.length === 0 && report.guests > 0 && report.places > 0;
}

/** The texts of the row problems; a code the web does not know gets the general one. */
const ISSUE_TEXT: Record<string, MessageKey> = {
  name_required: "invBatch.issue.name_required",
  invalid_email: "invBatch.issue.invalid_email",
  email_required: "invBatch.issue.email_required",
  place_missing: "invBatch.issue.place_missing",
  place_ambiguous: "invBatch.issue.place_ambiguous",
  unknown_seat: "invBatch.issue.unknown_seat",
  ambiguous_seat: "invBatch.issue.ambiguous_seat",
  seat_taken: "invBatch.issue.seat_taken",
  duplicate_seat: "invBatch.issue.duplicate_seat",
  unknown_category: "invBatch.issue.unknown_category",
  ambiguous_category: "invBatch.issue.ambiguous_category",
  category_not_ga: "invBatch.issue.category_not_ga",
  invalid_qty: "invBatch.issue.invalid_qty",
  not_enough_places: "invBatch.issue.not_enough_places",
  too_many_places: "invBatch.issue.too_many_places",
  too_many_guests: "invBatch.issue.too_many_guests",
  seat_without_category: "invBatch.issue.seat_without_category",
  capacity_exceeded: "invBatch.issue.capacity_exceeded",
  // The refusals of the transaction carry the code of the single-invitation route.
  "invitation.seats_taken": "invBatch.issue.seat_taken",
  "invitation.not_enough_places": "invBatch.issue.not_enough_places",
  "invitation.capacity_exceeded": "invBatch.issue.capacity_exceeded",
};

export function issueTextKey(code: string): MessageKey {
  return Object.prototype.hasOwnProperty.call(ISSUE_TEXT, code) ? ISSUE_TEXT[code] : "invBatch.issue.other";
}

/** The column names the person sees in the table: only the ones the file has. */
export function issueField(field: string): string {
  return (CSV_COLUMNS as readonly { name: string }[]).some((c) => c.name === field) ? field : "";
}

// ─── The billing preview ─────────────────────────────────────────────────────

export interface ChargeView {
  readonly mode: "free" | "standard" | "bulk" | "other";
  readonly currency: string;
  readonly amountMinor: number;
  readonly tickets: number;
  readonly freeTickets: number;
  readonly chargedTickets: number;
  readonly lines: readonly { tickets: number; unitMinor: number; amountMinor: number }[];
  /** What is left of the yearly free limit, or null when there is no such limit. */
  readonly freeLeft: number | null;
}

const num = (x: unknown): number | null => (typeof x === "number" && Number.isFinite(x) ? x : null);

/**
 * The charge preview as numbers, so the screen builds its own sentence in the
 * person's language (the server's `text` is English). Null when the server
 * gave none or it has no usable amount and currency.
 */
export function chargeView(charge: unknown): ChargeView | null {
  if (charge === null || typeof charge !== "object") return null;
  const c = charge as Record<string, unknown>;
  const amountMinor = num(c.amount_minor);
  if (amountMinor === null || typeof c.currency !== "string" || c.currency === "") return null;
  const lines = Array.isArray(c.lines)
    ? c.lines.flatMap((l) => {
        if (l === null || typeof l !== "object") return [];
        const x = l as Record<string, unknown>;
        const tickets = num(x.tickets);
        const unit = num(x.unit_minor);
        const amount = num(x.amount_minor);
        return tickets !== null && unit !== null && amount !== null ? [{ tickets, unitMinor: unit, amountMinor: amount }] : [];
      })
    : [];
  const mode = c.mode === "free" || c.mode === "standard" || c.mode === "bulk" ? c.mode : "other";
  return {
    mode,
    currency: c.currency,
    amountMinor,
    tickets: num(c.tickets) ?? 0,
    freeTickets: num(c.free_tickets) ?? 0,
    chargedTickets: num(c.charged_tickets) ?? 0,
    lines,
    freeLeft: num(c.free_left),
  };
}
