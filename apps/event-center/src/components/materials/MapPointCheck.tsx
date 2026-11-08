/**
 * The "see where buyers will end up" helper under the map point fields: a
 * Google Maps link for the coordinates as typed, and the rule of thumb for
 * where the point belongs. It shows nothing until both numbers are valid and
 * never blocks a save.
 */
import { useI18n } from "@/lib/i18n";
import { checkMapPoint, mapUrls } from "@/lib/materials";

export function MapPointCheck({ lat, lng }: { lat: string; lng: string }) {
  const { t } = useI18n();
  const checked = checkMapPoint(lat, lng);
  return (
    <div data-testid="mat-map-helper">
      <p className="field-hint" data-testid="mat-map-pick-hint">
        {t("mat.map.pickHint")}
      </p>
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
