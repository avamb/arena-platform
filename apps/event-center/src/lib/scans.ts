/**
 * What the door did with a ticket (task M2). A port of the bot's
 * eventbot/scan_view.go: the scan rows come from the API, the source is
 * spelled in words, and the gate (else the scanner's own name) follows.
 */
import type { MessageKey } from "@/lib/i18n";
import type { TicketScan } from "@/lib/api/types";

export const SCAN_ADMITTED = "admitted";

/** The catalog key that names who reported a scan; empty for an unknown source. */
export function scanSourceKey(source: string): MessageKey | null {
  switch (source) {
    case "arena_scanner":
      return "scan.source.arena_scanner";
    case "macs":
      return "scan.source.macs";
    default:
      return null;
  }
}

/** The gate the scanner stood at, else the scanner's own name, else nothing. */
export function scanPlace(scan: Pick<TicketScan, "gate" | "scanner_label">): { gate: string } | { scanner: string } | null {
  const gate = scan.gate.trim();
  if (gate.length > 0) return { gate };
  const scanner = scan.scanner_label.trim();
  return scanner.length > 0 ? { scanner } : null;
}

export function isAdmitted(scan: Pick<TicketScan, "result">): boolean {
  return scan.result === SCAN_ADMITTED;
}

/**
 * The earliest admitted scan of a list the API sends newest first (up to
 * five). null when none is listed, as for a ticket of the legacy import that
 * carries only `used_at`.
 */
export function firstAdmittedScan(scans: readonly TicketScan[]): TicketScan | null {
  for (let i = scans.length - 1; i >= 0; i -= 1) {
    const s = scans[i];
    if (s !== undefined && isAdmitted(s)) return s;
  }
  return null;
}
