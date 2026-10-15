/**
 * 4-7: the rules of the owner's "Organization data" screen, as pure
 * functions (no DOM in vitest, so every rule that matters lives here).
 *
 * The formats are a port of internal/platform/sellerinfo (tax-number schemes,
 * the shape of a public e-mail and phone, the "looks like a personal
 * mailbox" hint). The API stays the authority: the web checks the shape
 * first to save a round trip and to say what to change, exactly as the bot
 * does (eventbot/org_data.go), and shows the API's own refusal in plain
 * words when it still comes.
 */
import type { MessageKey } from "@/lib/i18n/en";
import type { OrganizationItem } from "@/lib/api/types";

// ─── tax numbers ─────────────────────────────────────────────────────────────

export const TAX_SCHEMES = ["eu_vat", "gb_vat", "il_vat", "us_ein", "other"] as const;
export type TaxScheme = (typeof TAX_SCHEMES)[number];

export function isTaxScheme(value: string): value is TaxScheme {
  return (TAX_SCHEMES as readonly string[]).includes(value);
}

const EU_VAT = /^[A-Z]{2}[A-Z0-9]{2,12}$/;
const GB_VAT = /^GB(?:[0-9]{9}|[0-9]{12}|GD[0-9]{3}|HA[0-9]{3})$/;
const IL_VAT = /^[0-9]{9}$/;
const US_EIN = /^[0-9]{2}-?[0-9]{7}$/;

/** Folds what a person typed into the form the scheme stores (sellerinfo.NormalizeTaxID). */
export function normalizeTaxId(scheme: TaxScheme, raw: string): string {
  const v = raw.trim();
  switch (scheme) {
    case "eu_vat":
    case "gb_vat":
      return v.replace(/[ .\-/]/g, "").toUpperCase();
    case "il_vat":
      return v.replace(/[ -]/g, "");
    default:
      return v;
  }
}

/** Whether `id` (already normalized) fits `scheme` (sellerinfo.ValidTaxID). */
export function validTaxId(scheme: TaxScheme, id: string): boolean {
  switch (scheme) {
    case "eu_vat":
      return EU_VAT.test(id.toUpperCase());
    case "gb_vat":
      return GB_VAT.test(id.toUpperCase());
    case "il_vat":
      return IL_VAT.test(id);
    case "us_ein":
      return US_EIN.test(id);
    case "other": {
      const bytes = new TextEncoder().encode(id).length;
      return bytes >= 2 && bytes <= 64;
    }
  }
}

// ─── public e-mail and phone ─────────────────────────────────────────────────

const MAX_EMAIL = 254;
const MAX_PHONE = 32;

/** Trims and lower-cases a public e-mail and checks its shape (sellerinfo.NormalizeEmail). */
export function normalizeEmail(raw: string): string | null {
  const v = raw.trim().toLowerCase();
  if (v === "" || [...v].length > MAX_EMAIL || /\s/.test(v) || /[<>,;"()]/.test(v)) return null;
  const at = v.lastIndexOf("@");
  if (at < 1 || v.indexOf("@") !== at) return null;
  const domain = v.slice(at + 1);
  if (!domain.includes(".") || domain.startsWith(".") || domain.endsWith(".") || domain.includes("..")) return null;
  return v;
}

/** Digits with an optional leading +, 6 to 15 digits (sellerinfo.NormalizePhone). */
export function normalizePhone(raw: string): string | null {
  const v = raw.trim().split(/\s+/).join(" ");
  if (v === "" || [...v].length > MAX_PHONE) return null;
  let digits = 0;
  for (let i = 0; i < v.length; i += 1) {
    const c = v.charAt(i);
    if (c >= "0" && c <= "9") digits += 1;
    else if (c === "+" && i === 0) continue;
    else if (c === " " || c === "(" || c === ")" || c === "-" || c === ".") continue;
    else return null;
  }
  return digits >= 6 && digits <= 15 ? v : null;
}

export const PERSONAL_DOMAINS = new Set([
  "gmail.com", "googlemail.com",
  "inbox.ru", "mail.ru", "bk.ru", "list.ru", "internet.ru",
  "yandex.ru", "yandex.com", "yandex.by", "yandex.kz", "ya.ru",
  "outlook.com", "outlook.es", "hotmail.com", "hotmail.es", "live.com", "msn.com",
  "icloud.com", "me.com", "mac.com",
  "yahoo.com", "yahoo.es", "ymail.com",
  "seznam.cz", "email.cz", "post.cz", "centrum.cz",
  "gmx.com", "gmx.net", "gmx.de", "web.de",
  "proton.me", "protonmail.com", "aol.com", "walla.co.il",
]);

/** A hint for the owner, never a refusal (sellerinfo.LooksPersonalEmail). */
export function looksPersonalEmail(email: string): boolean {
  const at = email.lastIndexOf("@");
  return at >= 0 && PERSONAL_DOMAINS.has(email.slice(at + 1).trim().toLowerCase());
}

// ─── the legal form ──────────────────────────────────────────────────────────

/** The form fields the owner edits in one place and saves with one PATCH. */
export interface OrgForm {
  name: string;
  legalName: string;
  legalForm: string;
  registrationNumber: string;
  taxScheme: "" | TaxScheme;
  taxId: string;
  line1: string;
  city: string;
  postalCode: string;
  country: string;
  contactEmail: string;
  contactPhone: string;
}

export type OrgFormField = keyof OrgForm;

const text0 = (v: string | null | undefined): string => (v ?? "").trim();

export function formFromOrg(org: OrganizationItem): OrgForm {
  const scheme = text0(org.tax_id_scheme);
  return {
    name: text0(org.name),
    legalName: text0(org.legal_name),
    legalForm: text0(org.legal_form),
    registrationNumber: text0(org.registration_number),
    taxScheme: isTaxScheme(scheme) ? scheme : "",
    taxId: text0(org.tax_id),
    line1: text0(org.legal_address_line1),
    city: text0(org.legal_address_city),
    postalCode: text0(org.legal_address_postal_code),
    country: text0(org.legal_address_country),
    contactEmail: text0(org.contact_email),
    contactPhone: text0(org.contact_phone),
  };
}

/** Limits of the API, in characters (runes). */
export const LIMITS = {
  legalName: 200,
  legalForm: 80,
  registrationNumber: 60,
  line1: 200,
  city: 100,
  postalCode: 20,
} as const;

export interface FormProblem {
  field: OrgFormField;
  key: MessageKey;
}

const len = (v: string): number => [...v.trim()].length;

/** What is wrong with the form, in the order of the fields. Empty means it can be saved. */
export function validateForm(f: OrgForm): FormProblem[] {
  const out: FormProblem[] = [];
  const add = (field: OrgFormField, key: MessageKey) => out.push({ field, key });
  if (f.name.trim() === "") add("name", "orgData.err.name");
  if (len(f.legalName) > LIMITS.legalName) add("legalName", "orgData.err.legalName");
  if (len(f.legalForm) > LIMITS.legalForm || /[\r\n]/.test(f.legalForm)) add("legalForm", "orgData.err.legalForm");
  if (len(f.registrationNumber) > LIMITS.registrationNumber) add("registrationNumber", "orgData.err.registrationNumber");
  const taxId = f.taxId.trim();
  if (f.taxScheme === "" && taxId !== "") add("taxScheme", "orgData.err.taxScheme");
  else if (f.taxScheme !== "" && taxId === "") add("taxId", "orgData.err.taxNumber");
  else if (f.taxScheme !== "" && !validTaxId(f.taxScheme, normalizeTaxId(f.taxScheme, taxId))) {
    add("taxId", `orgData.err.tax.${f.taxScheme}`);
  }
  if (len(f.line1) > LIMITS.line1) add("line1", "orgData.err.line1");
  if (len(f.city) > LIMITS.city) add("city", "orgData.err.city");
  if (len(f.postalCode) > LIMITS.postalCode) add("postalCode", "orgData.err.postalCode");
  const country = f.country.trim();
  if (country !== "" && !/^[A-Za-z]{2}$/.test(country)) add("country", "orgData.err.country");
  if (f.contactEmail.trim() !== "" && normalizeEmail(f.contactEmail) === null) add("contactEmail", "orgData.err.email");
  if (f.contactPhone.trim() !== "" && normalizePhone(f.contactPhone) === null) add("contactPhone", "orgData.err.phone");
  return out;
}

/**
 * The body of PATCH /v1/organizations/{id}: ONLY what changed. A cleared
 * optional field is sent as null (the API clears it); an untouched field is
 * left out, so a save never rewrites data another surface just changed. The
 * tax number and its type travel as a pair (the API refuses one without the
 * other). Call it only when `validateForm` found nothing.
 */
export function buildOrgPatch(initial: OrgForm, now: OrgForm): Record<string, unknown> {
  const patch: Record<string, unknown> = {};
  const text = (key: string, a: string, b: string, normalize: (v: string) => string = (v) => v.trim()) => {
    if (a.trim() === b.trim()) return;
    const value = normalize(b);
    patch[key] = value === "" ? null : value;
  };
  if (initial.name.trim() !== now.name.trim()) patch.name = now.name.trim();
  text("legal_name", initial.legalName, now.legalName);
  text("legal_form", initial.legalForm, now.legalForm);
  text("registration_number", initial.registrationNumber, now.registrationNumber);
  text("legal_address_line1", initial.line1, now.line1);
  text("legal_address_city", initial.city, now.city);
  text("legal_address_postal_code", initial.postalCode, now.postalCode);
  text("legal_address_country", initial.country, now.country, (v) => v.trim().toUpperCase());
  text("contact_email", initial.contactEmail, now.contactEmail, (v) => normalizeEmail(v) ?? "");
  text("contact_phone", initial.contactPhone, now.contactPhone, (v) => normalizePhone(v) ?? "");

  const scheme = now.taxScheme;
  const id = scheme === "" ? "" : normalizeTaxId(scheme, now.taxId);
  const before = initial.taxScheme === "" ? "" : normalizeTaxId(initial.taxScheme, initial.taxId);
  if (scheme !== initial.taxScheme || id !== before) {
    if (scheme === "" || id === "") {
      patch.tax_id = null;
      patch.tax_id_scheme = null;
    } else {
      patch.tax_id = id;
      patch.tax_id_scheme = scheme;
    }
  }
  return patch;
}

// ─── the public contact of a channel ─────────────────────────────────────────

export interface ContactForm {
  email: string;
  phone: string;
}

export interface ContactProblem {
  field: "email" | "phone";
  key: MessageKey;
}

export function validateContact(f: ContactForm): ContactProblem[] {
  const out: ContactProblem[] = [];
  if (f.email.trim() !== "" && normalizeEmail(f.email) === null) out.push({ field: "email", key: "orgData.err.email" });
  if (f.phone.trim() !== "" && normalizePhone(f.phone) === null) out.push({ field: "phone", key: "orgData.err.phone" });
  return out;
}

/** The `public_contact` of PATCH .../seller-display: only the keys that changed, null clears. */
export function buildContactPatch(initial: ContactForm, now: ContactForm): { email?: string | null; phone?: string | null } {
  const out: { email?: string | null; phone?: string | null } = {};
  if (initial.email.trim() !== now.email.trim()) out.email = normalizeEmail(now.email);
  if (initial.phone.trim() !== now.phone.trim()) out.phone = normalizePhone(now.phone);
  return out;
}

/** Names of the required fields the API reports as missing, in a stable order. */
export const MISSING_KEYS = {
  legal_name: "orgData.missing.legal_name",
  tax_id: "orgData.missing.tax_id",
  address_line1: "orgData.missing.address_line1",
  address_city: "orgData.missing.address_city",
  address_postal_code: "orgData.missing.address_postal_code",
  public_email: "orgData.missing.public_email",
} as const satisfies Record<string, MessageKey>;

export function missingKey(name: string): MessageKey | null {
  return Object.prototype.hasOwnProperty.call(MISSING_KEYS, name) ? MISSING_KEYS[name as keyof typeof MISSING_KEYS] : null;
}

/** The state of one channel's seller block, for its badge. */
export type SellerState = "shown" | "operator_hidden" | "incomplete";

export function sellerState(c: { seller_shown: boolean; seller_hidden: boolean }): SellerState {
  if (c.seller_shown) return "shown";
  if (c.seller_hidden) return "operator_hidden";
  return "incomplete";
}

/**
 * Whether the payments note shows: the owner may read the configs and none is
 * both active and accepted by the provider. A failed read says nothing
 * (`undefined` list), exactly as the bot's `paymentsNote`.
 */
export function paymentsNotConnected(
  configs: readonly { is_active: boolean; verification_status: string }[] | undefined,
): boolean {
  if (configs === undefined) return false;
  return !configs.some((c) => c.is_active && c.verification_status === "ok");
}
