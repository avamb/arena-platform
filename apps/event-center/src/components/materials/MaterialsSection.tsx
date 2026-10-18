/**
 * "Sales page materials" on the event card: gallery, videos, other sellers,
 * the address and map point, and the photo report, each saved through the
 * existing API and shown by the public page and the widget.
 *
 * The permissions are the person's real ones in this organization: event.read
 * shows the editors, event.update lets them change anything (the server checks
 * it again on every call), media.write adds the upload, media.read shows the
 * previews.
 */
import { useQuery } from "@tanstack/react-query";
import { PartnerEditor } from "@/components/materials/PartnerEditor";
import { PhotoListEditor } from "@/components/materials/PhotoListEditor";
import { VideoEditor } from "@/components/materials/VideoEditor";
import { WhereEditor } from "@/components/materials/WhereEditor";
import { listSessions } from "@/lib/api/endpoints";
import { formatDateTime } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { reportPublicFrom } from "@/lib/materials";

export function MaterialsSection({
  orgId,
  eventId,
  status,
  publicUrl,
  can,
}: {
  orgId: string;
  eventId: string;
  status: string;
  publicUrl: string | null | undefined;
  can: (permission: string) => boolean;
}) {
  const { t, locale } = useI18n();
  const canRead = can("event.read");
  const canWrite = can("event.update");
  const canReadSessions = can("session.read");
  const sessions = useQuery({
    queryKey: ["sessions", orgId, eventId],
    queryFn: () => listSessions(orgId, eventId),
    enabled: canReadSessions,
  });

  if (!canRead) {
    return (
      <section aria-labelledby="event-materials" data-testid="materials-section">
        <h2 id="event-materials">{t("mat.title")}</h2>
        <p className="notice" data-testid="materials-no-read">
          {t("mat.noRead")}
        </p>
      </section>
    );
  }

  const publicFrom = reportPublicFrom(sessions.data?.sessions ?? []);
  const reportNote =
    !canReadSessions || sessions.isLoading
      ? undefined
      : publicFrom === null
        ? t("mat.report.noEnd")
        : t("mat.report.publicFrom", { when: formatDateTime(publicFrom, null, locale) });
  const published = status === "published" && typeof publicUrl === "string" && publicUrl !== "";

  return (
    <section aria-labelledby="event-materials" data-testid="materials-section">
      <h2 id="event-materials">{t("mat.title")}</h2>
      <p className="field-hint">{t("mat.intro")}</p>
      {published ? (
        <p>
          <a className="btn" href={publicUrl as string} target="_blank" rel="noopener noreferrer" data-testid="materials-public-link">
            {t("mat.public.open")}
          </a>
        </p>
      ) : (
        <p className="field-hint" data-testid="materials-public-pending">
          {t("mat.public.notYet")}
        </p>
      )}
      {!canWrite && (
        <p className="notice" data-testid="materials-read-only">
          {t("mat.readOnly")}
        </p>
      )}
      <PhotoListEditor
        kind="gallery"
        orgId={orgId}
        eventId={eventId}
        canWrite={canWrite}
        canUpload={canWrite && can("media.write")}
        canView={can("media.read")}
      />
      <VideoEditor orgId={orgId} eventId={eventId} canWrite={canWrite} />
      <PartnerEditor orgId={orgId} eventId={eventId} canWrite={canWrite} />
      <WhereEditor
        orgId={orgId}
        eventId={eventId}
        canWrite={canWrite}
        canReadSessions={canReadSessions}
        canReadVenues={can("venue.read")}
        canEditVenues={can("venue.update")}
      />
      <PhotoListEditor
        kind="report"
        orgId={orgId}
        eventId={eventId}
        canWrite={canWrite}
        canUpload={canWrite && can("media.write")}
        canView={can("media.read")}
        note={reportNote}
      />
    </section>
  );
}
