/**
 * Tests for I18nProvider + useTranslation (Feature #251).
 *
 * Renders a small consumer via react-dom/server (the project's test
 * environment is Node, no jsdom — see vitest.config.ts) to validate
 * that the provider exposes the right locale and translates keys.
 *
 * Storage helpers (readInitialLocale / persistLocale) are tested
 * directly with an in-memory storage stub to avoid coupling to the
 * jsdom localStorage polyfill in test-setup.ts.
 */

import { renderToString } from "react-dom/server";
import { describe, expect, it } from "vitest";
import {
  I18nProvider,
  persistLocale,
  readInitialLocale,
  useTranslation,
  __INTERNAL__,
} from "@/lib/i18n/I18nContext";
import type { LocaleCode } from "@/lib/i18n/catalog";

function Consumer({ k }: { k: string }) {
  const { t, locale } = useTranslation();
  return (
    <span data-locale={locale}>{`[${locale}] ${t(k)}`}</span>
  );
}

function ParamConsumer() {
  const { t } = useTranslation();
  return <span>{t("shell.scopeActive", { label: "Acme" })}</span>;
}

describe("I18nProvider + useTranslation", () => {
  it("renders Russian by default", () => {
    const html = renderToString(
      <I18nProvider>
        <Consumer k="shell.signOut" />
      </I18nProvider>,
    );
    expect(html).toContain("[ru]");
    expect(html).toContain("Выйти");
  });

  it("renders English when initialLocale='en'", () => {
    const html = renderToString(
      <I18nProvider initialLocale="en">
        <Consumer k="shell.signOut" />
      </I18nProvider>,
    );
    expect(html).toContain("[en]");
    expect(html).toContain("Sign out");
  });

  it("interpolates parameters", () => {
    const html = renderToString(
      <I18nProvider initialLocale="en">
        <ParamConsumer />
      </I18nProvider>,
    );
    expect(html).toContain("Active scope: Acme");
  });

  it("useTranslation works outside a provider with default fallback", () => {
    const html = renderToString(<Consumer k="shell.signOut" />);
    // Falls back to default (ru).
    expect(html).toContain("[ru]");
    expect(html).toContain("Выйти");
  });

  it("renders entity-name nav labels with English gloss in both locales", () => {
    const ru = renderToString(
      <I18nProvider initialLocale="ru">
        <Consumer k="nav.organizations" />
      </I18nProvider>,
    );
    const en = renderToString(
      <I18nProvider initialLocale="en">
        <Consumer k="nav.organizations" />
      </I18nProvider>,
    );
    expect(ru).toContain("Organizations");
    expect(en).toContain("Organizations");
  });
});

describe("readInitialLocale() / persistLocale()", () => {
  class FakeStorage {
    public readonly data = new Map<string, string>();
    getItem(key: string): string | null {
      return this.data.has(key) ? (this.data.get(key) as string) : null;
    }
    setItem(key: string, value: string): void {
      this.data.set(key, value);
    }
  }

  it("returns the default when nothing is stored", () => {
    const storage = new FakeStorage();
    expect(readInitialLocale(storage)).toBe("ru");
  });

  it("returns the stored locale when valid", () => {
    const storage = new FakeStorage();
    storage.setItem(__INTERNAL__.STORAGE_KEY, "en");
    expect(readInitialLocale(storage)).toBe("en");
  });

  it("ignores garbage stored values and returns default", () => {
    const storage = new FakeStorage();
    storage.setItem(__INTERNAL__.STORAGE_KEY, "klingon");
    expect(readInitialLocale(storage)).toBe("ru");
  });

  it("persistLocale writes the locale to the storage key", () => {
    const storage = new FakeStorage();
    persistLocale("en", storage);
    expect(storage.data.get(__INTERNAL__.STORAGE_KEY)).toBe("en");
    persistLocale("ru", storage);
    expect(storage.data.get(__INTERNAL__.STORAGE_KEY)).toBe("ru");
  });

  it("round-trips a locale through persist + read", () => {
    const storage = new FakeStorage();
    const locales: LocaleCode[] = ["ru", "en"];
    for (const code of locales) {
      persistLocale(code, storage);
      expect(readInitialLocale(storage)).toBe(code);
    }
  });
});
