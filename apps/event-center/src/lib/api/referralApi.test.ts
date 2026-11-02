/**
 * "Refer a friend": the same calls serve an event and the shop, and each goes to its own route. The shop
 * has one programme per organization, so its routes carry no event id.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { createSession, useSessionForTests } from "@/lib/api/http";
import type { SessionStore, Stored } from "@/lib/api/tabSession";
import {
  fetchReferralProgram,
  fetchReferralRewards,
  fetchReferralSummary,
  referralAction,
  referralTargetKey,
  saveReferralProgram,
  type ReferralTarget,
} from "@/lib/api/endpoints";

function memoryStore(): SessionStore {
  let value: Stored | null = null;
  return { get: () => value, set: (v: Stored | null) => void (value = v) };
}

interface Seen {
  method: string;
  path: string;
  search: string;
  body: unknown;
}

async function fakeApi(answer: () => Response): Promise<Seen[]> {
  const seen: Seen[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init: RequestInit) => {
      const u = new URL(url);
      seen.push({ method: String(init.method), path: u.pathname, search: u.search, body: init.body === undefined ? undefined : JSON.parse(String(init.body)) });
      return answer();
    }),
  );
  const session = createSession({ store: memoryStore(), bus: null, locks: null });
  useSessionForTests(session);
  await session.signedIn({ accessToken: "a-good", refreshToken: "r-old", expiresAt: new Date(Date.now() + 600_000).toISOString(), userId: "u1" });
  return seen;
}

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const capability = { supported: true, source: "shop", channels: [] };

const EVENT: ReferralTarget = { kind: "event", eventId: "e1" };
const SHOP: ReferralTarget = { kind: "shop" };

afterEach(() => vi.unstubAllGlobals());

describe("the referral calls", () => {
  it("name a shop and an event by different keys, so their caches never meet", () => {
    expect(referralTargetKey(EVENT)).toBe("e1");
    expect(referralTargetKey(SHOP)).toBe("shop");
  });

  it("send an event to its event routes and the shop to the shop routes", async () => {
    const seen = await fakeApi(() => json(200, { program: null, capability, warnings: [], summary: 1, items: [], next_cursor: null }));
    for (const target of [EVENT, SHOP]) {
      await fetchReferralProgram("org1", target);
      await saveReferralProgram("org1", target, { friend_discount_pct: 10, cashback_pct: 20, cashback_cap_pct: 100, max_friends: null, ends_at: null });
      await referralAction("org1", target, "pause");
      await referralAction("org1", target, "resume");
      await referralAction("org1", target, "end");
      await fetchReferralSummary("org1", target);
      await fetchReferralRewards("org1", target, { state: "paid", limit: 20 });
    }
    const calls = seen.map((s) => `${s.method} ${s.path}${s.search}`);
    const event = "/v1/organizations/org1/events/e1";
    const shop = "/v1/organizations/org1/shop";
    expect(calls.slice(0, 7)).toEqual([
      `GET ${event}/referral-program`,
      `PUT ${event}/referral-program`,
      `POST ${event}/referral-program/pause`,
      `POST ${event}/referral-program/resume`,
      `POST ${event}/referral-program/end`,
      `GET ${event}/referral-program/summary`,
      `GET ${event}/referral-rewards?state=paid&limit=20`,
    ]);
    expect(calls.slice(7)).toEqual([
      `GET ${shop}/referral-program`,
      `PUT ${shop}/referral-program`,
      `POST ${shop}/referral-program/pause`,
      `POST ${shop}/referral-program/resume`,
      `POST ${shop}/referral-program/end`,
      `GET ${shop}/referral-program/summary`,
      `GET ${shop}/referral-rewards?state=paid&limit=20`,
    ]);
  });

  it("read the capability out of the shop's 404 the same way as an event's", async () => {
    await fakeApi(() => json(404, { error: { code: "referral.program_not_found", message: "none", details: { capability, warnings: [{ code: "referral.warning.no_channel", message: "x" }] } } }));
    const r = await fetchReferralProgram("org1", SHOP);
    expect(r.program).toBeNull();
    expect(r.capability.supported).toBe(true);
    expect(r.warnings).toHaveLength(1);
  });
});
