/**
 * The promoters of an organization (task 4-6, the web twin of the bot's
 * promoter screens, EC-14). A list with an "archived" filter, and a form
 * that adds one. Permissions come from the server per organization
 * (`access.can`): promoter.read shows the list, promoter.manage the buttons.
 */
import { useId, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createPromoter, listPromoters, type PromoterCreateBody } from "@/lib/api/endpoints";
import type { Promoter } from "@/lib/api/types";
import { useI18n, type MessageKey } from "@/lib/i18n";
import {
  filterPromoters,
  parseField,
  promoterErrorKey,
  type PromoterField,
  type PromoterFilter,
} from "@/lib/promoters";
import { Badge, Empty, ErrorBox, Loading } from "@/components/ui";
import { ErrorNotice } from "@/components/ErrorNotice";
import { useOrg } from "@/screens/OrgLayout";

export function usePromoters(orgId: string, enabled: boolean) {
  return useQuery({ queryKey: ["promoters", orgId], queryFn: () => listPromoters(orgId), enabled });
}

const CREATE_FIELDS: readonly PromoterField[] = ["name", "address", "legal_id", "phone", "email", "website"];

type FieldErrors = Partial<Record<PromoterField, MessageKey>>;

function CreateForm({ orgId, onDone, onCancel }: { orgId: string; onDone: (p: Promoter) => void; onCancel: () => void }) {
  const { t } = useI18n();
  const ids = useId();
  const queryClient = useQueryClient();
  const [values, setValues] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<FieldErrors>({});
  const create = useMutation({
    mutationFn: (body: PromoterCreateBody) => createPromoter(orgId, body),
    onSuccess: (res) => {
      void queryClient.invalidateQueries({ queryKey: ["promoters", orgId] });
      onDone(res.promoter);
    },
  });

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (create.isPending) return;
    const next: FieldErrors = {};
    const body: Record<string, string> = {};
    for (const field of CREATE_FIELDS) {
      const raw = values[field] ?? "";
      if (field !== "name" && raw.trim() === "") continue;
      const parsed = parseField(field, raw);
      if (!parsed.ok) next[field] = parsed.reason;
      else body[field] = field === "website" ? raw.trim() : parsed.value;
    }
    setErrors(next);
    if (Object.keys(next).length > 0) return;
    create.mutate(body as unknown as PromoterCreateBody);
  };

  const apiKey = create.error !== null ? promoterErrorKey(create.error) : null;

  return (
    <form className="card promoter-form" onSubmit={onSubmit} noValidate aria-labelledby={`${ids}-title`} data-testid="promoter-create">
      <h2 id={`${ids}-title`}>{t("promoters.create.title")}</h2>
      <p className="muted">{t("promoters.create.hint")}</p>
      {CREATE_FIELDS.map((field) => {
        const err = errors[field];
        const hintId = `${ids}-${field}-hint`;
        return (
          <div key={field}>
            <label htmlFor={`${ids}-${field}`}>{t(`promoters.field.${field}` as MessageKey)}</label>
            <input
              id={`${ids}-${field}`}
              type={field === "phone" ? "tel" : "text"}
              autoComplete="off"
              value={values[field] ?? ""}
              onChange={(e) => setValues((v) => ({ ...v, [field]: e.target.value }))}
              aria-invalid={err !== undefined}
              aria-describedby={err !== undefined ? `${hintId} ${ids}-${field}-err` : hintId}
              data-testid={`create-${field}`}
            />
            <p id={hintId} className="muted field-hint">
              {t(`promoters.hint.${field}` as MessageKey)}
            </p>
            {err !== undefined && (
              <p id={`${ids}-${field}-err`} className="bad-text" role="alert" data-testid={`create-${field}-error`}>
                {t(err)}
              </p>
            )}
          </div>
        );
      })}
      {create.error !== null &&
        (apiKey !== null ? (
          <p className="bad-text" role="alert" data-testid="create-error">
            {t(apiKey)}
          </p>
        ) : (
          <ErrorNotice error={create.error} testId="create-error" />
        ))}
      <div className="dialog-actions">
        <button type="submit" className="btn btn-primary" disabled={create.isPending} data-testid="create-submit">
          {create.isPending ? t("promoters.create.working") : t("promoters.create.submit")}
        </button>
        <button type="button" className="btn" disabled={create.isPending} onClick={onCancel} data-testid="create-cancel">
          {t("promoters.create.cancel")}
        </button>
      </div>
    </form>
  );
}

export function PromoterRow({ orgId, promoter }: { orgId: string; promoter: Promoter }) {
  const { t } = useI18n();
  return (
    <li className="card" data-testid="promoter-row">
      <Link to="/o/$orgId/promoters/$promoterId" params={{ orgId, promoterId: promoter.id }} className="card-link">
        <span className="card-title">{promoter.name}</span>
        <span className="card-meta">
          {promoter.archived && <Badge tone="neutral">{t("promoters.archivedBadge")}</Badge>}
          <span className="muted" data-testid="promoter-slug">
            {promoter.slug !== null ? t("promoters.page", { slug: promoter.slug }) : t("promoters.noPage")}
          </span>
        </span>
      </Link>
    </li>
  );
}

export function PromotersScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const search = useSearch({ from: "/o/$orgId/promoters" });
  const navigate = useNavigate({ from: "/o/$orgId/promoters" });
  const filter: PromoterFilter = search.f ?? "active";
  const canRead = access.can("promoter.read");
  const canManage = access.can("promoter.manage");
  const promoters = usePromoters(orgId, canRead);
  const [creating, setCreating] = useState(false);
  const [added, setAdded] = useState<string | null>(null);

  if (!canRead) {
    return (
      <section aria-labelledby="promoters-title">
        <h1 id="promoters-title">{t("promoters.title")}</h1>
        <p className="notice notice-bad" role="status" data-testid="promoters-no-access">
          {t("promoters.noAccess")}
        </p>
      </section>
    );
  }

  const all = promoters.data?.promoters ?? [];
  const counts = { active: filterPromoters(all, "active").length, archived: filterPromoters(all, "archived").length };
  const visible = filterPromoters(all, filter);

  return (
    <section aria-labelledby="promoters-title">
      <h1 id="promoters-title">{t("promoters.title")}</h1>
      <p className="muted">{t("promoters.intro")}</p>
      {!canManage && (
        <p className="notice" role="status" data-testid="promoters-read-only">
          {t("promoters.readOnly")}
        </p>
      )}
      <div className="tabs" role="group" aria-label={t("promoters.title")}>
        {(["active", "archived"] as const).map((f) => (
          <button
            key={f}
            type="button"
            className={filter === f ? "tab active" : "tab"}
            aria-pressed={filter === f}
            onClick={() => void navigate({ search: f === "active" ? {} : { f }, replace: true })}
            data-testid={`promoters-tab-${f}`}
          >
            {t(f === "active" ? "promoters.tab.active" : "promoters.tab.archived", { count: counts[f] })}
          </button>
        ))}
      </div>
      {canManage && !creating && (
        <p>
          <button
            type="button"
            className="btn btn-primary"
            onClick={() => {
              setCreating(true);
              setAdded(null);
            }}
            data-testid="promoter-new"
          >
            {t("promoters.new")}
          </button>
        </p>
      )}
      {canManage && creating && (
        <CreateForm
          orgId={orgId}
          onCancel={() => setCreating(false)}
          onDone={(p) => {
            setCreating(false);
            setAdded(p.name);
          }}
        />
      )}
      {added !== null && (
        <p className="notice notice-ok" role="status" data-testid="promoter-added">
          {t("promoters.create.done", { name: added })}
        </p>
      )}
      {promoters.isLoading && <Loading />}
      {promoters.error !== null && <ErrorBox error={promoters.error} onRetry={() => void promoters.refetch()} />}
      {promoters.data !== undefined && visible.length === 0 && (
        <Empty>{t(filter === "active" ? "promoters.empty.active" : "promoters.empty.archived")}</Empty>
      )}
      <ul className="cards" data-testid="promoter-list">
        {visible.map((p) => (
          <PromoterRow key={p.id} orgId={orgId} promoter={p} />
        ))}
      </ul>
    </section>
  );
}
