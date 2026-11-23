// @vitest-environment jsdom
/**
 * Spec 63 section 16: the "Translations" screen of an event. The language table (site languages first, state and
 * who made the translation), the editor with the original beside it, Save (PUT), delete with a question, the machine
 * button (only when the server offers it) and the two ways to update a stale translation. The editor of formatted
 * text is a plain text area here (its own tests are in richText.test.tsx).
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRoute, createRouter } from "@tanstack/react-router";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/components/RichTextField", async () => {
  const React = await import("react");
  return {
    RichTextField: ({ value, onChange, testId, disabled }: { value: string; onChange: (v: string) => void; testId?: string; disabled?: boolean }) =>
      React.createElement("textarea", {
        "data-testid": testId,
        value,
        disabled,
        onChange: (e: { target: { value: string } }) => onChange(e.target.value),
      }),
  };
});

vi.mock("@/lib/api/endpoints", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/endpoints")>()),
  listEventTranslations: vi.fn(),
  putEventTranslation: vi.fn(),
  deleteEventTranslation: vi.fn(),
  queueMachineTranslation: vi.fn(),
  saveEventReportText: vi.fn(),
}));

import { deleteEventTranslation, listEventTranslations, putEventTranslation, queueMachineTranslation, saveEventReportText } from "@/lib/api/endpoints";
import type { ContentTranslationItem, ContentTranslationList } from "@/lib/api/types";
import { I18nProvider } from "@/lib/i18n";
import { TranslationsPanel } from "@/screens/TranslationsScreen";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
window.scrollTo = () => undefined;
Element.prototype.scrollIntoView = () => undefined;

const ORG = "11111111-1111-4111-8111-111111111111";
const EVENT = "22222222-2222-4222-8222-222222222222";
const roots: Root[] = [];
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

function item(locale: string, over: Partial<ContentTranslationItem> = {}): ContentTranslationItem {
  return {
    locale,
    name: null,
    short_description: "Krátký popis",
    description_html: "<p>Starý text</p>",
    report_text_html: null,
    source: "machine",
    translation_stale: false,
    updated_at: "2026-11-20T10:00:00Z",
    updated_by: null,
    ...over,
  };
}

function list(over: Partial<ContentTranslationList> = {}): ContentTranslationList {
  return {
    content_locale: "en",
    original: { name: "Concert", short_description: "Short original", description_html: "<p>Original text</p>", report_text_html: "<p>Report</p>" },
    source_hash: "h1",
    machine_available: true,
    supported_locales: ["en", "cs", "de", "es"],
    site_locales: ["de"],
    translations: [item("cs"), item("de", { source: "manual", translation_stale: true })],
    ...over,
  };
}

beforeEach(() => {
  for (const fn of [listEventTranslations, putEventTranslation, deleteEventTranslation, queueMachineTranslation, saveEventReportText]) vi.mocked(fn).mockReset();
});
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

async function mount(data: ContentTranslationList) {
  vi.mocked(listEventTranslations).mockResolvedValue(data);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const page = createRoute({ getParentRoute: () => rootRoute, path: "/", component: () => <TranslationsPanel orgId={ORG} eventId={EVENT} /> });
  const router = createRouter({ routeTree: rootRoute.addChildren([page]), history: createMemoryHistory({ initialEntries: ["/"] }) });
  const el = document.createElement("div");
  document.body.appendChild(el);
  const root = createRoot(el);
  roots.push(root);
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <I18nProvider locale="en">
          <RouterProvider router={router} />
        </I18nProvider>
      </QueryClientProvider>,
    );
  });
  // The router and the first read take their time on a cold start: wait for the screen itself.
  // Each wait is its own act, so React flushes the renders in between.
  for (let i = 0; i < 300 && el.querySelector('[data-testid="translations"]') === null; i++) {
    await act(async () => {
      await sleep(10);
    });
  }
  return el;
}

const q = (el: HTMLElement, id: string) => el.querySelector(`[data-testid="${id}"]`) as HTMLElement;

async function click(el: HTMLElement, id: string) {
  await act(async () => {
    q(el, id).click();
    await sleep(30);
  });
}

async function typeInto(field: HTMLElement, text: string) {
  const proto = field.tagName === "TEXTAREA" ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  await act(async () => {
    Object.getOwnPropertyDescriptor(proto, "value")?.set?.call(field, text);
    field.dispatchEvent(new Event("input", { bubbles: true }));
    await sleep(0);
  });
}

describe("the language table", () => {
  it("lists the languages except the original, site languages first, with state and who made it", async () => {
    const el = await mount(list());
    const rows = [...el.querySelectorAll('[data-testid^="tr-row-"]')].map((r) => r.getAttribute("data-testid"));
    expect(rows).toEqual(["tr-row-de", "tr-row-cs", "tr-row-es"]);
    expect(q(el, "tr-state-de").textContent).toBe("Out of date");
    expect(q(el, "tr-source-de").textContent).toBe("By hand");
    expect(q(el, "tr-state-cs").textContent).toBe("Done");
    expect(q(el, "tr-source-cs").textContent).toBe("By machine");
    expect(q(el, "tr-state-es").textContent).toBe("None");
    expect(q(el, "tr-source-es").textContent).toBe("");
  });
});

describe("editing a translation", () => {
  it("types the texts, Save sends PUT for that locale and the row shows it was made by hand", async () => {
    vi.mocked(putEventTranslation).mockResolvedValue(
      item("cs", { short_description: "Nový popis", description_html: "<p>Ahoj</p>", source: "manual", updated_at: "2026-11-22T09:00:00Z" }),
    );
    const el = await mount(list());
    expect(q(el, "tr-source-cs").textContent).toBe("By machine");
    await click(el, "tr-pick-cs");
    expect(q(el, "tr-original-name").textContent).toBe("Concert");
    expect(q(el, "tr-save").hasAttribute("disabled")).toBe(true);
    await typeInto(q(el, "tr-short"), "Nový popis");
    await typeInto(q(el, "tr-description"), "<p>Ahoj</p>");
    expect(q(el, "tr-save").hasAttribute("disabled")).toBe(false);
    await click(el, "tr-save");
    expect(putEventTranslation).toHaveBeenCalledTimes(1);
    expect(putEventTranslation).toHaveBeenCalledWith(ORG, EVENT, "cs", {
      name: null,
      short_description: "Nový popis",
      description_html: "<p>Ahoj</p>",
      report_text_html: null,
    });
    expect(q(el, "tr-source-cs").textContent).toBe("By hand");
    expect(q(el, "tr-editor-source").textContent).toBe("By hand");
    expect(q(el, "tr-save-status").textContent).toContain("made by hand");
  });

  it("deletes a translation only after the question", async () => {
    vi.mocked(deleteEventTranslation).mockResolvedValue({});
    const el = await mount(list());
    await click(el, "tr-pick-cs");
    await click(el, "tr-delete");
    expect(deleteEventTranslation).not.toHaveBeenCalled();
    await click(el, "tr-delete-dialog-submit");
    expect(deleteEventTranslation).toHaveBeenCalledWith(ORG, EVENT, "cs");
    expect(q(el, "tr-state-cs").textContent).toBe("None");
  });

  it("saves the photo-report text of the original with its own button", async () => {
    vi.mocked(saveEventReportText).mockResolvedValue({ report_text_html: "<p>New report</p>" });
    const el = await mount(list());
    expect(q(el, "tr-report-save").hasAttribute("disabled")).toBe(true);
    await typeInto(q(el, "tr-report-original-edit"), "<p>New report</p>");
    await click(el, "tr-report-save");
    expect(saveEventReportText).toHaveBeenCalledWith(ORG, EVENT, "<p>New report</p>");
    expect(q(el, "tr-report-saved")).not.toBeNull();
  });
});

describe("machine translation", () => {
  it("is hidden when the server does not offer it", async () => {
    const el = await mount(list({ machine_available: false }));
    expect(q(el, "tr-machine")).toBeNull();
    expect(q(el, "tr-machine-hint")).toBeNull();
  });

  it("queues the site languages, says so and tells that a manual translation stays", async () => {
    vi.mocked(queueMachineTranslation).mockResolvedValue({ job_id: "j1", locales: ["de"] });
    const el = await mount(list());
    expect(q(el, "tr-machine-hint").textContent).toContain("never replaced by the machine");
    await click(el, "tr-machine");
    expect(queueMachineTranslation).toHaveBeenCalledWith(ORG, EVENT, ["de"]);
    expect(q(el, "tr-note").textContent).toContain("queued");
  });

  it("without a site language it translates the chosen one, and is off until one is chosen", async () => {
    vi.mocked(queueMachineTranslation).mockResolvedValue({ job_id: "j1", locales: ["es"] });
    const el = await mount(list({ site_locales: [] }));
    expect(q(el, "tr-machine").hasAttribute("disabled")).toBe(true);
    await click(el, "tr-pick-es");
    await click(el, "tr-machine");
    expect(queueMachineTranslation).toHaveBeenCalledWith(ORG, EVENT, ["es"]);
  });
});

describe("a stale translation", () => {
  it("a manual one is never overwritten: the original goes into the editor for editing, nothing is sent", async () => {
    const el = await mount(list());
    await click(el, "tr-pick-de");
    expect(q(el, "tr-stale-manual")).not.toBeNull();
    expect(q(el, "tr-stale-update")).toBeNull();
    await click(el, "tr-stale-fill");
    expect((q(el, "tr-description") as HTMLTextAreaElement).value).toBe("<p>Original text</p>");
    expect((q(el, "tr-short") as HTMLTextAreaElement).value).toBe("Short original");
    expect(queueMachineTranslation).not.toHaveBeenCalled();
    expect(putEventTranslation).not.toHaveBeenCalled();
    expect(q(el, "tr-save").hasAttribute("disabled")).toBe(false);
  });

  it("a machine one is queued again for that language", async () => {
    vi.mocked(queueMachineTranslation).mockResolvedValue({ job_id: "j2", locales: ["cs"] });
    const data = list({ translations: [item("cs", { translation_stale: true }), item("de", { source: "manual" })] });
    const el = await mount(data);
    expect(q(el, "tr-state-cs").textContent).toBe("Out of date");
    await click(el, "tr-pick-cs");
    await click(el, "tr-stale-update");
    expect(queueMachineTranslation).toHaveBeenCalledWith(ORG, EVENT, ["cs"]);
  });

  it("an up-to-date translation shows no stale block", async () => {
    const el = await mount(list());
    await click(el, "tr-pick-cs");
    expect(q(el, "tr-stale")).toBeNull();
  });
});
