/**
 * layeredStore: the web session survives closing the browser (localStorage)
 * while every tab keeps working in its own session (sessionStorage), and an
 * older session never overwrites or erases a newer sign-in (spec 37 §4.4).
 */
import { describe, expect, it } from "vitest";
import { layeredStore, type KeyValue } from "@/lib/api/tabSession";

function mem(): KeyValue & { dump(): string | null } {
  const m = new Map<string, string>();
  return {
    getItem: (k) => m.get(k) ?? null,
    setItem: (k, v) => void m.set(k, v),
    removeItem: (k) => void m.delete(k),
    dump: () => m.get("arena.events.session") ?? null,
  };
}

const s = (key: string, refreshToken: string) => ({ key, refreshToken, userId: "u1" });

describe("layeredStore", () => {
  it("a sign-in is kept for the next tab and the next browser start", () => {
    const browser = mem();
    const tab = layeredStore(mem(), browser);
    tab.set(s("k1", "r1"));
    // A new tab (empty sessionStorage) joins the browser's session.
    expect(layeredStore(mem(), browser).get()).toEqual(s("k1", "r1"));
  });

  it("a rotation keeps the browser's copy current", () => {
    const browser = mem();
    const tab = layeredStore(mem(), browser);
    tab.set(s("k1", "r1"));
    tab.set(s("k1", "r2"));
    expect(layeredStore(mem(), browser).get()?.refreshToken).toBe("r2");
  });

  it("a newer sign-in in another tab is never overwritten by the older session", () => {
    const browser = mem();
    const oldTab = layeredStore(mem(), browser);
    oldTab.set(s("k1", "r1"));
    const newTab = layeredStore(mem(), browser);
    newTab.set(s("k2", "x1")); // somebody signed in again
    oldTab.set(s("k1", "r2")); // the old session keeps rotating in its tab
    expect(oldTab.get()).toEqual(s("k1", "r2"));
    expect(layeredStore(mem(), browser).get()).toEqual(s("k2", "x1"));
  });

  it("signing out of an older session does not erase the newer one", () => {
    const browser = mem();
    const oldTab = layeredStore(mem(), browser);
    oldTab.set(s("k1", "r1"));
    layeredStore(mem(), browser).set(s("k2", "x1"));
    oldTab.set(null);
    expect(oldTab.get()?.key).toBe("k2"); // the tab now offers the browser's session
    expect(browser.dump()).not.toBeNull();
  });

  it("signing out of the browser's session forgets it everywhere", () => {
    const browser = mem();
    const tab = layeredStore(mem(), browser);
    tab.set(s("k1", "r1"));
    tab.set(null);
    expect(browser.dump()).toBeNull();
    expect(layeredStore(mem(), browser).get()).toBeNull();
  });

  it("works without storage at all", () => {
    const tab = layeredStore(null, null);
    tab.set(s("k1", "r1"));
    expect(tab.get()).toBeNull();
  });
});
