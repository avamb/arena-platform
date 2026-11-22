/** The translation calls go to the documented routes with the documented bodies. */
import { describe, expect, it, vi } from "vitest";

const { call } = vi.hoisted(() => ({ call: vi.fn(async () => ({})) }));
vi.mock("@/lib/api/http", async (importOriginal) => ({ ...(await importOriginal<typeof import("@/lib/api/http")>()), call }));

import { deleteEventTranslation, putEventTranslation, queueMachineTranslation, saveEventReportText } from "@/lib/api/endpoints";
import { buildPath } from "@/lib/api/routes";

type Arg = { route: { method: string; path: string }; params: Record<string, string>; body?: unknown };
const last = (): Arg => (call.mock.calls.at(-1) as unknown[])[0] as Arg;
const urlOf = (a: Arg) => buildPath(a.route as { method: "GET"; path: string }, a.params);

describe("translation calls", () => {
  it("PUT replaces the translation of one locale", async () => {
    await putEventTranslation("org-1", "ev-1", "cs", { name: null, short_description: "Krátce", description_html: "<p>Ahoj</p>", report_text_html: null });
    const a = last();
    expect(a.route.method).toBe("PUT");
    expect(urlOf(a)).toBe("/v1/organizations/org-1/events/ev-1/translations/cs");
    expect(a.body).toEqual({ name: null, short_description: "Krátce", description_html: "<p>Ahoj</p>", report_text_html: null });
  });

  it("DELETE removes it", async () => {
    await deleteEventTranslation("org-1", "ev-1", "cs");
    expect(last().route.method).toBe("DELETE");
    expect(urlOf(last())).toBe("/v1/organizations/org-1/events/ev-1/translations/cs");
  });

  it("POST machine queues the locales", async () => {
    await queueMachineTranslation("org-1", "ev-1", ["cs", "de"]);
    expect(last().route.method).toBe("POST");
    expect(urlOf(last())).toBe("/v1/organizations/org-1/events/ev-1/translations/machine");
    expect(last().body).toEqual({ locales: ["cs", "de"] });
  });

  it("PUT report-text sends null for a cleared text", async () => {
    await saveEventReportText("org-1", "ev-1", null);
    expect(last().route.method).toBe("PUT");
    expect(urlOf(last())).toBe("/v1/organizations/org-1/events/ev-1/report-text");
    expect(last().body).toEqual({ report_text_html: null });
  });
});
