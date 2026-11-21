/**
 * What the venue card shows, as plain functions (no React): the map link and
 * the seating plans the organization may see on the card.
 */
import type { SeatingPlan, SeatingPlanVersion } from "@/lib/api/types";

/** An external map at the point, or null when the venue has no usable point. */
export function mapLink(latitude: number | null, longitude: number | null): string | null {
  if (latitude === null || longitude === null) return null;
  if (!Number.isFinite(latitude) || !Number.isFinite(longitude)) return null;
  if (Math.abs(latitude) > 90 || Math.abs(longitude) > 180) return null;
  if (latitude === 0 && longitude === 0) return null;
  const lat = latitude.toFixed(6);
  const lon = longitude.toFixed(6);
  return `https://www.openstreetmap.org/?mlat=${lat}&mlon=${lon}#map=17/${lat}/${lon}`;
}

export interface CardPlan {
  id: string;
  name: string;
  /** Seats of the current version, null when it cannot be read. */
  seats: number | null;
}

/** The plans of the venue the organization may use: its own active ones and the verified templates, with a current version. */
export function usablePlans(plans: readonly SeatingPlan[], orgId: string): SeatingPlan[] {
  return plans.filter(
    (p) => p.status === "active" && !!p.current_version_id && (p.owner_org_id === orgId || p.visibility === "operator_verified" || p.visibility === "public_template"),
  );
}

export function planSeats(plan: SeatingPlan, versions: readonly SeatingPlanVersion[]): number | null {
  const cur = versions.find((v) => v.id === plan.current_version_id);
  return cur === undefined ? null : cur.capacity_seated;
}
