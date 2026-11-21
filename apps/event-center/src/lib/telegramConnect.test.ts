import { describe, expect, it } from "vitest";
import { connectedSinceLink, connectExpired, connectTimeLeft, pngBlobOf } from "@/lib/telegramConnect";

describe("telegram connect", () => {
  const now = Date.parse("2026-11-09T12:00:00Z");

  it("counts the minutes left, rounding up, and stops at the end", () => {
    expect(connectTimeLeft("2026-11-09T12:15:00Z", now)).toBe(15);
    expect(connectTimeLeft("2026-11-09T12:00:20Z", now)).toBe(1);
    expect(connectTimeLeft("2026-11-09T12:00:00Z", now)).toBe(0);
    expect(connectExpired("2026-11-09T11:59:59Z", now)).toBe(true);
    expect(connectExpired("not a date", now)).toBe(true);
    expect(connectExpired("2026-11-09T12:00:01Z", now)).toBe(false);
  });

  it("turns the server's base64 PNG into a blob, and refuses garbage", () => {
    const png = btoa(String.fromCharCode(0x89, 0x50, 0x4e, 0x47));
    const blob = pngBlobOf(png);
    expect(blob?.type).toBe("image/png");
    expect(blob?.size).toBe(4);
    expect(pngBlobOf("%%%")).toBeNull();
  });

  it("calls a link used only when an account was linked after it was made", () => {
    // The link was made at 12:00:00 (it ends at 12:15:00).
    const ends = "2026-11-09T12:15:00Z";
    expect(connectedSinceLink([{ linked_at: "2026-11-09T11:00:00Z" }], ends)).toBe(false);
    expect(connectedSinceLink([{ linked_at: "2026-11-09T11:00:00Z" }, { linked_at: "2026-11-09T12:00:00Z" }], ends)).toBe(true);
    expect(connectedSinceLink([{ linked_at: "2026-11-09T12:03:10Z" }], ends)).toBe(true);
    expect(connectedSinceLink([], ends)).toBe(false);
    expect(connectedSinceLink([{ linked_at: "x" }], ends)).toBe(false);
  });
});
