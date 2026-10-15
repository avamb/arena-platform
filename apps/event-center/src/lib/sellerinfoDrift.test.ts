import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { PERSONAL_DOMAINS, TAX_SCHEMES, validTaxId } from "@/lib/orgData";

/**
 * orgData.ts is a port of internal/platform/sellerinfo. These tests READ the Go
 * source, so a format or a domain changed there fails the web build until the
 * port follows (the same guard as confirmWords.test.ts).
 */
const GO = readFileSync(fileURLToPath(new URL("../../../backend/internal/platform/sellerinfo/sellerinfo.go", import.meta.url)), "utf8");

describe("the port equals sellerinfo.go", () => {
  it("lists the same free-mailbox domains", () => {
    const block = /var personalDomains = map\[string\]bool\{([\s\S]*?)\n\}/.exec(GO);
    expect(block).not.toBeNull();
    const goDomains = [...(block?.[1] ?? "").matchAll(/"([^"]+)": true/g)].map((m) => m[1]).sort();
    expect(goDomains.length).toBeGreaterThan(20);
    expect([...PERSONAL_DOMAINS].sort()).toEqual(goDomains);
  });

  it("knows the same tax schemes, in the same order", () => {
    const m = /var Schemes = \[\]string\{([^}]*)\}/.exec(GO);
    expect(m).not.toBeNull();
    const consts = new Map([...GO.matchAll(/(Scheme\w+)\s*=\s*"([^"]+)"/g)].map((x) => [x[1], x[2]] as const));
    const names = (m?.[1] ?? "").split(",").map((s) => s.trim()).filter((s) => s !== "");
    expect(names.map((n) => consts.get(n))).toEqual([...TAX_SCHEMES]);
  });

  it("accepts and refuses the same tax numbers as the Go regular expressions", () => {
    const patterns: Record<string, RegExp> = {};
    for (const m of GO.matchAll(/(euVAT|gbVAT|ilVAT|usEIN)\s*=\s*regexp\.MustCompile\(`([^`]+)`\)/g)) {
      patterns[m[1]] = new RegExp(m[2]);
    }
    expect(Object.keys(patterns).sort()).toEqual(["euVAT", "gbVAT", "ilVAT", "usEIN"]);
    const byScheme = { eu_vat: patterns.euVAT, gb_vat: patterns.gbVAT, il_vat: patterns.ilVAT, us_ein: patterns.usEIN } as const;
    const corpus = [
      "ESB12345678", "esb12345678", "B12345678", "CZ12345678", "DE1", "GB123456789", "GB123456789012", "GBGD123", "GBHA999", "GB12345",
      "512345678", "51234567", "5123456789", "12-3456789", "123456789", "1-23456789", "12--3456789", "", "x", "ES B1",
    ];
    for (const scheme of ["eu_vat", "gb_vat", "il_vat", "us_ein"] as const) {
      for (const id of corpus) {
        const goSays = scheme === "eu_vat" || scheme === "gb_vat" ? byScheme[scheme].test(id.toUpperCase()) : byScheme[scheme].test(id);
        expect(validTaxId(scheme, id), `${scheme} ${JSON.stringify(id)}`).toBe(goSays);
      }
    }
  });

  it("keeps the same length limits", () => {
    expect(/const MaxEmailRunes = 254/.test(GO)).toBe(true);
    expect(/const MaxPhoneRunes = 32/.test(GO)).toBe(true);
    expect(/const MaxLegalFormRunes = 80/.test(GO)).toBe(true);
  });
});
