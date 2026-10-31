import { describe, expect, it } from "vitest";
import {
  DEFAULT_FILTERS,
  RequestIds,
  filtersToQuery,
  gaIssueBody,
  gaIssueRequest,
  guestNameFor,
  isFromCsv,
  orgListKey,
  placeVip,
  withEvent,
} from "@/lib/orgInvitations";
import { STAFF_INVITATIONS_KEY } from "@/lib/staffInvitations";

const TIER = "0193f01a-0000-7000-8000-0000000000a1";

describe("org list key and filters", () => {
  it("sits under the key of the session list so one invalidation refreshes both", () => {
    const key = orgListKey("org1", DEFAULT_FILTERS);
    expect(key.slice(0, 2)).toEqual([STAFF_INVITATIONS_KEY, "org1"]);
    expect(key[2]).toBe("org");
  });
  it("sends only the filters that are set", () => {
    expect(filtersToQuery(DEFAULT_FILTERS)).toEqual({ state: "active", eventId: undefined, sessionId: undefined });
    expect(filtersToQuery({ state: "all", eventId: "e1", sessionId: "s1" })).toEqual({ state: "all", eventId: "e1", sessionId: "s1" });
  });
  it("drops the date when another event is chosen", () => {
    expect(withEvent({ state: "cancelled", eventId: "e1", sessionId: "s1" }, "e2")).toEqual({ state: "cancelled", eventId: "e2", sessionId: "" });
  });
});

describe("GA issue for one guest", () => {
  it("prints an e-mail-only guest under the address", () => {
    expect(guestNameFor({ name: "", email: "a@b.co" })).toBe("a@b.co");
    expect(guestNameFor({ name: " Anna ", email: "a@b.co" })).toBe("Anna");
  });
  it("takes one place of the category and a letter to the guest", () => {
    const body = gaIssueBody({ name: "Anna", email: "anna@example.com" }, TIER, "ru");
    expect(body.ga).toEqual([{ tier_id: TIER, qty: 1 }]);
    expect(body.seats).toEqual([]);
    expect(body.delivery).toBe("guest_email");
    expect(body.guest).toMatchObject({ name: "Anna", email: "anna@example.com", locale: "ru" });
  });
  it("falls back to English for a language the letter does not speak", () => {
    expect(gaIssueBody({ name: "A", email: "a@b.co" }, TIER, "zz").guest.locale).toBe("en");
  });
  it("carries the request id, and the VIP mark only when set", () => {
    const g = { name: "Anna", email: "anna@example.com" };
    const plain = gaIssueRequest(g, TIER, "en", "id-1", false);
    expect(plain.request_id).toBe("id-1");
    expect("vip" in plain).toBe(false);
    expect(gaIssueRequest(g, TIER, "en", "id-1", true).vip).toBe(true);
  });
});

describe("RequestIds", () => {
  const counter = () => {
    let n = 0;
    return () => `id-${++n}`;
  };
  it("gives a guest the same id on every ask, so a retry is the same request", () => {
    const ids = new RequestIds(counter());
    expect(ids.at(0)).toBe("id-1");
    expect(ids.at(1)).toBe("id-2");
    expect(ids.at(0)).toBe("id-1");
    expect(ids.size).toBe(2);
  });
  it("renews one guest after a refusal and all of them on a change", () => {
    const ids = new RequestIds(counter());
    ids.at(0);
    ids.at(1);
    ids.renew(1);
    expect(ids.at(0)).toBe("id-1");
    expect(ids.at(1)).toBe("id-3");
    ids.reset();
    expect(ids.size).toBe(0);
    expect(ids.at(0)).toBe("id-4");
  });
});

describe("row marks", () => {
  it("knows a CSV invitation", () => {
    expect(isFromCsv({ issued_via: "staff_bulk" })).toBe(true);
    expect(isFromCsv({ issued_via: "staff" })).toBe(false);
  });
  it("reads the VIP mark of a place, absent means not VIP", () => {
    expect(placeVip({ vip: true })).toBe(true);
    expect(placeVip({ vip: false })).toBe(false);
    expect(placeVip({})).toBe(false);
  });
});
