import type { Page } from "@playwright/test";

/** The five sections of the event card, in the standard order (src/lib/eventActions.ts). */
export const SECTIONS = ["link", "sales", "edit", "promo", "more"] as const;
export type Section = (typeof SECTIONS)[number];

/** The key the page uses to remember a section on this device. */
export const sectionKey = (id: Section) => `arena.ec.eventSection.${id}`;

/**
 * Opens every section of the event card before the page loads: it writes the choice the page would remember.
 * The harness (`mockApi`, `signInAs`) calls it by default, so a spec that needs a button or a block inside a
 * section finds it. A spec of the sections themselves passes `sections: "default"` and works with the real defaults.
 */
export async function openAllEventSections(page: Page) {
  await page.addInitScript((keys: string[]) => {
    try {
      for (const k of keys) window.localStorage.setItem(k, "1");
    } catch {
      // No storage: the page uses its defaults.
    }
  }, SECTIONS.map(sectionKey));
}

/** Opens one section by pressing its heading when it is closed. */
export async function openSection(page: Page, id: Section) {
  const toggle = page.getByTestId(`event-section-toggle-${id}`);
  if ((await toggle.getAttribute("aria-expanded")) !== "true") await toggle.click();
}
