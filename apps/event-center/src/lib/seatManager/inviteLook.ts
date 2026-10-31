/**
 * How a seat looks on the map of the invitations screen (spec 46, 4.1): free
 * seats like the buyer sees them, seats the organizer holds back hatched,
 * sold and held ones greyed and out of reach, the picked ones outlined.
 */
import { STATUS_COLORS } from "@/lib/seatManager/colors";
import { HATCH_FILL, type SeatLook } from "@/lib/seatManager/render";
import type { SeatInfo } from "@/lib/staffInvitations";

export const PICKED_STROKE = "#111827";
export const CONFLICT_STROKE = "#dc2626";

export interface InviteLookInput {
  /** What the server says about the seat; undefined while the inventory is not loaded. */
  readonly seat: SeatInfo | undefined;
  /** The colour of the seat's category. */
  readonly tierColor: string;
  readonly picked: boolean;
  /** The seat was picked and someone took it before the send. */
  readonly conflict: boolean;
}

export function inviteLook({ seat, tierColor, picked, conflict }: InviteLookInput): SeatLook {
  if (seat === undefined) return { fill: STATUS_COLORS.unknown, stroke: "none", strokeWidth: 0, dashed: false };
  if (seat.status === "sold" || seat.status === "held") {
    const fill = seat.status === "sold" ? STATUS_COLORS.sold : STATUS_COLORS.held;
    if (conflict) return { fill, stroke: CONFLICT_STROKE, strokeWidth: 3, dashed: true };
    return { fill, stroke: tierColor, strokeWidth: 2, dashed: false };
  }
  if (picked) {
    const fill = seat.status === "unavailable" ? HATCH_FILL : tierColor;
    return { fill, stroke: PICKED_STROKE, strokeWidth: 3, dashed: false };
  }
  if (seat.status === "unavailable") return { fill: HATCH_FILL, stroke: tierColor, strokeWidth: 1.5, dashed: false };
  return { fill: tierColor, stroke: "none", strokeWidth: 0, dashed: false };
}
