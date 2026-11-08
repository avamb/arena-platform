/**
 * The "see where buyers will end up" helper under the map point fields: a
 * Google Maps link for the coordinates as typed, and the rule of thumb for
 * where the point belongs. It shows nothing until both numbers are valid and
 * never blocks a save.
 */
import { useI18n } from "@/lib/i18n";
import type { MapPointWarning } from "@/lib/api/materialsApi";
import { checkMapPoint, mapUrls } from "@/lib/materials";

/** The text of an advisory warning in the person's language. */
export function WarningText({ warning }: { warning: MapPointWarning }) {
  const { t } = useI18n();
  const params = {
    city: warning.city,
    distance: warning.distance_km,
    limit: warning.threshold_km,
    other: warning.nearer_city ?? "",
    otherKm: warning.nearer_distance_km ?? 0,
  };
  const far = warning.code === "venue.point_far_from_city";
  const key = far ? (warning.nearer_city !== null ? "mat.map.warn.farNearer" : "mat.map.warn.far") : "mat.map.warn.nearer";
  return (
    <p className="notice" role="status" data-testid="mat-map-warning">
      {t(key, params)} {t("mat.map.warn.still")}
    </p>
  );
}

export function MapPointCheck({ lat, lng, warnings = [] }: { lat: string; lng: string; warnings?: MapPointWarning[] }) {
  const { t } = useI18n();
  const checked = checkMapPoint(lat, lng);
  return (
    <div data-testid="mat-map-helper">
      <p className="field-hint" data-testid="mat-map-pick-hint">
        {t("mat.map.pickHint")}
      </p>
      {checked.kind === "ok" && warnings.map((w) => <WarningText key={w.code} warning={w} />)}
      {checked.kind === "ok" && (
        <p>
          <a href={mapUrls(checked.latitude, checked.longitude).google} target="_blank" rel="noopener noreferrer" data-testid="mat-map-google">
            {t("mat.map.google")}
          </a>
        </p>
      )}
    </div>
  );
}
