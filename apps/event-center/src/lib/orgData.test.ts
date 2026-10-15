import { describe, expect, it } from "vitest";
import {
  buildContactPatch,
  buildOrgPatch,
  formFromOrg,
  looksPersonalEmail,
  missingKey,
  normalizeEmail,
  normalizePhone,
  normalizeTaxId,
  paymentsNotConnected,
  sellerState,
  validateContact,
  validateForm,
  validTaxId,
  type OrgForm,
} from "@/lib/orgData";
import type { OrganizationItem } from "@/lib/api/types";

const empty: OrgForm = {
  name: "Teatro Bot",
  legalName: "",
  legalForm: "",
  registrationNumber: "",
  taxScheme: "",
  taxId: "",
  line1: "",
  city: "",
  postalCode: "",
  country: "",
  contactEmail: "",
  contactPhone: "",
};

describe("tax numbers (port of sellerinfo)", () => {
  it("folds what a person typed", () => {
    expect(normalizeTaxId("eu_vat", "es b-1234 5678")).toBe("ESB12345678");
    expect(normalizeTaxId("gb_vat", " gb 123 456 789 ")).toBe("GB123456789");
    expect(normalizeTaxId("il_vat", "512-345 678")).toBe("512345678");
    expect(normalizeTaxId("us_ein", " 12-3456789 ")).toBe("12-3456789");
    expect(normalizeTaxId("other", " B12345678 ")).toBe("B12345678");
  });

  it("checks each scheme", () => {
    expect(validTaxId("eu_vat", "ESB12345678")).toBe(true);
    // A bare Spanish NIF has no country code: it belongs under "other".
    expect(validTaxId("eu_vat", "B12345678")).toBe(false);
    expect(validTaxId("eu_vat", "12345678")).toBe(false);
    expect(validTaxId("gb_vat", "GB123456789")).toBe(true);
    expect(validTaxId("gb_vat", "GBGD123")).toBe(true);
    expect(validTaxId("gb_vat", "GB12345")).toBe(false);
    expect(validTaxId("il_vat", "512345678")).toBe(true);
    expect(validTaxId("il_vat", "51234567")).toBe(false);
    expect(validTaxId("us_ein", "12-3456789")).toBe(true);
    expect(validTaxId("us_ein", "123456789")).toBe(true);
    expect(validTaxId("us_ein", "1-23456789")).toBe(false);
    expect(validTaxId("other", "B12345678")).toBe(true);
    expect(validTaxId("other", "x")).toBe(false);
    expect(validTaxId("other", "x".repeat(65))).toBe(false);
  });
});

describe("public e-mail and phone", () => {
  it("normalizes and refuses the way the API does", () => {
    expect(normalizeEmail(" Teatro.Tickets@Gmail.com ")).toBe("teatro.tickets@gmail.com");
    expect(normalizeEmail("not-an-email")).toBeNull();
    expect(normalizeEmail("a@b")).toBeNull();
    expect(normalizeEmail("a@b.")).toBeNull();
    expect(normalizeEmail("two words@x.com")).toBeNull();
    expect(normalizeEmail("a@@x.com")).toBeNull();
    expect(normalizeEmail("Name <a@x.com>")).toBeNull();
    expect(normalizeEmail("")).toBeNull();
  });

  it("accepts a phone with the country code and refuses words", () => {
    expect(normalizePhone("+34 600 111 222")).toBe("+34 600 111 222");
    expect(normalizePhone("  (34) 600-111.222 ")).toBe("(34) 600-111.222");
    expect(normalizePhone("позвоните")).toBeNull();
    expect(normalizePhone("12345")).toBeNull();
    expect(normalizePhone("1234567890123456")).toBeNull();
    expect(normalizePhone("600+111222")).toBeNull();
  });

  it("hints at a personal mailbox but never refuses it", () => {
    expect(looksPersonalEmail("a@gmail.com")).toBe(true);
    expect(looksPersonalEmail("a@Mail.ru")).toBe(true);
    expect(looksPersonalEmail("tickets@teatro.es")).toBe(false);
    expect(normalizeEmail("a@gmail.com")).toBe("a@gmail.com");
  });
});

describe("validateForm", () => {
  it("accepts an empty legal block: nothing is required to save", () => {
    expect(validateForm(empty)).toEqual([]);
  });

  it("points a bare NIF under EU VAT to the other type", () => {
    const p = validateForm({ ...empty, taxScheme: "eu_vat", taxId: "B12345678" });
    expect(p).toEqual([{ field: "taxId", key: "orgData.err.tax.eu_vat" }]);
  });

  it("wants the type and the number together", () => {
    expect(validateForm({ ...empty, taxId: "B12345678" })).toEqual([{ field: "taxScheme", key: "orgData.err.taxScheme" }]);
    expect(validateForm({ ...empty, taxScheme: "other" })).toEqual([{ field: "taxId", key: "orgData.err.taxNumber" }]);
  });

  it("checks the lengths, the country and the contact", () => {
    const keys = validateForm({
      ...empty,
      name: " ",
      legalName: "я".repeat(201),
      legalForm: "x".repeat(81),
      registrationNumber: "1".repeat(61),
      line1: "a".repeat(201),
      city: "c".repeat(101),
      postalCode: "9".repeat(21),
      country: "Spain",
      contactEmail: "nope",
      contactPhone: "call me",
    }).map((p) => p.key);
    expect(keys).toEqual([
      "orgData.err.name",
      "orgData.err.legalName",
      "orgData.err.legalForm",
      "orgData.err.registrationNumber",
      "orgData.err.line1",
      "orgData.err.city",
      "orgData.err.postalCode",
      "orgData.err.country",
      "orgData.err.email",
      "orgData.err.phone",
    ]);
  });

  it("counts characters, not bytes", () => {
    expect(validateForm({ ...empty, legalName: "я".repeat(200) })).toEqual([]);
  });
});

describe("buildOrgPatch", () => {
  it("sends nothing when nothing changed", () => {
    expect(buildOrgPatch(empty, { ...empty, name: " Teatro Bot " })).toEqual({});
  });

  it("sends only the changed fields, normalized", () => {
    const now: OrgForm = {
      ...empty,
      legalName: "  Teatro Bot S.L. ",
      legalForm: "S.L.",
      taxScheme: "eu_vat",
      taxId: "es b-1234 5678",
      country: "es",
      contactEmail: "Info@Teatro.ES",
    };
    expect(buildOrgPatch(empty, now)).toEqual({
      legal_name: "Teatro Bot S.L.",
      legal_form: "S.L.",
      legal_address_country: "ES",
      contact_email: "info@teatro.es",
      tax_id: "ESB12345678",
      tax_id_scheme: "eu_vat",
    });
  });

  it("clears an emptied field with null and the tax pair together", () => {
    const before: OrgForm = { ...empty, legalName: "Teatro Bot S.L.", taxScheme: "eu_vat", taxId: "ESB12345678" };
    expect(buildOrgPatch(before, { ...before, legalName: "", taxId: "" })).toEqual({
      legal_name: null,
      tax_id: null,
      tax_id_scheme: null,
    });
  });

  it("a changed scheme resends the pair", () => {
    const before: OrgForm = { ...empty, taxScheme: "eu_vat", taxId: "ESB12345678" };
    expect(buildOrgPatch(before, { ...before, taxScheme: "other", taxId: "B12345678" })).toEqual({
      tax_id: "B12345678",
      tax_id_scheme: "other",
    });
  });

  it("a renamed organization sends the name", () => {
    expect(buildOrgPatch(empty, { ...empty, name: "Teatro Nuevo" })).toEqual({ name: "Teatro Nuevo" });
  });
});

describe("formFromOrg", () => {
  it("reads the stored organization and drops an unknown scheme", () => {
    const org = {
      name: "Teatro Bot",
      legal_name: "Teatro Bot S.L.",
      legal_form: null,
      tax_id: "ESB12345678",
      tax_id_scheme: "eu_vat",
      legal_address_country: "ES",
      contact_email: "info@teatro.es",
    } as unknown as OrganizationItem;
    expect(formFromOrg(org)).toMatchObject({
      name: "Teatro Bot",
      legalName: "Teatro Bot S.L.",
      legalForm: "",
      taxScheme: "eu_vat",
      taxId: "ESB12345678",
      country: "ES",
      contactEmail: "info@teatro.es",
      contactPhone: "",
    });
    expect(formFromOrg({ ...org, tax_id_scheme: "weird" } as unknown as OrganizationItem).taxScheme).toBe("");
  });
});

describe("channel contact", () => {
  it("checks the shape and builds a tri-state patch", () => {
    expect(validateContact({ email: "nope", phone: "call" }).map((p) => p.field)).toEqual(["email", "phone"]);
    expect(validateContact({ email: "", phone: "" })).toEqual([]);
    const before = { email: "a@x.com", phone: "+34 600 111 222" };
    expect(buildContactPatch(before, before)).toEqual({});
    expect(buildContactPatch(before, { email: "B@x.com", phone: "" })).toEqual({ email: "b@x.com", phone: null });
  });
});

describe("status helpers", () => {
  it("names the missing fields and ignores unknown ones", () => {
    expect(missingKey("tax_id")).toBe("orgData.missing.tax_id");
    expect(missingKey("public_email")).toBe("orgData.missing.public_email");
    expect(missingKey("constructor")).toBeNull();
    expect(missingKey("brand_new")).toBeNull();
  });

  it("tells the three states of the block apart", () => {
    expect(sellerState({ seller_shown: true, seller_hidden: false })).toBe("shown");
    expect(sellerState({ seller_shown: false, seller_hidden: true })).toBe("operator_hidden");
    expect(sellerState({ seller_shown: false, seller_hidden: false })).toBe("incomplete");
  });

  it("the payments note needs a read, and an active config the provider accepted", () => {
    expect(paymentsNotConnected(undefined)).toBe(false);
    expect(paymentsNotConnected([])).toBe(true);
    expect(paymentsNotConnected([{ is_active: true, verification_status: "unverified" }])).toBe(true);
    expect(paymentsNotConnected([{ is_active: false, verification_status: "ok" }])).toBe(true);
    expect(paymentsNotConnected([{ is_active: true, verification_status: "ok" }])).toBe(false);
  });
});
