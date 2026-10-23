/** The seller-profile status block, rendered on the server (no DOM in vitest). */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { SellerProfile } from "@/lib/api/types";
import { I18nProvider, LOCALES, type Locale } from "@/lib/i18n";
import { SellerStatus, statusOf } from "@/screens/OrgDataScreen";

function profile(over: Partial<SellerProfile>): SellerProfile {
  return { missing: [], channels: [], seller_shown: false, ...over } as unknown as SellerProfile;
}

function render(p: SellerProfile, locale: Locale = "en"): string {
  return renderToStaticMarkup(
    <I18nProvider locale={locale}>
      <SellerStatus profile={p} />
    </I18nProvider>,
  );
}

describe("SellerStatus", () => {
  it("lists the missing required fields when incomplete", () => {
    const html = render(profile({ missing: ["legal_name", "tax_id", "unknown_future"] }));
    expect(html).toContain('data-state="incomplete"');
    expect(html).toContain("legal name");
    expect(html).toContain("tax number");
    expect(html.match(/<li>/g)).toHaveLength(2);
  });

  it("shows the complete state with no missing list", () => {
    const html = render(profile({ seller_shown: true }));
    expect(html).toContain('data-state="complete"');
    expect(html).not.toContain("org-data-missing");
  });

  it("flags a channel that still lacks its own data", () => {
    const p = profile({ channels: [{ id: "c", complete: false }] as unknown as SellerProfile["channels"] });
    expect(statusOf(p)).toBe("legal");
  });

  for (const locale of LOCALES) {
    it(`explains the automatic display and superadmin-only hiding in ${locale}`, () => {
      const html = render(profile({ missing: ["tax_id"] }), locale);
      expect(html).toContain('data-testid="org-data-status-auto"');
      expect(html).not.toContain("orgData.");
    });
  }
});
