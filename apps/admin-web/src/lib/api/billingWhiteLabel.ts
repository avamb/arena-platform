/**
 * White label periods — the superadmin tab of the price screen (spec
 * 08_architecture/53). Served by hbilling/admin_white_label.go:
 *
 *   GET  /v1/admin/billing/white-label[?org_id=]
 *   POST /v1/admin/billing/white-label
 *   POST /v1/admin/billing/white-label/{id}/end
 *
 * A period hides the platform badge of one brand (the organization, or one of
 * its promoters) from starts_on to ends_on, both UTC calendar days. The job
 * billing.white_label charges every completed month, prorated by days, at the
 * price list's monthly rate or at the period's individual price. Writes need
 * X-Admin-Reason (reason.ts asks for it) and are audited on the server.
 *
 * Mock data: NONE.
 */
import type { components } from "@openapi/index";
import { authedFetch } from "@/lib/api/client";

type Schemas = components["schemas"];

export type WhiteLabelPeriod = Schemas["BillingWhiteLabelPeriod"];
export type CreateWhiteLabelRequest = Schemas["BillingCreateWhiteLabelRequest"];

const BASE = "/v1/admin/billing/white-label";

export function listWhiteLabel(): Promise<{ items: WhiteLabelPeriod[] }> {
  return authedFetch({ method: "GET", path: BASE });
}

export function createWhiteLabel(req: CreateWhiteLabelRequest): Promise<WhiteLabelPeriod> {
  return authedFetch({ method: "POST", path: BASE, body: req });
}

export function endWhiteLabel(id: string, endsOn: string): Promise<WhiteLabelPeriod> {
  return authedFetch({ method: "POST", path: `${BASE}/${encodeURIComponent(id)}/end`, body: { ends_on: endsOn } });
}

/** What the operator types in the "turn white label on" dialog. */
export interface WhiteLabelForm {
  orgId: string;
  promoterId: string;
  startsOn: string;
  endsOn: string;
  /** "" = the price list; otherwise major units, e.g. "45" or "0". */
  monthlyPrice: string;
  /**
   * The currency the operator means for the price (ISO 4217, e.g. "EUR").
   * Optional: the server stores the organization's billing currency and
   * refuses a different one, so a price typed as euros is never billed as
   * dollars.
   */
  currency: string;
}

export function emptyWhiteLabelForm(): WhiteLabelForm {
  return { orgId: "", promoterId: "", startsOn: "", endsOn: "", monthlyPrice: "", currency: "" };
}

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const DATE_RE = /^\d{4}-\d{2}-\d{2}$/;

/** Today as a UTC calendar day, YYYY-MM-DD. */
export function todayUTC(now: Date = new Date()): string {
  return now.toISOString().slice(0, 10);
}

export type BuildResult = { ok: true; request: CreateWhiteLabelRequest } | { ok: false; errors: string[] };

/**
 * Checks the form and builds the request. The server stays the judge; this
 * only catches what the operator can fix before sending. The price is typed
 * in major units of the organization's billing currency and sent in minor
 * units (two decimals).
 */
export function buildWhiteLabelRequest(form: WhiteLabelForm, now: Date = new Date()): BuildResult {
  const errors: string[] = [];
  const orgId = form.orgId.trim();
  const promoterId = form.promoterId.trim();
  const startsOn = form.startsOn.trim();
  const endsOn = form.endsOn.trim();
  const price = form.monthlyPrice.trim().replace(",", ".");
  const currency = form.currency.trim().toUpperCase();
  if (!UUID_RE.test(orgId)) errors.push("Organization: choose an organization.");
  if (promoterId !== "" && !UUID_RE.test(promoterId)) errors.push("Promoter: paste the promoter's id, or leave it empty for the organization itself.");
  if (!DATE_RE.test(startsOn)) errors.push("First day: pick a date.");
  else if (startsOn < todayUTC(now)) errors.push("First day: pick today or a later day (UTC).");
  if (endsOn !== "") {
    if (!DATE_RE.test(endsOn)) errors.push("Last day: pick a date, or leave it empty.");
    else if (DATE_RE.test(startsOn) && endsOn < startsOn) errors.push("Last day: pick the first day or a later day.");
  }
  let monthly: number | undefined;
  if (price !== "") {
    if (!/^\d{1,7}(\.\d{1,2})?$/.test(price)) errors.push("Monthly price: type an amount such as 45 or 0, or leave it empty for the price list.");
    else monthly = Math.round(Number(price) * 100);
  }
  if (currency !== "" && !/^[A-Z]{3}$/.test(currency)) errors.push("Currency: type a code such as EUR or USD, or leave it empty.");
  if (errors.length > 0) return { ok: false, errors };
  const request: CreateWhiteLabelRequest = { org_id: orgId, starts_on: startsOn };
  if (promoterId !== "") request.promoter_id = promoterId;
  if (endsOn !== "") request.ends_on = endsOn;
  if (monthly !== undefined) {
    request.monthly_price_minor = monthly;
    if (currency !== "") request.currency = currency;
  }
  return { ok: true, request };
}

/** Whether a period can still be ended: it is not over yet. */
export function canEndWhiteLabel(p: WhiteLabelPeriod, now: Date = new Date()): boolean {
  if (p.status === "ended") return false;
  return p.ends_on == null || p.ends_on >= todayUTC(now);
}

export function whiteLabelStatusLabel(status: string): string {
  switch (status) {
    case "scheduled":
      return "Starts later";
    case "in_force":
      return "On";
    case "ended":
      return "Ended";
    default:
      return status;
  }
}

/** The monthly price as the operator reads it: "price list", "free" or "45.00 EUR". */
export function monthlyPriceLabel(minor: number | null | undefined, currency?: string | null): string {
  if (minor == null) return "Price list";
  if (minor === 0) return "Free";
  const amount = (minor / 100).toFixed(2);
  return currency ? `${amount} ${currency}` : amount;
}
