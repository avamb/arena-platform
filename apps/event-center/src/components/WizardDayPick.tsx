/**
 * A day of the wizard's dates and tickets (task 6-1b): the chosen day in
 * words, a button that opens the shared `DateField` (month grid, typed date
 * with confirmation) and closes it again when a day is picked. Several of
 * these stand on one page (a date, a last day per step, a day per price
 * change), so the calendar is closed until asked for.
 */
import { useState } from "react";
import { DateField } from "@/components/DateField";
import { dateWords } from "@/lib/dates";
import { useI18n } from "@/lib/i18n";

export interface WizardDayPickProps {
  /** ISO day or "". */
  value: string;
  onChange: (iso: string) => void;
  label: string;
  chooseLabel: string;
  changeLabel: string;
  /** Shown when no day is chosen. */
  emptyLabel: string;
  /** The venue's time zone: today is counted there. */
  timezone?: string;
  min?: string;
  max?: string;
  /** When given, a button takes the day away again. */
  clearLabel?: string;
  error?: string;
  testId: string;
}

export function WizardDayPick({ value, onChange, label, chooseLabel, changeLabel, emptyLabel, timezone, min, max, clearLabel, error, testId }: WizardDayPickProps) {
  const { t, locale } = useI18n();
  const [open, setOpen] = useState(false);
  return (
    <div className="wizard-day" data-testid={testId}>
      <p>
        <strong>{label}</strong>
        {": "}
        <span data-testid={`${testId}-value`} data-value={value}>
          {value === "" ? emptyLabel : dateWords(locale, value)}
        </span>
      </p>
      <div className="actions">
        <button type="button" className="btn" aria-expanded={open} onClick={() => setOpen((o) => !o)} data-testid={`${testId}-toggle`}>
          {open ? t("wizard.date.closeCalendar") : value === "" ? chooseLabel : changeLabel}
        </button>
        {clearLabel !== undefined && value !== "" && (
          <button
            type="button"
            className="btn"
            onClick={() => {
              onChange("");
              setOpen(false);
            }}
            data-testid={`${testId}-clear`}
          >
            {clearLabel}
          </button>
        )}
      </div>
      {open && (
        <DateField
          value={value === "" ? null : value}
          onChange={(iso) => {
            onChange(iso);
            setOpen(false);
          }}
          timezone={timezone}
          min={min}
          max={max}
          label={label}
        />
      )}
      {error !== undefined && (
        <p className="field-error" data-testid={`${testId}-error`}>
          {error}
        </p>
      )}
    </div>
  );
}
