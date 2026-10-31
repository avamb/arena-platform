/**
 * The section "Dates and tickets" of the wizard (task 6-1b): one block per
 * date of the event, each with its day, start time, optional end time, the end
 * of ticket sales (the start by default), the doors-open time, its places and
 * its tickets (WizardTicketsEditor). A date from the second on starts like the
 * one before it.
 *
 * Times are the venue's. Nothing is written: the platform's check runs on the
 * bundle of every date (EventWizardScreen).
 */
import { TextField } from "@/components/FormField";
import { WizardDayPick } from "@/components/WizardDayPick";
import { WizardTicketsEditor } from "@/components/WizardTicketsEditor";
import { useI18n } from "@/lib/i18n";
import { isSeatedDraft } from "@/lib/wizard/bundle";
import { dateFieldKey, type DateErrorView } from "@/lib/wizard/dateErrors";
import { MAX_DATES, effectiveTickets } from "@/lib/wizard/datesDraft";
import { venueCapacity, venueTimezone, type DraftAction, type WizardDraft } from "@/lib/wizard/draft";
import { isKnownZone } from "@/lib/wizard/zones";
import { makeId } from "@/lib/wizard/ids";

export interface DatesSectionProps {
  draft: WizardDraft;
  dispatch: (action: DraftAction) => void;
  errors: DateErrorView;
  /** The name of a new category: "Admission" in the person's language. */
  defaultCategoryName: string;
}

export function WizardDatesSection({ draft, dispatch, errors, defaultCategoryName }: DatesSectionProps) {
  const { t } = useI18n();
  const tz = venueTimezone(draft.venue);
  const seats = venueCapacity(draft.venue);
  // Sold by a hall plan: the places and the categories are the plan's ("Hall plan" section).
  const seated = isSeatedDraft(draft);

  return (
    <fieldset className="org-form" data-testid="wizard-dates">
      <legend>{t("wizard.section.dates")}</legend>
      <p className="field-hint" data-testid="wizard-dates-tz">
        {tz !== "" && isKnownZone(tz) ? t("wizard.date.tz", { tz }) : t("wizard.date.tzUnknown")}
      </p>

      {draft.dates.map((date, index) => {
        const key = date.key;
        const parallel = effectiveTickets(draft.dates, index).mode === "parallel";
        const text = (suffix: "time" | "endTime" | "salesEnd" | "doors" | "capacity") => ({
          error: errors.errorOf(key, suffix),
          onBlur: () => errors.touch(dateFieldKey(key, suffix)),
        });
        const set = (field: "time" | "endTime" | "salesEnd" | "doors" | "capacity", value: string) => {
          errors.touch(dateFieldKey(key, field));
          dispatch({ type: "dateField", key, field, value });
        };
        return (
          <fieldset key={key} className="wizard-date" data-testid={`wizard-date-${index}`}>
            <legend>{t("wizard.date.title", { n: index + 1 })}</legend>

            <WizardDayPick
              value={date.day}
              onChange={(iso) => {
                errors.touch(dateFieldKey(key, "day"));
                dispatch({ type: "dateDay", key, value: iso });
              }}
              label={t("wizard.date.day")}
              chooseLabel={t("wizard.date.chooseDay")}
              changeLabel={t("wizard.date.changeDay")}
              emptyLabel={t("wizard.date.noDay")}
              timezone={tz}
              error={errors.errorOf(key, "day")}
              testId={`wizard-date-${index}-day`}
            />
            <TextField label={t("wizard.date.time")} hint={t("wizard.date.timeHint")} value={date.time} onChange={(v) => set("time", v)} testId={`wizard-date-${index}-time`} {...text("time")} />
            <TextField
              label={t("wizard.date.endTime")}
              hint={t("wizard.date.endTimeHint")}
              value={date.endTime}
              onChange={(v) => set("endTime", v)}
              testId={`wizard-date-${index}-endtime`}
              {...text("endTime")}
            />
            <TextField
              label={t("wizard.date.salesEnd")}
              hint={t("wizard.date.salesEndHint")}
              value={date.salesEnd}
              onChange={(v) => set("salesEnd", v)}
              testId={`wizard-date-${index}-sales-end`}
              {...text("salesEnd")}
            />
            <TextField label={t("wizard.date.doors")} hint={t("wizard.date.doorsHint")} value={date.doors} onChange={(v) => set("doors", v)} testId={`wizard-date-${index}-doors`} {...text("doors")} />
            {seated ? (
              <p className="field-hint" data-testid={`wizard-date-${index}-seated`}>
                {t("wizard.seating.dateNote", { plan: draft.seating.planName, seats: draft.seating.seats })}
              </p>
            ) : parallel ? (
              <p className="field-hint">{t("wizard.date.capacityParallel")}</p>
            ) : (
              <TextField
                label={t("wizard.date.capacity")}
                hint={seats === null ? t("wizard.date.capacityUnknown") : t("wizard.date.capacityHint", { seats })}
                value={date.capacity}
                onChange={(v) => set("capacity", v)}
                testId={`wizard-date-${index}-capacity`}
                {...text("capacity")}
              />
            )}

            {!seated && <WizardTicketsEditor draft={draft} index={index} dispatch={dispatch} errors={errors} timezone={tz} />}

            {errors.generalOf(key).length > 0 && (
              <div className="notice notice-bad" role="alert" data-testid={`wizard-date-${index}-general`}>
                <ul>
                  {errors.generalOf(key).map((m) => (
                    <li key={m}>{m}</li>
                  ))}
                </ul>
              </div>
            )}

            {draft.dates.length > 1 && (
              <div className="actions">
                <button type="button" className="btn" onClick={() => dispatch({ type: "dateRemove", key })} data-testid={`wizard-date-${index}-remove`}>
                  {t("wizard.date.remove")}
                </button>
              </div>
            )}
          </fieldset>
        );
      })}

      {draft.dates.length < MAX_DATES ? (
        <div className="actions">
          <button type="button" className="btn" onClick={() => dispatch({ type: "dateAdd", key: makeId(), defaultName: defaultCategoryName })} data-testid="wizard-date-add">
            {t("wizard.date.add")}
          </button>
        </div>
      ) : (
        <p className="field-hint">{t("wizard.date.limit", { max: MAX_DATES })}</p>
      )}
    </fieldset>
  );
}
