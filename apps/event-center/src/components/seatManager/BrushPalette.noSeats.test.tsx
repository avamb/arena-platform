// @vitest-environment jsdom
/**
 * A category with no seat yet (an extra price category of an imported seated
 * date, or one the organizer made) stays in the brush menu, in its own colour,
 * with a note that it has no seats until the organizer paints them.
 */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it } from "vitest";

import { BrushPalette, type TierView } from "@/components/seatManager/SeatPanels";
import { I18nProvider } from "@/lib/i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const roots: Root[] = [];

afterEach(() => {
  for (const r of roots.splice(0)) act(() => r.unmount());
  document.body.innerHTML = "";
});

const view = (id: string, name: string, color: string, counts: TierView["counts"]): TierView => ({
  id,
  name,
  price: 7000,
  currency: "EUR",
  color,
  open: true,
  counts,
});

function render(locale: "ru" | "en", tiers: TierView[]) {
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  roots.push(root);
  act(() => {
    root.render(
      <I18nProvider locale={locale}>
        <BrushPalette mode="categories" tiers={tiers} brush={null} onBrush={() => {}} />
      </I18nProvider>,
    );
  });
}

const brush = (name: string) => document.querySelector(`[data-testid="seat-brush"][data-tier-name="${name}"]`) as HTMLElement | null;

describe("BrushPalette: a category without seats", () => {
  const tiers = [
    view("t1", "Parter", "#aa0000", { total: 2, free: 2, sold: 0, held: 0, closed: 0 } as TierView["counts"]),
    view("t2", "VIP", "#00aa00", undefined),
  ];

  it("stays in the menu with the note, in the colour of the category (ru)", () => {
    render("ru", tiers);
    const vip = brush("VIP");
    expect(vip).not.toBeNull();
    expect(vip!.textContent).toContain("нет мест: закрасьте места, чтобы они появились");
    expect((vip!.querySelector(".sm-swatch") as HTMLElement).dataset.color).toBe("#00aa00");
    expect(brush("Parter")!.textContent).not.toContain("нет мест");
  });

  it("says the same in English", () => {
    render("en", tiers);
    expect(brush("VIP")!.textContent).toContain("no seats: paint seats to add them");
  });
});
