/**
 * Render tests for the Onboarding screens (APP-09). The admin-web Vitest
 * environment is Node-only (no jsdom), so — following the sessionOverview
 * precedent — the router- and query-free presentational parts are rendered
 * with renderToStaticMarkup.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { createElement, type ReactNode } from "react";
import {
  OnboardingQueueTable,
  OnboardingSettingsView,
  OnboardingTabs,
  Pager,
  ViewSwitch,
} from "@/routes/onboarding";
import {
  ActionBar,
  ApproveDialogBody,
  OnboardingCardBody,
  NotesList,
  approvalSummary,
  checkProblems,
  organizationHref,
} from "@/routes/onboardingDetail";
import {
  settingsToForm,
  type OnboardingDetail,
  type OnboardingListItem,
  type OnboardingSettings,
} from "@/lib/api/onboarding";

const NOW = Date.parse("2026-10-08T12:00:00Z");
const A1 = "11111111-1111-7111-8111-111111111111";
const A2 = "22222222-2222-7222-8222-222222222222";

function item(patch: Partial<OnboardingListItem> = {}): OnboardingListItem {
  return {
    id: A1,
    status: "pending_approval",
    source: "site",
    email: "ana@example.test",
    email_confirmed_at: "2026-10-08T09:00:00Z",
    applicant_name: "Ana Novak",
    phone: "+420600111222",
    locale: "en",
    country: "CZ",
    org_name: "Acme Events",
    legal_name: "Acme Events s.r.o.",
    current_step: "review",
    progress_pct: 100,
    answers: {},
    last_activity_at: "2026-10-08T09:00:00Z",
    expires_at: "2027-04-06T09:00:00Z",
    created_at: "2026-10-08T08:00:00Z",
    updated_at: "2026-10-08T09:00:00Z",
    would_approve: true,
    ...patch,
  };
}

const link = (id: string, children: ReactNode) => createElement("a", { href: `/onboarding/${id}` }, children);

function table(items: readonly OnboardingListItem[], layout: "desktop" | "mobile" = "desktop"): string {
  return renderToStaticMarkup(
    <OnboardingQueueTable items={items} renderCardLink={link} now={NOW} forceLayout={layout} />,
  );
}

// ---------------------------------------------------------------------------
// Queue
// ---------------------------------------------------------------------------

describe("OnboardingTabs", () => {
  const counts = { pending_approval: 3, info_requested: 1, draft: 12, approved: 40, rejected: 2, expired: 0 };
  const html = renderToStaticMarkup(<OnboardingTabs counts={counts} active="pending_approval" onSelect={() => undefined} />);

  it("renders the six tabs with their counts", () => {
    for (const [status, n] of Object.entries(counts)) {
      expect(html).toContain(`data-testid="onboarding-tab-${status}"`);
      expect(html).toMatch(new RegExp(`data-testid="onboarding-tab-count-${status}"[^>]*>${n}<`));
    }
    expect(html).toContain("Pending approval");
    expect(html).toContain("In progress");
  });

  it("marks only the active tab as selected", () => {
    expect(html.match(/aria-selected="true"/g)).toHaveLength(1);
    expect(html).toMatch(/aria-selected="true"[^>]*data-testid="onboarding-tab-pending_approval"/);
  });

  it("shows zeros before the first response", () => {
    const empty = renderToStaticMarkup(<OnboardingTabs counts={undefined} active="draft" onSelect={() => undefined} />);
    expect(empty).toMatch(/onboarding-tab-count-pending_approval"[^>]*>0</);
  });
});

describe("OnboardingQueueTable", () => {
  it("shows organization, country, applicant contacts, step, activity, source and the verdict", () => {
    const html = table([item()]);
    expect(html).toContain("Acme Events");
    expect(html).toContain("Acme Events s.r.o.");
    expect(html).toContain(">CZ<");
    expect(html).toContain("Ana Novak");
    expect(html).toContain("ana@example.test");
    expect(html).toContain("+420600111222");
    expect(html).toContain("review · 100%");
    expect(html).toContain("3 h ago");
    expect(html).toContain("Site");
    expect(html).toContain(`href="/onboarding/${A1}"`);
  });

  it("badges a row the system would approve and one it would not", () => {
    const html = table([item(), item({ id: A2, would_approve: false })]);
    expect(html).toMatch(new RegExp(`onboarding-would-approve-${A1}"[^>]*>System would approve<`));
    expect(html).toMatch(new RegExp(`onboarding-would-approve-${A2}"[^>]*>Needs a look<`));
  });

  it("labels a Telegram application and copes with a draft that has nothing yet", () => {
    const html = table([
      item({ source: "telegram", status: "draft", org_name: null, legal_name: null, country: null, phone: null, applicant_name: null, progress_pct: 20, current_step: "organization", would_approve: false }),
    ]);
    expect(html).toContain("Telegram");
    expect(html).toContain("In progress");
    expect(html).toContain("organization · 20%");
    expect(html).toContain("—");
  });

  it("renders the mobile card layout from the same columns", () => {
    const html = table([item()], "mobile");
    expect(html).toContain('data-layout="mobile"');
    expect(html).toContain("Acme Events");
  });

  it("says so when a tab is empty", () => {
    expect(table([])).toContain("No applications in this tab.");
  });
});

describe("Pager and view switch", () => {
  it("shows the range and disables Previous on the first page", () => {
    const html = renderToStaticMarkup(<Pager total={60} offset={0} shown={25} limit={25} onPrev={() => undefined} onNext={() => undefined} />);
    expect(html).toContain("1–25 of 60");
    expect(html).toMatch(/disabled=""[^>]*data-testid="onboarding-prev"/);
    expect(html).not.toMatch(/disabled=""[^>]*data-testid="onboarding-next"/);
  });

  it("disables Next on the last page", () => {
    const html = renderToStaticMarkup(<Pager total={60} offset={50} shown={10} limit={25} onPrev={() => undefined} onNext={() => undefined} />);
    expect(html).toContain("51–60 of 60");
    expect(html).toMatch(/disabled=""[^>]*data-testid="onboarding-next"/);
  });

  it("hides the Settings view without onboarding.settings", () => {
    expect(renderToStaticMarkup(<ViewSwitch view="applications" onChange={() => undefined} canSeeSettings={false} />)).toBe("");
    expect(renderToStaticMarkup(<ViewSwitch view="applications" onChange={() => undefined} canSeeSettings />)).toContain("onboarding-view-settings");
  });
});

// ---------------------------------------------------------------------------
// Card
// ---------------------------------------------------------------------------

function detail(status: string, patch: Partial<OnboardingDetail["application"]> = {}, rest: Partial<OnboardingDetail> = {}): OnboardingDetail {
  const base = item({
    status,
    answers: {
      first_name: "Ana",
      last_name: "Novak",
      email: "ana@example.test",
      phone: "+420600111222",
      telegram_username: "ana_n",
      org_name: "Acme Events",
      legal_name: "Acme Events s.r.o.",
      country: "CZ",
      tax_id: "CZ12345678",
      tax_id_scheme: "vat",
      event_types: ["theatre", "festival"],
      seating: "both",
      payment_provider: "stripe",
      accept_terms: true,
      accept_privacy: true,
      confirm_authority: true,
      marketing_opt_in: false,
    },
    ...patch,
  });
  const { would_approve: _w, ...application } = base;
  return {
    application,
    checks: [
      { key: "email_confirmed", result: "pass", detail: "", checked_at: "2026-10-08T09:00:00Z" },
      { key: "duplicate", result: "warn", detail: "same tax number as another application", checked_at: "2026-10-08T09:00:00Z" },
      { key: "tax_id_format", result: "fail", detail: "does not look like a VAT number", checked_at: "2026-10-08T09:00:00Z" },
    ],
    would_approve: false,
    events: [
      { id: "e1", kind: "created", actor_type: "applicant", actor_id: "", detail: { source: "site" }, created_at: "2026-10-08T08:00:00Z" },
      { id: "e2", kind: "submitted", actor_type: "applicant", actor_id: "", detail: {}, created_at: "2026-10-08T09:00:00Z" },
    ],
    notes: [{ id: "n1", author_id: null, body: "Called, will send the VAT paper", created_at: "2026-10-08T10:00:00Z" }],
    ...rest,
  };
}

function card(d: OnboardingDetail, busy: string | null = null): string {
  return renderToStaticMarkup(
    <OnboardingCardBody detail={d} busy={busy} onAction={() => undefined} notes={<NotesList notes={d.notes} />} now={NOW} />,
  );
}

describe("OnboardingCardBody", () => {
  const html = card(detail("pending_approval"));

  it("groups the answers into the form's blocks with human labels", () => {
    for (const block of ["contact", "organization", "events", "platform", "consents"]) {
      expect(html).toContain(`data-testid="onboarding-block-${block}"`);
    }
    expect(html).toContain("Name on posters and tickets");
    expect(html).toMatch(/onboarding-answer-event_types"[^>]*>Theatre and shows, Festivals/);
    expect(html).toMatch(/onboarding-answer-accept_terms"[^>]*>Yes/);
    expect(html).toMatch(/onboarding-answer-marketing_opt_in"[^>]*>No/);
  });

  it("lists the checks with their result and detail", () => {
    expect(html).toContain("E-mail confirmed");
    expect(html).toContain("same tax number as another application");
    expect(html).toMatch(/onboarding-check-tax_id_format[\s\S]*?fail/);
    expect(html).toContain('data-testid="onboarding-recheck"');
  });

  it("shows the timeline and the private notes", () => {
    expect(html).toContain("Draft started");
    expect(html).toContain("Submitted for approval");
    expect(html).toContain("Called, will send the VAT paper");
  });

  it("shows the applicant's contacts and the system verdict", () => {
    expect(html).toContain("ana@example.test");
    expect(html).toContain("+420600111222");
    expect(html).toContain("@ana_n");
    expect(html).toContain("System would not approve yet");
  });

  it("offers the decisions of a pending application", () => {
    for (const a of ["approve", "request_info", "reject", "resend", "purge"]) {
      expect(html).toContain(`data-testid="onboarding-action-${a}"`);
    }
    expect(html).not.toContain('data-testid="onboarding-action-extend"');
  });

  it("an approved card links to the organization and offers no decision", () => {
    const orgId = "33333333-3333-7333-8333-333333333333";
    const approved = card(detail("approved", { org_id: orgId, reviewed_at: "2026-10-08T11:00:00Z" }));
    expect(approved).toContain(`href="${organizationHref(orgId)}"`);
    expect(approved).not.toContain("onboarding-action-approve");
    expect(approved).not.toContain("onboarding-action-reject");
  });

  it("a rejected card shows the reason and can only be erased", () => {
    const rejected = card(detail("rejected", { reviewed_at: "2026-10-08T11:00:00Z", decision_reason: "Not an organizer" }));
    expect(rejected).toContain("Not an organizer");
    expect(rejected).toContain("onboarding-action-purge");
    expect(rejected).not.toContain("onboarding-action-approve");
  });

  it("an info-requested card says what the applicant was asked", () => {
    const asked = card(
      detail("info_requested", { requested_fields: ["tax_id"], info_request_message: "Please send the VAT number" }),
    );
    expect(asked).toContain("Waiting for the applicant");
    expect(asked).toContain("Please send the VAT number");
    expect(asked).toContain("asked to fix");
    expect(asked).toContain("onboarding-action-extend");
  });

  it("a purged card is read-only and says so", () => {
    const purged = card(detail("rejected", { purged_at: "2026-10-08T11:00:00Z" }));
    expect(purged).toContain('data-testid="onboarding-purged"');
    expect(purged).not.toContain('data-testid="onboarding-actions"');
  });

  it("disables the buttons while a call is in flight", () => {
    const busy = card(detail("pending_approval"), "approve");
    expect(busy).toMatch(/disabled=""[^>]*data-testid="onboarding-action-reject"/);
    expect(busy).toContain("Approve…");
  });
});

describe("ActionBar", () => {
  it("renders nothing for a status with no decision", () => {
    expect(renderToStaticMarkup(<ActionBar app={{ status: "approved", purged_at: null }} busy={null} onAction={() => undefined} />)).toBe("");
  });

  it("offers Extend and Resend on a draft", () => {
    const html = renderToStaticMarkup(<ActionBar app={{ status: "draft", purged_at: null }} busy={null} onAction={() => undefined} />);
    expect(html).toContain("onboarding-action-extend");
    expect(html).toContain("onboarding-action-resend");
    expect(html).toContain("Delete personal data");
  });
});

describe("approval confirmation", () => {
  it("lists what will be created", () => {
    const d = detail("pending_approval");
    const lines = approvalSummary(d.application);
    expect(lines.join("\n")).toContain("Organization “Acme Events”");
    expect(lines.join("\n")).toContain("direct-merchant sales channel with a public sales page");
    expect(lines.join("\n")).toContain("never merchant of record");
    expect(lines.join("\n")).toContain("set-password e-mail");
    expect(lines.join("\n")).toContain("ana@example.test");
    expect(lines.join("\n")).toContain("unverified");
    expect(lines.join("\n")).not.toContain("Telegram");
  });

  it("adds the bot link when the application came from Telegram", () => {
    const d = detail("pending_approval", { source: "telegram", telegram_user_id: 4242 });
    expect(approvalSummary(d.application).join("\n")).toContain("Telegram account");
  });

  it("renders the list and warns when the system would not approve", () => {
    const html = renderToStaticMarkup(<ApproveDialogBody detail={detail("pending_approval")} />);
    expect(html).toContain('data-testid="onboarding-approve-list"');
    expect(html).toContain('data-testid="onboarding-approve-warning"');
    expect(html).toContain("1 failed, 1 with a warning");
  });

  it("stays quiet when every check passed", () => {
    const d = detail("pending_approval", {}, { would_approve: true, checks: [] });
    expect(renderToStaticMarkup(<ApproveDialogBody detail={d} />)).not.toContain("onboarding-approve-warning");
  });

  it("counts problem checks", () => {
    expect(checkProblems(detail("pending_approval").checks)).toEqual({ warn: 1, fail: 1 });
  });
});

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

const SETTINGS: OnboardingSettings = {
  approval_mode: "manual",
  draft_ttl_days: 180,
  purge_after_days: 365,
  countries: [],
  max_new_per_day: 200,
  terms_version: "2026-10-01",
  privacy_version: "2026-10-01",
  updated_at: "2026-10-08T10:00:00Z",
};

function settingsMarkup(patch: Partial<ReturnType<typeof settingsToForm>> = {}, errors: Record<string, string> = {}, saved = false): string {
  return renderToStaticMarkup(
    <OnboardingSettingsView
      form={{ ...settingsToForm(SETTINGS), ...patch }}
      errors={errors}
      formError={null}
      saved={saved}
      saving={false}
      updatedAt={SETTINGS.updated_at}
      onChange={() => undefined}
      onSave={() => undefined}
    />,
  );
}

describe("OnboardingSettingsView", () => {
  it("shows every setting", () => {
    const html = settingsMarkup();
    for (const key of ["approval_mode", "draft_ttl_days", "purge_after_days", "countries", "max_new_per_day", "terms_version", "privacy_version"]) {
      expect(html).toContain(`data-testid="onboarding-set-${key}"`);
    }
    expect(html).toContain('value="180"');
    expect(html).toContain("Empty means every country.");
  });

  it("warns about automatic approval only when that mode is chosen", () => {
    expect(settingsMarkup()).not.toContain("onboarding-auto-warning");
    const auto = settingsMarkup({ approval_mode: "auto_when_complete" });
    expect(auto).toContain('data-testid="onboarding-auto-warning"');
    expect(auto).toContain("approves");
    expect(auto).toContain("without any human");
  });

  it("prints a validation error under its field", () => {
    const html = settingsMarkup({ draft_ttl_days: "3" }, { draft_ttl_days: "Whole days, from 7 to 1095." });
    expect(html).toMatch(/onboarding-set-draft_ttl_days-error"[^>]*>Whole days, from 7 to 1095\./);
    expect(html).toContain('aria-invalid="true"');
  });

  it("confirms a save", () => {
    expect(settingsMarkup({}, {}, true)).toContain("Settings saved.");
  });
});
