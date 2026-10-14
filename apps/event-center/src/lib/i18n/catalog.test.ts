import { describe, expect, it } from "vitest";
import { CATALOGS, LOCALES, interpolate, pickLocale, translate } from "@/lib/i18n";
import { en } from "@/lib/i18n/en";

function placeholders(text: string): string[] {
  return [...text.matchAll(/\{([a-zA-Z_]+)\}/g)].map((m) => m[1]).sort();
}

describe("catalogs", () => {
  const keys = Object.keys(en).sort();

  for (const locale of LOCALES) {
    it(`${locale} has every key and no extra key`, () => {
      expect(Object.keys(CATALOGS[locale]).sort()).toEqual(keys);
    });
    it(`${locale} keeps the placeholders of every English text`, () => {
      for (const key of keys) {
        const k = key as keyof typeof en;
        expect(placeholders(CATALOGS[locale][k]), `${locale} ${key}`).toEqual(placeholders(en[k]));
      }
    });
    it(`${locale} has no empty text`, () => {
      for (const key of keys) expect(CATALOGS[locale][key as keyof typeof en].trim().length, key).toBeGreaterThan(0);
    });
  }

  it("Russian texts use the glossary terms", () => {
    // The strict list of docs/text_style_standard_ru.md section 4.
    const forbidden = [/событи/i, /(^|[^-])ивент(?!-центр)/i, /сесси/i, /удержан/i, /блокировк/i, /постер/i];
    for (const [key, text] of Object.entries(CATALOGS.ru)) {
      for (const re of forbidden) expect(re.test(text), `${key}: «${text}» matches ${re}`).toBe(false);
    }
  });

  it("English texts use American spelling and no semicolons", () => {
    for (const [key, text] of Object.entries(en)) {
      expect(text.replace(/{[a-zA-Z_]+}/g, "").includes("cancelled"), key).toBe(false);
      expect(text.includes(";"), key).toBe(false);
    }
  });
});

describe("helpers", () => {
  it("interpolates and keeps unknown placeholders visible", () => {
    expect(interpolate("Sold {sold} of {total}", { sold: 3, total: 10 })).toBe("Sold 3 of 10");
    expect(interpolate("Hi {name}", {})).toBe("Hi {name}");
  });
  it("picks the first supported browser language", () => {
    expect(pickLocale(["fr-FR", "es-ES", "en"])).toBe("es");
    expect(pickLocale(["de"])).toBe("en");
    expect(pickLocale(["RU"])).toBe("ru");
  });
  it("translates", () => {
    expect(translate("ru", "figures.held", { count: 2 })).toBe("В брони: 2");
  });
});
