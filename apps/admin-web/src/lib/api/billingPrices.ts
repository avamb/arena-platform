/**
 * Price lists and rates — the superadmin screen of billing wave B-A (task
 * B1-5, spec 08_architecture/42_billing_execution_spec_ru.md card B1-5).
 *
 * Served by apps/backend/internal/platform/httpserver/hbilling (admin*.go),
 * all under /v1/admin/billing, billing.admin AND a platform superadmin:
 *
 *   GET  /price-lists[?code=&currency=]        GET /price-lists/{id}
 *   POST /price-lists                          POST /price-lists/{id}/revoke
 *   GET  /rate-overrides[?org_id=&currency=]   POST /rate-overrides
 *   POST /rate-overrides/{id}/close            DELETE /rate-overrides/{id}
 *   GET  /accounts[?org_id=]                   POST /accounts/{id}/terms
 *
 * Every write needs X-Admin-Reason (reason.ts asks the operator once per tab)
 * and writes an audit row on the server.
 *
 * Rules of the screen, mirrored here only to hide buttons that would answer
 * 409 (the server stays the judge): a version is never edited, a new price is
 * a new version that starts in the future, a version can be revoked only
 * before it starts, an individual rate is closed for the future or removed
 * before it starts. There is NO default number anywhere in this file: every
 * rate, threshold and cap is typed by the operator, or copied from an
 * existing version by the "start from" button.
 *
 * Mock data: NONE.
 */
import type { components } from "@openapi/index";
import { authedFetch } from "@/lib/api/client";

type Schemas = components["schemas"];

export type PriceListVersion = Schemas["BillingPriceListVersion"];
export type PriceListDetail = Schemas["BillingPriceListDetail"];
export type PriceListRule = Schemas["BillingPriceListRule"];
export type PriceListPack = Schemas["BillingPriceListPack"];
export type RateOverride = Schemas["BillingRateOverride"];
export type BillingAccount = Schemas["BillingAdminAccount"];
export type AccountTerms = Schemas["BillingAccountTerms"];
export type CreatePriceListRequest = Schemas["BillingCreatePriceListRequest"];
export type CreateRateOverrideRequest = Schemas["BillingCreateRateOverrideRequest"];
export type CreateAccountTermsRequest = Schemas["BillingCreateAccountTermsRequest"];

const BASE = "/v1/admin/billing";

// ---------------------------------------------------------------------------
// Requests
// ---------------------------------------------------------------------------

export function listPriceLists(): Promise<{ items: PriceListVersion[] }> {
  return authedFetch({ method: "GET", path: `${BASE}/price-lists` });
}

export function getPriceList(id: string): Promise<PriceListDetail> {
  return authedFetch({ method: "GET", path: `${BASE}/price-lists/${encodeURIComponent(id)}` });
}

export function createPriceList(req: CreatePriceListRequest): Promise<PriceListDetail> {
  return authedFetch({ method: "POST", path: `${BASE}/price-lists`, body: req });
}

export function revokePriceList(id: string): Promise<PriceListDetail> {
  return authedFetch({ method: "POST", path: `${BASE}/price-lists/${encodeURIComponent(id)}/revoke` });
}

export function listRateOverrides(): Promise<{ items: RateOverride[] }> {
  return authedFetch({ method: "GET", path: `${BASE}/rate-overrides` });
}

export function createRateOverride(req: CreateRateOverrideRequest): Promise<RateOverride> {
  return authedFetch({ method: "POST", path: `${BASE}/rate-overrides`, body: req });
}

export function closeRateOverride(id: string, effectiveTo: string): Promise<RateOverride> {
  return authedFetch({
    method: "POST",
    path: `${BASE}/rate-overrides/${encodeURIComponent(id)}/close`,
    body: { effective_to: effectiveTo },
  });
}

export function deleteRateOverride(id: string): Promise<void> {
  return authedFetch({ method: "DELETE", path: `${BASE}/rate-overrides/${encodeURIComponent(id)}` });
}

export function listBillingAccounts(): Promise<{ items: BillingAccount[] }> {
  return authedFetch({ method: "GET", path: `${BASE}/accounts` });
}

export function createAccountTerms(accountId: string, req: CreateAccountTermsRequest): Promise<AccountTerms> {
  return authedFetch({
    method: "POST",
    path: `${BASE}/accounts/${encodeURIComponent(accountId)}/terms`,
    body: req,
  });
}

// ---------------------------------------------------------------------------
// Status helpers (the server computes `status`; these only choose buttons)
// ---------------------------------------------------------------------------

export type Tone = "good" | "warn" | "bad" | "info" | "muted";

export function versionStatusLabel(status: string): string {
  switch (status) {
    case "scheduled":
      return "Scheduled";
    case "in_force":
      return "In force";
    case "superseded":
      return "Superseded";
    case "revoked":
      return "Revoked";
    default:
      return status;
  }
}

export function versionStatusTone(status: string): Tone {
  switch (status) {
    case "in_force":
      return "good";
    case "scheduled":
      return "info";
    case "revoked":
      return "bad";
    default:
      return "muted";
  }
}

/** A version can be revoked only before it starts. */
export function canRevokeVersion(v: Pick<PriceListVersion, "status">): boolean {
  return v.status === "scheduled";
}

export function rateStatusLabel(status: string): string {
  switch (status) {
    case "scheduled":
      return "Scheduled";
    case "in_force":
      return "In force";
    case "ended":
      return "Ended";
    default:
      return status;
  }
}

export function rateStatusTone(status: string): Tone {
  switch (status) {
    case "in_force":
      return "good";
    case "scheduled":
      return "info";
    default:
      return "muted";
  }
}

/** A rate can be closed for the future until it has ended. */
export function canCloseRate(r: Pick<RateOverride, "status">): boolean {
  return r.status === "in_force" || r.status === "scheduled";
}

/** A rate that has not started can be removed; a started one is history. */
export function canRemoveRate(r: Pick<RateOverride, "status">): boolean {
  return r.status === "scheduled";
}

// ---------------------------------------------------------------------------
// Money and time
// ---------------------------------------------------------------------------

/** "0.59 EUR" from 59 minor units. Currencies here all have two decimals. */
export function formatMinor(minor: number, currency: string): string {
  const negative = minor < 0;
  const abs = Math.abs(minor);
  const whole = Math.trunc(abs / 100);
  const cents = String(abs % 100).padStart(2, "0");
  return `${negative ? "-" : ""}${whole}.${cents} ${currency}`;
}

/** Strict non-negative integer from an input string; null when it is not one. */
export function parseMinor(text: string): number | null {
  const t = text.trim();
  if (!/^\d{1,15}$/.test(t)) return null;
  const n = Number(t);
  return Number.isSafeInteger(n) ? n : null;
}

/** Live preview under a minor-units input: "= 0.59 EUR", "" when it is not a number. */
export function minorPreview(text: string, currency: string): string {
  const n = parseMinor(text);
  return n === null ? "" : `= ${formatMinor(n, currency)}`;
}

/** Basis points as a percent: 300 -> "3%", 150 -> "1.5%". */
export function formatBasisPoints(bp: number): string {
  const whole = Math.trunc(bp / 100);
  const frac = bp % 100;
  return frac === 0 ? `${whole}%` : `${whole}.${String(frac).padStart(2, "0").replace(/0$/, "")}%`;
}

/**
 * Value of a <input type="datetime-local"> read as UTC (the screen says so
 * next to every date: prices start at an exact instant, never "somewhere's
 * midnight") and returned as an RFC 3339 instant; null when empty or not a
 * date.
 */
export function localInputToIso(local: string): string | null {
  const t = local.trim();
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2})?$/.test(t)) return null;
  const d = new Date(`${t.length === 16 ? `${t}:00` : t}Z`);
  return Number.isNaN(d.getTime()) ? null : d.toISOString();
}

// ---------------------------------------------------------------------------
// Rule shapes (what the rating engine reads: billing/rule_input.go)
// ---------------------------------------------------------------------------

export type ParamKind = "amount" | "bp" | "bool";

export interface ParamDescriptor {
  readonly key: string;
  readonly label: string;
  readonly kind: ParamKind;
}

export interface RuleShape {
  readonly type: string;
  readonly label: string;
  readonly lines: readonly string[];
  readonly eventKind: string;
  /** low_price_formula is the only rule with a sale currency. */
  readonly needsSaleCurrency: boolean;
  readonly params: readonly ParamDescriptor[];
}

export const RULE_SHAPES: readonly RuleShape[] = [
  {
    type: "fixed_per_ticket",
    label: "Fixed per ticket",
    lines: ["standard"],
    eventKind: "ticket_sold",
    needsSaleCurrency: false,
    params: [
      { key: "unseated_minor", label: "Ticket without a seat", kind: "amount" },
      { key: "seated_minor", label: "Seated ticket", kind: "amount" },
    ],
  },
  {
    type: "low_price_formula",
    label: "Low-price formula",
    lines: ["standard"],
    eventKind: "ticket_sold",
    needsSaleCurrency: true,
    params: [
      { key: "threshold_minor", label: "Price threshold", kind: "amount" },
      { key: "strict_below", label: "Strictly below the threshold", kind: "bool" },
      { key: "pct_bp", label: "Share of the price (basis points)", kind: "bp" },
      { key: "min_minor", label: "Smallest charge", kind: "amount" },
      { key: "max_minor", label: "Largest charge", kind: "amount" },
      { key: "seated_surcharge_minor", label: "Surcharge for a seated ticket", kind: "amount" },
    ],
  },
  {
    type: "credit_pack",
    label: "Credit pack surcharge",
    lines: ["standard"],
    eventKind: "ticket_sold",
    needsSaleCurrency: false,
    params: [{ key: "seated_surcharge_minor", label: "Surcharge for a seated ticket", kind: "amount" }],
  },
  {
    type: "percent_of_price_with_cap",
    label: "Percent of the price with a cap (venue)",
    lines: ["venue"],
    eventKind: "ticket_sold",
    needsSaleCurrency: false,
    params: [
      { key: "pct_bp", label: "Share of the price (basis points)", kind: "bp" },
      { key: "cap_minor", label: "Largest charge per ticket", kind: "amount" },
    ],
  },
  {
    type: "fixed_per_event",
    label: "Fixed per published event",
    lines: ["standard", "venue"],
    eventKind: "event_published",
    needsSaleCurrency: false,
    params: [{ key: "amount_minor", label: "Amount per event", kind: "amount" }],
  },
];

export function ruleShape(type: string): RuleShape | undefined {
  return RULE_SHAPES.find((s) => s.type === type);
}

// ---------------------------------------------------------------------------
// New version form
// ---------------------------------------------------------------------------

export interface RuleForm {
  readonly uid: number;
  readonly ruleType: string;
  readonly line: string;
  readonly saleCurrency: string;
  /** Raw input text per params key; a bool is "true" or "false". */
  readonly params: Readonly<Record<string, string>>;
}

export interface PackForm {
  readonly uid: number;
  readonly tier: string;
  readonly faceMinor: string;
  readonly unseatedRateMinor: string;
  readonly seatedSurchargeMinor: string;
  readonly active: boolean;
}

export interface VersionForm {
  readonly code: string;
  readonly currency: string;
  readonly effectiveFromLocal: string;
  readonly bonusShareCapBp: string;
  readonly locked: boolean;
  readonly freeLimit: string;
  readonly floorMinor: string;
  readonly bonusCoversSurcharge: boolean;
  readonly rules: readonly RuleForm[];
  readonly packs: readonly PackForm[];
}

let uidCounter = 0;
export function nextUid(): number {
  uidCounter += 1;
  return uidCounter;
}

export function emptyRule(type = "fixed_per_ticket", currency = ""): RuleForm {
  const shape = ruleShape(type);
  const params: Record<string, string> = {};
  for (const p of shape?.params ?? []) params[p.key] = p.kind === "bool" ? "true" : "";
  return {
    uid: nextUid(),
    ruleType: type,
    line: shape?.lines[0] ?? "standard",
    saleCurrency: shape?.needsSaleCurrency === true ? currency : "",
    params,
  };
}

export function emptyPack(): PackForm {
  return {
    uid: nextUid(),
    tier: "",
    faceMinor: "",
    unseatedRateMinor: "",
    seatedSurchargeMinor: "",
    active: true,
  };
}

export function emptyVersionForm(code = "", currency = ""): VersionForm {
  return {
    code,
    currency,
    effectiveFromLocal: "",
    bonusShareCapBp: "",
    locked: false,
    freeLimit: "",
    floorMinor: "",
    bonusCoversSurcharge: false,
    rules: [],
    packs: [],
  };
}

/**
 * A form pre-filled from an existing version, so the operator edits numbers
 * instead of retyping a whole list. The start date is left empty on purpose.
 */
export function formFromDetail(d: PriceListDetail): VersionForm {
  return {
    code: d.code,
    currency: d.currency,
    effectiveFromLocal: "",
    bonusShareCapBp: String(d.bonus_share_cap_bp),
    locked: d.locked,
    freeLimit: d.free_tickets_annual_limit == null ? "" : String(d.free_tickets_annual_limit),
    floorMinor: d.floor_per_ticket_minor == null ? "" : String(d.floor_per_ticket_minor),
    bonusCoversSurcharge: d.bonus_covers_surcharge,
    rules: d.rules.map((r) => {
      const params: Record<string, string> = {};
      for (const [k, v] of Object.entries(r.params)) params[k] = String(v);
      return {
        uid: nextUid(),
        ruleType: r.rule_type,
        line: r.line,
        saleCurrency: r.sale_currency ?? "",
        params,
      };
    }),
    packs: d.packs.map((p) => ({
      uid: nextUid(),
      tier: String(p.tier),
      faceMinor: String(p.face_minor),
      unseatedRateMinor: String(p.unseated_rate_minor),
      seatedSurchargeMinor: String(p.seated_surcharge_minor),
      active: p.active,
    })),
  };
}

export type BuildResult<T> =
  | { readonly ok: true; readonly request: T }
  | { readonly ok: false; readonly errors: readonly string[] };

/**
 * Checks the form and builds the request. It repeats the cheap rules of the
 * server so an operator sees a mistake before the round trip; the server
 * checks them all again and also runs the rating engine's own parser.
 */
export function buildCreateRequest(form: VersionForm, now: Date = new Date()): BuildResult<CreatePriceListRequest> {
  const errors: string[] = [];
  if (!/^[a-z0-9][a-z0-9._-]{1,63}$/.test(form.code)) {
    errors.push("Code: 2-64 characters, lower-case letters, digits, dot, dash or underscore.");
  }
  if (!/^[A-Z]{3}$/.test(form.currency)) errors.push("Currency: three capital letters, for example EUR.");
  const from = localInputToIso(form.effectiveFromLocal);
  if (from === null) errors.push("Start: pick the date and time the version starts.");
  else if (new Date(from).getTime() <= now.getTime()) errors.push("Start: it must be in the future.");
  const cap = parseMinor(form.bonusShareCapBp);
  if (cap === null || cap > 10000 || cap % 10 !== 0) {
    errors.push("Bonus share cap: basis points, 0-10000, a multiple of 10.");
  }
  const free = form.freeLimit.trim() === "" ? null : parseMinor(form.freeLimit);
  if (form.freeLimit.trim() !== "" && free === null) errors.push("Free tickets per year: a whole number, or empty.");
  const floor = form.floorMinor.trim() === "" ? null : parseMinor(form.floorMinor);
  if (form.floorMinor.trim() !== "" && floor === null) errors.push("Floor per ticket: whole minor units, or empty.");
  if (form.rules.length === 0) errors.push("Add at least one rule.");

  const rules: CreatePriceListRequest["rules"] = [];
  const slots = new Set<string>();
  form.rules.forEach((r, i) => {
    const n = i + 1;
    const shape = ruleShape(r.ruleType);
    if (shape === undefined) {
      errors.push(`Rule ${n}: pick a rule type.`);
      return;
    }
    if (!shape.lines.includes(r.line)) errors.push(`Rule ${n}: ${shape.label} is not used on the ${r.line} line.`);
    const slot = [r.ruleType, r.line, shape.eventKind, shape.needsSaleCurrency ? r.saleCurrency : ""].join("|");
    if (slots.has(slot)) errors.push(`Rule ${n}: another rule already covers the same type, line and currency.`);
    slots.add(slot);
    if (shape.needsSaleCurrency && !/^[A-Z]{3}$/.test(r.saleCurrency)) {
      errors.push(`Rule ${n}: the sale currency is three capital letters.`);
    }
    const params: Record<string, number | boolean> = {};
    for (const p of shape.params) {
      const raw = r.params[p.key] ?? "";
      if (p.kind === "bool") {
        params[p.key] = raw === "true";
        continue;
      }
      const v = parseMinor(raw);
      if (v === null) {
        errors.push(`Rule ${n}: ${p.label} must be a whole number, 0 or more.`);
        continue;
      }
      if (p.kind === "bp" && v > 10000) errors.push(`Rule ${n}: ${p.label} is at most 10000.`);
      params[p.key] = v;
    }
    if (shape.type === "low_price_formula" && typeof params.min_minor === "number" &&
        typeof params.max_minor === "number" && params.min_minor > params.max_minor) {
      errors.push(`Rule ${n}: the smallest charge is above the largest.`);
    }
    rules.push({
      rule_type: r.ruleType,
      line: r.line,
      event_kind: shape.eventKind,
      ...(shape.needsSaleCurrency ? { sale_currency: r.saleCurrency } : {}),
      params,
    });
  });

  const packs: NonNullable<CreatePriceListRequest["packs"]> = [];
  const tiers = new Set<number>();
  form.packs.forEach((p, i) => {
    const n = i + 1;
    const tier = parseMinor(p.tier);
    const face = parseMinor(p.faceMinor);
    const unseated = parseMinor(p.unseatedRateMinor);
    const surcharge = parseMinor(p.seatedSurchargeMinor);
    if (tier === null || tier < 1) errors.push(`Pack ${n}: the tier is 1 or more.`);
    else if (tiers.has(tier)) errors.push(`Pack ${n}: tier ${tier} is used twice.`);
    else tiers.add(tier);
    if (face === null || face < 1) errors.push(`Pack ${n}: the face value is more than 0.`);
    if (unseated === null) errors.push(`Pack ${n}: the rate without a seat is a whole number.`);
    if (surcharge === null) errors.push(`Pack ${n}: the seated surcharge is a whole number.`);
    if (tier !== null && face !== null && unseated !== null && surcharge !== null) {
      packs.push({
        tier,
        face_minor: face,
        unseated_rate_minor: unseated,
        seated_surcharge_minor: surcharge,
        active: p.active,
      });
    }
  });

  if (errors.length > 0 || from === null || cap === null) return { ok: false, errors };
  return {
    ok: true,
    request: {
      code: form.code,
      currency: form.currency,
      effective_from: from,
      locked: form.locked,
      free_tickets_annual_limit: free,
      floor_per_ticket_minor: floor,
      bonus_share_cap_bp: cap,
      bonus_covers_surcharge: form.bonusCoversSurcharge,
      rules,
      packs,
    },
  };
}

// ---------------------------------------------------------------------------
// Individual rate form
// ---------------------------------------------------------------------------

export interface RateForm {
  readonly orgId: string;
  readonly currency: string;
  readonly unseatedMinor: string;
  readonly seatedMinor: string;
  readonly effectiveFromLocal: string;
  readonly effectiveToLocal: string;
  readonly note: string;
}

export function emptyRateForm(): RateForm {
  return {
    orgId: "",
    currency: "",
    unseatedMinor: "",
    seatedMinor: "",
    effectiveFromLocal: "",
    effectiveToLocal: "",
    note: "",
  };
}

export function buildRateRequest(form: RateForm, now: Date = new Date()): BuildResult<CreateRateOverrideRequest> {
  const errors: string[] = [];
  if (form.orgId === "") errors.push("Organization: pick one.");
  if (!/^[A-Z]{3}$/.test(form.currency)) errors.push("Currency: three capital letters, for example EUR.");
  const unseated = parseMinor(form.unseatedMinor);
  const seated = parseMinor(form.seatedMinor);
  if (unseated === null) errors.push("Rate without a seat: a whole number of minor units.");
  if (seated === null) errors.push("Rate of a seated ticket: a whole number of minor units.");
  const from = localInputToIso(form.effectiveFromLocal);
  if (from === null) errors.push("Start: pick the date and time the rate starts.");
  else if (new Date(from).getTime() <= now.getTime()) errors.push("Start: it must be in the future.");
  let to: string | null = null;
  if (form.effectiveToLocal.trim() !== "") {
    to = localInputToIso(form.effectiveToLocal);
    if (to === null) errors.push("End: not a date.");
    else if (from !== null && new Date(to).getTime() <= new Date(from).getTime()) errors.push("End: it must be after the start.");
  }
  if (form.note.length > 500) errors.push("Note: 500 characters at most.");
  if (errors.length > 0 || unseated === null || seated === null || from === null) return { ok: false, errors };
  return {
    ok: true,
    request: {
      org_id: form.orgId,
      currency: form.currency,
      unseated_minor: unseated,
      seated_minor: seated,
      effective_from: from,
      ...(to !== null ? { effective_to: to } : {}),
      ...(form.note.trim() !== "" ? { note: form.note.trim() } : {}),
    },
  };
}

// ---------------------------------------------------------------------------
// Account terms form
// ---------------------------------------------------------------------------

export interface TermsForm {
  readonly effectiveFromLocal: string;
  readonly priceListCode: string;
  readonly planKind: string;
  readonly billingCycle: string;
  readonly shortfallPolicy: string;
  readonly invoiceDelayDays: string;
  readonly dueDays: string;
}

export function termsFormFrom(current: AccountTerms | undefined): TermsForm {
  return {
    effectiveFromLocal: "",
    priceListCode: current?.price_list_code ?? "",
    planKind: current?.plan_kind ?? "standard",
    billingCycle: current?.billing_cycle ?? "monthly",
    shortfallPolicy: current?.shortfall_policy ?? "invoice_postpaid",
    invoiceDelayDays: current?.invoice_delay_days == null ? "" : String(current.invoice_delay_days),
    dueDays: current?.due_days == null ? "" : String(current.due_days),
  };
}

export const PLAN_KINDS: readonly { value: string; label: string }[] = [
  { value: "standard", label: "Standard" },
  { value: "venue", label: "Permanent venue" },
];

export const BILLING_CYCLES: readonly { value: string; label: string }[] = [
  { value: "monthly", label: "Monthly" },
  { value: "per_event", label: "After each event" },
  { value: "prepaid", label: "Prepaid credits" },
];

export const SHORTFALL_POLICIES: readonly { value: string; label: string }[] = [
  { value: "invoice_postpaid", label: "Invoice the shortfall" },
  { value: "block_publishing", label: "Block publishing" },
];

export function buildTermsRequest(
  form: TermsForm,
  accountKind: string,
  now: Date = new Date(),
): BuildResult<CreateAccountTermsRequest> {
  const errors: string[] = [];
  const from = localInputToIso(form.effectiveFromLocal);
  if (from === null) errors.push("Start: pick the date and time the terms start.");
  else if (new Date(from).getTime() <= now.getTime()) errors.push("Start: it must be in the future.");
  if (form.priceListCode.trim() === "") errors.push("Price list: pick one.");
  if (accountKind === "tools" && form.billingCycle !== "prepaid") errors.push("A tools account is prepaid only.");
  const delay = form.invoiceDelayDays.trim() === "" ? null : parseMinor(form.invoiceDelayDays);
  if (form.invoiceDelayDays.trim() !== "" && delay === null) errors.push("Invoice delay: whole days, or empty.");
  const due = form.dueDays.trim() === "" ? null : parseMinor(form.dueDays);
  if (form.dueDays.trim() !== "" && due === null) errors.push("Days to pay: whole days, or empty.");
  if (errors.length > 0 || from === null) return { ok: false, errors };
  return {
    ok: true,
    request: {
      effective_from: from,
      price_list_code: form.priceListCode.trim(),
      plan_kind: form.planKind,
      billing_cycle: form.billingCycle,
      shortfall_policy: form.shortfallPolicy,
      invoice_delay_days: delay,
      due_days: due,
    },
  };
}

/** The terms row in force now: the last one that has started. */
export function currentTerms(account: BillingAccount): AccountTerms | undefined {
  return account.terms.find((t) => t.id === account.current_terms_id);
}

/** Distinct price-list codes in a currency, for the terms form's picker. */
export function priceListCodes(versions: readonly PriceListVersion[], currency: string): string[] {
  const set = new Set<string>();
  for (const v of versions) {
    if (v.currency === currency && v.status !== "revoked") set.add(v.code);
  }
  return [...set].sort();
}

/** One group of versions per (code, currency), newest version first. */
export interface VersionSeries {
  readonly key: string;
  readonly code: string;
  readonly currency: string;
  readonly versions: readonly PriceListVersion[];
}

export function groupSeries(items: readonly PriceListVersion[]): VersionSeries[] {
  const map = new Map<string, PriceListVersion[]>();
  for (const v of items) {
    const key = `${v.code}|${v.currency}`;
    const list = map.get(key);
    if (list) list.push(v);
    else map.set(key, [v]);
  }
  return [...map.entries()]
    .map(([key, versions]) => ({
      key,
      code: versions[0]?.code ?? "",
      currency: versions[0]?.currency ?? "",
      versions: [...versions].sort((a, b) => b.version - a.version),
    }))
    .sort((a, b) => a.key.localeCompare(b.key));
}
