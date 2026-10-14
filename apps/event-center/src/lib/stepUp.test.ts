import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/http";
import {
  STEP_UP_HEADER,
  StepUpCancelled,
  StepUpQuestion,
  StepUpStore,
  stepUpErrorKey,
  trapFocusIndex,
  withStepUp,
} from "@/lib/stepUp";

const ORG = "11111111-1111-4111-8111-111111111111";
const OTHER = "22222222-2222-4222-8222-222222222222";

function clock(start = 1_000_000) {
  let t = start;
  return { now: () => t, add: (ms: number) => (t += ms) };
}

describe("StepUpStore", () => {
  it("keeps the token until 30 seconds before its end", () => {
    const c = clock();
    const store = new StepUpStore(c.now);
    store.set("tok", ORG, c.now() + 5 * 60_000);
    expect(store.current(ORG)).toBe("tok");
    c.add(4 * 60_000 + 31_000);
    expect(store.current(ORG)).toBeNull();
  });

  it("gives the token out only for its own organization", () => {
    const c = clock();
    const store = new StepUpStore(c.now);
    store.set("tok", ORG, c.now() + 5 * 60_000);
    expect(store.current(OTHER)).toBeNull();
    store.clear();
    expect(store.current(ORG)).toBeNull();
  });
});

describe("withStepUp", () => {
  it("asks once, then sends the token with every money request", async () => {
    const c = clock();
    const store = new StepUpStore(c.now);
    const asked: string[] = [];
    const ask = async (org: string) => {
      asked.push(org);
      store.set("tok", org, c.now() + 5 * 60_000);
      return true;
    };
    const seen: string[] = [];
    const run = async (h: Record<string, string>) => {
      seen.push(h[STEP_UP_HEADER]);
      return "ok";
    };
    expect(await withStepUp(ORG, run, ask, store)).toBe("ok");
    expect(await withStepUp(ORG, run, ask, store)).toBe("ok");
    expect(asked).toEqual([ORG]);
    expect(seen).toEqual(["tok", "tok"]);
  });

  it("asks again for another organization", async () => {
    const c = clock();
    const store = new StepUpStore(c.now);
    const asked: string[] = [];
    const ask = async (org: string) => {
      asked.push(org);
      store.set(`tok-${org}`, org, c.now() + 5 * 60_000);
      return true;
    };
    await withStepUp(ORG, async () => "a", ask, store);
    let sent = "";
    await withStepUp(OTHER, async (h) => (sent = h[STEP_UP_HEADER]), ask, store);
    expect(asked).toEqual([ORG, OTHER]);
    expect(sent).toBe(`tok-${OTHER}`);
  });

  it("sends nothing when the person cancels", async () => {
    const store = new StepUpStore();
    let ran = false;
    await expect(
      withStepUp(ORG, async () => {
        ran = true;
      }, async () => false, store),
    ).rejects.toBeInstanceOf(StepUpCancelled);
    expect(ran).toBe(false);
  });

  it("sends nothing when the confirmation came for another organization", async () => {
    const c = clock();
    const store = new StepUpStore(c.now);
    let ran = false;
    await expect(
      withStepUp(
        ORG,
        async () => {
          ran = true;
        },
        async () => {
          store.set("tok", OTHER, c.now() + 5 * 60_000);
          return true;
        },
        store,
      ),
    ).rejects.toBeInstanceOf(StepUpCancelled);
    expect(ran).toBe(false);
  });

  it("asks again once when the server says the confirmation ran out", async () => {
    const c = clock();
    const store = new StepUpStore(c.now);
    store.set("old", ORG, c.now() + 5 * 60_000);
    let asked = 0;
    const ask = async (org: string) => {
      asked++;
      store.set("new", org, c.now() + 5 * 60_000);
      return true;
    };
    const calls: string[] = [];
    const result = await withStepUp(
      ORG,
      async (h) => {
        calls.push(h[STEP_UP_HEADER]);
        if (h[STEP_UP_HEADER] === "old") throw new ApiError(403, "auth.step_up_required", "x");
        return 42;
      },
      ask,
      store,
    );
    expect(result).toBe(42);
    expect(calls).toEqual(["old", "new"]);
    expect(asked).toBe(1);
  });

  it("does not retry other errors", async () => {
    const store = new StepUpStore();
    store.set("tok", ORG, Date.now() + 5 * 60_000);
    await expect(
      withStepUp(ORG, async () => {
        throw new ApiError(409, "refund.in_progress", "x");
      }, async () => true, store),
    ).rejects.toMatchObject({ code: "refund.in_progress" });
  });
});

describe("StepUpQuestion", () => {
  it("lets a second ask share the open question instead of leaving the first waiting", async () => {
    const opened: Array<string | null> = [];
    const q = new StepUpQuestion((org) => opened.push(org));
    const first = q.ask(ORG);
    const second = q.ask(ORG);
    expect(opened).toEqual([ORG]); // one question on screen
    expect(q.org).toBe(ORG);
    q.answer(true);
    await expect(first).resolves.toBe(true);
    await expect(second).resolves.toBe(true);
    expect(opened).toEqual([ORG, null]);
    expect(q.org).toBeNull();
  });

  it("tells every waiter about a cancel, and opens afresh afterwards", async () => {
    const opened: Array<string | null> = [];
    const q = new StepUpQuestion((org) => opened.push(org));
    const a = q.ask(ORG);
    const b = q.ask(OTHER);
    q.answer(false);
    await expect(a).resolves.toBe(false);
    await expect(b).resolves.toBe(false);
    const c = q.ask(OTHER);
    expect(q.org).toBe(OTHER);
    q.answer(true);
    await expect(c).resolves.toBe(true);
    expect(opened).toEqual([ORG, null, OTHER, null]);
  });
});

describe("trapFocusIndex", () => {
  it("keeps Tab and Shift+Tab inside the dialog", () => {
    expect(trapFocusIndex(3, 0, false)).toBe(1);
    expect(trapFocusIndex(3, 2, false)).toBe(0);
    expect(trapFocusIndex(3, 0, true)).toBe(2);
    expect(trapFocusIndex(3, 1, true)).toBe(0);
    expect(trapFocusIndex(3, -1, false)).toBe(0);
    expect(trapFocusIndex(3, -1, true)).toBe(2);
    expect(trapFocusIndex(0, -1, false)).toBe(-1);
  });
});

describe("stepUpErrorKey", () => {
  it("says what to do next", () => {
    expect(stepUpErrorKey(new ApiError(403, "auth.step_up_wrong_password", "x"))).toBe("stepUp.error.wrong");
    expect(stepUpErrorKey(new ApiError(429, "auth.rate_limited", "x"))).toBe("stepUp.error.tooMany");
    expect(stepUpErrorKey(new ApiError(401, "auth.session_ended", "x"))).toBe("stepUp.error.sessionEnded");
    expect(stepUpErrorKey(new ApiError(0, "network.failure", "x"))).toBe("common.error.network");
    expect(stepUpErrorKey(new Error("x"))).toBe("common.error.generic");
  });
});
