import { formatDateTime } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import type { TicketScan } from "@/lib/api/types";
import { isAdmitted, scanPlace, scanSourceKey } from "@/lib/scans";
import { Badge } from "@/components/ui";

/** One scan as a line of text: who reported it and the gate or scanner. */
export function ScanOrigin({ scan }: { scan: Pick<TicketScan, "source" | "gate" | "scanner_label"> }) {
  const { t } = useI18n();
  const key = scanSourceKey(scan.source);
  const place = scanPlace(scan);
  const parts: string[] = [];
  if (key !== null) parts.push(t(key));
  if (place !== null) parts.push("gate" in place ? t("scan.gate", { gate: place.gate }) : place.scanner);
  return <>{parts.join(" · ")}</>;
}

/**
 * The last scans of one ticket at a door (up to five, newest first), as the
 * order card prints them (M2). Nothing is drawn for a ticket nobody scanned.
 */
export function TicketScans({ scans, tz }: { scans: readonly TicketScan[] | undefined; tz: string | null }) {
  const { t, locale } = useI18n();
  if (scans === undefined || scans.length === 0) return null;
  return (
    <div data-testid="ticket-scans">
      <p className="muted">{t("scan.title")}</p>
      <ul className="facts">
        {scans.map((s) => (
          <li key={`${s.scanned_at}-${s.source}-${s.result}-${s.gate}`} data-testid="ticket-scan" data-source={s.source}>
            <Badge tone={isAdmitted(s) ? "ok" : "bad"}>{t(isAdmitted(s) ? "scan.admitted" : "scan.denied")}</Badge>{" "}
            {formatDateTime(s.scanned_at, tz, locale)}
            {" · "}
            <ScanOrigin scan={s} />
          </li>
        ))}
      </ul>
    </div>
  );
}
