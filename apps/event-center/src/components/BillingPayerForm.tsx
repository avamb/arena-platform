/**
 * The payer details printed on invoices (PUT .../billing/payer, the owner's
 * `billing.payer_manage`). The API has no read for them, so the form starts
 * from the organization's own legal data and the person can change every
 * field. The web checks the same limits as the API first and says what to
 * change; a refusal of the API still comes through the error text.
 */
import { useId, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ErrorNotice } from "@/components/ErrorNotice";
import { TextField } from "@/components/FormField";
import { Loading } from "@/components/ui";
import { fetchOrganization, putBillingPayer } from "@/lib/api/endpoints";
import {
  EMPTY_PAYER,
  payerFromOrg,
  payerRequest,
  validatePayer,
  type PayerField,
  type PayerForm,
  type PayerProblem,
} from "@/lib/billingAccount";
import { useI18n } from "@/lib/i18n";

export function BillingPayerForm({ orgId }: { orgId: string }) {
  // The prefill is a courtesy: a failed read leaves the form empty.
  const org = useQuery({ queryKey: ["orgDetail", orgId], queryFn: () => fetchOrganization(orgId), retry: false });
  if (org.isLoading) return <Loading />;
  const initial = org.data !== undefined ? payerFromOrg(org.data.organization) : EMPTY_PAYER;
  return <PayerFormBody orgId={orgId} initial={initial} />;
}

function PayerFormBody({ orgId, initial }: { orgId: string; initial: PayerForm }) {
  const { t } = useI18n();
  const ids = useId();
  const queryClient = useQueryClient();
  const [form, setForm] = useState<PayerForm>(initial);
  const [problems, setProblems] = useState<PayerProblem[]>([]);
  const [saved, setSaved] = useState(false);

  const save = useMutation({
    mutationFn: (f: PayerForm) => putBillingPayer(orgId, payerRequest(f)),
    onSuccess: (res) => {
      setSaved(true);
      setForm({
        legal_name: res.payer.legal_name,
        address: res.payer.address ?? "",
        country: res.payer.country ?? "",
        tax_id: res.payer.tax_id ?? "",
        email: res.payer.email ?? "",
      });
      void queryClient.invalidateQueries({ queryKey: ["billingAccountInvoices", orgId] });
    },
  });

  const set = (field: PayerField, value: string) => {
    setForm((f) => ({ ...f, [field]: value }));
    setSaved(false);
    setProblems((p) => p.filter((x) => x.field !== field));
  };
  const errorOf = (field: PayerField) => {
    const p = problems.find((x) => x.field === field);
    return p === undefined ? undefined : t(`billing.payer.problem.${p.code}`);
  };

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    save.reset();
    setSaved(false);
    const found = validatePayer(form);
    setProblems(found);
    if (found.length === 0) save.mutate(form);
  };

  return (
    <section aria-labelledby={`${ids}-title`} data-testid="billing-payer">
      <h2 id={`${ids}-title`}>{t("billing.payer.title")}</h2>
      <p className="muted">{t("billing.payer.intro")}</p>
      <form onSubmit={onSubmit} noValidate className="org-form">
        <TextField label={t("billing.payer.legalName")} value={form.legal_name} onChange={(v) => set("legal_name", v)} error={errorOf("legal_name")} testId="billing-payer-name" maxLength={200} />
        <TextField label={t("billing.payer.address")} value={form.address} onChange={(v) => set("address", v)} error={errorOf("address")} testId="billing-payer-address" maxLength={500} />
        <TextField label={t("billing.payer.country")} hint={t("billing.payer.countryHint")} value={form.country} onChange={(v) => set("country", v)} error={errorOf("country")} testId="billing-payer-country" maxLength={2} />
        <TextField label={t("billing.payer.taxId")} value={form.tax_id} onChange={(v) => set("tax_id", v)} error={errorOf("tax_id")} testId="billing-payer-tax" maxLength={50} />
        <TextField label={t("billing.payer.email")} type="email" value={form.email} onChange={(v) => set("email", v)} error={errorOf("email")} testId="billing-payer-email" maxLength={254} />
        <p>
          <button type="submit" className="btn btn-primary" disabled={save.isPending} data-testid="billing-payer-save">
            {save.isPending ? t("billing.payer.saving") : t("billing.payer.save")}
          </button>
        </p>
      </form>
      {save.error !== null && <ErrorNotice error={save.error} testId="billing-payer-error" />}
      {saved && (
        <p className="notice notice-ok" role="status" data-testid="billing-payer-saved">
          {t("billing.payer.saved")}
        </p>
      )}
    </section>
  );
}
