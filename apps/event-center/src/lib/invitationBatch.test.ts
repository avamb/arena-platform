import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/http";
import type { InvitationBatchReport } from "@/lib/api/types";
import {
  CSV_COLUMNS,
  MAX_BATCH_BYTES,
  batchAttemptFor,
  batchError,
  canIssue,
  chargeView,
  csvErrorLine,
  exampleCsv,
  fileProblem,
  isReport,
  isTooBig,
  issueField,
  issueTextKey,
  reportOfError,
  shownIssues,
  splitIssues,
} from "@/lib/invitationBatch";
import { CATALOGS } from "@/lib/i18n";
import { apiErrorStem } from "@/lib/errors";

const report = (over: Partial<InvitationBatchReport> = {}): InvitationBatchReport => ({
  dry_run: true,
  valid: true,
  delivery: "pdf_only",
  rows: 2,
  guests: 1,
  places: 2,
  seats: 2,
  ga_places: 0,
  from_reserve: 0,
  errors: [],
  preview: [],
  ...over,
});

describe("fileProblem", () => {
  it("lets a normal CSV through", () => {
    expect(fileProblem({ name: "guests.csv", size: 1200 })).toBeNull();
    expect(fileProblem({ name: "guests.csv", size: MAX_BATCH_BYTES })).toBeNull();
  });
  it("refuses an empty file, one over 512 KB and a spreadsheet", () => {
    expect(fileProblem({ name: "a.csv", size: 0 })).toBe("empty");
    expect(fileProblem({ name: "a.csv", size: MAX_BATCH_BYTES + 1 })).toBe("tooLarge");
    expect(fileProblem({ name: "guests.XLSX", size: 100 })).toBe("spreadsheet");
  });
});

describe("exampleCsv", () => {
  it("starts with a byte order mark and a header that holds only documented columns", () => {
    const text = exampleCsv();
    expect(text.charCodeAt(0)).toBe(0xfeff);
    const header = text.slice(1).split("\r\n")[0].split(",");
    const known = CSV_COLUMNS.map((c) => c.name) as string[];
    for (const h of header) expect(known, h).toContain(h);
    expect(header).toContain("name");
  });
  it("has the same number of cells in every row", () => {
    const rows = exampleCsv().slice(1).trim().split("\r\n");
    const width = rows[0].split(",").length;
    for (const r of rows) expect(r.split(",").length).toBe(width);
  });
});

describe("batchAttemptFor", () => {
  let n = 0;
  const make = () => `id-${++n}`;
  it("a retry of the same pick and delivery keeps the id", () => {
    const first = batchAttemptFor(null, 1, "pdf_only", make);
    expect(batchAttemptFor(first, 1, "pdf_only", make)).toBe(first);
  });
  it("another delivery or another pick of the file is a new request", () => {
    const first = batchAttemptFor(null, 1, "pdf_only", make);
    expect(batchAttemptFor(first, 1, "my_email", make).id).not.toBe(first.id);
    expect(batchAttemptFor(first, 2, "pdf_only", make).id).not.toBe(first.id);
  });
});

describe("reading a refusal", () => {
  const rep = report({ valid: false, errors: [{ row: 3, field: "email", code: "invalid_email", message: "x" }] });
  it("takes the report out of details", () => {
    const err = new ApiError(422, "invitation_batch.invalid", "m", undefined, { report: rep });
    expect(reportOfError(err)).toEqual(rep);
    expect(reportOfError(new ApiError(422, "invitation_batch.invalid", "m", undefined, { report: { x: 1 } }))).toBeNull();
    expect(reportOfError(new ApiError(500, "x", "m"))).toBeNull();
    expect(reportOfError(new Error("x"))).toBeNull();
  });
  it("isReport tells a report from a batch", () => {
    expect(isReport(rep)).toBe(true);
    expect(isReport({ id: "b", orders: [], guests: 1, places: 1 })).toBe(false);
  });
  it("reads the line of a broken CSV", () => {
    expect(csvErrorLine(new ApiError(400, "invitation_batch.invalid_csv", "m", undefined, { line: 7 }))).toBe(7);
    expect(csvErrorLine(new ApiError(400, "invitation_batch.invalid_csv", "m"))).toBeNull();
    expect(csvErrorLine(new ApiError(400, "other", "m", undefined, { line: 7 }))).toBeNull();
  });
  it("knows the over-cap refusals and names the general payload limit as the same", () => {
    expect(isTooBig(new ApiError(422, "invitation_batch.too_many_places", "m"))).toBe(true);
    expect(isTooBig(new ApiError(422, "invitation_batch.too_many_rows", "m"))).toBe(true);
    expect(isTooBig(new ApiError(413, "http.payload_too_large", "m"))).toBe(true);
    expect(isTooBig(new ApiError(422, "invitation_batch.invalid", "m"))).toBe(false);
    const shown = batchError(new ApiError(413, "http.payload_too_large", "m")) as ApiError;
    expect(shown.code).toBe("invitation_batch.file_too_large");
    const other = new ApiError(422, "invitation_batch.invalid", "m");
    expect(batchError(other)).toBe(other);
  });
});

describe("the check table", () => {
  it("splits file-level problems from rows and orders rows by line", () => {
    const r = report({
      valid: false,
      errors: [
        { row: 9, field: "seat", code: "seat_taken", message: "" },
        { row: 0, field: "", code: "too_many_places", message: "" },
        { row: 4, field: "email", code: "invalid_email", message: "" },
      ],
    });
    const { file, rows } = splitIssues(r);
    expect(file.map((i) => i.code)).toEqual(["too_many_places"]);
    expect(rows.map((i) => i.row)).toEqual([4, 9]);
  });
  it("shows at most the cap and counts the rest", () => {
    expect(shownIssues([1, 2, 3], 2)).toEqual({ shown: [1, 2], hidden: 1 });
    expect(shownIssues([1], 5)).toEqual({ shown: [1], hidden: 0 });
  });
  it("issues only a valid file that hands out places", () => {
    expect(canIssue(null)).toBe(false);
    expect(canIssue(report())).toBe(true);
    expect(canIssue(report({ valid: false }))).toBe(false);
    expect(canIssue(report({ errors: [{ row: 2, field: "", code: "x", message: "" }] }))).toBe(false);
    expect(canIssue(report({ places: 0 }))).toBe(false);
  });
  it("names a column only when it is one of the file's", () => {
    expect(issueField("email")).toBe("email");
    expect(issueField("whatever")).toBe("");
  });
  it("gives every known code a text in every language and the general one to an unknown code", () => {
    const codes = ["name_required", "invalid_email", "email_required", "place_missing", "place_ambiguous", "unknown_seat", "ambiguous_seat", "seat_taken", "duplicate_seat", "unknown_category", "ambiguous_category", "category_not_ga", "invalid_qty", "not_enough_places", "too_many_places", "too_many_guests", "seat_without_category", "capacity_exceeded", "invitation.seats_taken"];
    for (const c of codes) {
      const key = issueTextKey(c);
      expect(key, c).not.toBe("invBatch.issue.other");
      for (const loc of ["en", "ru", "es"] as const) expect(CATALOGS[loc][key].length, `${loc} ${c}`).toBeGreaterThan(0);
    }
    expect(issueTextKey("something_new")).toBe("invBatch.issue.other");
    expect(issueTextKey("constructor")).toBe("invBatch.issue.other");
  });
});

describe("chargeView", () => {
  it("reads the numbers of a bulk charge", () => {
    const v = chargeView({ mode: "bulk", currency: "EUR", tickets: 12, free_tickets: 0, charged_tickets: 12, amount_minor: 600, lines: [{ tickets: 12, unit_minor: 50, amount_minor: 600, currency: "EUR" }], free_limit: null, free_left: null, text: "12 × 0.50 EUR = 6.00 EUR" });
    expect(v).toMatchObject({ mode: "bulk", currency: "EUR", amountMinor: 600, chargedTickets: 12, freeLeft: null });
    expect(v?.lines).toEqual([{ tickets: 12, unitMinor: 50, amountMinor: 600 }]);
  });
  it("reads a free charge with what is left of the limit", () => {
    expect(chargeView({ mode: "free", currency: "EUR", amount_minor: 0, free_left: 488 })).toMatchObject({ mode: "free", freeLeft: 488, amountMinor: 0 });
  });
  it("is null when there is nothing usable", () => {
    expect(chargeView(null)).toBeNull();
    expect(chargeView({ amount_minor: 5 })).toBeNull();
    expect(chargeView({ currency: "EUR" })).toBeNull();
    expect(chargeView("x")).toBeNull();
  });
});

describe("error texts of the upload", () => {
  const stem = (status: number, code: string) => apiErrorStem(new ApiError(status, code, "m"));
  it("maps the refusals of the route to their own texts", () => {
    expect(stem(422, "invitation_batch.too_many_places")).toBe("batchTooBig");
    expect(stem(422, "invitation_batch.too_many_rows")).toBe("batchTooBig");
    expect(stem(413, "invitation_batch.file_too_large")).toBe("batchTooBig");
    expect(stem(400, "invitation_batch.invalid_csv")).toBe("batchCsvBroken");
    expect(stem(400, "invitation_batch.invalid_encoding")).toBe("batchEncoding");
    expect(stem(422, "invitation_batch.invalid_header")).toBe("batchHeader");
    expect(stem(422, "invitation_batch.invalid")).toBe("batchInvalid");
    expect(stem(503, "invitation_batch.timeout")).toBe("batchTimeout");
    expect(stem(409, "invitation_batch.request_id_reused")).toBe("invitationRequestReused");
    expect(stem(404, "invitation_batch.not_found")).toBe("notFound");
  });
});
