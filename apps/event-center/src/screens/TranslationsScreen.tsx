import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { ConfirmPressDialog } from "@/components/ConfirmPressDialog";
import { ErrorNotice } from "@/components/ErrorNotice";
import { TextAreaField, TextField } from "@/components/FormField";
import { RichTextField } from "@/components/RichTextField";
import { SaveBar } from "@/components/SaveBar";
import { UnsavedChangesGuard } from "@/components/UnsavedChangesGuard";
import { Badge, Empty, ErrorBox, Loading } from "@/components/ui";
import {
  deleteEventTranslation,
  listEventTranslations,
  putEventTranslation,
  queueMachineTranslation,
  saveEventReportText,
} from "@/lib/api/endpoints";
import type { ContentTranslationItem, ContentTranslationList } from "@/lib/api/types";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { overMarkupLimit, overVisibleLimit } from "@/lib/richText";
import {
  draftBody,
  draftOf,
  languageLabel,
  machineTargets,
  originalDraft,
  sameDraft,
  translationRows,
  type TranslationRow,
} from "@/lib/translations";
import { useOrg } from "@/screens/OrgLayout";

const NAME_MAX = 200;
const SHORT_MAX = 500;
/** When the list is read again after a machine translation was queued (ms after the press). */
const RELOAD_AFTER_MS = [3000, 8000, 20000];

const listKey = (orgId: string, eventId: string) => ["eventTranslations", orgId, eventId] as const;

/**
 * "Translations" of an event: the languages in a table, and for the chosen language the original and the
 * translation side by side (stacked on a phone). Reached from the "Translations" item of the "Edit" section of the
 * event card. Reading needs event.read (the route), changing needs event.update.
 */
export function TranslationsScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const { eventId } = useParams({ from: "/o/$orgId/events/$eventId/translations" });
  if (!access.can("event.update")) {
    return (
      <article aria-labelledby="tr-title">
        <h1 id="tr-title">{t("tr.title")}</h1>
        <p className="notice" data-testid="tr-no-access">
          {t("tr.noAccess")}
        </p>
      </article>
    );
  }
  return <TranslationsPanel orgId={orgId} eventId={eventId} />;
}

export function TranslationsPanel({ orgId, eventId }: { orgId: string; eventId: string }) {
  const { t, locale } = useI18n();
  const queryClient = useQueryClient();
  const key = listKey(orgId, eventId);
  const list = useQuery({ queryKey: key, queryFn: () => listEventTranslations(orgId, eventId) });
  const [chosen, setChosen] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [failure, setFailure] = useState<{ error: unknown } | null>(null);
  const [busy, setBusy] = useState(false);
  // The editor is re-created when its row changes (new updated_at after a Save): remember the Save across that.
  const [savedLocale, setSavedLocale] = useState<string | null>(null);
  const timers = useRef<ReturnType<typeof setTimeout>[]>([]);

  useEffect(
    () => () => {
      for (const id of timers.current) clearTimeout(id);
      timers.current = [];
    },
    [],
  );

  if (list.isLoading) return <Loading />;
  if (list.error !== null || list.data === undefined) return <ErrorBox error={list.error} onRetry={() => void list.refetch()} />;
  const data = list.data;
  const rows = translationRows(data);
  const row = rows.find((r) => r.locale === chosen) ?? null;
  const targets = machineTargets(data, chosen);

  /** Queues the machine translation and reads the list again a few times: the job runs in the worker. */
  const machine = async (locales: string[], text: string) => {
    if (busy || locales.length === 0) return;
    setBusy(true);
    setFailure(null);
    setNote(null);
    try {
      await queueMachineTranslation(orgId, eventId, locales);
      setNote(text);
      for (const id of timers.current) clearTimeout(id);
      timers.current = RELOAD_AFTER_MS.map((ms) => setTimeout(() => void queryClient.invalidateQueries({ queryKey: key }), ms));
    } catch (error) {
      setFailure({ error });
    } finally {
      setBusy(false);
    }
  };

  const change = (next: (l: ContentTranslationList) => ContentTranslationList) => {
    queryClient.setQueryData<ContentTranslationList>(key, (old) => (old === undefined ? old : next(old)));
  };

  return (
    <article aria-labelledby="tr-title" className="has-save-bar" data-testid="translations">
      <p>
        <Link to="/o/$orgId/events/$eventId" params={{ orgId, eventId }} data-testid="tr-back">
          {t("common.back")}
        </Link>
      </p>
      <h1 id="tr-title">{t("tr.title")}</h1>
      <p className="field-hint">{t("tr.intro", { locale: languageLabel(data.content_locale, locale) })}</p>

      {rows.length === 0 ? (
        <Empty>{t("tr.empty")}</Empty>
      ) : (
        <>
          <LanguageTable rows={rows} chosen={chosen} onChoose={(l) => setChosen(l)} uiLocale={locale} />
          <div className="actions" data-testid="tr-machine-block">
            {data.machine_available && (
              <button
                type="button"
                className="btn"
                disabled={busy || targets.length === 0}
                onClick={() => void machine(targets, t("tr.machine.queued"))}
                data-testid="tr-machine"
              >
                {t("tr.machine")}
              </button>
            )}
          </div>
          {data.machine_available && (
            <p className="field-hint" data-testid="tr-machine-hint">
              {t("tr.machine.hint")}
              {targets.length === 0 && ` ${t("tr.machine.noTarget")}`}
            </p>
          )}
        </>
      )}
      {note !== null && (
        <p className="notice" role="status" data-testid="tr-note">
          {note}
        </p>
      )}
      {failure !== null && <ErrorNotice error={failure.error} testId="tr-error" />}

      {row === null ? (
        rows.length > 0 && <p className="field-hint" data-testid="tr-pick">{t("tr.pick")}</p>
      ) : (
        <TranslationEditor
          key={`${row.locale}:${row.item?.updated_at ?? "none"}`}
          orgId={orgId}
          eventId={eventId}
          list={data}
          row={row}
          busy={busy}
          justSaved={savedLocale === row.locale}
          onQueueMachine={() => void machine([row.locale], t("tr.stale.machineQueued", { locale: languageLabel(row.locale, locale) }))}
          onSaved={(item) => {
            setSavedLocale(item.locale);
            change((l) => ({
              ...l,
              translations: [...l.translations.filter((x) => x.locale !== item.locale), item].sort((a, b) => a.locale.localeCompare(b.locale)),
            }));
          }}
          onDeleted={(loc) => {
            setSavedLocale(null);
            change((l) => ({ ...l, translations: l.translations.filter((x) => x.locale !== loc) }));
            setNote(t("tr.deleted"));
          }}
        />
      )}

      <OriginalReport
        orgId={orgId}
        eventId={eventId}
        stored={data.original.report_text_html ?? ""}
        onSaved={() => void queryClient.invalidateQueries({ queryKey: key })}
      />
    </article>
  );
}

function LanguageTable({
  rows,
  chosen,
  onChoose,
  uiLocale,
}: {
  rows: TranslationRow[];
  chosen: string | null;
  onChoose: (locale: string) => void;
  uiLocale: string;
}) {
  const { t } = useI18n();
  const tone = { ok: "ok", stale: "wait", none: "neutral" } as const;
  return (
    <div className="table-wrap">
      <table className="table" data-testid="tr-table">
        <thead>
          <tr>
            <th scope="col">{t("tr.col.language")}</th>
            <th scope="col">{t("tr.col.state")}</th>
            <th scope="col">{t("tr.col.source")}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.locale} data-testid={`tr-row-${r.locale}`}>
              <th scope="row" data-label={t("tr.col.language")}>
                <button
                  type="button"
                  className={chosen === r.locale ? "btn btn-primary" : "btn"}
                  aria-pressed={chosen === r.locale}
                  onClick={() => onChoose(r.locale)}
                  data-testid={`tr-pick-${r.locale}`}
                >
                  {languageLabel(r.locale, uiLocale)}
                </button>
                {r.site && (
                  <>
                    {" "}
                    <Badge tone="neutral">{t("tr.siteMark")}</Badge>
                  </>
                )}
              </th>
              <td data-label={t("tr.col.state")} data-testid={`tr-state-${r.locale}`}>
                <Badge tone={tone[r.state]}>{t(`tr.state.${r.state}` as MessageKey)}</Badge>
              </td>
              <td data-label={t("tr.col.source")} data-testid={`tr-source-${r.locale}`}>
                {r.source !== null && t(`tr.source.${r.source}` as MessageKey)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function TranslationEditor({
  orgId,
  eventId,
  list,
  row,
  busy: machineBusy,
  justSaved,
  onQueueMachine,
  onSaved,
  onDeleted,
}: {
  orgId: string;
  eventId: string;
  list: ContentTranslationList;
  row: TranslationRow;
  busy: boolean;
  justSaved: boolean;
  onQueueMachine: () => void;
  onSaved: (item: ContentTranslationItem) => void;
  onDeleted: (locale: string) => void;
}) {
  const { t, locale: uiLocale } = useI18n();
  const base = draftOf(row.item);
  const [draft, setDraft] = useState(base);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<{ error: unknown } | null>(null);
  const [saved, setSaved] = useState(justSaved);
  const [filled, setFilled] = useState(false);
  const [asking, setAsking] = useState(false);
  const [guardOpen, setGuardOpen] = useState(false);
  const allowLeave = useRef(false);
  const saving = useRef(false);

  const dirty = !sameDraft(draft, base);
  const descLong = overVisibleLimit(draft.description, "description");
  const descMarkup = !descLong && overMarkupLimit(draft.description, "description");
  const repLong = overVisibleLimit(draft.report, "description");
  const repMarkup = !repLong && overMarkupLimit(draft.report, "description");
  const descError = descLong ? t("wizard.err.descriptionLong") : descMarkup ? t("wizard.err.descriptionMarkup") : undefined;
  const repError = repLong ? t("wizard.err.descriptionLong") : repMarkup ? t("wizard.err.descriptionMarkup") : undefined;
  const blocked = draft.name.trim().length > NAME_MAX || draft.short.trim().length > SHORT_MAX || descError !== undefined || repError !== undefined;

  useEffect(() => {
    if (dirty) setSaved(false);
  }, [dirty]);

  const put = (patch: Partial<typeof draft>) => setDraft((d) => ({ ...d, ...patch }));

  const save = async (): Promise<boolean> => {
    if (saving.current || !dirty || blocked) return false;
    saving.current = true;
    setBusy(true);
    setFailure(null);
    try {
      const item = await putEventTranslation(orgId, eventId, row.locale, draftBody(draft));
      setSaved(true);
      onSaved(item);
      return true;
    } catch (error) {
      setFailure({ error });
      return false;
    } finally {
      saving.current = false;
      setBusy(false);
    }
  };

  const stale = row.item?.translation_stale === true;
  const name = languageLabel(row.locale, uiLocale);

  return (
    <section aria-labelledby="tr-edit-title" data-testid="tr-editor" data-locale={row.locale}>
      <UnsavedChangesGuard dirty={dirty} allowLeave={allowLeave} onOpenChange={setGuardOpen} onSaveAndLeave={save} />
      <h2 id="tr-edit-title">
        {t("tr.edit", { locale: name })}{" "}
        {row.source !== null && (
          <Badge tone="neutral">
            <span data-testid="tr-editor-source">{t(`tr.source.${row.source}` as MessageKey)}</span>
          </Badge>
        )}
      </h2>

      {stale && (
        <div className="notice" role="status" data-testid="tr-stale">
          <p>{t("tr.stale.notice")}</p>
          {row.source === "machine" ? (
            <button type="button" className="btn" disabled={machineBusy || !list.machine_available} onClick={onQueueMachine} data-testid="tr-stale-update">
              {t("tr.stale.update")}
            </button>
          ) : (
            <>
              <p data-testid="tr-stale-manual">{t("tr.stale.manualExplain")}</p>
              <button
                type="button"
                className="btn"
                onClick={() => {
                  setDraft((d) => originalDraft(list, d));
                  setFilled(true);
                }}
                data-testid="tr-stale-fill"
              >
                {t("tr.stale.putOriginal")}
              </button>
              {filled && (
                <p className="field-hint" role="status" data-testid="tr-stale-filled">
                  {t("tr.stale.filled")}
                </p>
              )}
            </>
          )}
        </div>
      )}

      <div className="tr-pair">
        <div className="tr-cell">
          <h3>{t("tr.original")}</h3>
          <p className="field-hint">{t("tr.f.name")}</p>
          <p data-testid="tr-original-name">{list.original.name}</p>
        </div>
        <div className="tr-cell">
          <h3>{t("tr.translation", { locale: name })}</h3>
          <TextField
            label={t("tr.f.name")}
            hint={t("tr.f.nameHint")}
            value={draft.name}
            onChange={(v) => put({ name: v })}
            maxLength={NAME_MAX}
            testId="tr-name"
          />
        </div>
      </div>

      <div className="tr-pair">
        <div className="tr-cell">
          <p className="field-hint">{t("tr.f.short")}</p>
          <p data-testid="tr-original-short">{list.original.short_description ?? ""}</p>
        </div>
        <div className="tr-cell">
          <TextAreaField label={t("tr.f.short")} value={draft.short} onChange={(v) => put({ short: v })} maxLength={SHORT_MAX} rows={3} testId="tr-short" />
        </div>
      </div>

      <div className="tr-pair">
        <div className="tr-cell">
          <RichTextField
            profile="description"
            label={`${t("tr.f.description")}: ${t("tr.original")}`}
            value={list.original.description_html ?? ""}
            onChange={() => undefined}
            disabled
            testId="tr-original-description"
          />
        </div>
        <div className="tr-cell">
          <RichTextField
            profile="description"
            orgId={orgId}
            label={t("tr.f.description")}
            error={descError}
            value={draft.description}
            onChange={(v) => put({ description: v })}
            disabled={busy}
            testId="tr-description"
          />
        </div>
      </div>

      <div className="tr-pair">
        <div className="tr-cell">
          <RichTextField
            profile="description"
            label={`${t("tr.f.report")}: ${t("tr.original")}`}
            value={list.original.report_text_html ?? ""}
            onChange={() => undefined}
            disabled
            testId="tr-original-report"
          />
        </div>
        <div className="tr-cell">
          <RichTextField
            profile="description"
            orgId={orgId}
            label={t("tr.f.report")}
            error={repError}
            value={draft.report}
            onChange={(v) => put({ report: v })}
            disabled={busy}
            testId="tr-report"
          />
        </div>
      </div>

      {failure !== null && <ErrorNotice error={failure.error} testId="tr-save-error" />}
      <SaveBar
        dirty={dirty}
        busy={busy}
        blocked={blocked}
        onSave={() => void save()}
        hidden={guardOpen}
        testId="tr-save"
        labelKey="tr.save"
        status={saved && !dirty ? { key: "tr.saved", tone: "ok" } : undefined}
        extra={
          row.item !== null ? (
            <button type="button" className="btn" onClick={() => setAsking(true)} disabled={busy} data-testid="tr-delete">
              {t("tr.delete")}
            </button>
          ) : undefined
        }
      />
      {asking && (
        <ConfirmPressDialog
          title={t("tr.delete.title")}
          subject={name}
          explain={t("tr.delete.explain", { locale: name })}
          confirmLabel={t("tr.delete.confirm")}
          testId="tr-delete-dialog"
          onCancel={() => setAsking(false)}
          onConfirm={async () => {
            await deleteEventTranslation(orgId, eventId, row.locale);
            allowLeave.current = true;
            setAsking(false);
            onDeleted(row.locale);
          }}
        />
      )}
    </section>
  );
}

/** The photo-report text of the ORIGINAL: its own field and its own Save (PUT .../report-text). */
function OriginalReport({ orgId, eventId, stored: first, onSaved }: { orgId: string; eventId: string; stored: string; onSaved: () => void }) {
  const { t } = useI18n();
  // What the server holds now: the text we started with, then the text the last Save stored.
  const [stored, setStored] = useState(first);
  const [text, setText] = useState(first);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<{ error: unknown } | null>(null);
  const [saved, setSaved] = useState(false);
  const saving = useRef(false);
  const dirty = text !== stored;
  const long = overVisibleLimit(text, "description");
  const markup = !long && overMarkupLimit(text, "description");
  const error = long ? t("wizard.err.descriptionLong") : markup ? t("wizard.err.descriptionMarkup") : undefined;

  const save = async () => {
    if (saving.current || !dirty || error !== undefined) return;
    saving.current = true;
    setBusy(true);
    setFailure(null);
    try {
      await saveEventReportText(orgId, eventId, text.trim() === "" ? null : text);
      setStored(text);
      setSaved(true);
      onSaved();
    } catch (e) {
      setFailure({ error: e });
    } finally {
      saving.current = false;
      setBusy(false);
    }
  };

  return (
    <section aria-labelledby="tr-report-title" data-testid="tr-original-report-block">
      <h2 id="tr-report-title">{`${t("tr.f.report")}: ${t("tr.original")}`}</h2>
      <RichTextField
        profile="description"
        orgId={orgId}
        label={t("tr.f.report")}
        hint={t("tr.reportOriginalHint")}
        error={error}
        value={text}
        onChange={(v) => {
          setText(v);
          setSaved(false);
        }}
        disabled={busy}
        testId="tr-report-original-edit"
      />
      {failure !== null && <ErrorNotice error={failure.error} testId="tr-report-error" />}
      <p>
        <button type="button" className="btn" disabled={busy || !dirty || error !== undefined} onClick={() => void save()} data-testid="tr-report-save">
          {t("tr.reportSave")}
        </button>
      </p>
      {saved && !dirty && (
        <p className="notice" role="status" data-testid="tr-report-saved">
          {t("tr.reportSaved")}
        </p>
      )}
    </section>
  );
}
