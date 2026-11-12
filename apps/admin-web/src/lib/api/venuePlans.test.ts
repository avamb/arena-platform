/**
 * Tests for the seating plan intake client: the pure helpers, the audit-reason gate on
 * the new routes, the messages of the 409 and 422 answers, the requests the calls send,
 * and the "No state" scan.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  approveIntake,
  buildIntakeListPath,
  buildVerificationListPath,
  claimIntake,
  describeValidationError,
  downloadIntakeSource,
  draftValidationErrors,
  fetchDraftSvg,
  formatBytes,
  gateFor,
  holdsClaim,
  intakeErrorMessage,
  INITIAL_PLAN_FILTER,
  applyFilterChange,
  applyPage,
  cityChoices,
  countryChoices,
  filterOptionLabel,
  listAdminSeatingPlans,
  listIntakes,
  placeLabel,
  planNeedsFix,
  rejectIntake,
  returnIntake,
  seatsLabel,
  uploadIntakeDraft,
  verifyErrorMessage,
  verifyPlan,
  verificationLabel,
  waitingLabel,
} from "@/lib/api/venuePlans";
import { ApiError } from "@/lib/api/client";
import { __TEST_ONLY_resetReason, requiresAdminReason, setActiveReason } from "@/lib/api/reason";
import { __TEST_ONLY_resetTokenStore, setSession } from "@/lib/api/tokenStore";

const ID = "0193f01a-0001-7000-8000-000000000001";
const ME = "0193f01a-0002-7000-8000-000000000002";
const OTHER = "0193f01a-0003-7000-8000-000000000003";
const NOW = Date.parse("2026-11-12T12:00:00Z");

describe("waitingLabel", () => {
  it.each([
    ["2026-11-12T12:00:00Z", "0 min"],
    ["2026-11-12T11:55:00Z", "5 min"],
    ["2026-11-12T09:00:00Z", "3 h"],
    ["2026-11-12T08:40:00Z", "3 h 20 min"],
    ["2026-11-10T08:00:00Z", "2 d 4 h"],
    ["2026-11-09T12:00:00Z", "3 d"],
    ["2026-11-12T13:00:00Z", "0 min"],
  ])("%s -> %s", (created, expected) => {
    expect(waitingLabel(created, NOW)).toBe(expected);
  });

  it("shows a dash for a missing or unreadable date", () => {
    expect(waitingLabel(null, NOW)).toBe("—");
    expect(waitingLabel("", NOW)).toBe("—");
    expect(waitingLabel("not a date", NOW)).toBe("—");
  });
});

describe("small helpers", () => {
  it("formats sizes", () => {
    expect(formatBytes(12)).toBe("12 B");
    expect(formatBytes(2048)).toBe("2.0 KB");
    expect(formatBytes(5 * 1024 * 1024)).toBe("5.0 MB");
  });

  it("builds the list paths", () => {
    expect(buildIntakeListPath({ state: "operator", unclaimed: true })).toBe(
      "/v1/admin/plan-intakes?state=operator&unclaimed=true&limit=100",
    );
    expect(buildIntakeListPath({ state: "", unclaimed: false })).toBe("/v1/admin/plan-intakes?limit=100");
  });

  it("labels a missing state", () => {
    expect(verificationLabel(null)).toBe("No status");
    expect(verificationLabel("needs_fix")).toBe("Correction necessary");
  });
});

describe("list of seating plans to verify", () => {
  const F = INITIAL_PLAN_FILTER;

  it("builds the path from the filters", () => {
    expect(buildVerificationListPath(F)).toBe("/v1/admin/seating-plans?verification_state=unverified&lang=en&limit=50&offset=0");
    expect(
      buildVerificationListPath({ state: "none", orgId: "org-1", country: "cz", cityId: "city-1", q: "  Grand  ", offset: 100 }),
    ).toBe("/v1/admin/seating-plans?verification_state=none&org_id=org-1&country=CZ&city_id=city-1&q=Grand&lang=en&limit=50&offset=100");
  });

  it("sends no state for All and cuts the search to 100 characters", () => {
    const path = buildVerificationListPath({ ...F, state: "all", q: "x".repeat(150) });
    expect(path).not.toContain("verification_state");
    expect(new URL(`http://x${path}`).searchParams.get("q")).toHaveLength(100);
  });

  it("returns to page one on any filter change and clears the city with the country", () => {
    const onPage3 = applyPage({ ...F, country: "CZ", cityId: "c1" }, 100);
    expect(onPage3.offset).toBe(100);
    expect(applyFilterChange(onPage3, { state: "verified" })).toMatchObject({ state: "verified", offset: 0, cityId: "c1" });
    expect(applyFilterChange(onPage3, { q: "opera" }).offset).toBe(0);
    expect(applyFilterChange(onPage3, { orgId: "o" }).offset).toBe(0);
    expect(applyFilterChange(onPage3, { country: "DE" })).toMatchObject({ country: "DE", cityId: "", offset: 0 });
    expect(applyPage(F, -50).offset).toBe(0);
  });

  it("shows the counts in the labels", () => {
    const counts = { none: 50, unverified: 3, needs_fix: 2, verified: 7 };
    expect(filterOptionLabel("unverified", counts)).toBe("Not verified (3)");
    expect(filterOptionLabel("none", counts)).toBe("No status (50)");
    expect(filterOptionLabel("needs_fix", counts)).toBe("Correction necessary (2)");
    expect(filterOptionLabel("verified", counts)).toBe("Verified (7)");
    expect(filterOptionLabel("all", counts)).toBe("All (62)");
    expect(filterOptionLabel("all", undefined)).toBe("All");
  });

  it("builds country and city choices; a city needs a country", () => {
    const cities = [
      { id: "c1", name: "Prague", country_iso2: "CZ" },
      { id: "c2", name: "Brno", country_iso2: "cz" },
      { id: "c3", name: "Berlin", country_iso2: "DE" },
    ];
    expect(countryChoices([{ iso2: "DE", name: "Germany" }, { iso2: "CZ", name: "Czechia" }]).map((o) => o.value)).toEqual(["CZ", "DE"]);
    expect(cityChoices(cities, "").length).toBe(0);
    expect(cityChoices(cities, "CZ").map((o) => o.label)).toEqual(["Brno", "Prague"]);
  });

  it("writes the place and the seats of a row", () => {
    expect(placeLabel("Prague", "CZ")).toBe("Prague, CZ");
    expect(placeLabel(null, "CZ")).toBe("CZ");
    expect(placeLabel(null, null)).toBe("—");
    expect(seatsLabel(1200, 0)).toBe("1,200");
    expect(seatsLabel(1200, 300)).toBe("1,200 + 300 standing");
    expect(seatsLabel(0, 0)).toBe("0");
    expect(seatsLabel(null, 0)).toBe("—");
  });
});

describe("gateFor", () => {
  const base = { state: "operator", operator_claimed: true, operator_id: ME, current_draft: 2 } as const;

  it("knows who holds the claim", () => {
    expect(holdsClaim(base, ME)).toBe(true);
    expect(holdsClaim(base, OTHER)).toBe(false);
    expect(holdsClaim({ ...base, operator_claimed: false, operator_id: null }, ME)).toBe(false);
  });

  it("turns every write off for a final intake", () => {
    for (const a of ["claim", "upload_draft", "return", "approve", "reject"] as const) {
      const g = gateFor(a, { ...base, state: "approved" }, ME);
      expect(g.enabled).toBe(false);
      expect(g.why).toMatch(/No change is possible/);
    }
  });

  it("asks for a claim first", () => {
    const g = gateFor("approve", { ...base, operator_claimed: false, operator_id: null }, ME);
    expect(g.enabled).toBe(false);
    expect(g.why).toMatch(/Claim/);
    expect(gateFor("claim", { ...base, operator_claimed: false, operator_id: null }, ME).enabled).toBe(true);
  });

  it("offers Take control when another administrator holds it", () => {
    expect(gateFor("claim", base, OTHER).enabled).toBe(true);
    const g = gateFor("reject", base, OTHER);
    expect(g.enabled).toBe(false);
    expect(g.why).toMatch(/Take control/);
  });

  it("follows the state rules for the holder", () => {
    expect(gateFor("upload_draft", base, ME).enabled).toBe(true);
    expect(gateFor("upload_draft", { ...base, state: "review" }, ME).enabled).toBe(false);
    expect(gateFor("return", { ...base, current_draft: null }, ME).enabled).toBe(false);
    expect(gateFor("return", base, ME).enabled).toBe(true);
    expect(gateFor("approve", { ...base, state: "review" }, ME).enabled).toBe(true);
    expect(gateFor("reject", { ...base, state: "review" }, ME).enabled).toBe(false);
  });
});

describe("error messages", () => {
  const err = (status: number, code: string, details?: Record<string, unknown>) =>
    new ApiError(status, { code, message: "server text", details });

  it.each([
    ["venue_plan_intake.not_claimed", /Claim/],
    ["venue_plan_intake.claimed_by_other", /Take control/],
    ["venue_plan_intake.wrong_state", /Reload the card/],
    ["venue_plan_intake.draft_stale", /Reload the card/],
  ])("%s says what to do next", (code, re) => {
    expect(intakeErrorMessage(err(409, code))).toMatch(re);
  });

  it("lists the errors of a refused SVG", () => {
    const e = err(422, "venue_plan_intake.draft_validation_failed", {
      errors: [{ code: "seat_overlap", element: "#r1", detail: "two seats" }, { code: "no_seats" }, "junk", { element: "x" }],
    });
    expect(draftValidationErrors(e)).toHaveLength(2);
    expect(describeValidationError({ code: "seat_overlap", element: "#r1", detail: "two seats" })).toBe("seat_overlap (#r1): two seats");
    const text = intakeErrorMessage(e);
    expect(text).toMatch(/seat_overlap \(#r1\): two seats, no_seats/);
    expect(text).toMatch(/upload the file again/);
  });

  it("explains the verify refusals", () => {
    expect(verifyErrorMessage(err(409, "seating_plan.not_active"))).toMatch(/only an active plan/);
    expect(verifyErrorMessage(err(409, "seating_plan.no_current_version"))).toMatch(/upload an SVG/);
    expect(verifyErrorMessage(err(422, "seating_plan.geometry_invalid", { errors: [{ code: "x" }] }))).toMatch(/Correction necessary/);
  });
});

describe("audit reason gate", () => {
  beforeEach(() => __TEST_ONLY_resetReason());
  afterEach(() => __TEST_ONLY_resetReason());

  it.each([
    [`/v1/admin/plan-intakes/${ID}/claim`, "POST"],
    [`/v1/admin/plan-intakes/${ID}/drafts`, "POST"],
    [`/v1/admin/plan-intakes/${ID}/return`, "POST"],
    [`/v1/admin/plan-intakes/${ID}/approve`, "POST"],
    [`/v1/admin/plan-intakes/${ID}/reject`, "POST"],
    [`/v1/admin/plan-intakes/${ID}/sources/${OTHER}`, "GET"],
    [`/v1/admin/plan-intakes/${ID}/sources/${OTHER}?format=png`, "GET"],
    [`/v1/admin/seating-plans/${ID}/verify`, "POST"],
    [`/v1/admin/seating-plans/${ID}/needs-fix`, "POST"],
  ] as const)("%s %s needs a reason", (path, method) => {
    expect(requiresAdminReason(path, method)).toBe(true);
  });

  it.each([
    ["/v1/admin/plan-intakes?limit=50", "GET"],
    [`/v1/admin/plan-intakes/${ID}`, "GET"],
    [`/v1/admin/plan-intakes/${ID}/drafts/1/svg`, "GET"],
    ["/v1/admin/seating-plans?verification_state=unverified", "GET"],
  ] as const)("%s %s is a free read", (path, method) => {
    expect(requiresAdminReason(path, method)).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// Requests
// ---------------------------------------------------------------------------

interface Sent {
  readonly url: string;
  readonly method: string;
  readonly headers: Record<string, string>;
  readonly body: unknown;
}

function install(respond: (url: string) => Response): { calls: Sent[] } {
  const calls: Sent[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init: RequestInit) => {
      const raw = init.body;
      calls.push({
        url,
        method: init.method ?? "GET",
        headers: init.headers as Record<string, string>,
        body: typeof raw === "string" ? JSON.parse(raw) : raw,
      });
      return respond(url);
    }),
  );
  return { calls };
}

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

describe("API calls", () => {
  beforeEach(() => {
    __TEST_ONLY_resetReason();
    __TEST_ONLY_resetTokenStore();
    setSession({ accessToken: "access-1", refreshToken: "refresh-1", expiresAt: new Date(Date.now() + 60_000).toISOString(), userId: ME });
    setActiveReason("Building a seating plan");
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    __TEST_ONLY_resetReason();
    __TEST_ONLY_resetTokenStore();
  });

  it("lists intakes with the filters", async () => {
    const { calls } = install(() => json({ intakes: [{ id: ID }] }));
    const rows = await listIntakes({ state: "operator", unclaimed: true });
    expect(rows).toHaveLength(1);
    expect(calls[0].url).toMatch(/\/v1\/admin\/plan-intakes\?state=operator&unclaimed=true&limit=100$/);
    expect(calls[0].headers["X-Admin-Reason"]).toBeUndefined();
  });

  it("claims with force and the reason header", async () => {
    const { calls } = install(() => json({ intake: { id: ID } }));
    await claimIntake(ID, { force: true });
    expect(calls[0].url).toMatch(new RegExp(`/plan-intakes/${ID}/claim$`));
    expect(calls[0].method).toBe("POST");
    expect(calls[0].body).toEqual({ force: true });
    expect(calls[0].headers["X-Admin-Reason"]).toBe("Building a seating plan");
    expect(calls[0].headers.Authorization).toBe("Bearer access-1");
  });

  it("sends return, approve, reject and verify bodies", async () => {
    const { calls } = install(() => json({ intake: { id: ID }, plan_id: ID }));
    await returnIntake(ID, " fix the stage ");
    await approveIntake(ID, 3);
    await rejectIntake(ID, " unreadable ");
    await verifyPlan(ID);
    await planNeedsFix(ID, " row 4 ");
    expect(calls.map((c) => c.body)).toEqual([
      { note: "fix the stage" },
      { draft: 3 },
      { reason: "unreadable" },
      undefined,
      { note: "row 4" },
    ]);
    for (const c of calls) expect(c.headers["X-Admin-Reason"]).toBe("Building a seating plan");
  });

  it("uploads the draft as multipart with svg and note", async () => {
    const { calls } = install(() => json({ intake: { id: ID } }, 201));
    const file = new File(["<svg xmlns='http://www.w3.org/2000/svg'/>"], "hall.svg", { type: "image/svg+xml" });
    await uploadIntakeDraft(ID, file, " by hand ");
    expect(calls[0].url).toMatch(new RegExp(`/plan-intakes/${ID}/drafts$`));
    expect(calls[0].method).toBe("POST");
    expect(calls[0].body).toBeInstanceOf(FormData);
    const form = calls[0].body as FormData;
    expect((form.get("svg") as File).name).toBe("hall.svg");
    expect(form.get("note")).toBe("by hand");
    expect(calls[0].headers["X-Admin-Reason"]).toBe("Building a seating plan");
    expect(calls[0].headers["Content-Type"]).toBeUndefined();
  });

  it("downloads a source with the reason header", async () => {
    const { calls } = install(() => new Response("PDFDATA", { status: 200 }));
    const res = await downloadIntakeSource(ID, { id: OTHER, file_name: "plan.pdf" });
    expect(res.fileName).toBe("plan.pdf");
    expect(await res.blob.text()).toBe("PDFDATA");
    expect(calls[0].url).toMatch(new RegExp(`/sources/${OTHER}$`));
    expect(calls[0].headers["X-Admin-Reason"]).toBe("Building a seating plan");
  });

  it("reads a draft SVG as an image/svg+xml blob without a reason", async () => {
    const { calls } = install(() => new Response("<svg/>", { status: 200, headers: { "Content-Type": "text/plain" } }));
    const blob = await fetchDraftSvg(ID, 2);
    expect(blob.type).toBe("image/svg+xml");
    expect(calls[0].url).toMatch(new RegExp(`/plan-intakes/${ID}/drafts/2/svg$`));
    expect(calls[0].headers["X-Admin-Reason"]).toBeUndefined();
    expect(calls[0].headers.Authorization).toBe("Bearer access-1");
  });

  it("reads one page of plans with ONE request and no reason header", async () => {
    const answer = {
      seating_plans: [{ plan_id: ID, name: "Main floor" }],
      total: 123,
      limit: 50,
      offset: 50,
      counts: { none: 50, unverified: 3, needs_fix: 2, verified: 7 },
    };
    const { calls } = install(() => json(answer));
    const page = await listAdminSeatingPlans({
      state: "needs_fix",
      orgId: OTHER,
      country: "CZ",
      cityId: ME,
      q: "opera",
      offset: 50,
    });
    expect(calls).toHaveLength(1);
    expect(calls[0].method).toBe("GET");
    const u = new URL(calls[0].url);
    expect(u.pathname).toBe("/v1/admin/seating-plans");
    expect(Object.fromEntries(u.searchParams)).toEqual({
      verification_state: "needs_fix",
      org_id: OTHER,
      country: "CZ",
      city_id: ME,
      q: "opera",
      lang: "en",
      limit: "50",
      offset: "50",
    });
    expect(calls[0].headers["X-Admin-Reason"]).toBeUndefined();
    expect(page.total).toBe(123);
    expect(page.counts.none).toBe(50);
  });

  it("turns an error answer of the raw transport into an ApiError", async () => {
    install(() => json({ error: { code: "venue_plan_intake.draft_validation_failed", message: "bad", details: { errors: [{ code: "x" }] } } }, 422));
    const file = new File(["x"], "a.svg");
    await expect(uploadIntakeDraft(ID, file, "")).rejects.toMatchObject({
      status: 422,
      code: "venue_plan_intake.draft_validation_failed",
    });
  });
});

