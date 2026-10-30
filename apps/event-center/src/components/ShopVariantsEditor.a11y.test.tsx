// @vitest-environment jsdom
/**
 * SB-14 on the variants editor: every row's buttons say WHICH row they move or remove, the focus goes somewhere
 * sensible after a row is added, moved or removed, and a too-long option is named under its own field.
 */
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it } from "vitest";
import { ShopVariantsEditor } from "@/components/ShopVariantsEditor";
import { I18nProvider } from "@/lib/i18n";
import { SHOP_LIMITS, checkVariants, emptyRow, type VariantRow } from "@/lib/shop";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const roots: Root[] = [];
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const byId = (el: HTMLElement, id: string) => el.querySelector('[data-testid="' + id + '"]') as HTMLElement;

function row(color: string, size: string): VariantRow {
  return { ...emptyRow(), price: "10", options: [color, size, ""] };
}

function Harness({ start, tried = false }: { start: VariantRow[]; tried?: boolean }) {
  const [rows, setRows] = useState(start);
  const check = tried ? checkVariants(rows, "EUR", 2) : null;
  return <ShopVariantsEditor rows={rows} onChange={setRows} currency="EUR" optionNames={["Color", "Size"]} optionCount={2} check={check} />;
}

async function mount(start: VariantRow[], tried = false): Promise<HTMLElement> {
  const el = document.createElement("div");
  document.body.appendChild(el);
  const root = createRoot(el);
  roots.push(root);
  await act(async () => {
    root.render(
      <I18nProvider locale="en">
        <Harness start={start} tried={tried} />
      </I18nProvider>,
    );
    await sleep(0);
  });
  return el;
}

async function press(el: HTMLElement, id: string) {
  await act(async () => {
    byId(el, id).click();
    await sleep(0);
  });
}

describe("the variants editor, for a keyboard and a screen reader (SB-14)", () => {
  it("names the row in the move and remove buttons", async () => {
    const el = await mount([row("Red", "S"), row("Blue", "M")]);
    expect(byId(el, "shop-variant-1-up").getAttribute("aria-label")).toBe("Move variant 2 up");
    expect(byId(el, "shop-variant-0-down").getAttribute("aria-label")).toBe("Move variant 1 down");
    expect(byId(el, "shop-variant-remove-1").getAttribute("aria-label")).toBe("Remove variant 2");
  });

  it("puts the focus in the first field of a row that was just added", async () => {
    const el = await mount([row("Red", "S")]);
    await press(el, "shop-variant-add");
    expect(el.querySelectorAll('[data-testid="shop-variant-row"]')).toHaveLength(2);
    expect(document.activeElement).toBe(byId(el, "shop-variant-option1-1"));
  });

  it("keeps the focus on the moved row, and moves it to the neighbour's button after a remove", async () => {
    const el = await mount([row("Red", "S"), row("Blue", "M"), row("Green", "L")]);
    await press(el, "shop-variant-0-down");
    expect(byId(el, "shop-variant-option1-0").getAttribute("value")).toBe("Blue");
    expect(document.activeElement).toBe(byId(el, "shop-variant-1-down"));
    await press(el, "shop-variant-remove-1");
    expect(el.querySelectorAll('[data-testid="shop-variant-row"]')).toHaveLength(2);
    expect(document.activeElement).toBe(byId(el, "shop-variant-remove-1"));
  });

  it("falls back to the other move button when the row reached the end", async () => {
    const el = await mount([row("Red", "S"), row("Blue", "M")]);
    await press(el, "shop-variant-0-down");
    // The row is now last: its "down" is disabled, so the focus lands on "up" instead of vanishing.
    expect(document.activeElement).toBe(byId(el, "shop-variant-1-up"));
  });

  it("names a too-long option under its own field and ties the message to it", async () => {
    const long = "x".repeat(SHOP_LIMITS.option + 1);
    const el = await mount([row("Red", long), row("Blue", "M")], true);
    const size = byId(el, "shop-variant-option2-0");
    expect(size.getAttribute("aria-invalid")).toBe("true");
    const described = size.getAttribute("aria-describedby") ?? "";
    expect(described).not.toBe("");
    expect(document.getElementById(described.split(" ").pop() as string)?.textContent).not.toBe("");
    expect(byId(el, "shop-variant-option1-0").getAttribute("aria-invalid")).toBeNull();
  });
});
