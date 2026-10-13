import { useId } from "react";
import { LOCALES, LOCALE_NAMES, isLocale, useI18n } from "@/lib/i18n";

export function LanguageSelect() {
  const { t, locale, setLocale } = useI18n();
  const id = useId();
  return (
    <span className="lang-select">
      <label htmlFor={id}>{t("nav.language")}</label>
      <select
        id={id}
        value={locale}
        onChange={(e) => {
          if (isLocale(e.target.value)) setLocale(e.target.value);
        }}
        data-testid="language-select"
      >
        {LOCALES.map((l) => (
          <option key={l} value={l} lang={l}>
            {LOCALE_NAMES[l]}
          </option>
        ))}
      </select>
    </span>
  );
}
