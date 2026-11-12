import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { API_PENDING } from "@/lib/api/pendingRoutes";
import { buildPath } from "@/lib/api/routes";

const OPENAPI = fileURLToPath(new URL("../../../../backend/openapi/openapi.yaml", import.meta.url));

/** "METHOD /path" for every operation of openapi.yaml (the same reading as parity.test.ts). */
function openapiOperations(): Set<string> {
  const text = readFileSync(OPENAPI, "utf8").replace(/\r\n/g, "\n");
  const start = text.search(/^paths:\s*$/m);
  if (start < 0) throw new Error("openapi.yaml has no paths section");
  const ops = new Set<string>();
  let current: string | null = null;
  for (const line of text.slice(start).split("\n").slice(1)) {
    if (/^\S/.test(line)) break;
    const path = /^ {2}(\/\S+):\s*$/.exec(line);
    if (path !== null) {
      current = path[1];
      continue;
    }
    const method = /^ {4}(get|post|put|patch|delete):\s*$/.exec(line);
    if (method !== null && current !== null) ops.add(`${method[1].toUpperCase()} ${current}`);
  }
  return ops;
}

describe("pending routes", () => {
  const ops = openapiOperations();
  for (const [name, r] of Object.entries(API_PENDING)) {
    it(`${name} (${r.method} ${r.path}) is still pending`, () => {
      expect(
        ops.has(`${r.method} ${r.path}`),
        `${r.method} ${r.path} is now in openapi.yaml: move it to routes.ts (API) and to the parity matrix (src/parity/matrix.ts), then delete it from pendingRoutes.ts`,
      ).toBe(false);
    });
  }
  it("builds the address of the directory with its filters", () => {
    expect(buildPath(API_PENDING.venueDirectory, { org_id: "o1" }, { country: "ES", q: "", locale: "ru" })).toBe(
      "/v1/organizations/o1/venue-directory?country=ES&locale=ru",
    );
  });
});
