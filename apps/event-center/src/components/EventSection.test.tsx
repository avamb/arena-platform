// @vitest-environment jsdom
/** The sections of the event card: a button heading, a remembered choice, a hash that opens its section. */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { EventSection, useSectionState } from "@/components/EventSection";
import { SECTION_IDS, type SectionId } from "@/lib/eventActions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

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
        </EventSection>
      ))}
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
