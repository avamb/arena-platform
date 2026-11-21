// @vitest-environment jsdom
/**
 * The seat card on the organizer's map: a mouse hover and a touch long press show it and paint nothing;
 * a normal tap still paints exactly as before; a gesture, a pan and leaving the map put the card away.
 */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fitTransform } from "@seatmap/lib/pan-zoom";
import { seatCardModel } from "@seatmap/lib/seat-card";
import { LONG_PRESS_MS, SeatMapCanvas, type MapTool } from "@/components/seatManager/SeatMapCanvas";
import { smallHall } from "@/lib/seatManager/hall.fixture";
import { buildHallIndex } from "@/lib/seatManager/model";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const roots: Root[] = [];
const realRect = HTMLElement.prototype.getBoundingClientRect;

beforeEach(() => {
  // jsdom has no layout: the map is 800 x 600 at the page's corner.
  HTMLElement.prototype.getBoundingClientRect = function () {
    return { left: 0, top: 0, right: 800, bottom: 600, width: 800, height: 600, x: 0, y: 0, toJSON: () => ({}) } as DOMRect;
  };
});
afterEach(() => {
  HTMLElement.prototype.getBoundingClientRect = realRect;
  for (const r of roots.splice(0)) act(() => r.unmount());
  document.body.innerHTML = "";
  vi.useRealTimers();
});

const geometry = smallHall();
const index = buildHallIndex(geometry);
const look = () => ({ fill: "#4f46e5", stroke: "none", strokeWidth: 0, dashed: false });
const labels = { sector: "Sector", row: "Row", seat: "Seat" };

function cardOf(key: string) {
  const s = index.byKey.get(key);
  if (s === undefined) return null;
  return seatCardModel({ sector: "Parter", row: s.rowKey, seat: s.number, category: "Gold", price: "30.00 EUR", labels });
}

function mount(tool: MapTool = "seat") {
  const onSeats = vi.fn();
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  roots.push(root);
  act(() => {
    root.render(
      <SeatMapCanvas
        geometry={geometry}
        index={index}
        label="Map"
        look={look}
        cardOf={cardOf}
        tool={tool}
        stage={null}
        onSeats={onSeats}
        onRowHandle={() => {}}
        onStage={() => {}}
      />,
    );
  });
  return { onSeats, viewport: document.querySelector<HTMLElement>('[data-testid="seat-map"]')! };
}

/** Where a seat is on screen, with the same fit the canvas made. */
function seatPoint(key: string) {
  const v = fitTransform(index.width, index.height, 800, 600, 8);
  const s = index.byKey.get(key)!;
  return { x: v.x + s.x * v.scale, y: v.y + s.y * v.scale };
}

function fire(el: Element, type: string, p: { x: number; y: number }, pointerType: "mouse" | "touch", id = 1, buttons = 0) {
  const init = { bubbles: true, cancelable: true, clientX: p.x, clientY: p.y, button: 0, buttons };
  const Ctor = (window as unknown as { PointerEvent?: typeof MouseEvent }).PointerEvent ?? MouseEvent;
  const e = new Ctor(type, init);
  Object.defineProperty(e, "pointerType", { value: pointerType });
  Object.defineProperty(e, "pointerId", { value: id });
  act(() => {
    el.dispatchEvent(e);
  });
}

const wait = (ms: number) =>
  act(async () => {
    await new Promise((r) => setTimeout(r, ms));
  });
const cardEl = () => document.querySelector<HTMLElement>(".arena-seat-card")!;
const KEY = "A|1|2";

describe("seat card on the organizer's map", () => {
  it("a mouse hover shows the card with the seat's lines and paints nothing", async () => {
    const { viewport, onSeats } = mount();
    expect(cardEl().hidden).toBe(true);
    fire(viewport, "pointermove", seatPoint(KEY), "mouse");
    await wait(40);
    expect(cardEl().hidden).toBe(false);
    expect(cardEl().getAttribute("role")).toBe("tooltip");
    expect(cardEl().textContent).toContain("Sector Parter · Row 1 · Seat 2");
    expect(cardEl().textContent).toContain("30.00 EUR");
    expect(onSeats).not.toHaveBeenCalled();
  });

  it("the card goes when the mouse moves to empty space and when it leaves the map", async () => {
    const { viewport } = mount();
    fire(viewport, "pointermove", seatPoint(KEY), "mouse");
    await wait(40);
    expect(cardEl().hidden).toBe(false);
    fire(viewport, "pointermove", { x: 2, y: 2 }, "mouse");
    await wait(40);
    expect(cardEl().hidden).toBe(true);
    fire(viewport, "pointermove", seatPoint(KEY), "mouse");
    await wait(40);
    expect(cardEl().hidden).toBe(false);
    fire(viewport, "pointerout", { x: 2, y: 2 }, "mouse");
    expect(cardEl().hidden).toBe(true);
  });

  it("a long press with a finger shows the card and does NOT paint", async () => {
    const { viewport, onSeats } = mount();
    const p = seatPoint(KEY);
    fire(viewport, "pointerdown", p, "touch", 7, 1);
    expect(cardEl().hidden).toBe(true);
    await wait(LONG_PRESS_MS + 80);
    expect(cardEl().hidden).toBe(false);
    expect(cardEl().textContent).toContain("Seat 2");
    fire(viewport, "pointerup", p, "touch", 7);
    expect(onSeats).not.toHaveBeenCalled();
    // Lifting the finger does not take the card away: it stays to be read.
    fire(viewport, "pointerout", p, "touch", 7);
    expect(cardEl().hidden).toBe(false);
  });

  it("a normal tap still paints and shows no card", async () => {
    const { viewport, onSeats } = mount();
    const p = seatPoint(KEY);
    fire(viewport, "pointerdown", p, "touch", 7, 1);
    fire(viewport, "pointerup", p, "touch", 7);
    expect(onSeats).toHaveBeenCalledTimes(1);
    expect(onSeats).toHaveBeenCalledWith([KEY], "seat");
    await wait(LONG_PRESS_MS + 80);
    expect(cardEl().hidden).toBe(true);
  });

  it("a mouse click still paints", () => {
    const { viewport, onSeats } = mount();
    const p = seatPoint(KEY);
    fire(viewport, "pointerdown", p, "mouse", 1, 1);
    fire(viewport, "pointerup", p, "mouse", 1);
    expect(onSeats).toHaveBeenCalledWith([KEY], "seat");
  });

  it("a finger that moves before the long press is a pan: no card", async () => {
    const { viewport, onSeats } = mount();
    const p = seatPoint(KEY);
    fire(viewport, "pointerdown", p, "touch", 7, 1);
    fire(viewport, "pointermove", { x: p.x + 40, y: p.y }, "touch", 7, 1);
    await wait(LONG_PRESS_MS + 80);
    expect(cardEl().hidden).toBe(true);
    fire(viewport, "pointerup", { x: p.x + 40, y: p.y }, "touch", 7);
    expect(onSeats).not.toHaveBeenCalled();
  });

  it("a second finger puts the card away: two fingers move and zoom the map", async () => {
    const { viewport } = mount();
    const p = seatPoint(KEY);
    fire(viewport, "pointerdown", p, "touch", 7, 1);
    await wait(LONG_PRESS_MS + 80);
    expect(cardEl().hidden).toBe(false);
    fire(viewport, "pointerdown", { x: p.x + 60, y: p.y }, "touch", 8, 1);
    expect(cardEl().hidden).toBe(true);
  });

  it("a long press on empty space shows nothing", async () => {
    const { viewport } = mount();
    fire(viewport, "pointerdown", { x: 5, y: 5 }, "touch", 7, 1);
    await wait(LONG_PRESS_MS + 80);
    expect(cardEl().hidden).toBe(true);
  });
});
