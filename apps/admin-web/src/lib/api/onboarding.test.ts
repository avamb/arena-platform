/**
 * Tests for the onboarding applications client (APP-09): the pure helpers the
 * queue and the card are built on, the audit-reason gate on the decision
 * routes, and the requests the API calls actually send.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  AUTO_MODE_WARNING,
  CHECK_LABELS,
  FIELD_LABELS,
  FORM_BLOCKS,
  ONBOARDING_TABS,
  REQUESTABLE_FIELDS,
  actorText,
  addOnboardingNote,
  approveOnboardingApplication,
  availableActions,
  buildOnboardingListPath,
  eventDetailText,
  extendOnboardingApplication,
  formatAnswer,
  getOnboardingSettings,
  groupAnswers,
  listOnboardingApplications,
  parseCountries,
  purgeOnboardingApplication,
  recheckOnboardingApplication,
  rejectOnboardingApplication,
  relativeAge,
  requestOnboardingInfo,
  resendOnboardingLink,
  serverFieldErrors,
  settingsToForm,
  tabCount,
  updateOnboardingSettings,
  validateExtendDays,
  validateReject,
  validateRequestInfo,
  validateSettings,
  type OnboardingSettings,
  type SettingsForm,
} from "@/lib/api/onboarding";
import { __TEST_ONLY_resetReason, requiresAdminReason, setActiveReason } from "@/lib/api/reason";
import { __TEST_ONLY_resetTokenStore, setSession } from "@/lib/api/tokenStore";

const ID = "0193f01a-0001-7000-8000-000000000001";

// ---------------------------------------------------------------------------
// Tabs, counts, actions
// ---------------------------------------------------------------------------

describe("tabs and counts", () => {
  it("has the six tabs in queue order with Pending approval first", () => {
    expect(ONBOARDING_TABS.map((t) => t.status)).toEqual([
      "pending_approval",
      "info_requested",
      "draft",
      "approved",
      "rejected",
      "expired",
    ]);
    expect(ONBOARDING_TABS.map((t) => t.label)).toEqual([
      "Pending approval",
      "Info requested",
      "In progress",
      "Approved",
      "Rejected",
      "Expired",
    ]);
  });

  it("reads a tab count from the server counters and treats a missing one as zero", () => {
    const counts = { pending_approval: 4, draft: 11 };
    expect(tabCount(counts, "pending_approval")).toBe(4);
    expect(tabCount(counts, "draft")).toBe(11);
    expect(tabCount(counts, "expired")).toBe(0);
    expect(tabCount(undefined, "approved")).toBe(0);
  });
});

describe("availableActions", () => {
  const actions = (status: string, purged_at: string | null = null) => availableActions({ status, purged_at });

  it("offers the full decision set on a pending application", () => {
    expect(actions("pending_approval")).toEqual(
      expect.arrayContaining(["approve", "request_info", "reject", "resend", "purge", "recheck"]),
    );
    expect(actions("pending_approval")).not.toContain("extend");
  });

  it("offers neither approve nor reject on an approved application, only a recheck", () => {
    expect(actions("approved")).toEqual(["recheck"]);
  });

  it("lets a rejected application be erased but not decided again", () => {
    expect(actions("rejected")).toEqual(expect.arrayContaining(["purge", "recheck"]));
    expect(actions("rejected")).not.toContain("approve");
    expect(actions("rejected")).not.toContain("reject");
  });

  it("lets a draft be extended, rejected and re-sent, but not approved", () => {
    expect(actions("draft")).toEqual(expect.arrayContaining(["extend", "reject", "resend"]));
    expect(actions("draft")).not.toContain("approve");
    expect(actions("draft")).not.toContain("request_info");
  });

  it("revives an expired draft with Extend", () => {
    expect(actions("expired")).toContain("extend");
    expect(actions("expired")).not.toContain("reject");
  });

  it("offers nothing once the personal data is erased", () => {
    expect(actions("rejected", "2026-10-08T10:00:00Z")).toEqual([]);
  });
});

describe("buildOnboardingListPath", () => {
  it("sends status, search and paging", () => {
    expect(buildOnboardingListPath("pending_approval", " acme ", 25, 50)).toBe(
      "/v1/admin/onboarding/applications?status=pending_approval&q=acme&limit=25&offset=50",
    );
  });

  it("omits an empty status and search", () => {
    expect(buildOnboardingListPath("", "  ", 25, 0)).toBe("/v1/admin/onboarding/applications?limit=25&offset=0");
  });
});

// ---------------------------------------------------------------------------
// Answers
// ---------------------------------------------------------------------------

describe("formatAnswer", () => {
  it("prints booleans, lists and option labels readably", () => {
    expect(formatAnswer("accept_terms", true)).toBe("Yes");
    expect(formatAnswer("has_website", false)).toBe("No");
    expect(formatAnswer("event_types", ["concert", "festival"])).toBe("Concerts, Festivals");
    expect(formatAnswer("payment_provider", "flitt")).toBe("Flitt");
    expect(formatAnswer("events_per_year", "6-20")).toBe("6 to 20");
  });

  it("shows a dash for nothing and a raw value for an unknown option", () => {
    expect(formatAnswer("notes", "")).toBe("—");
    expect(formatAnswer("notes", null)).toBe("—");
    expect(formatAnswer("event_types", [])).toBe("—");
    expect(formatAnswer("seating", "weird")).toBe("weird");
  });
});

describe("groupAnswers", () => {
  const answers = {
    first_name: "Ana",
    org_name: "Acme Events",
    country: "CZ",
    event_types: ["theatre"],
    payment_provider: "stripe",
    accept_terms: true,
    brand_new_field: "from a newer server",
  };
  const groups = groupAnswers(answers, ["tax_id"]);

  it("follows the form's blocks in order, then an Other block for unknown keys", () => {
    expect(groups.map((g) => g.id)).toEqual(["contact", "organization", "events", "platform", "consents", "other"]);
    expect(groups.at(-1)?.rows.map((r) => r.key)).toEqual(["brand_new_field"]);
  });

  it("keeps every form field as a row, filled or not, with a human label", () => {
    const organization = groups.find((g) => g.id === "organization");
    const orgName = organization?.rows.find((r) => r.key === "org_name");
    expect(orgName).toMatchObject({ label: "Name on posters and tickets", text: "Acme Events", filled: true });
    const legal = organization?.rows.find((r) => r.key === "legal_name");
    expect(legal).toMatchObject({ text: "—", filled: false });
  });

  it("marks the fields the operator asked the applicant to fix", () => {
    const taxId = groups.flatMap((g) => g.rows).find((r) => r.key === "tax_id");
    expect(taxId?.requested).toBe(true);
    const orgName = groups.flatMap((g) => g.rows).find((r) => r.key === "org_name");
    expect(orgName?.requested).toBe(false);
  });

  it("survives null answers and null requested_fields from the server", () => {
    expect(() => groupAnswers(null, null)).not.toThrow();
    expect(groupAnswers({}, null).map((g) => g.id)).toEqual(["contact", "organization", "events", "platform", "consents"]);
  });
});

describe("form description", () => {
  it("labels every field of every block exactly once", () => {
    const keys = FORM_BLOCKS.flatMap((b) => b.fields);
    expect(new Set(keys).size).toBe(keys.length);
    for (const k of keys) expect(FIELD_LABELS[k], `label for ${k}`).toBeTruthy();
  });

  it("never offers the confirmed e-mail for a re-ask", () => {
    expect(REQUESTABLE_FIELDS).not.toContain("email");
    expect(REQUESTABLE_FIELDS).toContain("phone");
    expect(REQUESTABLE_FIELDS).toContain("tax_id");
  });

  it("names the eight checks the server runs", () => {
    expect(Object.keys(CHECK_LABELS)).toHaveLength(8);
  });
});

describe("timeline text", () => {
  it("names who did it", () => {
    expect(actorText({ actor_type: "applicant", actor_id: "" })).toBe("Applicant");
    expect(actorText({ actor_type: "system", actor_id: "" })).toBe("System");
    expect(actorText({ actor_type: "operator", actor_id: "12345678-aaaa" })).toBe("Operator 12345678");
  });

  it("explains a few events", () => {
    expect(eventDetailText({ kind: "rejected", detail: { reason: "duplicate" } })).toBe("Reason: duplicate");
    expect(eventDetailText({ kind: "info_requested", detail: { fields: ["tax_id", "phone"] } })).toBe(
      "Fields: Tax number, Phone",
    );
    expect(eventDetailText({ kind: "extended", detail: { days: 30 } })).toBe("30 days");
    expect(eventDetailText({ kind: "approved", detail: { org_slug: "acme", automatic: true } })).toBe(
      "Organization acme, approved automatically",
    );
    expect(eventDetailText({ kind: "submitted", detail: null })).toBe("");
  });
});

describe("relativeAge", () => {
  const now = Date.parse("2026-10-08T12:00:00Z");
  it.each([
    ["2026-10-08T11:59:30Z", "just now"],
    ["2026-10-08T11:55:00Z", "5 min ago"],
    ["2026-10-08T09:00:00Z", "3 h ago"],
    ["2026-10-05T12:00:00Z", "3 d ago"],
    ["2026-10-09T12:00:00Z", "just now"],
  ])("%s -> %s", (iso, text) => {
    expect(relativeAge(iso, now)).toBe(text);
  });

  it("shows a dash for nothing", () => {
    expect(relativeAge(null, now)).toBe("—");
  });
});

// ---------------------------------------------------------------------------
// Dialog input checks
// ---------------------------------------------------------------------------

describe("decision dialogs validation", () => {
  it("rejecting needs a reason", () => {
    expect(validateReject({ reason: "  ", message: "" })).toHaveProperty("reason");
    expect(validateReject({ reason: "Not a real organizer", message: "" })).toEqual({});
  });

  it("requesting details needs a field and a message", () => {
    expect(validateRequestInfo({ fields: [], message: "" })).toEqual({
      fields: expect.any(String),
      message: expect.any(String),
    });
    expect(validateRequestInfo({ fields: ["tax_id"], message: "Please add it" })).toEqual({});
  });

  it("extending takes whole days within the server's bound, or nothing", () => {
    expect(validateExtendDays("")).toEqual({});
    expect(validateExtendDays(" 30 ")).toEqual({ days: 30 });
    expect(validateExtendDays("0").error).toBeTruthy();
    expect(validateExtendDays("1096").error).toBeTruthy();
    expect(validateExtendDays("3.5").error).toBeTruthy();
    expect(validateExtendDays("abc").error).toBeTruthy();
  });
});

// ---------------------------------------------------------------------------
// Settings form
// ---------------------------------------------------------------------------

const SETTINGS: OnboardingSettings = {
  approval_mode: "manual",
  draft_ttl_days: 180,
  purge_after_days: 365,
  countries: ["CZ", "DE"],
  max_new_per_day: 200,
  terms_version: "2026-10-01",
  privacy_version: "2026-10-01",
  updated_at: "2026-10-08T10:00:00Z",
};

function form(patch: Partial<SettingsForm> = {}): SettingsForm {
  return { ...settingsToForm(SETTINGS), ...patch };
}

describe("settings form", () => {
  it("round-trips the server's settings into a valid request", () => {
    const check = validateSettings(form());
    expect(check.errors).toEqual({});
    expect(check.request).toEqual({
      approval_mode: "manual",
      draft_ttl_days: 180,
      purge_after_days: 365,
      countries: ["CZ", "DE"],
      max_new_per_day: 200,
      terms_version: "2026-10-01",
      privacy_version: "2026-10-01",
    });
  });

  it("takes comma- or space-separated country codes, upper-cased and de-duplicated; empty means all", () => {
    expect(parseCountries("cz, de es;cz")).toEqual({ codes: ["CZ", "DE", "ES"], invalid: [] });
    expect(parseCountries("")).toEqual({ codes: [], invalid: [] });
    expect(validateSettings(form({ countries: "" })).request?.countries).toEqual([]);
  });

  it("rejects anything that is not a two-letter country code", () => {
    expect(parseCountries("CZ, Czechia, 12").invalid).toEqual(["Czechia", "12"]);
    const check = validateSettings(form({ countries: "CZ, Czechia" }));
    expect(check.request).toBeUndefined();
    expect(check.errors.countries).toContain("Czechia");
  });

  it.each([
    ["draft_ttl_days", "6"],
    ["draft_ttl_days", "1096"],
    ["draft_ttl_days", "abc"],
    ["purge_after_days", "29"],
    ["purge_after_days", "3651"],
    ["max_new_per_day", "0"],
    ["max_new_per_day", "100001"],
  ] as const)("refuses %s = %s", (key, value) => {
    const check = validateSettings(form({ [key]: value }));
    expect(check.request).toBeUndefined();
    expect(check.errors[key]).toBeTruthy();
  });

  it.each([
    ["draft_ttl_days", "7"],
    ["draft_ttl_days", "1095"],
    ["purge_after_days", "30"],
    ["purge_after_days", "3650"],
    ["max_new_per_day", "1"],
    ["max_new_per_day", "100000"],
  ] as const)("accepts the bound %s = %s", (key, value) => {
    expect(validateSettings(form({ [key]: value })).errors).toEqual({});
  });

  it("requires both legal text versions, at most 40 characters", () => {
    expect(validateSettings(form({ terms_version: "  " })).errors.terms_version).toBeTruthy();
    expect(validateSettings(form({ privacy_version: "x".repeat(41) })).errors.privacy_version).toBeTruthy();
    expect(validateSettings(form({ terms_version: "v".repeat(40) })).errors).toEqual({});
  });

  it("only knows the two approval modes", () => {
    expect(validateSettings(form({ approval_mode: "auto_when_complete" })).request?.approval_mode).toBe("auto_when_complete");
    expect(validateSettings(form({ approval_mode: "whenever" })).errors.approval_mode).toBeTruthy();
  });

  it("warns that automatic mode approves without a human", () => {
    expect(AUTO_MODE_WARNING).toMatch(/without any human/);
  });

  it("reads field errors out of a server envelope", () => {
    expect(serverFieldErrors({ fields: { draft_ttl_days: "invalid", x: 3 } })).toEqual({ draft_ttl_days: "invalid" });
    expect(serverFieldErrors(undefined)).toEqual({});
  });
});

// ---------------------------------------------------------------------------
// The audit-reason gate
// ---------------------------------------------------------------------------

describe("X-Admin-Reason gate on /v1/admin/onboarding", () => {
  it.each(["approve", "reject", "request-info", "extend", "resend", "purge"])(
    "demands a reason on POST .../%s",
    (action) => {
      const path = `/v1/admin/onboarding/applications/${ID}/${action}`;
      expect(requiresAdminReason(path, "POST")).toBe(true);
      expect(requiresAdminReason(path, "GET")).toBe(false);
    },
  );

  it("demands a reason on PUT settings but not on reading them", () => {
    expect(requiresAdminReason("/v1/admin/onboarding/settings", "PUT")).toBe(true);
    expect(requiresAdminReason("/v1/admin/onboarding/settings", "GET")).toBe(false);
  });

  it("does not prompt for reading the queue, a recheck or a private note", () => {
    expect(requiresAdminReason("/v1/admin/onboarding/applications?status=draft", "GET")).toBe(false);
    expect(requiresAdminReason(`/v1/admin/onboarding/applications/${ID}`, "GET")).toBe(false);
    expect(requiresAdminReason(`/v1/admin/onboarding/applications/${ID}/recheck`, "POST")).toBe(false);
    expect(requiresAdminReason(`/v1/admin/onboarding/applications/${ID}/notes`, "POST")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// The requests the client sends
// ---------------------------------------------------------------------------

function ok(body: unknown): Response {
  return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
}

interface Sent {
  readonly url: string;
  readonly method: string;
  readonly headers: Record<string, string>;
  readonly body: unknown;
}

function installFetch(response: unknown = {}): { calls: Sent[] } {
  const calls: Sent[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init: RequestInit) => {
      calls.push({
        url,
        method: init.method ?? "GET",
        headers: init.headers as Record<string, string>,
        body: init.body === undefined ? undefined : JSON.parse(String(init.body)),
      });
      return ok(response);
    }),
  );
  return { calls };
}

describe("API calls", () => {
  beforeEach(() => {
    __TEST_ONLY_resetReason();
    __TEST_ONLY_resetTokenStore();
    setSession({
      accessToken: "access-1",
      refreshToken: "refresh-1",
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
      userId: "user-1",
    });
    setActiveReason("Reviewing application");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    __TEST_ONLY_resetReason();
    __TEST_ONLY_resetTokenStore();
  });

  it("approve posts to the application with the reason header and the bearer token", async () => {
    const { calls } = installFetch({ org_id: "o", org_slug: "acme", channel_id: "c", owner_id: "u", owner_created: true, closed_duplicates: 0 });
    const res = await approveOnboardingApplication(ID);
    expect(res.org_slug).toBe("acme");
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toMatch(new RegExp(`/v1/admin/onboarding/applications/${ID}/approve$`));
    expect(calls[0].method).toBe("POST");
    expect(calls[0].headers["X-Admin-Reason"]).toBe("Reviewing application");
    expect(calls[0].headers.Authorization).toBe("Bearer access-1");
  });

  it.each([
    ["reject", () => rejectOnboardingApplication(ID, { reason: "spam", message: "" }), { reason: "spam", message: "" }],
    ["request-info", () => requestOnboardingInfo(ID, { fields: ["tax_id"], message: "add it" }), { fields: ["tax_id"], message: "add it" }],
    ["extend", () => extendOnboardingApplication(ID, 30), { days: 30 }],
    ["extend", () => extendOnboardingApplication(ID), {}],
    ["resend", () => resendOnboardingLink(ID), undefined],
    ["purge", () => purgeOnboardingApplication(ID), undefined],
  ] as const)("%s carries the reason header and its body", async (action, call, body) => {
    const { calls } = installFetch({});
    await call();
    expect(calls[0].url).toMatch(new RegExp(`/applications/${ID}/${action}$`));
    expect(calls[0].method).toBe("POST");
    expect(calls[0].headers["X-Admin-Reason"]).toBe("Reviewing application");
    expect(calls[0].body).toEqual(body);
  });

  it("recheck and notes go out without a reason header", async () => {
    const { calls } = installFetch({});
    await recheckOnboardingApplication(ID);
    await addOnboardingNote(ID, "called the applicant");
    expect(calls[0].url).toMatch(/\/recheck$/);
    expect(calls[0].headers["X-Admin-Reason"]).toBeUndefined();
    expect(calls[1].url).toMatch(/\/notes$/);
    expect(calls[1].body).toEqual({ body: "called the applicant" });
    expect(calls[1].headers["X-Admin-Reason"]).toBeUndefined();
  });

  it("listing and reading settings are plain GETs without the header", async () => {
    const { calls } = installFetch({ items: [], total: 0, counts: {} });
    await listOnboardingApplications("draft", "acme", 25, 0);
    await getOnboardingSettings();
    expect(calls[0].url).toMatch(/\/v1\/admin\/onboarding\/applications\?status=draft&q=acme&limit=25&offset=0$/);
    expect(calls[0].method).toBe("GET");
    expect(calls[0].headers["X-Admin-Reason"]).toBeUndefined();
    expect(calls[1].url).toMatch(/\/v1\/admin\/onboarding\/settings$/);
  });

  it("saving settings is a PUT with the reason header", async () => {
    const { calls } = installFetch(SETTINGS);
    await updateOnboardingSettings({ approval_mode: "manual", countries: [] });
    expect(calls[0].method).toBe("PUT");
    expect(calls[0].url).toMatch(/\/v1\/admin\/onboarding\/settings$/);
    expect(calls[0].headers["X-Admin-Reason"]).toBe("Reviewing application");
    expect(calls[0].body).toEqual({ approval_mode: "manual", countries: [] });
  });
});
