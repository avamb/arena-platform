/**
 * 4-7: the owner's legal data and the organization's contact, one form and
 * ONE PATCH /v1/organizations/{id} with only the fields that changed.
 *
 * The web checks the shape first (src/lib/orgData.ts, a port of the bot's
 * rules) and says what to change in plain words, then sends. A refusal of
 * the API still comes through describeApiError. Emptying a required legal
 * field is allowed: the sales page then stops showing the seller block by
 * itself, and the answer's `seller_display_hidden` is shown by name.
 */
import { useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { SellerErrorNotice } from "@/components/SellerErrorNotice";
import { SelectField, TextField } from "@/components/FormField";
import { patchOrganization } from "@/lib/api/endpoints";
import type { OrganizationItem, SellerProfile } from "@/lib/api/types";
import { useI18n, type MessageKey } from "@/lib/i18n";
import {
  TAX_SCHEMES,
  buildOrgPatch,
  formFromOrg,
  isTaxScheme,
  validateForm,
  type FormProblem,
  type OrgForm,
  type OrgFormField,
} from "@/lib/orgData";

export interface OrgDataFormProps {
  orgId: string;
  org: OrganizationItem;
  profile: SellerProfile;
}

export function OrgDataForm({ orgId, org, profile }: OrgDataFormProps) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [base, setBase] = useState<OrgForm>(() => formFromOrg(org));
  const [form, setForm] = useState<OrgForm>(base);
  const [problems, setProblems] = useState<FormProblem[]>([]);
  const [note, setNote] = useState<"saved" | "nothing" | null>(null);
  const [hiddenIds, setHiddenIds] = useState<string[]>([]);

  const save = useMutation({
    mutationFn: (patch: Record<string, unknown>) => patchOrganization(orgId, patch),
    onSuccess: async (res) => {
      const next = formFromOrg(res.organization);
      const renamed = next.name !== base.name;
      setBase(next);
      setForm(next);
      setNote("saved");
      setHiddenIds(res.seller_display_hidden ?? []);
      queryClient.setQueryData(["orgDetail", orgId], { organization: res.organization });
      await queryClient.invalidateQueries({ queryKey: ["sellerProfile", orgId] });
      if (renamed) await queryClient.invalidateQueries({ queryKey: ["orgMe", orgId] });
    },
  });

  const set = <K extends OrgFormField>(key: K, value: OrgForm[K]) => {
    setForm((f) => ({ ...f, [key]: value }));
    setNote(null);
    setProblems((p) => p.filter((x) => x.field !== key));
  };

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    save.reset();
    setHiddenIds([]);
    const found = validateForm(form);
    setProblems(found);
    if (found.length > 0) {
      setNote(null);
      return;
    }
    const patch = buildOrgPatch(base, form);
    if (Object.keys(patch).length === 0) {
      setNote("nothing");
      return;
    }
    setNote(null);
    save.mutate(patch);
  };

  const err = (field: OrgFormField): string | undefined => {
    const p = problems.find((x) => x.field === field);
    return p === undefined ? undefined : t(p.key);
  };

  const hiddenNames = hiddenIds.map((id) => profile.channels.find((c) => c.id === id)?.name ?? id).join(", ");

  return (
    <form className="org-form" onSubmit={onSubmit} noValidate data-testid="org-data-form" aria-label={t("orgData.legal.title")}>
      <h2>{t("orgData.legal.title")}</h2>
      <p className="muted">{t("orgData.legal.hint")}</p>

      <TextField label={t("orgData.field.name")} value={form.name} onChange={(v) => set("name", v)} error={err("name")} testId="field-name" />
      <TextField
        label={t("orgData.field.legalName")}
        hint={t("orgData.field.legalName.hint")}
        value={form.legalName}
        onChange={(v) => set("legalName", v)}
        error={err("legalName")}
        testId="field-legal-name"
      />
      <TextField
        label={t("orgData.field.legalForm")}
        hint={t("orgData.field.legalForm.hint")}
        value={form.legalForm}
        onChange={(v) => set("legalForm", v)}
        error={err("legalForm")}
        testId="field-legal-form"
      />
      <TextField
        label={t("orgData.field.registrationNumber")}
        value={form.registrationNumber}
        onChange={(v) => set("registrationNumber", v)}
        error={err("registrationNumber")}
        testId="field-registration"
      />
      <SelectField
        label={t("orgData.field.taxScheme")}
        value={form.taxScheme}
        onChange={(v) => set("taxScheme", isTaxScheme(v) ? v : "")}
        error={err("taxScheme")}
        testId="field-tax-scheme"
      >
        <option value="">{t("orgData.field.taxScheme.none")}</option>
        {TAX_SCHEMES.map((s) => (
          <option key={s} value={s}>
            {t(`orgData.scheme.${s}` as MessageKey)}
          </option>
        ))}
      </SelectField>
      <TextField
        label={t("orgData.field.taxId")}
        hint={form.taxScheme === "" ? undefined : t(`orgData.taxHint.${form.taxScheme}` as MessageKey)}
        value={form.taxId}
        onChange={(v) => set("taxId", v)}
        error={err("taxId")}
        testId="field-tax-id"
      />
      <TextField label={t("orgData.field.line1")} value={form.line1} onChange={(v) => set("line1", v)} error={err("line1")} testId="field-line1" autoComplete="street-address" />
      <TextField label={t("orgData.field.city")} value={form.city} onChange={(v) => set("city", v)} error={err("city")} testId="field-city" />
      <TextField label={t("orgData.field.postalCode")} value={form.postalCode} onChange={(v) => set("postalCode", v)} error={err("postalCode")} testId="field-postal-code" autoComplete="postal-code" />
      <TextField
        label={t("orgData.field.country")}
        hint={t("orgData.field.country.hint")}
        value={form.country}
        onChange={(v) => set("country", v)}
        error={err("country")}
        testId="field-country"
        maxLength={2}
      />

      <h2>{t("orgData.contact.title")}</h2>
      <p className="muted">{t("orgData.contact.hint")}</p>
      <TextField label={t("orgData.field.contactEmail")} type="email" value={form.contactEmail} onChange={(v) => set("contactEmail", v)} error={err("contactEmail")} testId="field-contact-email" />
      <TextField label={t("orgData.field.contactPhone")} type="tel" value={form.contactPhone} onChange={(v) => set("contactPhone", v)} error={err("contactPhone")} testId="field-contact-phone" />

      {problems.length > 0 && (
        <div className="notice notice-bad" role="alert" data-testid="org-data-problems">
          <p>
            <strong>{t("orgData.problems.title")}</strong>
          </p>
          <ul className="plain-list">
            {problems.map((p) => (
              <li key={p.field}>{t(p.key)}</li>
            ))}
          </ul>
        </div>
      )}
      {save.error !== null && <SellerErrorNotice error={save.error} testId="org-data-save-error" />}
      {note === "saved" && (
        <p className="notice notice-ok" role="status" data-testid="org-data-saved">
          {t("orgData.saved")}
        </p>
      )}
      {note === "nothing" && (
        <p className="notice" role="status" data-testid="org-data-nothing">
          {t("orgData.nothingChanged")}
        </p>
      )}
      {hiddenIds.length > 0 && (
        <p className="notice notice-bad" role="status" data-testid="org-data-hidden">
          {t("orgData.hidden", { pages: hiddenNames })}
        </p>
      )}

      <button type="submit" className="btn btn-primary" disabled={save.isPending} data-testid="org-data-save">
        {save.isPending ? t("orgData.saving") : t("orgData.save")}
      </button>
    </form>
  );
}
