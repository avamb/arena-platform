/**
 * The tickets of ONE date in the wizard (task 6-1b): the bot's three ways to
 * sell categories. One category (with price changes by day), several at once
 * (each with its own places), or several one after another (each step sells
 * until a last day, until a number of tickets, or both; the last one has no
 * end). A date from the second on can sell "the same as the date before".
 *
 * Nothing here decides a rule: it shows the draft, sends the answers to the
 * reducer and shows the errors it is given (lib/wizard/dateErrors.ts).
 */
import { useId } from "react";
import { TextField } from "@/components/FormField";
import { WizardDayPick } from "@/components/WizardDayPick";
import { useI18n } from "@/lib/i18n";
import { catSuffix, dateFieldKey, stepSuffix, type DateErrorView } from "@/lib/wizard/dateErrors";
import { MAX_CATEGORIES, MAX_PRICE_STEPS, TICKET_MODES, type DateEntry, type TicketMode } from "@/lib/wizard/datesDraft";
import type { DraftAction, WizardDraft } from "@/lib/wizard/draft";
import { makeId } from "@/lib/wizard/ids";

export interface TicketsEditorProps {
  draft: WizardDraft;
  index: number;
  dispatch: (action: DraftAction) => void;
  errors: DateErrorView;
  timezone: string;
}

export function WizardTicketsEditor({ draft, index, dispatch, errors, timezone }: TicketsEditorProps) {
  const { t } = useI18n();
  const ids = useId();
  const date: DateEntry = draft.dates[index];
  const same = index > 0 && date.sameTickets;
  const own = date.tickets;
  const key = date.key;
  const tkErr = errors.errorOf(key, "tickets");

  const sourceChoice = index > 0 && (
    <div role="radiogroup" aria-label={t("wizard.tickets.title")}>
      {([true, false] as const).map((isSame) => (
        <label key={String(isSame)} className="check">
          <input
            type="radio"
            name={`${ids}-source`}
            checked={date.sameTickets === isSame}
            onChange={() => dispatch({ type: "dateSame", key, same: isSame })}
            data-testid={`wizard-tickets-${index}-${isSame ? "same" : "own"}`}
          />
          <span>{t(isSame ? "wizard.tickets.same" : "wizard.tickets.own")}</span>
        </label>
      ))}
    </div>
  );

  if (same) {
    return (
      <fieldset className="org-form" data-testid={`wizard-tickets-${index}`}>
        <legend>{t("wizard.tickets.title")}</legend>
        {sourceChoice}
        <p className="field-hint" data-testid={`wizard-tickets-${index}-same-note`}>
          {t("wizard.tickets.sameNote")}
        </p>
        {tkErr !== undefined && (
          <p className="field-error" data-testid={`wizard-tickets-${index}-error`}>
            {tkErr}
          </p>
        )}
      </fieldset>
    );
  }

  const setMode = (mode: TicketMode) => dispatch({ type: "ticketMode", key, mode, catKey: makeId() });
  const single = own.mode === "single";
  const sequence = own.mode === "sequence";
  const cats = own.categories;
  // A step may not end before the one before it: that step's last day is the earliest to choose.
  const previousUntil = (j: number): string | undefined => {
    for (let k = j - 1; k >= 0; k -= 1) if (cats[k].until !== "") return cats[k].until;
    return undefined;
  };

  return (
    <fieldset className="org-form" data-testid={`wizard-tickets-${index}`}>
      <legend>{t("wizard.tickets.title")}</legend>
      {sourceChoice}

      <div role="radiogroup" aria-label={t("wizard.tickets.mode")}>
        {TICKET_MODES.map((m) => (
          <label key={m} className="check">
            <input
              type="radio"
              name={`${ids}-mode`}
              checked={own.mode === m}
              onChange={() => setMode(m)}
              data-testid={`wizard-tickets-${index}-mode-${m}`}
            />
            <span>{t(`wizard.tickets.mode.${m}` as const)}</span>
          </label>
        ))}
      </div>
      <p className="field-hint">{t(`wizard.tickets.modeHint.${own.mode}` as const)}</p>
      {tkErr !== undefined && (
        <p className="field-error" data-testid={`wizard-tickets-${index}-error`}>
          {tkErr}
        </p>
      )}

      {cats.map((c, j) => {
        const last = j + 1 === cats.length;
        const field = (name: "name" | "price" | "places" | "limit") => ({
          error: errors.errorOf(key, catSuffix(c.key, name)),
          onBlur: () => errors.touch(dateFieldKey(key, catSuffix(c.key, name))),
        });
        const set = (name: "name" | "price" | "places" | "limit", value: string) => {
          errors.touch(dateFieldKey(key, catSuffix(c.key, name)));
          dispatch({ type: "catField", key, catKey: c.key, field: name, value });
        };
        return (
          <fieldset key={c.key} className="wizard-cat" data-testid={`wizard-cat-${index}-${j}`}>
            {!single && <legend>{t(sequence ? "wizard.cat.titleStep" : "wizard.cat.title", { n: j + 1 })}</legend>}
            <TextField label={t("wizard.cat.name")} value={c.name} onChange={(v) => set("name", v)} testId={`wizard-cat-${index}-${j}-name`} {...field("name")} />
            <TextField
              label={t("wizard.cat.price")}
              hint={t("wizard.cat.priceHint")}
              value={c.price}
              onChange={(v) => set("price", v)}
              testId={`wizard-cat-${index}-${j}-price`}
              {...field("price")}
            />
            {own.mode === "parallel" && (
              <TextField
                label={t("wizard.cat.places")}
                hint={t("wizard.cat.placesHint")}
                value={c.places}
                onChange={(v) => set("places", v)}
                testId={`wizard-cat-${index}-${j}-places`}
                {...field("places")}
              />
            )}
            {sequence && !last && (
              <>
                <WizardDayPick
                  value={c.until}
                  onChange={(iso) => {
                    errors.touch(dateFieldKey(key, catSuffix(c.key, "until")));
                    dispatch({ type: "catField", key, catKey: c.key, field: "until", value: iso });
                  }}
                  label={t("wizard.cat.until")}
                  chooseLabel={t("wizard.cat.untilChoose")}
                  changeLabel={t("wizard.cat.untilChange")}
                  emptyLabel={t("wizard.cat.untilNone")}
                  clearLabel={t("wizard.cat.untilClear")}
                  timezone={timezone}
                  min={previousUntil(j)}
                  max={date.day === "" ? undefined : date.day}
                  error={errors.errorOf(key, catSuffix(c.key, "until"))}
                  testId={`wizard-cat-${index}-${j}-until`}
                />
                <TextField
                  label={t("wizard.cat.limit")}
                  hint={t("wizard.cat.limitHint")}
                  value={c.limit}
                  onChange={(v) => set("limit", v)}
                  testId={`wizard-cat-${index}-${j}-limit`}
                  {...field("limit")}
                />
              </>
            )}
            {sequence && last && <p className="field-hint">{t("wizard.cat.lastHint")}</p>}
            {!single && cats.length > 2 && (
              <div className="actions">
                <button type="button" className="btn" onClick={() => dispatch({ type: "catRemove", key, catKey: c.key })} data-testid={`wizard-cat-${index}-${j}-remove`}>
                  {t("wizard.cat.remove")}
                </button>
              </div>
            )}
          </fieldset>
        );
      })}

      {!single && cats.length < MAX_CATEGORIES && (
        <div className="actions">
          <button type="button" className="btn" onClick={() => dispatch({ type: "catAdd", key, catKey: makeId() })} data-testid={`wizard-cat-${index}-add`}>
            {t(sequence ? "wizard.cat.addStep" : "wizard.cat.add")}
          </button>
        </div>
      )}

      {single && (
        <fieldset className="wizard-cat" data-testid={`wizard-steps-${index}`}>
          <legend>{t("wizard.steps.title")}</legend>
          <p className="field-hint">{t("wizard.steps.hint")}</p>
          {own.schedule.map((s, k) => (
            <div key={s.key} data-testid={`wizard-step-${index}-${k}`}>
              <p>
                <strong>{t("wizard.steps.title1", { n: k + 1 })}</strong>
              </p>
              <WizardDayPick
                value={s.from}
                onChange={(iso) => {
                  errors.touch(dateFieldKey(key, stepSuffix(s.key, "from")));
                  dispatch({ type: "stepField", key, stepKey: s.key, field: "from", value: iso });
                }}
                label={t("wizard.steps.from")}
                chooseLabel={t("wizard.steps.fromChoose")}
                changeLabel={t("wizard.date.changeDay")}
                emptyLabel={t("wizard.date.noDay")}
                timezone={timezone}
                min={k > 0 && own.schedule[k - 1].from !== "" ? own.schedule[k - 1].from : undefined}
                max={date.day === "" ? undefined : date.day}
                error={errors.errorOf(key, stepSuffix(s.key, "from"))}
                testId={`wizard-step-${index}-${k}-from`}
              />
              <TextField
                label={t("wizard.steps.price")}
                value={s.price}
                onChange={(v) => {
                  errors.touch(dateFieldKey(key, stepSuffix(s.key, "price")));
                  dispatch({ type: "stepField", key, stepKey: s.key, field: "price", value: v });
                }}
                onBlur={() => errors.touch(dateFieldKey(key, stepSuffix(s.key, "price")))}
                error={errors.errorOf(key, stepSuffix(s.key, "price"))}
                testId={`wizard-step-${index}-${k}-price`}
              />
              <div className="actions">
                <button type="button" className="btn" onClick={() => dispatch({ type: "stepRemove", key, stepKey: s.key })} data-testid={`wizard-step-${index}-${k}-remove`}>
                  {t("wizard.steps.remove")}
                </button>
              </div>
            </div>
          ))}
          {own.schedule.length < MAX_PRICE_STEPS && (
            <div className="actions">
              <button type="button" className="btn" onClick={() => dispatch({ type: "stepAdd", key, stepKey: makeId() })} data-testid={`wizard-step-${index}-add`}>
                {t("wizard.steps.add")}
              </button>
            </div>
          )}
        </fieldset>
      )}
    </fieldset>
  );
}
