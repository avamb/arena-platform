/**
 * Paid modules of an organization, the superadmin side (spec 08_architecture/61, section 4.4 and 6).
 * Served by the module routes of the backend:
 *
 *   GET  /v1/admin/organizations/{org_id}/modules
 *   POST /v1/admin/organizations/{org_id}/modules/shop        connect the shop
 *   POST /v1/admin/org-modules/{id}/end                       set the last day
 *   POST /v1/admin/org-modules/{id}/reprice                   change the monthly price
 *   GET  /v1/organizations/{org_id}/shop/settings             the domain lives here
 *   PUT  /v1/organizations/{org_id}/shop/settings
 *
 * A period is ended, never edited or deleted. Dates are UTC calendar days and only go forward: a start or a
 * new price is not before today, a last day not before yesterday (422 `org_module.starts_on_in_past`,
 * `from_in_past`, `ends_on_in_past`), so a month that was already charged is never rewritten.
 *
 * Every write carries X-Admin-Reason; the dialogs ask for it in their own field and pass it here, so it is
 * the reason the person just wrote and not one cached from the session.
 *
 * Mock data: NONE.
 */
import type { components } from "@openapi/index";
import { ApiError, authedFetch } from "@/lib/api/client";

type Schemas = components["schemas"];

export type OrgModulePeriod = Schemas["OrgModulePeriod"];
export type OrgModuleShopState = Schemas["OrgModuleShopState"];
export type AdminOrgModulesResponse = Schemas["AdminOrgModulesResponse"];
export type ConnectShopModuleRequest = Schemas["ConnectShopModuleRequest"];
export type EndOrgModuleRequest = Schemas["EndOrgModuleRequest"];
export type RepriceOrgModuleRequest = Schemas["RepriceOrgModuleRequest"];
export type ShopSettings = Schemas["ShopSettings"];
export type ShopSettingsUpdateRequest = Schemas["ShopSettingsUpdateRequest"];

const enc = encodeURIComponent;

export function getOrgModules(orgId: string): Promise<AdminOrgModulesResponse> {
  return authedFetch({ method: "GET", path: `/v1/admin/organizations/${enc(orgId)}/modules` });
}

export function connectShopModule(orgId: string, body: ConnectShopModuleRequest, reason: string): Promise<OrgModulePeriod> {
  return authedFetch({ method: "POST", path: `/v1/admin/organizations/${enc(orgId)}/modules/shop`, body, adminReason: reason });
}

export function endOrgModule(periodId: string, body: EndOrgModuleRequest, reason: string): Promise<OrgModulePeriod> {
  return authedFetch({ method: "POST", path: `/v1/admin/org-modules/${enc(periodId)}/end`, body, adminReason: reason });
}

export function repriceOrgModule(periodId: string, body: RepriceOrgModuleRequest, reason: string): Promise<OrgModulePeriod> {
  return authedFetch({ method: "POST", path: `/v1/admin/org-modules/${enc(periodId)}/reprice`, body, adminReason: reason });
}

export function getShopSettings(orgId: string, reason?: string): Promise<ShopSettings> {
  return authedFetch({ method: "GET", path: `/v1/organizations/${enc(orgId)}/shop/settings`, ...(reason !== undefined ? { adminReason: reason } : {}) });
}

export function putShopSettings(orgId: string, body: ShopSettingsUpdateRequest, reason: string): Promise<ShopSettings> {
  return authedFetch({ method: "PUT", path: `/v1/organizations/${enc(orgId)}/shop/settings`, body, adminReason: reason });
}

// ---------------------------------------------------------------------------
// Days
// ---------------------------------------------------------------------------

const DATE_RE = /^\d{4}-\d{2}-\d{2}$/;

/** Today as a UTC calendar day, YYYY-MM-DD. */
export function todayUTC(now: Date = new Date()): string {
  return now.toISOString().slice(0, 10);
}

/** The UTC day `n` days from `day` (YYYY-MM-DD). */
export function addDays(day: string, n: number): string {
  const d = new Date(`${day}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() + n);
  return d.toISOString().slice(0, 10);
}

// ---------------------------------------------------------------------------
// The shop card: what the superadmin reads
// ---------------------------------------------------------------------------

export type ShopPeriodState =
  | { kind: "never" }
  | { kind: "connected"; since: string; until: string | null }
  | { kind: "scheduled"; from: string; until: string | null }
  | { kind: "disconnected"; since: string; lastDay: string };

/** The shop periods, oldest first. */
export function shopPeriods(items: readonly OrgModulePeriod[]): OrgModulePeriod[] {
  return items.filter((p) => p.module === "shop").sort((a, b) => a.starts_on.localeCompare(b.starts_on));
}

/**
 * The period the actions work on: the one in force, else the next one that starts. null when there is none
 * (never connected, or every period ended).
 */
export function currentPeriod(items: readonly OrgModulePeriod[]): OrgModulePeriod | null {
  const periods = shopPeriods(items);
  return periods.find((p) => p.status === "in_force") ?? periods.find((p) => p.status === "scheduled") ?? null;
}

/** "connected since ...", "disconnected since ...", "never connected". */
export function shopPeriodState(items: readonly OrgModulePeriod[]): ShopPeriodState {
  const periods = shopPeriods(items);
  const now = periods.find((p) => p.status === "in_force");
  if (now !== undefined) {
    // A run of periods that continue each other (a price change) is one connection: go back to its start.
    let since = now.starts_on;
    for (let i = periods.indexOf(now) - 1; i >= 0; i--) {
      const p = periods[i];
      if (p.ends_on != null && addDays(p.ends_on, 1) === since) since = p.starts_on;
      else break;
    }
    return { kind: "connected", since, until: now.ends_on ?? null };
  }
  const next = periods.find((p) => p.status === "scheduled");
  if (next !== undefined) return { kind: "scheduled", from: next.starts_on, until: next.ends_on ?? null };
  const last = periods.filter((p) => p.status === "ended").pop();
  if (last !== undefined && last.ends_on != null) return { kind: "disconnected", since: addDays(last.ends_on, 1), lastDay: last.ends_on };
  return { kind: "never" };
}

/** Connect is offered while no period without an end is in force or waiting (the server judges overlaps). */
export function canConnect(items: readonly OrgModulePeriod[]): boolean {
  return !shopPeriods(items).some((p) => p.status !== "ended" && p.ends_on == null);
}

/** Reprice works on the current period. */
export function canReprice(items: readonly OrgModulePeriod[]): boolean {
  return currentPeriod(items) !== null;
}

/** Disconnect works on a current period that is open-ended or ends today or later. */
export function canEnd(items: readonly OrgModulePeriod[], now: Date = new Date()): boolean {
  const p = currentPeriod(items);
  return p !== null && (p.ends_on == null || p.ends_on >= todayUTC(now));
}

/** The monthly price of a period as the superadmin reads it: "Price list", "Free" or "19.00 EUR". */
export function priceLabel(minor: number | null | undefined, currency?: string | null): string {
  if (minor == null) return "Price list";
  if (minor === 0) return "Free";
  const amount = (minor / 100).toFixed(2);
  return currency ? `${amount} ${currency}` : amount;
}

/** The price that applies today, with where it comes from. */
export function effectivePriceLabel(shop: Pick<OrgModuleShopState, "effective_monthly_minor" | "currency" | "price_source">): string {
  if (shop.effective_monthly_minor == null) return "Not known";
  const amount = shop.effective_monthly_minor === 0 ? "Free" : priceLabel(shop.effective_monthly_minor, shop.currency);
  if (shop.price_source === "individual") return `${amount} (individual price)`;
  if (shop.price_source === "price_list") return `${amount} (price list)`;
  return amount;
}

export function periodStatusLabel(status: string): string {
  switch (status) {
    case "scheduled":
      return "Not started";
    case "in_force":
      return "In force";
    case "ended":
      return "Ended";
    default:
      return status;
  }
}

export function shopModeLabel(mode: string | null | undefined): string {
  if (mode === "full") return "Full shop";
  if (mode === "orders_only") return "Orders only";
  return "No shop section";
}

// ---------------------------------------------------------------------------
// Forms
// ---------------------------------------------------------------------------

export type PricingChoice = "price_list" | "individual" | "free";

export interface PricingFields {
  pricing: PricingChoice;
  /** Major units, only for "individual". */
  amount: string;
  /** ISO 4217, optional: empty is the organization's billing currency. */
  currency: string;
}

export interface ConnectForm extends PricingFields {
  startsOn: string;
  reason: string;
}

export interface RepriceForm extends PricingFields {
  from: string;
  reason: string;
}

export interface EndForm {
  endsOn: string;
  reason: string;
}

export function emptyConnectForm(now: Date = new Date()): ConnectForm {
  return { startsOn: todayUTC(now), pricing: "price_list", amount: "", currency: "", reason: "" };
}

export function emptyRepriceForm(period: Pick<OrgModulePeriod, "starts_on">, now: Date = new Date()): RepriceForm {
  const today = todayUTC(now);
  return { from: period.starts_on > today ? period.starts_on : today, pricing: "price_list", amount: "", currency: "", reason: "" };
}

export function emptyEndForm(now: Date = new Date()): EndForm {
  return { endsOn: todayUTC(now), reason: "" };
}

export type Built<T> = { ok: true; request: T; reason: string } | { ok: false; errors: string[] };

interface PriceBody {
  monthly_price_minor?: number;
  currency?: string;
}

/** The price part of a request, or the problems with what was typed. */
function priceOf(f: PricingFields, errors: string[]): PriceBody {
  if (f.pricing === "price_list") return {};
  if (f.pricing === "free") return { monthly_price_minor: 0 };
  const amount = f.amount.trim().replace(",", ".");
  const currency = f.currency.trim().toUpperCase();
  if (!/^\d{1,7}(\.\d{1,2})?$/.test(amount)) errors.push("Price for each month: type a number, for example 19 or 19.90.");
  else if (Number(amount) <= 0) errors.push("Price for each month: type a number more than 0, or select Free.");
  if (currency !== "" && !/^[A-Z]{3}$/.test(currency)) errors.push("Currency: type a code, for example EUR. If the field is empty, the platform uses the billing currency.");
  if (errors.length > 0) return {};
  const body: PriceBody = { monthly_price_minor: Math.round(Number(amount) * 100) };
  if (currency !== "") body.currency = currency;
  return body;
}

function reasonProblem(reason: string): string | null {
  return reason.trim() === "" ? "Reason: type the reason. The platform saves it in the audit log." : null;
}

/** Checks the "connect" form. The server stays the judge; this catches what can be fixed before sending. */
export function buildConnectRequest(form: ConnectForm, now: Date = new Date()): Built<ConnectShopModuleRequest> {
  const errors: string[] = [];
  const startsOn = form.startsOn.trim();
  if (!DATE_RE.test(startsOn)) errors.push("First day: select a date.");
  else if (startsOn < todayUTC(now)) errors.push("First day: select today or a day after today (UTC). The platform does not accept a day before today.");
  const price = priceOf(form, errors);
  const rp = reasonProblem(form.reason);
  if (rp !== null) errors.push(rp);
  if (errors.length > 0) return { ok: false, errors };
  const reason = form.reason.trim();
  return { ok: true, reason, request: { starts_on: startsOn, ...price, reason } };
}

/** Checks the "change the price" form. `period` is the one being repriced. */
export function buildRepriceRequest(form: RepriceForm, period: Pick<OrgModulePeriod, "starts_on">, now: Date = new Date()): Built<RepriceOrgModuleRequest> {
  const errors: string[] = [];
  const from = form.from.trim();
  if (!DATE_RE.test(from)) errors.push("From: select a date.");
  else if (from < todayUTC(now)) errors.push("From: select today or a day after today (UTC). The platform does not accept a day before today.");
  else if (from < period.starts_on) errors.push(`From: the period starts on ${period.starts_on}. Select that day or a day after it.`);
  const price = priceOf(form, errors);
  const rp = reasonProblem(form.reason);
  if (rp !== null) errors.push(rp);
  if (errors.length > 0) return { ok: false, errors };
  const reason = form.reason.trim();
  return { ok: true, reason, request: { from, ...price, reason } };
}

/** Checks the "disconnect" form. `period` is the one being ended. */
export function buildEndRequest(form: EndForm, period: Pick<OrgModulePeriod, "starts_on">, now: Date = new Date()): Built<EndOrgModuleRequest> {
  const errors: string[] = [];
  const endsOn = form.endsOn.trim();
  if (!DATE_RE.test(endsOn)) errors.push("Last day: select a date.");
  else if (endsOn < addDays(todayUTC(now), -1)) errors.push("Last day: select yesterday or a day after it (UTC). Days that we charged stay charged.");
  else if (endsOn < addDays(period.starts_on, -1)) errors.push(`Last day: the period starts on ${period.starts_on}. Select the day before it, or a day after it.`);
  const rp = reasonProblem(form.reason);
  if (rp !== null) errors.push(rp);
  if (errors.length > 0) return { ok: false, errors };
  const reason = form.reason.trim();
  return { ok: true, reason, request: { ends_on: endsOn, reason } };
}

// ---------------------------------------------------------------------------
// The domain: the existing PUT .../shop/settings, which replaces the WHOLE row
// ---------------------------------------------------------------------------

/**
 * The body of PUT .../shop/settings that changes the domain and nothing else: every other field is sent back
 * as it was read, `enabled` is left out (absent keeps it), and the version read goes back as
 * `expected_updated_at`, so a save by somebody else in between is refused (409 `shop.settings_changed`)
 * instead of overwritten. An empty domain clears it.
 */
export function domainUpdateBody(settings: ShopSettings, domain: string): ShopSettingsUpdateRequest {
  const body: ShopSettingsUpdateRequest = {
    currency: settings.currency,
    shipping_fee_minor: settings.shipping_fee_minor,
    shipping_label: settings.shipping_label,
    country_iso2: settings.country_iso2 ?? null,
    age_gate_min_age: settings.age_gate_min_age ?? null,
    announcement_text: settings.announcement_text,
    contact_email: settings.contact_email ?? null,
    logistics_note: settings.logistics_note,
    sales_channel_id: settings.sales_channel_id ?? null,
    shop_domain: domain.trim(),
    max_line_qty: settings.max_line_qty,
  };
  if (settings.updated_at != null) body.expected_updated_at = settings.updated_at;
  return body;
}

/** A host name as the API takes it: no scheme, no path, lower case letters, digits, dots and hyphens. */
export function checkDomain(domain: string): string | null {
  const d = domain.trim();
  if (d === "") return null;
  if (d.length > 253 || !/^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/i.test(d)) {
    return "Domain: type a host name, for example shop.example.com. Do not type https:// or a path. To remove the domain, keep the field empty.";
  }
  return null;
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

/** What a refused call means for the superadmin, in plain words. */
export function moduleErrorMessage(err: unknown): string {
  if (!(err instanceof ApiError)) return err instanceof Error ? err.message : "We did not complete the request. Please try again.";
  switch (err.code) {
    case "org_module.starts_on_in_past":
      return "The first day is before today. Select today or a day after today (UTC). The platform does not change a month that it charged.";
    case "org_module.from_in_past":
      return "The new price cannot start before today. Select today or a day after today (UTC). The platform does not change a month that it charged.";
    case "org_module.ends_on_in_past":
      return "The last day is before yesterday. Select yesterday or a day after it (UTC). Days that we charged stay charged.";
    case "org_module.overlap":
      return "The organization has the module on some of these days. End the active period first, or select different days.";
    case "org_module.already_ended":
      return "The period ends on or before that day.";
    case "org_module.invalid":
      return "We did not accept a date, the price or the currency. The currency of an individual price must be the billing currency of the organization. The first day cannot be after the last day.";
    case "org_module.superadmin_only":
      return "Only a platform superadmin can change modules.";
    case "org_module.not_found":
      return "We did not find the period. Reload the page.";
    case "org_module.organization_not_found":
      return "We did not find the organization.";
    case "superadmin.missing_reason":
      return "Type the reason. The platform saves it in the audit log.";
    case "shop.duplicate_domain":
      return "A different organization uses this domain.";
    case "shop.invalid_domain":
      return "The API does not accept this host name. Type a name such as shop.example.com.";
    case "shop.settings_changed":
      return "A different person saved the shop settings while this form was open. Close the form and open it again.";
    case "shop.module_not_enabled":
      return "The shop module is not connected today. Connect the module first. The shop settings stay closed until then.";
    default:
      return err.message;
  }
}
