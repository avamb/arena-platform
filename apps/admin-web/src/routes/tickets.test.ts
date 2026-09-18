/**
 * Unit tests for the SuperAdmin Tickets support console (SAUI-10).
 *
 * Pins the badge picker and the documented status vocabulary so future
 * backend additions cannot silently drift away from the dropdown.
 */
import { describe, it, expect } from "vitest";
import {
  badgeForTicketStatus,
  badgeForScanResult,
  TICKET_STATUSES,
  computeTicketCounters,
  buildTicketsCsv,
  csvField,
  type AdminTicket,
} from "./tickets";

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

describe("badgeForScanResult", () => {
  it("renders admitted scans as success (green)", () => {
    expect(badgeForScanResult("admitted")).toMatchObject({ color: "#166534" });
  });
  it("renders denied scans as error (red)", () => {
    expect(badgeForScanResult("denied")).toMatchObject({ color: "#7f1d1d" });
  });
  it("falls back to neutral for unknown values (defensive)", () => {
    expect(badgeForScanResult("totally-unknown")).toMatchObject({
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
      // AB-49: complimentary revocation status (0038) joined the filter.
      "revoked",
    ]);
  });
});

// ---------------------------------------------------------------------------
// Manual reconciliation console (owner decision 2026-09-18)
// ---------------------------------------------------------------------------

function makeTicket(overrides: Partial<AdminTicket>): AdminTicket {
  return {
    id: "ticket-1",
    checkout_session_id: "cs-1",
    session_id: "session-1",
    status: "active",
    issued_at: "2026-09-01T10:00:00Z",
    created_at: "2026-09-01T10:00:00Z",
    updated_at: "2026-09-01T10:00:00Z",
    tier_id: null,
    holder_email: "buyer@example.com",
    ...overrides,
  };
}

describe("computeTicketCounters", () => {
  it("counts an empty list as all zeroes", () => {
    expect(computeTicketCounters([])).toEqual({
      valid: 0,
      refunded: 0,
      cancelled: 0,
      revoked: 0,
      total: 0,
    });
  });

  it("classifies active/issued/redeemed as valid, cancelled and revoked separately, refunded orthogonally", () => {
    const rows: AdminTicket[] = [
      makeTicket({ id: "a", status: "active" }),
      makeTicket({ id: "b", status: "issued" }),
      makeTicket({ id: "c", status: "redeemed" }),
      makeTicket({ id: "d", status: "cancelled", refund_date: "2026-09-05T00:00:00Z" }),
      makeTicket({ id: "e", status: "cancelled", refund_date: null }),
      makeTicket({ id: "f", status: "revoked", refund_date: "2026-09-06T00:00:00Z" }),
      makeTicket({ id: "g", status: "expired" }),
    ];
    expect(computeTicketCounters(rows)).toEqual({
      valid: 3,
      refunded: 2,
      cancelled: 2,
      revoked: 1,
      total: 7,
    });
  });
});

describe("csvField", () => {
  it("returns plain values unchanged", () => {
    expect(csvField("simple")).toBe("simple");
  });

  it("quotes and escapes values containing commas, quotes or newlines", () => {
    expect(csvField("a,b")).toBe('"a,b"');
    expect(csvField('say "hi"')).toBe('"say ""hi"""');
    expect(csvField("line1\nline2")).toBe('"line1\nline2"');
  });
});

describe("buildTicketsCsv", () => {
  it("emits the documented header row even for an empty list", () => {
    const csv = buildTicketsCsv([]);
    expect(csv).toBe(
      "barcode,category,order_number,buyer_email,status,refund_or_cancel_date,issued_at,ticket_id",
    );
  });

  it("emits one row per ticket with refund_date falling back to cancelled_at", () => {
    const csv = buildTicketsCsv([
      makeTicket({
        id: "t1",
        barcode: "4000000000012",
        category: "VIP",
        order_system_id: 1000000501,
        holder_email: "vip@example.com",
        status: "cancelled",
        refund_date: null,
        cancelled_at: "2026-09-10T12:00:00Z",
      }),
    ]);
    const lines = csv.split("\r\n");
    expect(lines).toHaveLength(2);
    expect(lines[1]).toBe(
      "4000000000012,VIP,1000000501,vip@example.com,cancelled,2026-09-10T12:00:00Z,2026-09-01T10:00:00Z,t1",
    );
  });

  it("prefers refund_date over cancelled_at when both are present", () => {
    const csv = buildTicketsCsv([
      makeTicket({
        refund_date: "2026-09-11T00:00:00Z",
        cancelled_at: "2026-09-10T00:00:00Z",
      }),
    ]);
    expect(csv.split("\r\n")[1]).toContain("2026-09-11T00:00:00Z");
  });

  it("renders missing barcode/category/order fields as empty CSV cells, not literal null/undefined", () => {
    const csv = buildTicketsCsv([makeTicket({})]);
    const cells = csv.split("\r\n")[1]!.split(",");
    // barcode, category, order_number are the first three columns.
    expect(cells[0]).toBe("");
    expect(cells[1]).toBe("");
    expect(cells[2]).toBe("");
  });
});
