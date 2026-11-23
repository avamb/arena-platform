import type { OutboundClicksResponse } from "@/lib/api/types";

/** A row of a clicks table. `direct` is true for a visit with no utm_source. */
export type ClickRow = { key: string; label: string; clicks: number; direct: boolean };

/** Sources, most clicks first; an empty or blank utm_source is the "direct" row. */
export function sourceClickRows(list: OutboundClicksResponse["by_source"]): ClickRow[] {
  const rows = list.map((s) => {
    const label = s.utm_source.trim();
    return { key: label === "" ? "(direct)" : label, label, clicks: s.clicks, direct: label === "" };
  });
  return sortRows(rows);
}

/** Box offices, most clicks first. */
export function sellerClickRows(list: OutboundClicksResponse["by_seller"]): ClickRow[] {
  return sortRows(list.map((s) => ({ key: s.seller, label: s.seller, clicks: s.clicks, direct: false })));
}

/** Days, oldest first. */
export function dayClickRows(list: OutboundClicksResponse["by_day"]): ClickRow[] {
  return [...list].sort((a, b) => a.day.localeCompare(b.day)).map((d) => ({ key: d.day, label: d.day, clicks: d.clicks, direct: false }));
}

function sortRows(rows: ClickRow[]): ClickRow[] {
  return [...rows].sort((a, b) => b.clicks - a.clicks || a.label.localeCompare(b.label));
}

/** The share of visitors as a percent text, or null when the API gave no share. */
export function formatClickShare(share: number | null, locale: string): string | null {
  if (share === null) return null;
  return new Intl.NumberFormat(locale, { style: "percent", maximumFractionDigits: 1 }).format(share);
}
