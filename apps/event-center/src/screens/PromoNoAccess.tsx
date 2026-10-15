import { useI18n } from "@/lib/i18n";
import { useOrg } from "@/screens/OrgLayout";

/** What a person sees when their role in this organization holds no promo-code access. */
export function NoPromoAccess({ create = false }: { create?: boolean }) {
  const { t } = useI18n();
  const { orgName } = useOrg();
  return (
    <section aria-labelledby="promo-title">
      <h1 id="promo-title">{t("promo.title")}</h1>
      <p className="notice" role="status" data-testid="promo-no-access">
        {t(create ? "promo.noCreate" : "promo.noAccess", { org: orgName })}
      </p>
    </section>
  );
}
