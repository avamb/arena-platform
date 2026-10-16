/**
 * The event wizard, sections "Event" and "Venue" (task 6-1a, spec 40 5.6,
 * plan stage 6). A FORM, not a dialog: both sections stand on one page and
 * the platform checks them while the person types, with the same code the
 * import runs (`?dry_run=true`). Dates, tickets and saving are 6-1b and 6-1c.
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
import { buildBundle } from "@/lib/wizard/bundle";
import { clientErrors } from "@/lib/wizard/clientErrors";
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
} from "@/lib/wizard/validation";
import { WizardEventSection } from "@/components/WizardEventSection";
import { WizardVenueSection } from "@/components/WizardVenueSection";
import { useOrg } from "@/screens/OrgLayout";

function makeDraftId(): string {
  try {
    return crypto.randomUUID();
  } catch {
    return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
  }
}

const IDLE: SchedulerState<CheckAnswer> = { status: "idle", last: null, error: null };

/** The rate-limited check of the bundle. The scheduler asks, the hook only shows. */
function useBundleCheck(orgId: string) {
  const [state, setState] = useState<SchedulerState<CheckAnswer>>(IDLE);
  const ref = useRef<Scheduler<string, CheckAnswer> | null>(null);
  useEffect(() => {
    const s = createScheduler<string, CheckAnswer>({
      // The payload travels as its JSON text, which is also its identity.
      run: (json) => checkEventBundle(orgId, JSON.parse(json) as unknown),
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

function Wizard() {
  const { t } = useI18n();
  const { orgId } = useOrg();
  const [draft, setDraft] = useState<WizardDraft>(() => loadDraft(orgId) ?? newDraft(makeDraftId()));
  const [touched, setTouched] = useState<ReadonlySet<FormField>>(new Set());

  const dispatch = useCallback((action: DraftAction) => setDraft((d) => reduceDraft(d, action)), []);
  const touch = useCallback(
    (field: FormField) => setTouched((s) => (s.has(field) ? s : new Set(s).add(field))),
    [],
  );
  useEffect(() => saveDraft(orgId, draft), [orgId, draft]);

  const { state, request, retry } = useBundleCheck(orgId);
  const blank = isBlank(draft);
  const bundleJson = useMemo(() => JSON.stringify(buildBundle(draft, { now: new Date(), orgId })), [draft, orgId]);
  // The stub date moves only with the calendar day, so the key is steady while the person types.
  useEffect(() => {
    if (!blank) request(bundleJson);
  }, [blank, bundleJson, request]);

  const answer = state.last?.result;
  const split = useMemo(() => (answer === undefined ? null : splitErrors(answer.errors)), [answer]);
  const mine = clientErrors(draft);
  const shownServer = split === null ? {} : visibleErrors(split, touched, false);

  const errorOf = (field: FormField): string | undefined => {
    const own = mine[field];
    if (own !== undefined && touched.has(field)) return t(own);
    const first = shownServer[field]?.[0];
    return first === undefined ? undefined : textOfError(first, t);
  };

  const serverFailing = split === null ? [] : failingFields(split);
  const allFailing = new Set<FormField>([...serverFailing, ...(Object.keys(mine) as FormField[])]);
  const visibleCount = [...allFailing].filter((f) => errorOf(f) !== undefined).length;
  const fresh = state.last !== undefined && state.last !== null && state.last.key === bundleJson;

  let status: { text: string; tone: "ok" | "bad" | "plain" };
  if (state.error !== null) status = { text: t("wizard.check.failed"), tone: "bad" };
  else if (state.status === "pending") status = { text: t("wizard.check.pending"), tone: "plain" };
  else if (state.status === "checking") status = { text: t("wizard.check.checking"), tone: "plain" };
  else if (blank || !fresh) status = { text: t("wizard.check.idle"), tone: "plain" };
  else if (allFailing.size === 0 && split !== null && split.general.length === 0) status = { text: t("wizard.check.ok"), tone: "ok" };
  else if (visibleCount > 0) status = { text: t("wizard.check.problems", { count: visibleCount }), tone: "bad" };
  else status = { text: t("wizard.check.idle"), tone: "plain" };

  const warnings = (answer?.warnings ?? []).map((w) => {
    const key = checkWarningKey(w.code);
    return { code: w.code, text: key === null ? w.message : t(key) };
  });
  const general = split?.general ?? [];

  const onClear = () => {
    clearDraft(orgId);
    setDraft(newDraft(makeDraftId()));
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
              <li key={`${e.code}|${e.field}`}>{textOfError(e, t)}</li>
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
