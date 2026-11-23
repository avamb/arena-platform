/**
 * One tour (spec 63, L-2): name, page address, the description and the partners text in the shared rich editor,
 * the portrait poster 4:5 and the wide image, or a YouTube or Vimeo link for the top of the page, the "hide the
 * cities in the feed" flag and the position. ONE Save sends only what changed (one PATCH). Under the form: the
 * events of the tour with a link to each, and the archive. The API's refusals are shown in words.
 */
import { useRef, useState } from "react";
import { Link, useParams } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { archiveSeries, getSeries, updateSeries } from "@/lib/api/endpoints";
import type { SeriesDetail, SeriesEventCard } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import { formatDate } from "@/lib/format";
import { overMarkupLimit, overVisibleLimit } from "@/lib/richText";
import { changesOf, draftOf, draftProblems, hasChanges, seriesErrorKey, SERIES_NAME_MAX, type DraftProblems, type SeriesDraft } from "@/lib/series";
import { Badge, Empty, ErrorBox, Loading } from "@/components/ui";
import { ErrorNotice } from "@/components/ErrorNotice";
import { TextField } from "@/components/FormField";
import { RichTextField } from "@/components/RichTextField";
import { SaveBar } from "@/components/SaveBar";
import { SeriesImage } from "@/components/SeriesImage";
import { UnsavedChangesGuard } from "@/components/UnsavedChangesGuard";
import { useOrg } from "@/screens/OrgLayout";

function ApiProblem({ error, testId }: { error: unknown; testId: string }) {
  const { t } = useI18n();
  const key = seriesErrorKey(error);
  if (key !== null) {
    return (
      <p className="bad-text" role="alert" data-testid={testId}>
        {t(key)}
      </p>
    );
  }
  return <ErrorNotice error={error} testId={testId} />;
}

export interface SeriesFieldsProps {
  orgId: string;
  draft: SeriesDraft;
  onDraft: (next: Partial<SeriesDraft>) => void;
  problems: DraftProblems;
  /** Why a formatted text is refused, already translated. */
  textProblems: { description?: string; partners?: string };
  canEdit: boolean;
  canView: boolean;
  canUpload: boolean;
  busy: boolean;
}

/** The fields of the tour form. No saving here: the screen owns the draft. */
export function SeriesFields({ orgId, draft, onDraft, problems, textProblems, canEdit, canView, canUpload, busy }: SeriesFieldsProps) {
  const { t } = useI18n();
  const resting = busy || !canEdit;
  const say = (key: DraftProblems[keyof DraftProblems]) => (key === undefined ? undefined : t(key));
  return (
    <div data-testid="series-fields">
      <TextField
        label={t("series.field.name")}
        hint={t("series.hint.name")}
        value={draft.name}
        onChange={(name) => onDraft({ name })}
        maxLength={SERIES_NAME_MAX}
        error={say(problems.name)}
        testId="series-name"
      />
      <TextField
        label={t("series.field.slug")}
        hint={t("series.hint.slug")}
        value={draft.slug}
        onChange={(slug) => onDraft({ slug })}
        error={say(problems.slug)}
        testId="series-slug-input"
      />
      <RichTextField
        profile="description"
        orgId={orgId}
        label={t("series.field.description")}
        hint={t("series.hint.description")}
        error={textProblems.description}
        value={draft.descriptionHtml}
        onChange={(descriptionHtml) => onDraft({ descriptionHtml })}
        disabled={resting}
        testId="series-description"
      />
      <RichTextField
        profile="description"
        orgId={orgId}
        label={t("series.field.partners")}
        hint={t("series.hint.partners")}
        error={textProblems.partners}
        value={draft.partnersHtml}
        onChange={(partnersHtml) => onDraft({ partnersHtml })}
        disabled={resting}
        testId="series-partners"
      />
      <SeriesImage
        orgId={orgId}
        label={t("series.field.poster")}
        hint={t("series.hint.poster", { mb: 5 })}
        mediaId={draft.posterMediaId}
        onChange={(posterMediaId) => onDraft({ posterMediaId })}
        canView={canView}
        canUpload={canUpload && canEdit}
        disabled={busy}
        testId="series-poster"
      />
      <SeriesImage
        orgId={orgId}
        label={t("series.field.hero")}
        hint={t("series.hint.hero", { mb: 5 })}
        mediaId={draft.heroMediaId}
        onChange={(heroMediaId) => onDraft({ heroMediaId })}
        canView={canView}
        canUpload={canUpload && canEdit}
        disabled={busy}
        testId="series-hero"
      />
      <TextField
        label={t("series.field.video")}
        hint={t("series.hint.video")}
        value={draft.videoUrl}
        onChange={(videoUrl) => onDraft({ videoUrl })}
        error={say(problems.videoUrl)}
        testId="series-video"
      />
      <label className="check">
        <input
          type="checkbox"
          checked={draft.hideChildren}
          disabled={resting}
          onChange={(e) => onDraft({ hideChildren: e.target.checked })}
          data-testid="series-hide-children"
        />
        <span>{t("series.field.hideChildren")}</span>
      </label>
      <p className="field-hint">{t("series.hint.hideChildren")}</p>
      <TextField
        label={t("series.field.position")}
        hint={t("series.hint.position")}
        value={draft.position}
        onChange={(position) => onDraft({ position })}
        inputMode="numeric"
        error={say(problems.position)}
        testId="series-position"
      />
    </div>
  );
}

/** The events of the tour, each with a link to its own screen. */
export function SeriesEvents({ orgId, events, canOpen }: { orgId: string; events: readonly SeriesEventCard[]; canOpen: boolean }) {
  const { t, locale } = useI18n();
  return (
    <section aria-labelledby="series-events-title" data-testid="series-events">
      <h2 id="series-events-title">{t("series.events.title")}</h2>
      {events.length === 0 && <Empty>{t("series.events.empty")}</Empty>}
      <ul className="cards">
        {events.map((e) => (
          <li className="card" key={e.id} data-testid="series-event-row">
            {canOpen ? (
              <Link to="/o/$orgId/events/$eventId" params={{ orgId, eventId: e.id }} className="card-link">
                <EventLine event={e} locale={locale} />
              </Link>
            ) : (
              <EventLine event={e} locale={locale} />
            )}
          </li>
        ))}
      </ul>
    </section>
  );
}

function EventLine({ event, locale }: { event: SeriesEventCard; locale: string }) {
  const { t } = useI18n();
  return (
    <>
      <span className="card-title">{event.name}</span>
      <span className="card-meta">
        {event.status === "draft" && <Badge tone="neutral">{t("events.draft")}</Badge>}
        <span>{event.first_session_at !== null ? formatDate(event.first_session_at, null, locale) : t("series.events.noDate")}</span>
      </span>
    </>
  );
}

function ArchiveSection({ series, onDone }: { series: SeriesDetail; onDone: (message: string) => void }) {
  const { t } = useI18n();
  const { orgId } = useOrg();
  const queryClient = useQueryClient();
  const [asking, setAsking] = useState(false);
  const archive = useMutation({
    mutationFn: () => archiveSeries(orgId, series.id),
    onSuccess: () => {
      setAsking(false);
      void queryClient.invalidateQueries({ queryKey: ["series", orgId] });
      onDone(t("series.archive.done", { name: series.name }));
    },
  });
  return (
    <section aria-labelledby="series-archive-title">
      <h2 id="series-archive-title">{t("series.archive.btn")}</h2>
      {!asking ? (
        <p>
          <button type="button" className="btn" onClick={() => { archive.reset(); setAsking(true); }} data-testid="series-archive-open">
            {t("series.archive.btn")}
          </button>
        </p>
      ) : (
        <div className="notice" role="group" aria-label={t("series.archive.title", { name: series.name })} data-testid="series-archive-ask">
          <p>
            <strong>{t("series.archive.title", { name: series.name })}</strong>
          </p>
          <p>{t("series.archive.explain")}</p>
          {archive.error !== null && <ApiProblem error={archive.error} testId="series-archive-error" />}
          <div className="dialog-actions">
            <button type="button" className="btn btn-primary" disabled={archive.isPending} onClick={() => archive.mutate()} data-testid="series-archive-confirm">
              {t("series.archive.yes")}
            </button>
            <button type="button" className="btn" disabled={archive.isPending} onClick={() => setAsking(false)} data-testid="series-archive-cancel">
              {t("series.archive.no")}
            </button>
          </div>
        </div>
      )}
    </section>
  );
}

function SeriesForm({ orgId, series, canEdit, canView, canUpload }: { orgId: string; series: SeriesDetail; canEdit: boolean; canView: boolean; canUpload: boolean }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [base, setBase] = useState(series);
  const [draft, setDraft] = useState<SeriesDraft>(() => draftOf(series));
  const [saved, setSaved] = useState(false);
  const [guardOpen, setGuardOpen] = useState(false);
  const allowLeave = useRef(false);
  const saving = useRef(false);
  const update = useMutation({ mutationFn: (patch: ReturnType<typeof changesOf>) => updateSeries(orgId, series.id, patch) });

  const dirty = canEdit && hasChanges(base, draft);
  const problems = draftProblems(draft);
  const descTooLong = overVisibleLimit(draft.descriptionHtml, "description");
  const partnersTooLong = overVisibleLimit(draft.partnersHtml, "description");
  const textProblems = {
    description: descTooLong
      ? t("wizard.err.descriptionLong")
      : overMarkupLimit(draft.descriptionHtml, "description")
        ? t("wizard.err.descriptionMarkup")
        : undefined,
    partners: partnersTooLong
      ? t("wizard.err.descriptionLong")
      : overMarkupLimit(draft.partnersHtml, "description")
        ? t("wizard.err.descriptionMarkup")
        : undefined,
  };
  const blocked = Object.keys(problems).length > 0 || textProblems.description !== undefined || textProblems.partners !== undefined;

  const onDraft = (next: Partial<SeriesDraft>) => {
    setSaved(false);
    update.reset();
    setDraft((d) => ({ ...d, ...next }));
  };

  /** Saves what changed; true when it is stored (the leave question uses it). */
  const save = async (): Promise<boolean> => {
    if (saving.current || !dirty || blocked) return false;
    saving.current = true;
    try {
      const res = await update.mutateAsync(changesOf(base, draft));
      const next = { ...base, ...res };
      setBase(next);
      setDraft(draftOf(res));
      setSaved(true);
      queryClient.setQueryData<SeriesDetail>(["series", orgId, series.id], (old) => (old === undefined ? old : { ...old, ...res }));
      void queryClient.invalidateQueries({ queryKey: ["series", orgId], exact: true });
      void queryClient.invalidateQueries({ queryKey: ["events", orgId] });
      return true;
    } catch {
      return false;
    } finally {
      saving.current = false;
    }
  };

  return (
    <div>
      <UnsavedChangesGuard dirty={dirty} allowLeave={allowLeave} onOpenChange={setGuardOpen} onSaveAndLeave={save} />
      <SeriesFields
        orgId={orgId}
        draft={draft}
        onDraft={onDraft}
        problems={dirty ? problems : {}}
        textProblems={textProblems}
        canEdit={canEdit}
        canView={canView}
        canUpload={canUpload}
        busy={update.isPending}
      />
      {update.error !== null && <ApiProblem error={update.error} testId="series-error" />}
      {canEdit && (
        <SaveBar
          dirty={dirty}
          busy={update.isPending}
          blocked={blocked}
          onSave={() => void save()}
          hidden={guardOpen}
          testId="series-save"
          status={saved && !dirty ? { key: "saveBar.saved", tone: "ok" } : undefined}
        />
      )}
    </div>
  );
}

export function SeriesDetailScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const { seriesId } = useParams({ from: "/o/$orgId/series/$seriesId" });
  const canRead = access.can("event.read");
  const query = useQuery({ queryKey: ["series", orgId, seriesId], queryFn: () => getSeries(orgId, seriesId), enabled: canRead });
  const [status, setStatus] = useState<string | null>(null);

  const back = (
    <p>
      <Link to="/o/$orgId/series" params={{ orgId }} data-testid="back-to-series">
        {t("series.backToList")}
      </Link>
    </p>
  );

  if (!canRead) {
    return (
      <section>
        <h1>{t("series.title")}</h1>
        <p className="notice notice-bad" role="status" data-testid="series-no-access">
          {t("series.noAccess")}
        </p>
        {back}
      </section>
    );
  }
  if (query.isLoading) return <Loading />;
  if (query.error !== null) {
    if (seriesErrorKey(query.error) === "series.err.gone") {
      return (
        <section>
          {back}
          <p className="notice" role="status" data-testid="series-gone">
            {t("series.gone")}
          </p>
        </section>
      );
    }
    return <ErrorBox error={query.error} onRetry={() => void query.refetch()} />;
  }
  const series = query.data;
  if (series === undefined) return <Loading />;
  const archived = series.archived_at !== null;
  const canManage = access.can("event.update") && !archived;

  return (
    <section aria-labelledby="series-title" className={canManage ? "has-save-bar" : undefined}>
      {back}
      <h1 id="series-title" data-testid="series-heading">
        {series.name}
      </h1>
      {archived && <Badge tone="neutral">{t("series.archivedBadge")}</Badge>}
      {!access.can("event.update") && (
        <p className="notice" role="status" data-testid="series-read-only">
          {t("series.readOnly")}
        </p>
      )}
      <p className="notice notice-ok" role="status" aria-live="polite" data-testid="series-status" hidden={status === null}>
        {status ?? ""}
      </p>
      <SeriesForm
        key={series.id}
        orgId={orgId}
        series={series}
        canEdit={canManage}
        canView={access.can("media.read")}
        canUpload={access.can("media.write")}
      />
      <SeriesEvents orgId={orgId} events={series.events} canOpen />
      {canManage && <ArchiveSection series={series} onDone={setStatus} />}
    </section>
  );
}
