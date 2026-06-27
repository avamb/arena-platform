/**
 * Unit tests for the SuperAdmin Tickets support console (SAUI-10).
 *
 * Pins the badge picker and the documented status vocabulary so future
 * backend additions cannot silently drift away from the dropdown.
 */
import { describe, it, expect } from "vitest";
import { badgeForTicketStatus, TICKET_STATUSES } from "./tickets";

describe("badgeForTicketStatus", () => {
  it("renders active / issued as success", () => {
    expect(badgeForTicketStatus("active")).toMatchObject({ color: "#166534" });
    expect(badgeForTicketStatus("issued")).toMatchObject({ color: "#166534" });
  });

  it("renders redeemed as neutral", () => {
    expect(badgeForTicketStatus("redeemed")).toMatchObject({ color: "#3730a3" });
  });

  it("renders cancelled / expired as error", () => {
    expect(badgeForTicketStatus("cancelled")).toMatchObject({ color: "#7f1d1d" });
    expect(badgeForTicketStatus("expired")).toMatchObject({ color: "#7f1d1d" });
  });

  it("renders transferred as warn", () => {
    expect(badgeForTicketStatus("transferred")).toMatchObject({ color: "#78350f" });
  });

  it("falls back to neutral badge for unknown values", () => {
    expect(badgeForTicketStatus("totally-unknown")).toMatchObject({
      color: "#3730a3",
    });
  });
});

describe("TICKET_STATUSES", () => {
  it("covers the documented ticket vocabulary", () => {
    expect(TICKET_STATUSES).toEqual([
      "active",
      "issued",
      "redeemed",
      "cancelled",
      "expired",
      "transferred",
    ]);
  });
});
