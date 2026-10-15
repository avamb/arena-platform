/**
 * A date question the way the bot asks it (wizard_dates.go): a month grid of
 * buttons whose days outside [min, max] are dead (never before today in the
 * venue's zone), and a text field whose answer is NEVER taken directly. A
 * typed date becomes one or more readings, spelled with the month in words
 * and the weekday, and the person confirms one of them.
 *
 * It is not a <form>: a screen can put it inside its own form. The value is an
 * ISO day "YYYY-MM-DD"; `onChange` fires only for a pressed day or a confirmed reading.
 */
import { useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { initialDateFieldState, noDaysToPick, reduceDateField, type DateFieldAction, type DateFieldContext } from "@/lib/dateField";
import {
  MONTH_NAMES,
  WEEKDAY_SHORT,
  clampMonth,
  dateBounds,
  dateWords,
  displayDate,
  monthView,
  quickPicks,
  todayInZone,
} from "@/lib/dates";
import { useI18n, type MessageKey } from "@/lib/i18n";

export interface DateFieldProps {
  /** The chosen day, ISO, or null. */
  value: string | null;
  onChange: (iso: string) => void;
  /** The venue's time zone. Today is counted there. UTC when missing. */
  timezone?: string;
  /** Today as an ISO day. Overrides the clock and the zone (tests). */
  today?: string;
  /** First allowed day. Never before today. */
  min?: string;
  /** Last allowed day. */
  max?: string;
  label?: ReactNode;
  disabled?: boolean;
}

export function DateField({ value, onChange, timezone, today: todayProp, min, max, label, disabled = false }: DateFieldProps) {
  const { t, locale } = useI18n();
  const ids = useId();
  const today = todayProp ?? todayInZone(timezone);
  const bounds = dateBounds(today, min, max);
  const ctx: DateFieldContext = { today, bounds };
  const [state, setState] = useState(() => initialDateFieldState(value, ctx));

  // Reading a date disables the field and its button, which would drop the focus
  // on the page body. The focus goes to the first reading, so a keyboard and a
  // screen reader land on the question. "A different date" gives it back to the field.
  const confirmBox = useRef<HTMLDivElement>(null);
  const textInput = useRef<HTMLInputElement>(null);
  const hadReadings = useRef(false);
  const hasReadings = state.pending.length > 0;
  useEffect(() => {
    if (hasReadings) {
      confirmBox.current?.querySelector<HTMLElement>("button")?.focus();
    } else if (hadReadings.current) {
      textInput.current?.focus();
    }
    hadReadings.current = hasReadings;
  }, [hasReadings]);

  const send = (action: DateFieldAction) => {
    const result = reduceDateField(state, action, ctx);
    setState(result.state);
    if (result.picked !== null) onChange(result.picked);
  };

  const month = clampMonth(state.month, bounds);
  const view = monthView(month, bounds);
  const [year, monthNo] = month.split("-").map(Number);
  const monthTitle = `${MONTH_NAMES[locale][monthNo - 1].replace(/^./u, (c) => c.toUpperCase())} ${year}`;
  const empty = noDaysToPick(ctx);

  const onTypedKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key !== "Enter") return;
    e.preventDefault(); // Enter reads the date, it does not submit a form around the field
    send({ type: "submit" });
  };

  const rangeText =
    bounds.max !== ""
      ? t("dateField.range", { min: displayDate(bounds.min), max: displayDate(bounds.max) })
      : t("dateField.rangeFrom", { min: displayDate(bounds.min) });

  return (
    <fieldset className="date-field" disabled={disabled} data-testid="date-field">
      <legend>{label ?? t("dateField.label")}</legend>
      <p role="status" aria-live="polite" data-testid="date-field-selected">
        {value !== null && value !== "" ? t("dateField.selected", { date: dateWords(locale, value) }) : t("dateField.none")}
      </p>
      <p className="muted">{rangeText}</p>

      {empty ? (
        <p className="notice notice-bad" role="alert" data-testid="date-field-empty">
          {t("dateField.noDays", { min: displayDate(bounds.min), max: displayDate(bounds.max) })}
        </p>
      ) : (
        <>
          <div className="date-field-nav">
            <button
              type="button"
              className="btn"
              disabled={view.prevMonth === ""}
              aria-label={t("dateField.prevMonth")}
              onClick={() => send({ type: "month", month: view.prevMonth })}
              data-testid="date-field-prev"
            >
              ‹
            </button>
            <strong aria-live="polite" data-testid="date-field-month">
              {monthTitle}
            </strong>
            <button
              type="button"
              className="btn"
              disabled={view.nextMonth === ""}
              aria-label={t("dateField.nextMonth")}
              onClick={() => send({ type: "month", month: view.nextMonth })}
              data-testid="date-field-next"
            >
              ›
            </button>
          </div>
          <table className="date-field-grid">
            <thead>
              <tr>
                {WEEKDAY_SHORT[locale].map((d) => (
                  <th key={d} scope="col">
                    {d}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {view.weeks.map((week, wi) => (
                <tr key={wi}>
                  {week.map((cell, ci) => (
                    <td key={ci}>
                      {cell !== null && (
                        <button
                          type="button"
                          className={`btn date-day${cell.iso === value ? " btn-primary" : ""}`}
                          disabled={!cell.enabled}
                          aria-pressed={cell.iso === value}
                          aria-label={dateWords(locale, cell.iso)}
                          onClick={() => send({ type: "pick", iso: cell.iso })}
                          data-testid={`date-day-${cell.iso}`}
                        >
                          {cell.day}
                        </button>
                      )}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
          <div className="date-field-quick">
            {quickPicks(today, bounds).map((q) => (
              <button
                key={q.key}
                type="button"
                className="btn"
                onClick={() => send({ type: "pick", iso: q.iso })}
                data-testid={`date-quick-${q.key}`}
              >
                {t(`dateField.${q.key}` as MessageKey)}
              </button>
            ))}
          </div>
        </>
      )}

      <div className="date-field-type">
        <label htmlFor={`${ids}-text`}>{t("dateField.typeLabel")}</label>
        <input
          ref={textInput}
          id={`${ids}-text`}
          type="text"
          inputMode="text"
          autoComplete="off"
          value={state.typed}
          disabled={state.pending.length > 0}
          onChange={(e) => send({ type: "type", text: e.target.value })}
          onKeyDown={onTypedKey}
          aria-describedby={`${ids}-hint`}
          data-testid="date-field-input"
        />
        <p id={`${ids}-hint`} className="muted">
          {t("dateField.typeHint")}
        </p>
        <button
          type="button"
          className="btn"
          disabled={state.pending.length > 0}
          onClick={() => send({ type: "submit" })}
          data-testid="date-field-read"
        >
          {t("dateField.read")}
        </button>
      </div>

      {state.note !== null && (
        <p className="notice notice-bad" role="alert" data-testid="date-field-note">
          {t(state.note)}
        </p>
      )}

      {state.pending.length > 0 && (
        <div ref={confirmBox} role="group" aria-labelledby={`${ids}-confirm`} className="date-field-confirm" data-testid="date-field-confirm">
          <p id={`${ids}-confirm`}>
            {state.pending.length === 1
              ? t("dateField.confirmOne", { raw: state.raw, date: dateWords(locale, state.pending[0]) })
              : t("dateField.confirmMany", { raw: state.raw })}
          </p>
          {state.pending.map((iso) => (
            <button
              key={iso}
              type="button"
              className="btn btn-primary"
              onClick={() => send({ type: "confirm", iso })}
              data-testid={`date-confirm-${iso}`}
            >
              ✔ {dateWords(locale, iso)}
            </button>
          ))}
          <button type="button" className="btn" onClick={() => send({ type: "retry" })} data-testid="date-field-retry">
            {t("dateField.retry")}
          </button>
        </div>
      )}
    </fieldset>
  );
}
