/**
 * What the shop calls put on the wire: the stock the person saw travels back as
 * `expected_stock`, every call names the web channel, a money call carries the
 * step-up token, and the courier text is read as plain text.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { createSession, useSessionForTests } from "@/lib/api/http";
import type { SessionStore, Stored } from "@/lib/api/tabSession";
import { cancelShopOrder, fetchCourierText, refundShopOrder, replaceShopVariants, safeFileName, shipShopOrder } from "@/lib/api/shopApi";
import { rowFromVariant, variantsRequest } from "@/lib/shop";

function memoryStore(): SessionStore {
  let value: Stored | null = null;
  return { get: () => value, set: (v: Stored | null) => void (value = v) };
}

interface Seen {
  method: string;
  path: string;
  headers: Record<string, string>;
  body: unknown;
}

async function fakeApi(answer: () => Response): Promise<Seen[]> {
  const seen: Seen[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init: RequestInit) => {
      seen.push({ method: String(init.method), path: new URL(url).pathname, headers: init.headers as Record<string, string>, body: init.body === undefined ? undefined : JSON.parse(String(init.body)) });
      return answer();
    }),
  );
  const session = createSession({ store: memoryStore(), bus: null, locks: null });
  useSessionForTests(session);
  await session.signedIn({ accessToken: "a-good", refreshToken: "r-old", expiresAt: new Date(Date.now() + 600_000).toISOString(), userId: "u1" });
  return seen;
}

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

afterEach(() => vi.unstubAllGlobals());

describe("the shop calls", () => {
  it("puts the stock the person saw into the variants request", async () => {
    const seen = await fakeApi(() => json(200, { id: "p1" }));
    const row = { ...rowFromVariant({ id: "v1", title: "Default Title", sku: null, price_minor: 900, compare_at_minor: null, stock: 7, track_stock: true, held: 0, available: 7, position: 0, option1: null, option2: null, option3: null }, "EUR"), stock: "10" };
    await replaceShopVariants("org1", "p1", variantsRequest([row], "EUR", 0));
    expect(seen).toHaveLength(1);
    expect(seen[0].method).toBe("PUT");
    expect(seen[0].path).toBe("/v1/organizations/org1/shop/products/p1/variants");
    expect(seen[0].headers["X-Client-Channel"]).toBe("event_center_web");
    expect(seen[0].body).toMatchObject({ variants: [{ id: "v1", stock: 10, expected_stock: 7, price_minor: 900 }] });
  });

  it("sends the tracking note only when there is one", async () => {
    const seen = await fakeApi(() => json(200, {}));
    await shipShopOrder("org1", "o1", "  ");
    await shipShopOrder("org1", "o1", " 1Z999 ");
    expect(seen[0].body).toEqual({});
    expect(seen[1].body).toEqual({ tracking_note: "1Z999" });
  });

  it("carries the step-up token on cancel and refund", async () => {
    const seen = await fakeApi(() => json(200, {}));
    await cancelShopOrder("org1", "o1", { "X-Step-Up-Token": "tok" });
    await refundShopOrder("org1", "o1", { amount_minor: 500 }, { "X-Step-Up-Token": "tok" });
    expect(seen.map((s) => s.path)).toEqual(["/v1/organizations/org1/shop/orders/o1/cancel", "/v1/organizations/org1/shop/orders/o1/refund"]);
    for (const s of seen) expect(s.headers["X-Step-Up-Token"]).toBe("tok");
    expect(seen[1].body).toEqual({ amount_minor: 500 });
  });

  it("reads the courier text as plain text", async () => {
    const seen = await fakeApi(() => new Response("Anna\n+34 600\nMadrid", { status: 200, headers: { "Content-Type": "text/plain" } }));
    expect(await fetchCourierText("org1", "o1")).toBe("Anna\n+34 600\nMadrid");
    expect(seen[0].headers.Accept).toBe("text/plain");
  });

  it("names a file without its path or odd characters", () => {
    expect(safeFileName("C:\\docs\\guide<1>.pdf")).toBe("guide_1_.pdf");
    expect(safeFileName("")).toBe("file");
    expect(safeFileName("../../etc/passwd")).toBe("passwd");
  });
});
