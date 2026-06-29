/**
 * Locale switcher (Feature #251).
 *
 * Renders a small <select> dropdown in the admin top bar that switches the
 * SuperAdmin UI between Russian (default) and English. Selection is
 * persisted to localStorage by the I18nProvider and reflected on
 * <html lang="...">.
 *
 * Entity names and technical identifiers (slug/code/enum values) are NOT
 * translated — only descriptive copy. See lib/i18n/catalog.ts.
 */

import type { CSSProperties, ChangeEvent } from "react";
import { useTranslation } from "@/lib/i18n/I18nContext";
import {
  LOCALE_LABELS,
  isLocaleCode,
  type LocaleCode,
} from "@/lib/i18n/catalog";

export function LocaleSwitcher(): JSX.Element {
  const { locale, setLocale, locales, t } = useTranslation();

  const onChange = (e: ChangeEvent<HTMLSelectElement>): void => {
    const next: string = e.target.value;
    if (isLocaleCode(next)) {
      setLocale(next);
    }
  };

  return (
    <label style={wrapperStyle} data-testid="locale-switcher">
      <span style={labelStyle}>{t("locale.label")}:</span>
      <select
        value={locale}
        onChange={onChange}
        aria-label={t("locale.switch.aria")}
        style={selectStyle}
        data-testid="locale-switcher-select"
      >
        {locales.map((code: LocaleCode) => (
          <option key={code} value={code}>
            {LOCALE_LABELS[code]}
          </option>
        ))}
      </select>
    </label>
  );
}

const wrapperStyle: CSSProperties = {
  display: "inline-flex",
  alignItems: "center",
  gap: 6,
  fontSize: 12,
  color: "#475569",
};

const labelStyle: CSSProperties = {
  fontSize: 12,
  color: "#64748b",
};

const selectStyle: CSSProperties = {
  fontSize: 12,
  padding: "2px 6px",
  borderRadius: 4,
  border: "1px solid #cbd5e1",
  background: "#fff",
  color: "#0f172a",
  cursor: "pointer",
};
