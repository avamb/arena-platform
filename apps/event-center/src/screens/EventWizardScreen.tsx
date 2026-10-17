/**
 * The event wizard, sections "Event", "Venue" and "Dates and tickets" (tasks
 * 6-1a and 6-1b, spec 40 5.6, plan stage 6). A FORM, not a dialog: the
 * sections stand on one page and the platform checks them while the person
 * types, with the same code the import runs (`?dry_run=true`), once per date
 * (the import takes one date per request).
 *
 * "Create event" (6-1c) is the ONLY write: one explicit POST of the checked
 * bundle, a DRAFT event that is not on sale (lib/wizard/save.ts). Release
 * gating: the build flag VITE_FEATURE_WIZARD (on in the release image, "0"
 * switches it off) and the `import.bil24_session` permission the import route
 * itself demands. The draft lives in this browser (lib/wizard/draft.ts) and is
 * removed only after the server confirmed the save.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { checkEventBundle, createEventFromBundle } from "@/lib/api/endpoints";
import { posterApi } from "@/components/EventPoster";
import { usePosterPicker } from "@/components/PosterPicker";
import { useWizardPoster } from "@/components/useWizardPoster";
import { WizardPosterSection } from "@/components/WizardPosterSection";
import { attachPoster, createWithPoster, type Failure } from "@/lib/posterFlow";
import { posterCapabilities, posterStepSettled } from "@/lib/posters";
import { wizardGate } from "@/lib/features";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import { buildBundle, buildBundles } from "@/lib/wizard/bundle";
import { createCheckRunner, type MultiAnswer } from "@/lib/wizard/checkRunner";
import { clientErrors } from "@/lib/wizard/clientErrors";
import { dateClientErrors, dateFieldKey, mapDateErrors, type DateErrors } from "@/lib/wizard/dateErrors";
import { effectiveTickets } from "@/lib/wizard/datesDraft";
import {
  clearDraft,
  clearDraftIfSaved,
  hasPosterRefs,
  isBlank,
  loadDraft,
  newDraft,
  recordPosterProgress,
  reduceDraft,
  saveDraft,
  type DraftAction,
  type WizardDraft,
} from "@/lib/wizard/draft";
import { makeId } from "@/lib/wizard/ids";
import { checkErrorKey, checkWarningKey } from "@/lib/wizard/messages";
import { createSaver, saveBlockers, type SaveOutcome } from "@/lib/wizard/save";
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

/** What the Create button shows after a press. */
type SaveView =
  | { phase: "idle" }
  | { phase: "saving" }
  | { phase: "saved" }
  /** The poster could not be uploaded, so NO event was created. */
  | { phase: "posterUploadFailed"; failure: Failure }
  /** The event EXISTS; only its poster is not attached. */
  | { phase: "attachFailed"; failure: Failure | null }
  | { phase: "attachRetrying" }
  | { phase: "failed"; outcome: Exclude<SaveOutcome, { kind: "saved" }>; fields: CheckError[] };

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
  const { access, orgId } = useOrg();
  const gate = wizardGate(access.can);
  if (gate === "off") {
    return (
      <section aria-labelledby="wizard-title">
        <h1 id="wizard-title">{t("wizard.title")}</h1>
        <p className="notice" role="status" data-testid="wizard-off">
          {t("common.error.notFound")}
        </p>
      </section>
    );
  }
  if (gate === "noAccess") {
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
  // Cached organization access can keep this route mounted across a scope change.
  // A new instance loads that organization's draft and retires old async handlers.
  return <Wizard key={orgId} />;
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
  const { t, locale } = useI18n();
  const { orgId, access } = useOrg();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const saver = useMemo(() => createSaver((bundle) => createEventFromBundle(orgId, bundle)), [orgId]);
  const [saveView, setSaveView] = useState<SaveView>({ phase: "idle" });
  const defaultName = t("wizard.cat.defaultName");
  const [draft, setDraft] = useState<WizardDraft>(() => loadDraft(orgId) ?? newDraft(makeId(), new Date(), defaultName));
  const [touched, setTouched] = useState<ReadonlySet<string>>(new Set());

  const dispatch = useCallback((action: DraftAction) => setDraft((d) => reduceDraft(d, action)), []);
  // One picker per draft: another draft (or organization) revokes the preview and drops a slow check.
  const picker = usePosterPicker(`${orgId}:${draft.id}`);
  const posterCan = posterCapabilities(access.can);
  const posterBusyRef = useRef(false);
  const aliveRef = useRef(true);
  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
    };
  }, []);
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
    // An error about the whole set of categories shows once the person has worked on a category or a price change.
    const reached =
      suffix === "tickets"
        ? [...touched].some((k) => k.startsWith(`${dateKey}.c:`) || k.startsWith(`${dateKey}.s:`))
        : touched.has(fk);
    if (!reached) return undefined;
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

  const poster = useWizardPoster({
    orgId,
    draftId: draft.id,
    mediaId: draft.poster.mediaId,
    canChange: posterCan.canChange,
    picker,
    dispatch,
    locked: saveView.phase === "saving" || saveView.phase === "saved" || saveView.phase === "attachRetrying" || draft.poster.eventId !== "",
  });
  const blockers = saveBlockers(draft);
  const posterBusy = saveView.phase === "saving" && picker.state.phase === "ready" && draft.poster.mediaId === "";
  const checkSettled = !blank && fresh && state.status === "idle" && state.error === null;
  const saving = saveView.phase === "saving" || saveView.phase === "saved" || saveView.phase === "attachRetrying";
  // The event already exists (its poster is what is left): the form is closed, only the attach panel works.
  const created = draft.poster.eventId !== "";
  // A picture that is still being checked, or was refused, must be settled first: it would be silently left out.
  const posterUnsettled = picker.state.phase === "checking" || picker.state.phase === "rejected";
  // Poster first: the person chose a poster or "Continue without a poster" (a person who may not upload is never asked).
  const posterUploading = poster.view.upload.phase === "uploading";
  const posterDecided = posterStepSettled(posterCan, draft.posterDecision);
  const canCreate =
    checkSettled && nothingWrong && blockers.length === 0 && !saving && !created && !posterUnsettled && posterDecided && !posterUploading;
  let createHint: string | null = null;
  if (blockers[0] === "oneDate") createHint = t("wizard.create.hint.oneDate");
  else if (blockers[0] === "existingVenue") createHint = t("wizard.create.hint.existingVenue");
  else if (!checkSettled) createHint = t("wizard.create.hint.checking");
  else if (!nothingWrong) createHint = t("wizard.create.hint.invalid");
  else if (posterUnsettled) createHint = t("wizard.create.hint.poster");
  else if (posterUploading) createHint = t("wizard.create.hint.posterUploading");
  else if (!posterDecided) createHint = t("wizard.create.hint.posterDecision");

  const onCreate = async () => {
    // A double click, or a click while the answer is awaited, never makes a second POST or upload.
    if (!canCreate || saver.inFlight() || posterBusyRef.current) return;
    posterBusyRef.current = true;
    const sent = draft;
    const chosen = picker.state.phase === "ready" ? picker.state : null;
    setSaveView({ phase: "saving" });
    const progress = (patch: { mediaId?: string; eventId?: string }) => {
      // Stored at once and under the draft own id: a screen that is gone must not lose an id.
      recordPosterProgress(orgId, sent.id, patch);
      if (!aliveRef.current) return;
      if (patch.mediaId !== undefined) dispatch({ type: "posterUploaded", mediaId: patch.mediaId });
      if (patch.eventId !== undefined) dispatch({ type: "posterCreated", eventId: patch.eventId });
    };
    const result = await createWithPoster(posterApi(orgId, locale), {
      file: posterCan.canChange && chosen !== null ? chosen.file : null,
      type: posterCan.canChange && chosen !== null ? chosen.info.type : null,
      mediaId: sent.poster.mediaId,
      eventId: sent.poster.eventId,
      save: () => saver.save(sent, orgId),
      onProgress: progress,
    });
    posterBusyRef.current = false;
    if (!aliveRef.current) {
      if (result.kind === "done") clearDraftIfSaved(orgId, sent.id);
      if (result.kind !== "upload_failed" && result.kind !== "save_failed") {
        void queryClient.invalidateQueries({ queryKey: ["events", orgId] });
      }
      return;
    }
    if (result.kind === "done") {
      setSaveView({ phase: "saved" });
      clearDraftIfSaved(orgId, sent.id);
      await queryClient.invalidateQueries({ queryKey: ["events", orgId] });
      void navigate({ to: "/o/$orgId/events/$eventId", params: { orgId, eventId: result.eventId } });
      return;
    }
    if (result.kind === "upload_failed") {
      // Nothing was created. The draft stays whole.
      setSaveView({ phase: "posterUploadFailed", failure: result.failure });
      return;
    }
    if (result.kind === "attach_failed") {
      // The event exists: the list must know it, and nothing here may create another.
      setSaveView({ phase: "attachFailed", failure: result.failure });
      void queryClient.invalidateQueries({ queryKey: ["events", orgId] });
      return;
    }
    const outcome = result.outcome;
    // The draft stays whole. A refusal is explained field by field with the platform own check.
    let fields: CheckError[] = [];
    if (outcome.kind === "rejected") {
      try {
        const again = (await checkEventBundle(orgId, buildBundle(sent, { orgId }, 0))) as CheckAnswer;
        fields = again.errors;
      } catch {
        fields = [];
      }
    }
    if (aliveRef.current) setSaveView({ phase: "failed", outcome, fields });
  };

  /** Retries ONLY the attachment of the stored picture to the created event: no upload, no bundle. */
  const onRetryAttach = async () => {
    if (!created || posterBusyRef.current || draft.poster.mediaId === "") return;
    posterBusyRef.current = true;
    const sent = draft;
    setSaveView({ phase: "attachRetrying" });
    const res = await attachPoster(posterApi(orgId, locale), sent.poster.eventId, sent.poster.mediaId);
    posterBusyRef.current = false;
    if (!aliveRef.current) {
      if (res.kind === "attached") clearDraftIfSaved(orgId, sent.id);
      return;
    }
    if (res.kind === "attached") {
      clearDraftIfSaved(orgId, sent.id);
      await queryClient.invalidateQueries({ queryKey: ["events", orgId] });
      void navigate({ to: "/o/$orgId/events/$eventId", params: { orgId, eventId: sent.poster.eventId } });
      return;
    }
    setSaveView({ phase: "attachFailed", failure: res.failure });
  };

  const onOpenCreated = () => {
    const eventId = draft.poster.eventId;
    clearDraftIfSaved(orgId, draft.id);
    void navigate({ to: "/o/$orgId/events/$eventId", params: { orgId, eventId } });
  };

  const onClear = () => {
    if (posterBusyRef.current) return;
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

      <fieldset className="wizard-lock" disabled={saving || created} data-testid="wizard-sections">
        <WizardPosterSection
          state={picker.state}
          poster={poster}
          decision={draft.posterDecision}
          mediaId={draft.poster.mediaId}
          canChange={posterCan.canChange}
          canView={posterCan.canView}
          busy={saving || created}
          draft={draft}
          dispatch={dispatch}
          defaultCategoryName={defaultName}
        />
        <WizardEventSection draft={draft} dispatch={dispatch} touch={touch} errorOf={errorOf} />
        <WizardVenueSection draft={draft} dispatch={dispatch} touch={touch} errorOf={errorOf} />
        <WizardDatesSection draft={draft} dispatch={dispatch} errors={dateErrors} defaultCategoryName={defaultName} />
      </fieldset>

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

      {saveView.phase === "failed" && <SaveFailure view={saveView} orgId={orgId} t={t} />}
      {saveView.phase === "posterUploadFailed" && (
        <div className="notice notice-bad" role="alert" data-testid="wizard-poster-upload-failed" data-kind={saveView.failure.kind}>
          <p>{t(saveView.failure.kind === "rejected" ? "wizard.create.uploadRejected" : "wizard.create.uploadFailed")}</p>
        </div>
      )}
      {created && saveView.phase !== "saving" && saveView.phase !== "saved" && (
        <div
          className="notice notice-bad"
          role="alert"
          data-testid="wizard-attach-failed"
          data-kind={saveView.phase === "attachFailed" && saveView.failure !== null ? saveView.failure.kind : "stored"}
        >
          <p>{t("wizard.attach.title")}</p>
          <p>{t("wizard.attach.body")}</p>
          {saveView.phase === "attachFailed" && saveView.failure?.kind === "unknown" && <p>{t("poster.fail.attachUnknown")}</p>}
          {saveView.phase === "attachFailed" && saveView.failure?.kind === "rejected" && <p>{t("poster.fail.attachRejected")}</p>}
          {saveView.phase === "attachFailed" && saveView.failure?.kind === "denied" && <p>{t("poster.fail.denied")}</p>}
          <div className="actions">
            {draft.poster.mediaId !== "" &&
              !(saveView.phase === "attachFailed" && (saveView.failure?.kind === "rejected" || saveView.failure?.kind === "denied")) && (
                <button
                  type="button"
                  className="btn btn-primary"
                  onClick={() => void onRetryAttach()}
                  disabled={saveView.phase === "attachRetrying"}
                  aria-busy={saveView.phase === "attachRetrying"}
                  data-testid="wizard-attach-retry"
                >
                  {saveView.phase === "attachRetrying" ? t("wizard.create.attaching") : t("poster.attach.retry")}
                </button>
              )}
            <button type="button" className="btn" onClick={onOpenCreated} data-testid="wizard-attach-open">
              {t("wizard.attach.open")}
            </button>
            <button type="button" className="btn" onClick={onClear} disabled={saveView.phase === "attachRetrying"} data-testid="wizard-attach-fresh">
              {t("wizard.attach.fresh")}
            </button>
          </div>
        </div>
      )}

      <p className="field-hint" data-testid="wizard-create-note">
        {t("wizard.create.note")}
      </p>
      <div className="actions">
        <button
          type="button"
          className="btn btn-primary"
          onClick={() => void onCreate()}
          disabled={!canCreate}
          aria-describedby={createHint === null ? undefined : "wizard-create-hint"}
          data-testid="wizard-create"
        >
          {saving
            ? t(posterBusy ? "wizard.create.uploading" : "wizard.create.saving")
            : saveView.phase === "failed" && saveView.outcome.kind === "unknown"
              ? t("wizard.create.retry")
              : t("wizard.create.button")}
        </button>
        <button type="button" className="btn" onClick={onClear} disabled={(blank && !hasPosterRefs(draft)) || saving} data-testid="wizard-clear">
          {t("wizard.clear")}
        </button>
      </div>
      {createHint !== null && (
        <p className="field-hint" id="wizard-create-hint" data-testid="wizard-create-hint">
          {createHint}
        </p>
      )}
    </section>
  );
}

/** The error in the person's language when the wizard has a text for the code, else the server's own. */
function textOfError(e: CheckError, t: (key: MessageKey) => string): string {
  const key = checkErrorKey(e.code);
  return key === null ? e.message : t(key);
}

function SaveFailure({
  view,
  orgId,
  t,
}: {
  view: Extract<SaveView, { phase: "failed" }>;
  orgId: string;
  t: (key: MessageKey, params?: Record<string, string | number>) => string;
}) {
  const o = view.outcome;
  if (o.kind === "unknown") {
    return (
      <div className="notice notice-bad" role="alert" data-testid="wizard-save-unknown">
        <p>{t("wizard.create.unknown.title")}</p>
        <p>{t("wizard.create.unknown.body")}</p>
        <Link to="/o/$orgId/events" params={{ orgId }} data-testid="wizard-save-open-list">
          {t("wizard.create.openList")}
        </Link>
      </div>
    );
  }
  if (o.kind === "denied") {
    return (
      <div className="notice notice-bad" role="alert" data-testid="wizard-save-denied">
        <p>{t("wizard.create.denied")}</p>
      </div>
    );
  }
  const key = checkErrorKey(o.code);
  return (
    <div className="notice notice-bad" role="alert" data-testid="wizard-save-rejected">
      <p>{t("wizard.create.rejected.title")}</p>
      <p>{key === null ? o.message : t(key)}</p>
      {view.fields.length > 0 && (
        <ul>
          {view.fields.map((e) => (
            <li key={`${e.code}|${e.field}`}>{textOfError(e, t)}</li>
          ))}
        </ul>
      )}
      <p className="field-hint">{t("wizard.create.rejected.fix")}</p>
      {o.requestId !== undefined && <p className="field-hint">{t("wizard.create.requestId", { id: o.requestId })}</p>}
    </div>
  );
}
