// @vitest-environment jsdom
/** The sections of the event card: a button heading, a remembered choice, a hash that opens its section. */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ANCHOR_HOLD_MS, EventSection, ScrollToAnchor, useSectionState } from "@/components/EventSection";
import { SECTION_IDS, sectionForAnchor, type SectionId } from "@/lib/eventActions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// jsdom has no scrollIntoView.
if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {};

const roots: Root[] = [];
let mounts = 0;

beforeEach(() => {
  localStorage.clear();
  mounts = 0;
});
afterEach(() => {
  for (const r of roots.splice(0)) act(() => r.unmount());
  document.body.innerHTML = "";
});

function Probe() {
  mounts += 1;
  return <p data-testid="probe">inside</p>;
}

/** Link first and open, the other sections closed: the on-sale default. */
function Card({ hash = "" }: { hash?: string }) {
  const state = useSectionState(hash);
  const sections: SectionId[] = ["link", "sales", "edit"];
  return (
    <div>
      {sections.map((id, i) => (
        <EventSection key={id} id={id} title={id} open={state.isOpen(id, i === 0)} onToggle={() => state.toggle(id, i === 0)}>
          <Probe />
          <h2 id={`inner-${id}`}>inner</h2>
          <span id={`event-item-${id}`} />
        </EventSection>
      ))}
      <ScrollToAnchor hash={hash} open={sectionForAnchor(hash) !== null && state.isOpen(sectionForAnchor(hash)!, false)} />
    </div>
  );
}

function mount(hash = "") {
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  roots.push(root);
  act(() => root.render(<Card hash={hash} />));
  return root;
}

const toggle = (id: string) => document.querySelector<HTMLButtonElement>(`[data-testid="event-section-toggle-${id}"]`)!;
const panel = (id: string) => document.querySelector<HTMLElement>(`[data-testid="event-section-panel-${id}"]`)!;
const press = (el: HTMLElement) => act(() => el.dispatchEvent(new MouseEvent("click", { bubbles: true })));

describe("EventSection", () => {
  it("the heading is a button that names its panel, and only the default section starts open", () => {
    mount();
    expect(toggle("link").getAttribute("aria-expanded")).toBe("true");
    expect(toggle("sales").getAttribute("aria-expanded")).toBe("false");
    expect(toggle("sales").getAttribute("aria-controls")).toBe(panel("sales").id);
    expect(panel("sales").hidden).toBe(true);
    expect(panel("link").hidden).toBe(false);
  });

  it("a section nobody opened never mounts its blocks", () => {
    mount();
    expect(mounts).toBe(1);
    expect(panel("sales").querySelector("[data-testid='probe']")).toBeNull();
    press(toggle("sales"));
    expect(mounts).toBeGreaterThan(1);
    expect(panel("sales").querySelector("[data-testid='probe']")).not.toBeNull();
  });

  it("a press opens and a second press closes, and the blocks stay in the page", () => {
    mount();
    press(toggle("edit"));
    expect(toggle("edit").getAttribute("aria-expanded")).toBe("true");
    expect(panel("edit").hidden).toBe(false);
    press(toggle("edit"));
    expect(toggle("edit").getAttribute("aria-expanded")).toBe("false");
    expect(panel("edit").hidden).toBe(true);
    expect(panel("edit").querySelector("[data-testid='probe']")).not.toBeNull();
  });

  it("remembers the choice on the device, per section id", () => {
    const first = mount();
    press(toggle("edit"));
    press(toggle("link"));
    expect(localStorage.getItem("arena.ec.eventSection.edit")).toBe("1");
    expect(localStorage.getItem("arena.ec.eventSection.link")).toBe("0");
    act(() => first.unmount());
    document.body.innerHTML = "";
    mount();
    expect(toggle("edit").getAttribute("aria-expanded")).toBe("true");
    expect(toggle("link").getAttribute("aria-expanded")).toBe("false");
    expect(toggle("sales").getAttribute("aria-expanded")).toBe("false");
  });

  it("works when the storage throws", () => {
    const real = Storage.prototype.getItem;
    const set = Storage.prototype.setItem;
    Storage.prototype.getItem = () => {
      throw new Error("blocked");
    };
    Storage.prototype.setItem = () => {
      throw new Error("blocked");
    };
    try {
      mount();
      press(toggle("sales"));
      expect(toggle("sales").getAttribute("aria-expanded")).toBe("true");
    } finally {
      Storage.prototype.getItem = real;
      Storage.prototype.setItem = set;
    }
  });

  it("a hash that points into a closed section opens it, the block is in the page, and the choice is not stored", () => {
    mount("#event-item-edit");
    expect(SECTION_IDS).toContain("edit");
    expect(toggle("edit").getAttribute("aria-expanded")).toBe("true");
    expect(document.getElementById("inner-edit")).not.toBeNull();
    expect(toggle("sales").getAttribute("aria-expanded")).toBe("false");
    expect(localStorage.getItem("arena.ec.eventSection.edit")).toBeNull();
  });

  it("an old anchor of a block opens the section that holds it", () => {
    mount("#event-totals");
    expect(toggle("sales").getAttribute("aria-expanded")).toBe("true");
  });
});

describe("ScrollToAnchor hold", () => {
  let resize: (() => void) | null = null;
  let disconnected = 0;
  let top = 0;
  const scrolls: unknown[] = [];
  const realRO = (globalThis as { ResizeObserver?: unknown }).ResizeObserver;
  const realScroll = Element.prototype.scrollIntoView;

  beforeEach(() => {
    vi.useFakeTimers();
    resize = null;
    disconnected = 0;
    top = 0;
    scrolls.length = 0;
    (globalThis as { ResizeObserver?: unknown }).ResizeObserver = class {
      constructor(cb: () => void) {
        resize = cb;
      }
      observe() {}
      disconnect() {
        disconnected += 1;
      }
    };
    Element.prototype.scrollIntoView = function (arg?: unknown) {
      scrolls.push(arg);
    };
  });
  afterEach(() => {
    vi.useRealTimers();
    (globalThis as { ResizeObserver?: unknown }).ResizeObserver = realRO;
    Element.prototype.scrollIntoView = realScroll;
  });

  function mountAt(hash: string) {
    mount(hash);
    const target = document.getElementById("event-item-edit");
    if (target) target.getBoundingClientRect = () => ({ top }) as DOMRect;
  }
  const relayout = (newTop: number) => {
    top = newTop;
    act(() => resize?.());
  };

  it("scrolls on open, instantly, and does nothing without a hash or for an unknown name", () => {
    mountAt("#event-item-edit");
    expect(scrolls.length).toBe(1);
    expect(scrolls[0]).toMatchObject({ behavior: "auto" });
    scrolls.length = 0;
    mount("");
    mount("#nothing-here");
    expect(scrolls.length).toBe(0);
  });

  it("scrolls again when the layout moves the block, but not when it is in place", () => {
    mountAt("#event-item-edit");
    expect(scrolls.length).toBe(1);
    relayout(0);
    expect(scrolls.length).toBe(1);
    relayout(600);
    expect(scrolls.length).toBe(2);
  });

  it("stops after the person wheels", () => {
    mountAt("#event-item-edit");
    act(() => {
      window.dispatchEvent(new Event("wheel"));
    });
    relayout(600);
    expect(scrolls.length).toBe(1);
    expect(disconnected).toBeGreaterThan(0);
  });

  it("stops after a key press", () => {
    mountAt("#event-item-edit");
    act(() => {
      window.dispatchEvent(new Event("keydown"));
    });
    relayout(600);
    expect(scrolls.length).toBe(1);
  });

  it("stops after the time window", () => {
    mountAt("#event-item-edit");
    act(() => {
      vi.advanceTimersByTime(ANCHOR_HOLD_MS + 1);
    });
    relayout(600);
    expect(scrolls.length).toBe(1);
    expect(disconnected).toBeGreaterThan(0);
  });
});
