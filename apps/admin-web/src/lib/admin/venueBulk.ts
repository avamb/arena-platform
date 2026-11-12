/**
 * Pure parts of the bulk venue verification on the Venues screen: the country and
 * city filters, the row selection, and the batch that marks the selected venues as
 * verified. The batch only ever writes review_status "verified": a venue that is
 * already verified is skipped, and nothing here takes a verification back.
 */
import { ApiError, authedFetch } from "@/lib/api/client";
import { runBatch, type BatchProgress } from "@/lib/admin/batch";

export interface VenueLike {
  readonly id: string;
  readonly name: string;
  readonly city_id: string | null;
  readonly country?: string | null;
  readonly review_status?: string;
}

export interface GeoFilter {
  /** ISO-2 code, upper case; "" means every country. */
  readonly country: string;
  /** City id; "" means every city. */
  readonly cityId: string;
}

export const NO_GEO_FILTER: GeoFilter = { country: "", cityId: "" };

function countryOf(v: Pick<VenueLike, "country">): string {
  return typeof v.country === "string" ? v.country.trim().toUpperCase() : "";
}

export function isUnverified(v: Pick<VenueLike, "review_status">): boolean {
  return v.review_status !== "verified";
}

export function filterVenuesByGeo<T extends VenueLike>(rows: readonly T[], f: GeoFilter): readonly T[] {
  return rows.filter((v) => {
    if (f.country !== "" && countryOf(v) !== f.country.toUpperCase()) return false;
    if (f.cityId !== "" && v.city_id !== f.cityId) return false;
    return true;
  });
}

/** The countries that appear in the rows, sorted. A venue with no country adds nothing. */
export function countryOptions(rows: readonly VenueLike[]): readonly string[] {
  const set = new Set<string>();
  for (const v of rows) {
    const c = countryOf(v);
    if (c !== "") set.add(c);
  }
  return [...set].sort();
}

export interface CityOption {
  readonly id: string;
  readonly label: string;
}

/**
 * The cities that appear in the rows (limited to one country when asked). The label is
 * the name from the geo registry; the short id stays when the name is not known.
 */
export function cityOptions(
  rows: readonly VenueLike[],
  names: ReadonlyMap<string, string>,
  country: string,
): readonly CityOption[] {
  const ids = new Set<string>();
  for (const v of rows) {
    if (v.city_id === null) continue;
    if (country !== "" && countryOf(v) !== country.toUpperCase()) continue;
    ids.add(v.city_id);
  }
  return [...ids]
    .map((id) => ({ id, label: names.get(id) ?? id.slice(0, 8) }))
    .sort((a, b) => a.label.localeCompare(b.label));
}

/** The ids a checkbox may select: only venues that are not verified yet. */
export function selectableIds(rows: readonly VenueLike[]): readonly string[] {
  return rows.filter(isUnverified).map((v) => v.id);
}

export function toggleId(selected: ReadonlySet<string>, id: string): ReadonlySet<string> {
  const next = new Set(selected);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  return next;
}

/** Select all the selectable rows, or clear when they are all selected already. */
export function toggleAll(selected: ReadonlySet<string>, rows: readonly VenueLike[]): ReadonlySet<string> {
  const ids = selectableIds(rows);
  const allSelected = ids.length > 0 && ids.every((id) => selected.has(id));
  return allSelected ? new Set<string>() : new Set(ids);
}

/** Drops from the selection every id that is no longer a selectable row (filter changed, venue verified). */
export function pruneSelection(selected: ReadonlySet<string>, rows: readonly VenueLike[]): ReadonlySet<string> {
  const allowed = new Set(selectableIds(rows));
  const next = new Set<string>();
  for (const id of selected) {
    if (allowed.has(id)) next.add(id);
  }
  return next.size === selected.size ? selected : next;
}

export interface VenueVerifyFailure {
  readonly id: string;
  readonly name: string;
  readonly message: string;
}

export interface VenueVerifyResult {
  readonly verified: readonly string[];
  readonly skipped: readonly string[];
  readonly failed: readonly VenueVerifyFailure[];
}

export type VenueReviewPatch = (venueId: string, reason: string) => Promise<unknown>;

/** PATCH /v1/admin/venues/{id}/review {review_status: "verified"} with the one reason. */
export const patchVenueVerified: VenueReviewPatch = (venueId, reason) =>
  authedFetch<unknown>({
    method: "PATCH",
    path: `/v1/admin/venues/${encodeURIComponent(venueId)}/review`,
    body: { review_status: "verified" },
    adminReason: reason,
  });

function failureMessage(err: unknown): string {
  if (err instanceof ApiError) return `${err.message} (${err.code})`;
  return err instanceof Error ? err.message : String(err);
}

/**
 * Marks the selected venues as verified, a few at a time. Venues that are already verified
 * are skipped (never written), so the batch cannot unverify anything.
 */
export async function verifyVenuesBatch(args: {
  readonly venues: readonly VenueLike[];
  readonly reason: string;
  readonly concurrency?: number;
  readonly patch?: VenueReviewPatch;
  readonly onProgress?: (p: BatchProgress) => void;
}): Promise<VenueVerifyResult> {
  const patch = args.patch ?? patchVenueVerified;
  const todo = args.venues.filter(isUnverified);
  const skipped = args.venues.filter((v) => !isUnverified(v)).map((v) => v.id);
  const result = await runBatch(todo, (v) => patch(v.id, args.reason), {
    concurrency: args.concurrency ?? 4,
    onProgress: args.onProgress,
  });
  return {
    verified: result.done.map((d) => d.item.id),
    skipped,
    failed: result.failed.map((f) => ({ id: f.item.id, name: f.item.name, message: failureMessage(f.error) })),
  };
}
