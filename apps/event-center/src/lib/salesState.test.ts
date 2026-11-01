import { describe, expect, it } from "vitest";
import { effectiveState, salesStateChoices, salesStateTone, siteSyncInFlight, siteSyncStatus } from "@/lib/salesState";

describe("salesStateChoices", () => {
  it("offers every choice but the one in force", () => {
    expect(salesStateChoices(null, "open")).toEqual(["soon", "sold_out"]);
    expect(salesStateChoices("soon", "soon")).toEqual(["open", "sold_out"]);
    expect(salesStateChoices("sold_out", "sold_out")).toEqual(["open", "soon"]);
  });
  it("treats a state the platform decided as no state set by hand", () => {
    expect(salesStateChoices(null, "soon")).toEqual(["soon", "sold_out"]);
    expect(salesStateChoices(undefined, "sold_out")).toEqual(["soon", "sold_out"]);
  });
  it("offers nothing once the sale is over or the session is canceled", () => {
    expect(salesStateChoices(null, "ended")).toEqual([]);
    expect(salesStateChoices("sold_out", "cancelled")).toEqual([]);
  });
});

describe("states", () => {
  it("knows the API's names and nothing else", () => {
    expect(effectiveState("sold_out")).toBe("sold_out");
    expect(effectiveState("soldout")).toBeNull();
    expect(effectiveState(null)).toBeNull();
  });
  it("colors the badge", () => {
    expect(salesStateTone("open")).toBe("ok");
    expect(salesStateTone("soon")).toBe("wait");
    expect(salesStateTone("sold_out")).toBe("bad");
    expect(salesStateTone("ended")).toBe("neutral");
  });
  it("reads the site delivery", () => {
    expect(siteSyncStatus("retrying")).toBe("retrying");
    expect(siteSyncStatus("something new")).toBe("none");
    expect(siteSyncInFlight("pending")).toBe(true);
    expect(siteSyncInFlight("delivered")).toBe(false);
    expect(siteSyncInFlight("failed")).toBe(false);
  });
});
