/** An archived organization is named, never offered as a place to work. */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { MeOrganizationMembership } from "@/lib/api/types";
import { archivedOrganizationsOf, organizationsOf } from "@/lib/auth";
import { I18nProvider, type Locale } from "@/lib/i18n";
import { ArchivedNote } from "@/screens/OrgsScreen";

function member(over: Partial<MeOrganizationMembership>): MeOrganizationMembership {
  return {
    id: "m",
    org_id: "o",
    org_name: "Org",
    org_display_number: 1,
    role: "org_admin",
    status: "active",
    joined_at: "2026-01-01T00:00:00Z",
    archived: false,
    ...over,
  };
}

function render(archived: MeOrganizationMembership[], only: boolean, locale: Locale = "en"): string {
  return renderToStaticMarkup(
    <I18nProvider locale={locale}>
      <ArchivedNote archived={archived} only={only} />
    </I18nProvider>,
  );
}

describe("organizationsOf / archivedOrganizationsOf", () => {
  const live = member({ org_id: "live", org_name: "Live" });
  const gone = member({ org_id: "gone", org_name: "Arena Test Promotions", archived: true });

  it("never offers an archived organization as a working one", () => {
    expect(organizationsOf([gone, live]).map((m) => m.org_id)).toEqual(["live"]);
    expect(organizationsOf([gone])).toEqual([]);
  });

  it("lists archived organizations separately, once each", () => {
    expect(archivedOrganizationsOf([gone, live, { ...gone, role: "organizer" }]).map((m) => m.org_id)).toEqual(["gone"]);
    expect(archivedOrganizationsOf([live])).toEqual([]);
  });

  it("treats a response without the flag as not archived", () => {
    const old = { ...live } as Partial<MeOrganizationMembership>;
    delete old.archived;
    expect(organizationsOf([old as MeOrganizationMembership])).toHaveLength(1);
  });
});

describe("ArchivedNote", () => {
  const gone = member({ org_id: "gone", org_name: "Arena Test Promotions", archived: true });

  it("renders nothing without archived organizations", () => {
    expect(render([], false)).toBe("");
  });

  it("names the archived organization next to working ones", () => {
    const html = render([gone], false, "ru");
    expect(html).toContain('data-testid="archived-orgs"');
    expect(html).toContain("Arena Test Promotions");
    expect(html).toContain("в архиве");
    expect(html).not.toContain("<a ");
  });

  it("says plainly that nothing is left to work in when every organization is archived", () => {
    const html = render([gone], true);
    expect(html).toContain("Arena Test Promotions");
    expect(html).toContain("is archived");
  });
});
