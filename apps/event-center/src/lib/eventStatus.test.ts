import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  ACTION_TARGET,
  EVENT_STATUSES,
  EVENT_TRANSITIONS,
  canMoveEvent,
  deleteVerdict,
  offerArchiveInstead,
  statusActions,
} from "@/lib/eventStatus";

const STATE_GO = fileURLToPath(new URL("../../../backend/internal/domain/catalog/event_state.go", import.meta.url));

/** Reads `ValidEventTransitions` out of the Go source. */
function serverTransitions(): Record<string, string[]> {
  const src = readFileSync(STATE_GO, "utf8");
  const start = src.indexOf("var ValidEventTransitions");
  expect(start, "ValidEventTransitions is in event_state.go").toBeGreaterThan(-1);
  const body = src
    .slice(start)
    .split("\n")
    .map((l) => l.replace(/\/\/.*$/, ""))
    .join("\n");
  const out: Record<string, string[]> = {};
  const block = /EventStatus(\w+):\s*\{([^}]*)\}/g;
  let m: RegExpExecArray | null;
  while ((m = block.exec(body)) !== null) {
    out[m[1].toLowerCase()] = [...m[2].matchAll(/EventStatus(\w+):\s*true/g)].map((x) => x[1].toLowerCase());
  }
  return out;
}

describe("EVENT_TRANSITIONS", () => {
  it("equals the server's ValidEventTransitions", () => {
    const server = serverTransitions();
    expect(Object.keys(server).sort()).toEqual([...EVENT_STATUSES].sort());
    for (const status of EVENT_STATUSES) {
      expect([...EVENT_TRANSITIONS[status]].sort(), status).toEqual([...(server[status] ?? [])].sort());
    }
  });
  it("has no self loop and archived is final", () => {
    for (const s of EVENT_STATUSES) expect(canMoveEvent(s, s)).toBe(false);
    expect(EVENT_TRANSITIONS.archived).toEqual([]);
  });
});

describe("statusActions", () => {
  const both = { canPublish: true, canDelete: true };
  it("a draft can be published and deleted", () => {
    expect(statusActions("draft", both)).toEqual(["publish", "delete"]);
  });
  it("a published event can be taken off sale, archived and deleted", () => {
    expect(statusActions("published", both)).toEqual(["off", "archive", "delete"]);
  });
  it("a cancelled event can only be archived or deleted", () => {
    expect(statusActions("cancelled", both)).toEqual(["archive", "delete"]);
  });
  it("an archived event can only be deleted", () => {
    expect(statusActions("archived", both)).toEqual(["delete"]);
  });
  it("without rights there are no buttons", () => {
    for (const s of EVENT_STATUSES) expect(statusActions(s, { canPublish: false, canDelete: false })).toEqual([]);
  });
  it("event.publish alone gives no Delete, event.delete alone gives only Delete", () => {
    expect(statusActions("published", { canPublish: true, canDelete: false })).toEqual(["off", "archive"]);
    expect(statusActions("published", { canPublish: false, canDelete: true })).toEqual(["delete"]);
  });
  it("an unknown status offers no move", () => {
    expect(statusActions("weird", both)).toEqual(["delete"]);
  });
  it("every moving button follows the lifecycle table", () => {
    for (const s of EVENT_STATUSES) {
      for (const a of statusActions(s, both)) {
        if (a === "delete") continue;
        expect(canMoveEvent(s, ACTION_TARGET[a])).toBe(true);
      }
    }
  });
});

describe("delete verdict", () => {
  const free = { can_delete: true, blocked: "", can_archive: true };
  const sold = { can_delete: false, blocked: "has_paid_orders", can_archive: true };
  it("allowed only when the server says so", () => {
    expect(deleteVerdict(free)).toBe("allowed");
    expect(deleteVerdict(sold)).toBe("blocked");
    expect(deleteVerdict({ can_delete: true, blocked: "x" })).toBe("blocked");
  });
  it("offers the archive next to a refusal only when the lifecycle allows it", () => {
    expect(offerArchiveInstead(sold)).toBe(true);
    expect(offerArchiveInstead({ ...sold, can_archive: false })).toBe(false);
    expect(offerArchiveInstead(free)).toBe(false);
  });
});
