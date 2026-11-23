import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { CATALOGS, LOCALES } from "@/lib/i18n";
import { KNOWN_ERROR_CODES, KNOWN_WARNING_CODES, checkErrorKey, checkWarningKey } from "@/lib/wizard/messages";

describe("check messages", () => {
  it("have a text in every language for every code the sections can raise", () => {
    for (const code of KNOWN_ERROR_CODES) {
      const key = checkErrorKey(code);
      expect(key, code).not.toBeNull();
      for (const locale of LOCALES) expect(CATALOGS[locale][key!].length, `${locale} ${code}`).toBeGreaterThan(0);
    }
    for (const code of KNOWN_WARNING_CODES) {
      const key = checkWarningKey(code);
      expect(key, code).not.toBeNull();
      for (const locale of LOCALES) expect(CATALOGS[locale][key!].length, `${locale} ${code}`).toBeGreaterThan(0);
    }
  });

  it("fall back to the server's message for a code with no text", () => {
    expect(checkErrorKey("import.something_new")).toBeNull();
    expect(checkErrorKey("toString")).toBeNull();
    expect(checkWarningKey("constructor")).toBeNull();
  });

  it("only name codes the server really raises", () => {
    // The server owns the codes: a renamed one would silently lose its text.
    const root = fileURLToPath(new URL("../../../../backend/internal/platform/httpserver/himports/", import.meta.url));
    const sources = ["import_arena.go", "bil24_session.go", "import_exec.go", "types.go", "dry_run.go", "import_chain.go", "import_sale_times.go", "import_external_sale.go"]
      .map((f) => readFileSync(root + f, "utf8"))
      .join("\n");
    // The codes of a date a partner box office sells (spec 63, L-3) are the extsale package's.
    const extsale = readFileSync(fileURLToPath(new URL("../../../../backend/internal/platform/extsale/extsale.go", import.meta.url)), "utf8");
    for (const code of [...KNOWN_ERROR_CODES, ...KNOWN_WARNING_CODES]) {
      expect((sources + extsale).includes(`"${code}"`), code).toBe(true);
    }
  });
});
