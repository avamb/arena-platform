/**
 * The section "Event" of the wizard (task 6-1a): name, description, age and
 * who promotes it. The poster is task 6-3. Errors come in by field: some are
 * the wizard's own (a length), the rest are the server's check.
 */
import { useQuery } from "@tanstack/react-query";
import { listPromoters } from "@/lib/api/endpoints";
import { useI18n } from "@/lib/i18n";
import { AGE_OPTIONS, isAgeRating, type DraftAction, type WizardDraft } from "@/lib/wizard/draft";
import type { FormField } from "@/lib/wizard/validation";
import { SelectField, TextAreaField, TextField } from "@/components/FormField";
import { useOrg } from "@/screens/OrgLayout";

export interface SectionProps {
  draft: WizardDraft;
  dispatch: (action: DraftAction) => void;
  touch: (field: FormField) => void;
  /** The text to show under a field, already in the person's language; undefined = no error. */
  errorOf: (field: FormField) => string | undefined;
}

export function WizardEventSection({ draft, dispatch, touch, errorOf }: SectionProps) {
  const { t } = useI18n();
  const { orgId, orgName, access } = useOrg();
  const canList = access.can("promoter.read");
  const promoters = useQuery({ queryKey: ["promoters", orgId], queryFn: () => listPromoters(orgId), enabled: canList });
  const active = (promoters.data?.promoters ?? []).filter((p) => !p.archived).sort((a, b) => a.name.localeCompare(b.name));
  const e = draft.event;
  // A promoter chosen earlier may have been archived since: keep it visible so the select is honest.
  const chosenMissing = e.promoterId !== "" && !active.some((p) => p.id === e.promoterId);

  return (
    <fieldset className="org-form" data-testid="wizard-event">
      <legend>{t("wizard.section.event")}</legend>
      <TextField
        label={t("wizard.f.name")}
        hint={t("wizard.f.nameHint")}
        error={errorOf("name")}
        value={e.name}
        onChange={(value) => {
          touch("name");
          dispatch({ type: "event", field: "name", value });
        }}
        onBlur={() => touch("name")}
        testId="wizard-name"
      />
      <TextAreaField
        label={t("wizard.f.description")}
        hint={t("wizard.f.descriptionHint")}
        error={errorOf("description")}
        value={e.description}
        onChange={(value) => {
          touch("description");
          dispatch({ type: "event", field: "description", value });
        }}
        onBlur={() => touch("description")}
        testId="wizard-description"
      />
      <SelectField
        label={t("wizard.f.age")}
        error={errorOf("age")}
        value={e.age}
        onChange={(value) => {
          touch("age");
          dispatch({ type: "age", value: isAgeRating(value) ? value : "" });
        }}
        testId="wizard-age"
      >
        <option value="">{t("wizard.age.none")}</option>
        {AGE_OPTIONS.map((a) => (
          <option key={a} value={a}>
            {a === "NR" ? t("wizard.age.NR") : a}
          </option>
        ))}
      </SelectField>
      <SelectField
        label={t("wizard.f.promoter")}
        error={errorOf("promoter")}
        value={e.promoterId}
        onChange={(value) => {
          touch("promoter");
          const found = active.find((p) => p.id === value);
          dispatch({ type: "promoter", id: value, name: found?.name ?? "" });
        }}
        testId="wizard-promoter"
      >
        <option value="">{t("wizard.promoter.org", { org: orgName })}</option>
        {chosenMissing && <option value={e.promoterId}>{e.promoterName}</option>}
        {active.map((p) => (
          <option key={p.id} value={p.id}>
            {p.name}
          </option>
        ))}
      </SelectField>
      {canList && promoters.isLoading && <p className="field-hint">{t("wizard.promoter.loading")}</p>}
    </fieldset>
  );
}
