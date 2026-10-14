import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/http";
import { STEP_UP_HEADER, StepUpCancelled, StepUpStore, stepUpErrorKey, withStepUp } from "@/lib/stepUp";

function clock(start = 1_000_000) {
  let t = start;
  return { now: () => t, add: (ms: number) => (t += ms) };
}

describe("StepUpStore", () => {
  it("keeps the token until 30 seconds before its end", () => {
    const c = clock();
    const store = new StepUpStore(c.now);
    store.set("tok", c.now() + 5 * 60_000);
    expect(store.current()).toBe("tok");
    c.add(4 * 60_000 + 31_000);
    expect(store.current()).toBeNull();
  });
});

describe("withStepUp", () => {
  it("asks once, then sends the token with every money request", async () => {
    const c = clock();
    const store = new StepUpStore(c.now);
    let asked = 0;
    const ask = async () => {
      asked++;
      store.set("tok", c.now() + 5 * 60_000);
      return true;
    };
    const seen: string[] = [];
    const run = async (h: Record<string, string>) => {
      seen.push(h[STEP_UP_HEADER]);
      return "ok";
    };
    expect(await withStepUp(run, ask, store)).toBe("ok");
    expect(await withStepUp(run, ask, store)).toBe("ok");
    expect(asked).toBe(1);
    expect(seen).toEqual(["tok", "tok"]);
  });

  it("sends nothing when the person cancels", async () => {
    const store = new StepUpStore();
    let ran = false;
    await expect(
      withStepUp(async () => {
        ran = true;
      }, async () => false, store),
    ).rejects.toBeInstanceOf(StepUpCancelled);
    expect(ran).toBe(false);
  });

  it("asks again once when the server says the confirmation ran out", async () => {
    const c = clock();
    const store = new StepUpStore(c.now);
    store.set("old", c.now() + 5 * 60_000);
    let asked = 0;
    const ask = async () => {
      asked++;
      store.set("new", c.now() + 5 * 60_000);
      return true;
    };
    const calls: string[] = [];
    const result = await withStepUp(
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
    store.set("tok", Date.now() + 5 * 60_000);
    await expect(
      withStepUp(async () => {
        throw new ApiError(409, "refund.in_progress", "x");
      }, async () => true, store),
    ).rejects.toMatchObject({ code: "refund.in_progress" });
  });
});

describe("stepUpErrorKey", () => {
  it("says what to do next", () => {
    expect(stepUpErrorKey(new ApiError(403, "auth.step_up_wrong_password", "x"))).toBe("stepUp.error.wrong");
    expect(stepUpErrorKey(new ApiError(429, "auth.rate_limited", "x"))).toBe("stepUp.error.tooMany");
    expect(stepUpErrorKey(new ApiError(0, "network.failure", "x"))).toBe("common.error.network");
    expect(stepUpErrorKey(new Error("x"))).toBe("common.error.generic");
  });
});
