/** The description is saved by ONE PATCH of the event that carries the text and its format and nothing else. */
import { describe, expect, it, vi } from "vitest";

const { call } = vi.hoisted(() => ({ call: vi.fn(async () => ({})) }));
vi.mock("@/lib/api/http", async (importOriginal) => ({ ...(await importOriginal<typeof import("@/lib/api/http")>()), call }));

import { saveDescription } from "@/lib/api/endpoints";

describe("saveDescription", () => {
  it("sends description and description_format art to the event PATCH, and no other field", async () => {
    await saveDescription("org-1", "ev-1", "<p>Hi</p>");
    expect(call).toHaveBeenCalledTimes(1);
    const arg = (call.mock.calls[0] as unknown[])[0] as { route: { method: string; path: string }; params: unknown; body: Record<string, unknown> };
    expect(arg.route).toEqual({ method: "PATCH", path: "/v1/organizations/{org_id}/events/{id}" });
    expect(arg.params).toEqual({ org_id: "org-1", id: "ev-1" });
    expect(arg.body).toEqual({ description: "<p>Hi</p>", description_format: "art" });
  });

  it("sends an empty text as an empty description (the server clears it)", async () => {
    call.mockClear();
    await saveDescription("org-1", "ev-1", "");
    expect(((call.mock.calls[0] as unknown[])[0] as { body: unknown }).body).toEqual({ description: "", description_format: "art" });
  });
});
