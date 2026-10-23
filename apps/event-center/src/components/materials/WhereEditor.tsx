/**
 * "Where the event takes place": the postal address of the REAL venue of each
 * date (saved on the venue, never taken from the seller's legal address) and
 * the one optional map point of the event.
 *
 * Nothing is guessed. An empty field stays empty on the public page, a point
 * needs both coordinates (an empty one is not zero, 0 and 0 is valid), and when
 * the dates are at several venues the point is shown beside them, not as the
 * point of each.
 */
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ConfirmPressDialog } from "@/components/ConfirmPressDialog";
import { Empty, ErrorBox, Loading } from "@/components/ui";
import { ActionNotice, useAction } from "@/components/materials/shared";
import { deleteMapPoint, getMapPoint, patchVenueAddress, putMapPoint } from "@/lib/api/materialsApi";
import { listSessions, listVenues } from "@/lib/api/endpoints";
import type { VenueItem } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import { LIMITS, checkMapPoint, cleanText, mapUrls, type CoordProblem } from "@/lib/materials";

const COORD_KEY: Record<CoordProblem, MessageKey> = {
  incomplete: "mat.map.problem.incomplete",
  number: "mat.map.problem.number",
  latRange: "mat.map.problem.latRange",
  lngRange: "mat.map.problem.lngRange",
};

function VenueForm({
  orgId,
  venue,
  dates,
  canEdit,
}: {
  orgId: string;
  venue: VenueItem;
  dates: number;
  canEdit: boolean;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const action = useAction();
  const [line1, setLine1] = useState(venue.address_line1 ?? venue.address ?? "");
  const [line2, setLine2] = useState(venue.address_line2 ?? "");
  const [postal, setPostal] = useState(venue.postal_code ?? "");
  const [country, setCountry] = useState(venue.country ?? "");
  const [countryBad, setCountryBad] = useState(false);
  const id = `mat-venue-${venue.id}`;

  const save = async () => {
    const c = country.trim().toUpperCase();
    if (c !== "" && !/^[A-Z]{2}$/.test(c)) {
      setCountryBad(true);
      return;
    }
    setCountryBad(false);
    await action.run(async () => {
      try {
        await patchVenueAddress(orgId, venue.id, {
          address: [cleanText(line1), cleanText(line2)].filter(Boolean).join(", "),
          address_line1: cleanText(line1),
          address_line2: cleanText(line2),
          postal_code: cleanText(postal),
          country: c === "" ? null : c,
        });
      } finally {
        await queryClient.invalidateQueries({ queryKey: ["venues", orgId] });
      }
    });
  };

  const field = (suffix: string, label: MessageKey, value: string, set: (v: string) => void, max: number) => (
    <div className="field">
      <label className="field-label" htmlFor={`${id}-${suffix}`}>
        {t(label)}
      </label>
      <input
        id={`${id}-${suffix}`}
        className="input"
        type="text"
        value={value}
        maxLength={max}
        disabled={!canEdit || action.busy}
        onChange={(e) => set(e.target.value)}
        data-testid={`${id}-${suffix}`}
      />
    </div>
  );

  return (
    <li className="card mat-row" data-testid="mat-venue">
      <p>
        <strong>{venue.name}</strong>
      </p>
      <p className="field-hint">{t("mat.venue.dates", { count: dates })}</p>
      {field("line1", "mat.venue.line1", line1, setLine1, 200)}
      {field("line2", "mat.venue.line2", line2, setLine2, 200)}
      {field("postal", "mat.venue.postal", postal, setPostal, 20)}
      {field("country", "mat.venue.country", country, setCountry, 2)}
      {countryBad && (
        <p className="field-error" role="alert" data-testid={`${id}-country-problem`}>
          {t("mat.venue.countryInvalid")}
        </p>
      )}
      {canEdit && (
        <div className="actions">
          <button type="button" className="btn btn-primary" disabled={action.busy} onClick={() => void save()} data-testid={`${id}-save`}>
            {t("mat.venue.save")}
          </button>
        </div>
      )}
      <ActionNotice action={action} testId={id} />
    </li>
  );
}

function MapPointForm({ orgId, eventId, canWrite, several }: { orgId: string; eventId: string; canWrite: boolean; several: boolean }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const queryKey = ["materials", "map", orgId, eventId] as const;
  const point = useQuery({ queryKey, queryFn: ({ signal }) => getMapPoint(orgId, eventId, signal) });
  const action = useAction();
  const [form, setForm] = useState<{ lat: string; lng: string; label: string; from: string } | null>(null);
  const [problem, setProblem] = useState<CoordProblem | null>(null);
  const [confirmRemove, setConfirmRemove] = useState(false);

  if (point.isLoading) return <Loading />;
  if (point.error !== null) return <ErrorBox error={point.error} onRetry={() => void point.refetch()} />;
  const saved = point.data ?? null;
  const stamp = saved === null ? "none" : `${saved.latitude}|${saved.longitude}|${saved.label ?? ""}`;
  // The inputs start from the stored point and restart whenever the stored point changes.
  const current =
    form !== null && form.from === stamp
      ? form
      : {
          lat: saved === null ? "" : String(saved.latitude),
          lng: saved === null ? "" : String(saved.longitude),
          label: saved?.label ?? "",
          from: stamp,
        };
  const set = (patch: Partial<typeof current>) => setForm({ ...current, ...patch });
  const reload = () => queryClient.invalidateQueries({ queryKey });

  const save = async () => {
    const checked = checkMapPoint(current.lat, current.lng);
    if (checked.kind === "bad") {
      setProblem(checked.problem);
      return;
    }
    setProblem(null);
    const done = await action.run(async () => {
      try {
        if (checked.kind === "empty") {
          if (saved !== null) await deleteMapPoint(orgId, eventId);
        } else {
          await putMapPoint(orgId, eventId, { latitude: checked.latitude, longitude: checked.longitude, label: cleanText(current.label) });
        }
      } finally {
        await reload();
      }
    });
    if (done) setForm(null);
  };

  const urls = saved === null ? null : mapUrls(saved.latitude, saved.longitude);
  return (
    <div className="mat-block" data-testid="mat-map">
      <h4>{t("mat.map.title")}</h4>
      <p className="field-hint">{t("mat.map.hint")}</p>
      {several && (
        <p className="notice" data-testid="mat-map-several">
          {t("mat.map.several")}
        </p>
      )}
      {saved === null && <Empty>{t("mat.empty")}</Empty>}
      <div className="field">
        <label className="field-label" htmlFor="mat-map-lat">
          {t("mat.map.lat")}
        </label>
        <input id="mat-map-lat" className="input" type="text" inputMode="decimal" value={current.lat} disabled={!canWrite || action.busy} onChange={(e) => set({ lat: e.target.value })} data-testid="mat-map-lat" />
      </div>
      <div className="field">
        <label className="field-label" htmlFor="mat-map-lng">
          {t("mat.map.lng")}
        </label>
        <input id="mat-map-lng" className="input" type="text" inputMode="decimal" value={current.lng} disabled={!canWrite || action.busy} onChange={(e) => set({ lng: e.target.value })} data-testid="mat-map-lng" />
      </div>
      <div className="field">
        <label className="field-label" htmlFor="mat-map-label">
          {t("mat.map.label")}
        </label>
        <input id="mat-map-label" className="input" type="text" value={current.label} maxLength={LIMITS.mapLabel} disabled={!canWrite || action.busy} onChange={(e) => set({ label: e.target.value })} data-testid="mat-map-label" />
        <p className="field-hint">{t("mat.map.labelHint", { max: LIMITS.mapLabel })}</p>
      </div>
      {problem !== null && (
        <p className="field-error" role="alert" data-testid="mat-map-problem">
          {t(COORD_KEY[problem])}
        </p>
      )}
      {canWrite && (
        <div className="actions">
          <button type="button" className="btn btn-primary" disabled={action.busy} onClick={() => void save()} data-testid="mat-map-save">
            {t("mat.map.save")}
          </button>
          {saved !== null && (
            <button type="button" className="btn" disabled={action.busy} onClick={() => setConfirmRemove(true)} data-testid="mat-map-remove">
              {t("mat.map.remove")}
            </button>
          )}
        </div>
      )}
      {urls !== null && (
        <p>
          <a href={urls.osm} target="_blank" rel="noopener noreferrer" data-testid="mat-map-osm">
            {t("mat.map.check")}
          </a>
        </p>
      )}
      <ActionNotice action={action} testId="mat-map" />
      {confirmRemove && (
        <ConfirmPressDialog
          title={t("mat.confirm.title")}
          explain={t("mat.confirm.explain")}
          confirmLabel={t("mat.remove")}
          testId="mat-map-confirm"
          onCancel={() => setConfirmRemove(false)}
          onConfirm={async () => {
            await deleteMapPoint(orgId, eventId);
            await reload();
            setForm(null);
            setConfirmRemove(false);
          }}
        />
      )}
    </div>
  );
}

export function WhereEditor({
  orgId,
  eventId,
  canWrite,
  canReadSessions,
  canReadVenues,
  canEditVenues,
}: {
  orgId: string;
  eventId: string;
  canWrite: boolean;
  canReadSessions: boolean;
  canReadVenues: boolean;
  canEditVenues: boolean;
}) {
  const { t } = useI18n();
  const sessions = useQuery({ queryKey: ["sessions", orgId, eventId], queryFn: () => listSessions(orgId, eventId), enabled: canReadSessions });
  const venues = useQuery({ queryKey: ["venues", orgId], queryFn: () => listVenues(orgId), enabled: canReadVenues });

  const live = (sessions.data?.sessions ?? []).filter((s) => s.status !== "cancelled");
  const counts = new Map<string, number>();
  for (const s of live) counts.set(s.venue_id, (counts.get(s.venue_id) ?? 0) + 1);
  const venueIds = [...counts.keys()];

  return (
    <section className="mat-block" aria-labelledby="mat-where-title" data-testid="mat-where">
      <h3 id="mat-where-title">{t("mat.where.title")}</h3>
      <p className="field-hint">{t("mat.where.hint")}</p>
      {!canReadSessions && <p className="notice">{t("mat.where.noSessionRead")}</p>}
      {canReadSessions && sessions.isLoading && <Loading />}
      {sessions.error !== null && <ErrorBox error={sessions.error} onRetry={() => void sessions.refetch()} />}
      {canReadSessions && sessions.data !== undefined && venueIds.length === 0 && <Empty>{t("mat.where.noSessions")}</Empty>}
      {canReadSessions && venueIds.length > 0 && !canReadVenues && <p className="notice">{t("mat.venue.noList")}</p>}
      {canReadVenues && venues.isLoading && <Loading />}
      {venues.error !== null && <ErrorBox error={venues.error} onRetry={() => void venues.refetch()} />}
      {canReadVenues && !canEditVenues && venueIds.length > 0 && <p className="notice">{t("mat.venue.noRight")}</p>}
      {venues.data !== undefined && (
        <ul className="cards">
          {venueIds.map((id) => {
            const venue = venues.data.venues.find((v) => v.id === id);
            if (venue === undefined) {
              return (
                <li key={id} className="card mat-row">
                  <p className="field-hint">{t("mat.venue.missing")}</p>
                </li>
              );
            }
            return (
              <VenueForm
                key={venue.id}
                orgId={orgId}
                venue={venue}
                dates={counts.get(id) ?? 0}
                canEdit={canEditVenues}
              />
            );
          })}
        </ul>
      )}
      <MapPointForm orgId={orgId} eventId={eventId} canWrite={canWrite} several={venueIds.length > 1} />
    </section>
  );
}
