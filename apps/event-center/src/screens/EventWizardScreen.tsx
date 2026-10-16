/**
 * The event wizard, sections "Event", "Venue" and "Dates and tickets" (tasks
 * 6-1a and 6-1b, spec 40 5.6, plan stage 6). A FORM, not a dialog: the
 * sections stand on one page and the platform checks them while the person
 * types, with the same code the import runs (`?dry_run=true`), once per date
 * (the import takes one date per request). Saving is 6-1c.
 *
 * Behind the build flag VITE_FEATURE_WIZARD=1. The draft lives in this
 * browser (lib/wizard/draft.ts). Nothing is written to arena: the check only
 * reads, and the check is rate limited (lib/wizard/validation.ts).
 */
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { checkEventBundle } from "@/lib/api/endpoints";
import { wizardEnabled } from "@/lib/features";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import { buildBundles } from "@/lib/wizard/bundle";
import { createCheckRunner, type MultiAnswer } from "@/lib/wizard/checkRunner";
import { clientErrors } from "@/lib/wizard/clientErrors";
import { dateClientErrors, dateFieldKey, mapDateErrors, type DateErrors } from "@/lib/wizard/dateErrors";
import { effectiveTickets } from "@/lib/wizard/datesDraft";
import {
  clearDraft,
  isBlank,
  loadDraft,
  newDraft,
  reduceDraft,
  saveDraft,
  type DraftAction,
  type WizardDraft,
} from "@/lib/wizard/draft";
import { makeId } from "@/lib/wizard/ids";
import { checkErrorKey, checkWarningKey } from "@/lib/wizard/messages";
import {
  createScheduler,
  failingFields,
  splitErrors,
  visibleErrors,
  type CheckAnswer,
  type CheckError,
  type FormField,
  type Scheduler,
  type SchedulerState,
  type SplitErrors,
} from "@/lib/wizard/validation";
import { WizardDatesSection } from "@/components/WizardDatesSection";
import { WizardEventSection } from "@/components/WizardEventSection";
import { WizardVenueSection } from "@/components/WizardVenueSection";
import { useOrg } from "@/screens/OrgLayout";

const IDLE: SchedulerState<MultiAnswer> = { status: "idle", last: null, error: null };

/** The rate-limited check of the bundles of every date. The scheduler asks, the hook only shows. */
function useBundleCheck(orgId: string) {
  const [state, setState] = useState<SchedulerState<MultiAnswer>>(IDLE);
  const ref = useRef<Scheduler<string, MultiAnswer> | null>(null);
  useEffect(() => {
    const s = createScheduler<string, MultiAnswer>({
      // The payload is the JSON text of the bundles, which is also its identity.
      run: createCheckRunner((bundle) => checkEventBundle(orgId, bundle) as Promise<CheckAnswer>),
      onState: setState,
      keyOf: (json) => json,
    });
    ref.current = s;
    return () => {
      s.dispose();
      ref.current = null;
    };
  }, [orgId]);
  const request = useCallback((json: string) => ref.current?.request(json), []);
  const retry = useCallback(() => ref.current?.retry(), []);
  return { state, request, retry };
}

export function EventWizardScreen() {
  const { t } = useI18n();
  const { access } = useOrg();
  if (!wizardEnabled()) {
    return (
      <section aria-labelledby="wizard-title">
        <h1 id="wizard-title">{t("wizard.title")}</h1>
        <p className="notice" role="status" data-testid="wizard-off">
          {t("common.error.notFound")}
        </p>
      </section>
    );
  }
  if (!access.can("import.bil24_session")) {
    return (
      <section aria-labelledby="wizard-title">
        <h1 id="wizard-title">{t("wizard.title")}</h1>
        <div className="notice notice-bad" role="alert" data-testid="wizard-no-access">
          <p>{t("wizard.noAccess.title")}</p>
          <p>{t("wizard.noAccess.next")}</p>
        </div>
      </section>
    );
  }
  return <Wizard />;
}

/** The event-level errors of every date's answer, each once (the same error comes back for each date). */
function mergeEventErrors(splits: readonly SplitErrors[]): SplitErrors {
  const out: SplitErrors = { byField: {}, general: [], dated: [] };
  const seen = new Set<string>();
  const once = (e: CheckError) => {
    const k = `${e.code}|${e.field}`;
    if (seen.has(k)) return false;
    seen.add(k);
    return true;
  };
  for (const s of splits) {
    for (const f of Object.keys(s.byField) as FormField[]) {
      for (const e of s.byField[f] ?? []) if (once(e)) (out.byField[f] ??= []).push(e);
    }
    for (const e of s.general) if (once(e)) out.general.push(e);
  }
  return out;
}

function Wizard() {
  const { t } = useI18n();
  const { orgId } = useOrg();
  const defaultName = t("wizard.cat.defaultName");
  const [draft, setDraft] = useState<WizardDraft>(() => loadDraft(orgId) ?? newDraft(makeId(), new Date(), defaultName));
  const [touched, setTouched] = useState<ReadonlySet<string>>(new Set());

  const dispatch = useCallback((action: DraftAction) => setDraft((d) => reduceDraft(d, action)), []);
  const touch = useCallback((field: string) => setTouched((s) => (s.has(field) ? s : new Set(s).add(field))), []);
  useEffect(() => saveDraft(orgId, draft), [orgId, draft]);

  const { state, request, retry } = useBundleCheck(orgId);
  const blank = isBlank(draft);
  const bundlesJson = useMemo(() => JSON.stringify(buildBundles(draft, { orgId })), [draft, orgId]);
  useEffect(() => {
    if (!blank) request(bundlesJson);
  }, [blank, bundlesJson, request]);

  const answers = state.last?.result.answers;
  const fresh = state.last !== undefined && state.last !== null && state.last.key === bundlesJson;
  // The answers belong to the dates they were asked about: while the draft has
  // another number of dates, they say nothing about the dates on screen.
  const usable = answers !== undefined && answers.length === draft.dates.length ? answers : undefined;
  const splits = useMemo(() => (usable === undefined ? [] : usable.map((a) => splitErrors(a.errors))), [usable]);
  const eventSplit = useMemo(() => mergeEventErrors(splits), [splits]);

  const mine = clientErrors(draft);
  const mineDates = useMemo(() => dateClientErrors(draft), [draft]);
  const shownServer = visibleErrors(eventSplit, touched as ReadonlySet<FormField>, false);

  const dateServer = useMemo(() => {
    const out = new Map<string, DateErrors>();
    if (usable === undefined) return out;
    draft.dates.forEach((date, i) => {
      out.set(date.key, mapDateErrors(date, effectiveTickets(draft.dates, i), splits[i].dated, i > 0 && date.sameTickets));
    });
    return out;
  }, [usable, splits, draft.dates]);

  const textOf = (e: CheckError) => textOfError(e, t);
  const errorOf = (field: FormField): string | undefined => {
    const own = mine[field];
    if (own !== undefined && touched.has(field)) return t(own);
    const first = shownServer[field]?.[0];
    return first === undefined ? undefined : textOf(first);
  };
  const dateErrorOf = (dateKey: string, suffix: string): string | undefined => {
    const fk = dateFieldKey(dateKey, suffix);
    if (!touched.has(fk)) return undefined;
    const own = mineDates.get(fk);
    if (own !== undefined) return t(own);
    const first = dateServer.get(dateKey)?.byKey.get(fk)?.[0];
    return first === undefined ? undefined : textOf(first);
  };
  const generalOf = (dateKey: string): string[] => (dateServer.get(dateKey)?.general ?? []).map(textOf);

  // Every field that has an error, shown or not.
  const failingDateFields = new Set<string>(mineDates.keys());
  for (const d of dateServer.values()) for (const k of d.byKey.keys()) failingDateFields.add(k);
  const dateGeneral = [...dateServer.values()].some((d) => d.general.length > 0);
  const failing = new Set<FormField>([...failingFields(eventSplit), ...(Object.keys(mine) as FormField[])]);
  const visibleCount =
    [...failing].filter((f) => errorOf(f) !== undefined).length +
    [...failingDateFields].filter((fk) => {
      const dot = fk.indexOf(".");
      return dateErrorOf(fk.slice(0, dot), fk.slice(dot + 1)) !== undefined;
    }).length;
  const nothingWrong = failing.size === 0 && failingDateFields.size === 0 && !dateGeneral && eventSplit.general.length === 0;

  let status: { text: string; tone: "ok" | "bad" | "plain" };
  if (state.error !== null) status = { text: t("wizard.check.failed"), tone: "bad" };
  else if (state.status === "pending") status = { text: t("wizard.check.pending"), tone: "plain" };
  else if (state.status === "checking") status = { text: t("wizard.check.checking"), tone: "plain" };
  else if (blank || !fresh) status = { text: t("wizard.check.idle"), tone: "plain" };
  else if (nothingWrong) status = { text: t("wizard.check.ok"), tone: "ok" };
  else if (visibleCount > 0) status = { text: t("wizard.check.problems", { count: visibleCount }), tone: "bad" };
  else status = { text: t("wizard.check.idle"), tone: "plain" };

  const warningSeen = new Set<string>();
  const warnings = (usable ?? [])
    .flatMap((a) => a.warnings)
    .filter((w) => !warningSeen.has(w.code) && warningSeen.add(w.code))
    .map((w) => {
      const key = checkWarningKey(w.code);
      return { code: w.code, text: key === null ? w.message : t(key) };
    });
  const general = eventSplit.general;

  const dateErrors = { errorOf: dateErrorOf, generalOf, touch };

  const onClear = () => {
    clearDraft(orgId);
    setDraft(newDraft(makeId(), new Date(), defaultName));
    setTouched(new Set());
  };

  return (
    <section aria-labelledby="wizard-title" className="wizard">
      <h1 id="wizard-title">{t("wizard.title")}</h1>
      <p>{t("wizard.intro")}</p>
      <p className="field-hint" data-testid="wizard-draft-note">
        {t("wizard.draftNote")}
      </p>

      <WizardEventSection draft={draft} dispatch={dispatch} touch={touch} errorOf={errorOf} />
      <WizardVenueSection draft={draft} dispatch={dispatch} touch={touch} errorOf={errorOf} />
      <WizardDatesSection draft={draft} dispatch={dispatch} errors={dateErrors} defaultCategoryName={defaultName} />

      <div
        className={status.tone === "bad" ? "notice notice-bad" : status.tone === "ok" ? "notice notice-ok" : "notice"}
        role="status"
        aria-live="polite"
        data-testid="wizard-status"
        data-state={state.error !== null ? "failed" : state.status}
      >
        <p>{status.text}</p>
        {state.error !== null && (
          <button type="button" className="btn" onClick={retry} data-testid="wizard-check-again">
            {t("wizard.check.again")}
          </button>
        )}
        <p className="field-hint">{t("wizard.check.later")}</p>
      </div>

      {general.length > 0 && (
        <div className="notice notice-bad" role="alert" data-testid="wizard-general">
          <p>{t("wizard.general.title")}</p>
          <ul>
            {general.map((e) => (
              <li key={`${e.code}|${e.field}`}>{textOf(e)}</li>
            ))}
          </ul>
        </div>
      )}
      {warnings.length > 0 && (
        <div className="notice" data-testid="wizard-warnings">
          <p>{t("wizard.warnings.title")}</p>
          <ul>
            {warnings.map((w) => (
              <li key={w.code}>{w.text}</li>
            ))}
          </ul>
        </div>
      )}

      <div className="actions">
        <button type="button" className="btn" onClick={onClear} disabled={blank} data-testid="wizard-clear">
          {t("wizard.clear")}
        </button>
      </div>
    </section>
  );
}

/** The error in the person's language when the wizard has a text for the code, else the server's own. */
function textOfError(e: CheckError, t: (key: MessageKey) => string): string {
  const key = checkErrorKey(e.code);
  return key === null ? e.message : t(key);
}
