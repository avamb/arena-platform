/**
 * A new sales link (spec 43, QL-12): the bot's three steps on one form. The
 * target (preselected when the person came from an event or a date card),
 * the placement, the label, and the utm labels behind a fold: left empty
 * they stay the server's defaults, exactly as the bot never asks for them.
 */
import { useId, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createSalesLink } from "@/lib/api/endpoints";
import type { SalesLinkTarget } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import { PLACEMENTS, parseLabel, parseUtm, targetBody, targetComplete, type Placement, type UtmKey, UTM_KEYS } from "@/lib/salesLinks";
import { ErrorNotice } from "@/components/ErrorNotice";
import { SalesLinkTargetPicker, type TargetChoice } from "@/components/SalesLinkTargetPicker";
import { NoLinksAccess } from "@/screens/LinksNoAccess";
import { useOrg } from "@/screens/OrgLayout";

interface Errors {
  target?: MessageKey;
  label?: MessageKey;
  utm?: { key: UtmKey; error: MessageKey };
}

/** The utm fields of a form, all five, for the create and the edit. */
export function UtmFields({
  ids,
  values,
  onChange,
  hint,
  error,
  disabled,
}: {
  ids: string;
  values: Record<UtmKey, string>;
  onChange: (key: UtmKey, value: string) => void;
  hint: MessageKey;
  error?: { key: UtmKey; error: MessageKey };
  disabled?: boolean;
}) {
  const { t } = useI18n();
  return (
    <details className="link-utm" data-testid="link-utm">
      <summary>{t("links.f.utm")}</summary>
      <p id={`${ids}-utm-hint`} className="muted">
        {t(hint)}
      </p>
      {UTM_KEYS.map((key) => (
        <div key={key}>
          <label htmlFor={`${ids}-${key}`} className="mono">
            {key}
          </label>
          <input
            id={`${ids}-${key}`}
            type="text"
            autoComplete="off"
            spellCheck={false}
            maxLength={64}
            value={values[key]}
            onChange={(e) => onChange(key, e.target.value)}
            disabled={disabled}
            aria-describedby={`${ids}-utm-hint`}
            aria-invalid={error?.key === key}
            data-testid={`link-f-${key}`}
          />
        </div>
      ))}
      {error !== undefined && (
        <p className="field-error" role="alert" data-testid="link-utm-error">
          {t(error.error, { key: error.key })}
        </p>
      )}
    </details>
  );
}

function CreateForm() {
  const { t } = useI18n();
  const ids = useId();
  const { orgId, orgName, access } = useOrg();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const search = useSearch({ from: "/o/$orgId/links/new" });
  const initial: SalesLinkTarget | null =
    search.session !== undefined && search.event !== undefined
      ? { kind: "session", session_id: search.session, event_id: search.event }
      : search.event !== undefined
        ? { kind: "event", event_id: search.event }
        : null;
  const [choice, setChoice] = useState<TargetChoice>({ target: null, text: "" });
  const [placement, setPlacement] = useState<Placement>("poster");
  const [label, setLabel] = useState("");
  const [utm, setUtm] = useState<Record<UtmKey, string>>({ utm_source: "", utm_medium: "", utm_campaign: "", utm_content: "", utm_term: "" });
  const [errors, setErrors] = useState<Errors>({});

  const create = useMutation({
    mutationFn: (body: Parameters<typeof createSalesLink>[1]) => createSalesLink(orgId, body),
    onSuccess: async (link) => {
      await queryClient.invalidateQueries({ queryKey: ["salesLinks", orgId] });
      queryClient.setQueryData(["salesLink", orgId, link.id], link);
      await navigate({ to: "/o/$orgId/links/$linkId", params: { orgId, linkId: link.id }, search: { created: true } });
    },
  });

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    const next: Errors = {};
    if (!targetComplete(choice.target)) next.target = "links.err.target";
    const lab = parseLabel(label);
    if (!lab.ok) next.label = lab.error;
    const labels = parseUtm(utm);
    if (!labels.ok) next.utm = { key: labels.key, error: labels.error };
    setErrors(next);
    if (next.target !== undefined || !lab.ok || !labels.ok || !targetComplete(choice.target)) return;
    create.reset();
    create.mutate({
      target: targetBody(choice.target),
      label: lab.value,
      placement_kind: placement,
      ...(Object.keys(labels.value).length > 0 ? { utm: labels.value } : {}),
    });
  };

  return (
    <section aria-labelledby="link-create-title">
      <p>
        <Link to="/o/$orgId/links" params={{ orgId }} search={{}}>
          {t("links.back")}
        </Link>
      </p>
      <h1 id="link-create-title">{t("links.create.title")}</h1>
      {choice.text !== "" && (
        <p className="muted" data-testid="link-target-text">
          {choice.text}
        </p>
      )}
      <form onSubmit={onSubmit} noValidate data-testid="link-form">
        <SalesLinkTargetPicker
          orgId={orgId}
          orgName={orgName}
          initial={initial}
          onChange={setChoice}
          canSales={access.can("order.read")}
          disabled={create.isPending}
        />
        {errors.target !== undefined && (
          <p className="field-error" role="alert" data-testid="link-target-error">
            {t(errors.target)}
          </p>
        )}

        <fieldset>
          <legend>{t("links.f.placement")}</legend>
          {PLACEMENTS.map((p) => (
            <label key={p} className="check">
              <input type="radio" name={`${ids}-placement`} checked={placement === p} onChange={() => setPlacement(p)} disabled={create.isPending} data-testid={`link-pl-${p}`} />
              <span>{t(`links.pl.${p}`)}</span>
            </label>
          ))}
        </fieldset>

        <label htmlFor={`${ids}-label`}>{t("links.f.label")}</label>
        <input
          id={`${ids}-label`}
          type="text"
          autoComplete="off"
          maxLength={160}
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          disabled={create.isPending}
          aria-describedby={`${ids}-label-hint`}
          aria-invalid={errors.label !== undefined}
          data-testid="link-f-label"
        />
        <p id={`${ids}-label-hint`} className="muted">
          {t("links.f.labelHint")}
        </p>
        {errors.label !== undefined && (
          <p className="field-error" role="alert" data-testid="link-label-error">
            {t(errors.label)}
          </p>
        )}

        <UtmFields ids={ids} values={utm} onChange={(k, v) => setUtm((u) => ({ ...u, [k]: v }))} hint="links.f.utmHint" error={errors.utm} disabled={create.isPending} />

        {create.error !== null && <ErrorNotice error={create.error} testId="link-create-error" />}
        <div className="actions">
          <button type="submit" className="btn btn-primary" disabled={create.isPending} data-testid="link-create-go">
            {create.isPending ? t("links.creating") : t("links.create.go")}
          </button>
        </div>
      </form>
    </section>
  );
}

export function LinkCreateScreen() {
  const { access } = useOrg();
  if (!access.can("sales_link.manage")) return <NoLinksAccess create />;
  return <CreateForm />;
}
