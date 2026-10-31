import { describe, expect, it } from "vitest";
import { STATUS_COLORS } from "@/lib/seatManager/colors";
import { CONFLICT_STROKE, PICKED_STROKE, inviteLook } from "@/lib/seatManager/inviteLook";
import { HATCH_FILL } from "@/lib/seatManager/render";
import type { SeatInfo } from "@/lib/staffInvitations";

const seat = (status: SeatInfo["status"]): SeatInfo => ({ key: "S|1|1", sector: "S", row: "1", seat: "1", tierId: "t", status });
const base = { tierColor: "#4f46e5", picked: false, conflict: false };

describe("the look of a seat in invite mode", () => {
  it("a free seat has its category colour, a held back one is hatched", () => {
    expect(inviteLook({ ...base, seat: seat("available") }).fill).toBe("#4f46e5");
    const reserve = inviteLook({ ...base, seat: seat("unavailable") });
    expect(reserve.fill).toBe(HATCH_FILL);
    expect(reserve.stroke).toBe("#4f46e5");
  });
  it("sold and held seats are greyed and never carry the picked outline", () => {
    expect(inviteLook({ ...base, seat: seat("sold") }).fill).toBe(STATUS_COLORS.sold);
    const held = inviteLook({ ...base, seat: seat("held"), picked: true });
    expect(held.fill).toBe(STATUS_COLORS.held);
    expect(held.stroke).not.toBe(PICKED_STROKE);
  });
  it("a picked seat keeps its fill (the hatch of the reserve stays visible) and gets an outline", () => {
    const free = inviteLook({ ...base, seat: seat("available"), picked: true });
    expect(free).toMatchObject({ fill: "#4f46e5", stroke: PICKED_STROKE, strokeWidth: 3 });
    expect(inviteLook({ ...base, seat: seat("unavailable"), picked: true }).fill).toBe(HATCH_FILL);
  });
  it("a seat that was just taken is outlined in red", () => {
    expect(inviteLook({ ...base, seat: seat("sold"), conflict: true })).toMatchObject({ stroke: CONFLICT_STROKE, dashed: true });
  });
  it("an unknown seat is neutral", () => {
    expect(inviteLook({ ...base, seat: undefined }).fill).toBe(STATUS_COLORS.unknown);
  });
});
