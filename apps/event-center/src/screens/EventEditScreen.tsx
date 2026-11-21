import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { EmailNoteSection } from "@/components/EmailNoteSection";
import { ErrorNotice } from "@/components/ErrorNotice";
import { EventPoster } from "@/components/EventPoster";
import { HelpPopover } from "@/components/HelpPopover";
import { TextField } from "@/components/FormField";
import { RichTextField } from "@/components/RichTextField";
import { SaveBar } from "@/components/SaveBar";
import { UnsavedChangesGuard } from "@/components/UnsavedChangesGuard";
import { ApiError } from "@/lib/api/http";
import { saveEventDetails } from "@/lib/api/endpoints";
import { useI18n } from "@/lib/i18n";
import { artOfStored, looksLikeSiteHtml, overMarkupLimit, overVisibleLimit } from "@/lib/richText";
import { descriptionBaseOf } from "@/lib/tgEditEvent";
import { posterCapabilities } from "@/lib/posters";
import { ErrorBox, Loading } from "@/components/ui";
import { useEventsList } from "@/screens/EventsScreen";
import { useOrg } from "@/screens/OrgLayout";

const NAME_MAX = 200;

/**
 * "Edit" of an event in the web event center: the name and the description (one Save for both, in place), the
 * poster and the text of the buyer letters (each with its own save). Reached from the "Edit" button of the event
 * card; the route's right is event.update, the one the API asks for the PATCH.
 */
export function EventEditScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const { eventId } = useParams({ from: "/o/$orgId/events/$eventId/edit" });
  const events = useEventsList(orgId);
  const event = events.data?.events.find((e) => e.id === eventId);
  if (events.isLoading) return <Loading />;
  if (events.error !== null) return <ErrorBox error={events.error} onRetry={() => void events.refetch()} />;
  if (event === undefined) return <ErrorBox error={new ApiError(404, "event.not_found", "")} />;
  if (!access.can("event.update")) {
    return (
      <article aria-labelledby="event-edit-title">
        <h1 id="event-edit-title">{t("eventEdit.title")}</h1>
        <p className="notice" data-testid="event-edit-no-access">
          {t("eventEdit.noAccess")}
        </p>
      </article>
    );
  }
  const poster = posterCapabilities(access.can);
  return (
    <article aria-labelledby="event-edit-title" className="has-save-bar">
      <p>
        <Link to="/o/$orgId/events/$eventId" params={{ orgId, eventId }} data-testid="event-edit-back">
          {t("common.back")}
        </Link>
      </p>
      <h1 id="event-edit-title">{t("eventEdit.title")}</h1>
      <DetailsForm
        key={`${orgId}:${eventId}`}
        orgId={orgId}
        eventId={eventId}
        name={event.name}
        description={descriptionBaseOf(event)}
        descriptionFormat={event.description_format}
      />
      <EventPoster
        key={`poster:${orgId}:${eventId}`}
        orgId={orgId}
        eventId={eventId}
        eventName={event.name}
        posterMediaId={event.poster_media_id ?? null}
        canView={poster.canView}
        canChange={poster.canChange}
      />
      <EmailNoteSection
        key={`note:${orgId}:${eventId}`}
        orgId={orgId}
        eventId={eventId}
        note={event.email_note}
        noteFormat={event.email_note_format}
        canEdit
      />
    </article>
  );
}

function DetailsForm({
  orgId,
  eventId,
  name,
  description,
  descriptionFormat,
}: {
  orgId: string;
  eventId: string;
  name: string;
  description: string | null | undefined;
  descriptionFormat: string | null | undefined;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const siteHtml = looksLikeSiteHtml(description, descriptionFormat);
  const [base, setBase] = useState({ name, description: artOfStored(description, descriptionFormat, "description") });
  const [draftName, setDraftName] = useState(base.name);
  const [draftText, setDraftText] = useState(base.description);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<{ error: unknown } | null>(null);
  const [saved, setSaved] = useState(false);
  const [guardOpen, setGuardOpen] = useState(false);
  const allowLeave = useRef(false);
  const saving = useRef(false);

  const nameChanged = draftName.trim() !== base.name;
  const textChanged = !siteHtml && draftText !== base.description;
  const dirty = nameChanged || textChanged;
  const nameBad = draftName.trim() === "" || draftName.trim().length > NAME_MAX;
  const tooLong = overVisibleLimit(draftText, "description");
  const tooMuchMarkup = !tooLong && overMarkupLimit(draftText, "description");
  const problem = tooLong ? t("wizard.err.descriptionLong") : tooMuchMarkup ? t("wizard.err.descriptionMarkup") : undefined;
  const blocked = (nameChanged && nameBad) || (textChanged && (tooLong || tooMuchMarkup));

  useEffect(() => {
    if (dirty) setSaved(false);
  }, [dirty]);

  /** Saves what changed; true when it is stored (the leave question uses it). */
  const save = async (): Promise<boolean> => {
    if (saving.current || !dirty || blocked) return false;
    saving.current = true;
    setBusy(true);
    setFailure(null);
    try {
      await saveEventDetails(orgId, eventId, {
        ...(nameChanged ? { name: draftName.trim() } : {}),
        ...(textChanged ? { description: draftText } : {}),
      });
      setBase({ name: nameChanged ? draftName.trim() : base.name, description: textChanged ? draftText : base.description });
      if (nameChanged) setDraftName(draftName.trim());
      setSaved(true);
      await queryClient.invalidateQueries({ queryKey: ["events", orgId] });
      return true;
    } catch (error) {
      setFailure({ error });
      return false;
    } finally {
      saving.current = false;
      setBusy(false);
    }
  };

  return (
    <section aria-labelledby="event-details-title" data-testid="event-details">
      <UnsavedChangesGuard dirty={dirty} allowLeave={allowLeave} onOpenChange={setGuardOpen} onSaveAndLeave={save} />
      <h2 id="event-details-title">
        {t("eventEdit.details")}
        <HelpPopover textKey="help.eventDetails" testId="help-event-details" />
      </h2>
      <TextField
        label={t("wizard.f.name")}
        value={draftName}
        onChange={setDraftName}
        maxLength={NAME_MAX}
        error={nameChanged && nameBad ? t("wizard.err.nameLong") : undefined}
        testId="event-edit-name"
      />
      {siteHtml ? (
        <p className="notice" role="status" data-testid="event-edit-site-html">
          {t("tgEdit.description.siteHtml")}
        </p>
      ) : (
        <RichTextField
          profile="description"
          orgId={orgId}
          label={t("wizard.f.description")}
          hint={t("wizard.f.descriptionHint")}
          error={problem}
          value={draftText}
          onChange={setDraftText}
          disabled={busy}
          testId="event-edit-description"
        />
      )}
      {failure !== null && <ErrorNotice error={failure.error} testId="event-edit-error" />}
      <SaveBar
        dirty={dirty}
        busy={busy}
        blocked={blocked}
        onSave={() => void save()}
        hidden={guardOpen}
        testId="event-edit-save"
        status={saved && !dirty ? { key: "saveBar.saved", tone: "ok" } : undefined}
      />
    </section>
  );
}
