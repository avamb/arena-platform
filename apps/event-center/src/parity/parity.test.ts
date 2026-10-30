/**
 * Keeps the web event center honest against the API and the spec:
 *
 * 1. every route the web calls (src/lib/api/routes.ts) exists in
 *    openapi.yaml with the same method;
 * 2. every route of the parity matrix exists in openapi.yaml;
 * 3. every route the web calls belongs to a function of the matrix;
 * 4. no web call goes to a superadmin route (/v1/admin/*);
 * 5. a function is "done" only with its twin test file in tests/smoke (R1 is
 *    waived as legacy; see matrix.ts);
 * 6. the table in 08_architecture/37_web_event_center_ru.md equals
 *    renderParityTable(). Run with UPDATE_PARITY=1 to rewrite it.
 */
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { API } from "@/lib/api/routes";
import { PARITY, parityRoutes, readyViolations, renderParityTable } from "@/parity/matrix";

const OPENAPI = fileURLToPath(new URL("../../../backend/openapi/openapi.yaml", import.meta.url));
const SPEC = fileURLToPath(new URL("../../../../08_architecture/37_web_event_center_ru.md", import.meta.url));

/** "METHOD /path" for every operation of openapi.yaml. */
function openapiOperations(): Set<string> {
  const text = readFileSync(OPENAPI, "utf8").replace(/\r\n/g, "\n");
  const start = text.search(/^paths:\s*$/m);
  if (start < 0) throw new Error("openapi.yaml has no paths section");
  const ops = new Set<string>();
  let current: string | null = null;
  for (const line of text.slice(start).split("\n").slice(1)) {
    if (/^\S/.test(line)) break; // the next top-level key
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

const SMOKE_DIR = fileURLToPath(new URL("../../tests/smoke/", import.meta.url));
const smokeFileExists = (file: string): boolean => existsSync(SMOKE_DIR + file);

const ops = openapiOperations();
// Routes the web already calls whose backend lane is still in review (the operator decision on a shop order that
// waits for a check). Delete the entry when the route reaches openapi.yaml: the test then guards it like the rest.
const AWAITING_BACKEND = new Set(["POST /v1/organizations/{org_id}/shop/orders/{order_id}/resolve"]);
const inSpec = (op: string): boolean => ops.has(op) || AWAITING_BACKEND.has(op);
const webCalls = Object.entries(API).map(([name, r]) => ({ name, op: `${r.method} ${r.path}` }));

describe("openapi.yaml", () => {
  it("was parsed", () => {
    expect(ops.size).toBeGreaterThan(300);
    expect(ops.has("GET /v1/me")).toBe(true);
  });
});

describe("web calls", () => {
  for (const { name, op } of webCalls) {
    it(`${name} (${op}) exists in openapi.yaml`, () => {
      expect(inSpec(op), `${op} is not in openapi.yaml`).toBe(true);
    });
  }
  it("never call a superadmin route", () => {
    for (const { op } of webCalls) expect(op.includes(" /v1/admin/"), op).toBe(false);
  });
  it("belong to a function of the parity matrix", () => {
    const listed = new Set(PARITY.filter((r) => r.web !== "—").flatMap((r) => r.routes));
    const missing = webCalls.map((c) => c.op).filter((op) => !listed.has(op));
    expect(missing, "add these web calls to src/parity/matrix.ts").toEqual([]);
  });
});

describe("twin test rule", () => {
  const base = { area: "A", fn: "F", bot: "b", routes: [] as string[] };
  const none = () => false;
  const has = () => true;
  it("refuses a done function whose twin file is missing", () => {
    const rows = [{ ...base, web: "R4" as const, ready: { twin: "refund.twin.spec.ts" } }];
    expect(readyViolations(rows, none)).toHaveLength(1);
    expect(readyViolations(rows, has)).toEqual([]);
  });
  it("refuses a done function with no twin outside the R1 waiver", () => {
    expect(readyViolations([{ ...base, web: "R4" as const, ready: { twin: null, legacy: true as const } }], has)).toHaveLength(1);
    expect(readyViolations([{ ...base, web: "R1" as const, ready: { twin: null } }], has)).toHaveLength(1);
    expect(readyViolations([{ ...base, web: "R1" as const, ready: { twin: null, legacy: true as const } }], has)).toEqual([]);
  });
  it("refuses a built R1 function with no ready entry and a missing smoke file", () => {
    expect(readyViolations([{ ...base, web: "R1" as const }], has)).toHaveLength(1);
    const rows = [{ ...base, web: "R1" as const, ready: { twin: null, legacy: true as const, smoke: "x.spec.ts" } }];
    expect(readyViolations(rows, none)).toHaveLength(1);
  });
});

describe("parity matrix", () => {
  it("names only routes that exist in openapi.yaml", () => {
    const missing = parityRoutes().filter((r) => !inSpec(r));
    expect(missing).toEqual([]);
  });
  it("has a phase for every R1 web call it lists", () => {
    const r1 = new Set(PARITY.filter((r) => r.web === "R1").flatMap((r) => r.routes));
    const called = new Set(webCalls.map((c) => c.op));
    // Every R1 route is called by the web (R1 is built, so nothing is promised only).
    expect([...r1].filter((op) => !called.has(op))).toEqual([]);
  });
  it("has no function marked done without its twin test", () => {
    expect(readyViolations(PARITY, smokeFileExists)).toEqual([]);
  });
  it("has an organization data row for the owner", () => {
    const row = PARITY.find((r) => r.area === "Данные организации");
    expect(row).toBeDefined();
    expect(row?.web).toBe("R3");
    expect(row?.ready?.twin).toBe("live/org-data.spec.ts"); // done: stage 4, task 4-7
    expect(row?.routes).toEqual(
      expect.arrayContaining([
        "GET /v1/organizations/{org_id}/seller-profile",
        "PATCH /v1/organizations/{org_id}/channels/{channel_id}/seller-display",
        "PATCH /v1/organizations/{id}",
        "GET /v1/organizations/{org_id}/me",
      ]),
    );
  });
  it("is the table of the spec", () => {
    const doc = readFileSync(SPEC, "utf8").replace(/\r\n/g, "\n");
    const re = /<!-- PARITY:BEGIN -->\n([\s\S]*?)\n?<!-- PARITY:END -->/;
    const match = re.exec(doc);
    expect(match, "the spec lost its PARITY markers").not.toBeNull();
    const table = renderParityTable();
    if (process.env.UPDATE_PARITY === "1" && match !== null && match[1] !== table) {
      writeFileSync(SPEC, doc.replace(re, `<!-- PARITY:BEGIN -->\n${table}\n<!-- PARITY:END -->`), "utf8");
      return;
    }
    expect(match?.[1]).toBe(table);
  });
});
