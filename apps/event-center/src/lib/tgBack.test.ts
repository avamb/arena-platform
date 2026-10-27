import { describe, expect, it } from "vitest";
import { followHistory, type HistoryLike } from "@/lib/tgBack";

function fakeHistory() {
  let index = 0;
  const subs = new Set<() => void>();
  const h: HistoryLike & { push: () => void; pop: () => void; backs: number } = {
    backs: 0,
    canGoBack: () => index > 0,
    back: () => {
      h.backs++;
      h.pop();
    },
    subscribe: (cb) => {
      subs.add(cb);
      return () => subs.delete(cb);
    },
    push: () => {
      index++;
      subs.forEach((f) => f());
    },
    pop: () => {
      index--;
      subs.forEach((f) => f());
    },
  };
  return { h, subs };
}

function fakeButton() {
  const state = { shown: false, handler: null as (() => void) | null, binds: 0 };
  const bind = (handler: () => void) => {
    state.shown = true;
    state.handler = handler;
    state.binds++;
    return () => {
      state.shown = false;
      state.handler = null;
    };
  };
  return { state, bind };
}

describe("followHistory", () => {
  it("shows no button on the first page and shows it after a step forward", () => {
    const { h } = fakeHistory();
    const b = fakeButton();
    followHistory(h, { active: () => true, bind: b.bind });
    expect(b.state.shown).toBe(false);
    h.push();
    expect(b.state.shown).toBe(true);
    h.push();
    expect(b.state.binds).toBe(1);
  });

  it("walks back through the history and hides the button on the first page", () => {
    const { h } = fakeHistory();
    const b = fakeButton();
    followHistory(h, { active: () => true, bind: b.bind });
    h.push();
    h.push();
    b.state.handler?.();
    expect(h.backs).toBe(1);
    expect(b.state.shown).toBe(true);
    b.state.handler?.();
    expect(h.backs).toBe(2);
    expect(b.state.shown).toBe(false);
  });

  it("does nothing for a page that was not entered through /tg", () => {
    const { h } = fakeHistory();
    const b = fakeButton();
    followHistory(h, { active: () => false, bind: b.bind });
    h.push();
    expect(b.state.binds).toBe(0);
  });

  it("takes the button and the subscription away on stop", () => {
    const { h, subs } = fakeHistory();
    const b = fakeButton();
    const stop = followHistory(h, { active: () => true, bind: b.bind });
    h.push();
    stop();
    expect(b.state.shown).toBe(false);
    expect(subs.size).toBe(0);
  });
});
