/**
 * Vitest global setup.
 *
 * Provides a minimal in-memory sessionStorage polyfill so the token
 * store (and any future browser-only modules) can run under the default
 * Node test environment without pulling in jsdom or happy-dom -- both
 * add significant install + boot overhead for what is currently a tiny
 * unit-test surface.
 */

class MemoryStorage implements Storage {
  private store = new Map<string, string>();
  get length(): number {
    return this.store.size;
  }
  clear(): void {
    this.store.clear();
  }
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) as string) : null;
  }
  key(index: number): string | null {
    return Array.from(this.store.keys())[index] ?? null;
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
}

if (typeof globalThis.sessionStorage === "undefined") {
  Object.defineProperty(globalThis, "sessionStorage", {
    value: new MemoryStorage(),
    writable: false,
  });
}

if (typeof globalThis.localStorage === "undefined") {
  Object.defineProperty(globalThis, "localStorage", {
    value: new MemoryStorage(),
    writable: false,
  });
}

// jsdom has no layout (the tests that run under it: the editor of spec 44, RT-11). ProseMirror asks for client
// rects when it scrolls the selection into view; answer with empty ones. Node tests have no Range: skipped.
if (typeof Range !== "undefined") {
  const emptyRect = { x: 0, y: 0, top: 0, left: 0, right: 0, bottom: 0, width: 0, height: 0, toJSON: () => ({}) };
  const noRects = () => ({ length: 0, item: () => null, [Symbol.iterator]: [][Symbol.iterator] }) as unknown as DOMRectList;
  Range.prototype.getBoundingClientRect = () => emptyRect as DOMRect;
  Range.prototype.getClientRects = noRects;
  Element.prototype.getClientRects = noRects;
  document.elementFromPoint = () => null;
}
