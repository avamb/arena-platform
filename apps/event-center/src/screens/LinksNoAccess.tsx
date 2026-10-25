import { useI18n } from "@/lib/i18n";
import { useOrg } from "@/screens/OrgLayout";

/** What a person sees when their role in this organization holds no sales-link access (the PromoNoAccess pattern). */
export function NoLinksAccess({ create = false }: { create?: boolean }) {
  const { t } = useI18n();
  const { orgName } = useOrg();
  return (
    <section aria-labelledby="links-title">
      <h1 id="links-title">{t("links.title")}</h1>
      <p className="notice" role="status" data-testid="links-no-access">
        {t(create ? "links.noCreate" : "links.noAccess", { org: orgName })}
      </p>
    </section>
  );
}
