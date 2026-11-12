/**
 * The place on the venue card for the seating plan intake (spec 64): its
 * state, the button "Upload a seating plan" and the previews of the plans.
 * It renders nothing yet. The next task fills it, so the venue card does not
 * change when that work lands.
 */
export interface VenuePlanIntakeSlotProps {
  orgId: string;
  venueId: string;
  venueName: string;
}

export function VenuePlanIntakeSlot(_props: VenuePlanIntakeSlotProps) {
  return null;
}
