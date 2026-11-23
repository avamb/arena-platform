/**
 * The tours of an organization (spec 63, L-2). A list with an "archived" filter and a form that adds one. A tour is
 * an "event series" in the API. Permissions come from the server per organization (`access.can`): event.read shows
 * the list, event.update the buttons.
 */
import { useId, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createSeries, listSeries } from "@/lib/api/endpoints";
import type { Series } from "@/lib/api/types";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { ScreenHelp } from "@/components/ScreenHelp";
import { filterSeries, seriesErrorKey, SERIES_NAME_MAX, validSeriesSlug } from "@/lib/series";
import { slugFromInput } from "@/lib/promoters";
import { Badge, Empty, ErrorBox, Loading } from "@/components/ui";
import { ErrorNotice } from "@/components/ErrorNotice";
import { useOrg } from "@/screens/OrgLayout";

export function useSeriesList(orgId: string, enabled: boolean) {
  return useQuery({ queryKey: ["series", orgId], queryFn: () => listSeries(orgId), enabled });
}

function CreateForm({ orgId, onDone, onCancel }: { orgId: string; onDone: (s: Series) => void; onCancel: () => void }) {
  const { t } = useI18n();
  const ids = useId();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [errors, setErrors] = useState<{ name?: MessageKey; slug?: MessageKey }>({});
  const create = useMutation({
    mutationFn: (body: { name: string; slug?: string }) => createSeries(orgId, body),
    onSuccess: (res) => {
      void queryClient.invalidateQueries({ queryKey: ["series", orgId] });
      onDone(res);
    },
  });

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (create.isPending) return;
    const next: { name?: MessageKey; slug?: MessageKey } = {};
    const cleanName = name.trim().split(/\s+/).filter(Boolean).join(" ");
    if (cleanName === "") next.name = "series.err.nameEmpty";
    else if ([...cleanName].length > SERIES_NAME_MAX) next.name = "series.err.nameLong";
    const cleanSlug = slugFromInput(slug);
    if (cleanSlug !== "" && !validSeriesSlug(cleanSlug)) next.slug = "series.err.slugInvalid";
    setErrors(next);
    if (next.name !== undefined || next.slug !== undefined) return;
    create.mutate(cleanSlug === "" ? { name: cleanName } : { name: cleanName, slug: cleanSlug });
  };

  const apiKey = create.error !== null ? seriesErrorKey(create.error) : null;
  const fields = [
    { id: "name", label: t("series.field.name"), hint: t("series.hint.name"), value: name, set: setName, err: errors.name },
    { id: "slug", label: t("series.field.slug"), hint: t("series.hint.slugOptional"), value: slug, set: setSlug, err: errors.slug },
  ] as const;

  return (
    <form className="card promoter-form" onSubmit={onSubmit} noValidate aria-labelledby={`${ids}-title`} data-testid="series-create">
      <h2 id={`${ids}-title`}>{t("series.create.title")}</h2>
      <p className="muted">{t("series.create.hint")}</p>
      {fields.map((f) => (
        <div key={f.id}>
          <label htmlFor={`${ids}-${f.id}`}>{f.label}</label>
          <input
            id={`${ids}-${f.id}`}
            type="text"
            autoComplete="off"
            value={f.value}
            onChange={(e) => f.set(e.target.value)}
            aria-invalid={f.err !== undefined}
            aria-describedby={f.err !== undefined ? `${ids}-${f.id}-hint ${ids}-${f.id}-err` : `${ids}-${f.id}-hint`}
            data-testid={`series-create-${f.id}`}
          />
          <p id={`${ids}-${f.id}-hint`} className="muted field-hint">
            {f.hint}
          </p>
          {f.err !== undefined && (
            <p id={`${ids}-${f.id}-err`} className="bad-text" role="alert" data-testid={`series-create-${f.id}-error`}>
              {t(f.err)}
            </p>
          )}
        </div>
      ))}
      {create.error !== null &&
        (apiKey !== null ? (
          <p className="bad-text" role="alert" data-testid="series-create-error">
            {t(apiKey)}
          </p>
        ) : (
          <ErrorNotice error={create.error} testId="series-create-error" />
        ))}
      <div className="dialog-actions">
        <button type="submit" className="btn btn-primary" disabled={create.isPending} data-testid="series-create-submit">
          {create.isPending ? t("series.create.working") : t("series.create.submit")}
        </button>
        <button type="button" className="btn" disabled={create.isPending} onClick={onCancel} data-testid="series-create-cancel">
          {t("series.create.cancel")}
        </button>
      </div>
    </form>
  );
}

export function SeriesRow({ orgId, series }: { orgId: string; series: Series }) {
  const { t } = useI18n();
  return (
    <li className="card" data-testid="series-row">
      <Link to="/o/$orgId/series/$seriesId" params={{ orgId, seriesId: series.id }} className="card-link">
        <span className="card-title">{series.name}</span>
        <span className="card-meta">
          {series.archived_at !== null && <Badge tone="neutral">{t("series.archivedBadge")}</Badge>}
          <span className="muted" data-testid="series-slug">
            {t("series.page", { slug: series.slug })}
          </span>
          <span data-testid="series-count">{t("series.eventCount", { count: series.event_count })}</span>
        </span>
      </Link>
    </li>
  );
}

export function SeriesScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const search = useSearch({ from: "/o/$orgId/series" });
  const navigate = useNavigate({ from: "/o/$orgId/series" });
  const archived = search.f === "archived";
  const canRead = access.can("event.read");
  const canManage = access.can("event.update");
  const list = useSeriesList(orgId, canRead);
  const [creating, setCreating] = useState(false);

  if (!canRead) {
    return (
      <section aria-labelledby="series-title">
        <h1 id="series-title">{t("series.title")}</h1>
        <p className="notice notice-bad" role="status" data-testid="series-no-access">
          {t("series.noAccess")}
        </p>
      </section>
    );
  }

  const all = list.data?.items ?? [];
  const counts = { active: filterSeries(all, false).length, archived: filterSeries(all, true).length };
  const visible = filterSeries(all, archived);

  return (
    <section aria-labelledby="series-title">
      <div className="screen-head">
        <h1 id="series-title">{t("series.title")}</h1>
        <ScreenHelp screenId="series" />
      </div>
      <p className="muted">{t("series.intro")}</p>
      {!canManage && (
        <p className="notice" role="status" data-testid="series-read-only">
          {t("series.readOnly")}
        </p>
      )}
      <div className="tabs" role="group" aria-label={t("series.title")}>
        {(["active", "archived"] as const).map((f) => (
          <button
            key={f}
            type="button"
            className={(f === "archived") === archived ? "tab active" : "tab"}
            aria-pressed={(f === "archived") === archived}
            onClick={() => void navigate({ search: f === "active" ? {} : { f }, replace: true })}
            data-testid={`series-tab-${f}`}
          >
            {t(f === "active" ? "series.tab.active" : "series.tab.archived", { count: counts[f] })}
          </button>
        ))}
      </div>
      {canManage && !creating && (
        <p>
          <button type="button" className="btn btn-primary" onClick={() => setCreating(true)} data-testid="series-new">
            {t("series.new")}
          </button>
        </p>
      )}
      {canManage && creating && (
        <CreateForm
          orgId={orgId}
          onCancel={() => setCreating(false)}
          onDone={(s) => {
            setCreating(false);
            void navigate({ to: "/o/$orgId/series/$seriesId", params: { orgId, seriesId: s.id }, });
          }}
        />
      )}
      {list.isLoading && <Loading />}
      {list.error !== null && <ErrorBox error={list.error} onRetry={() => void list.refetch()} />}
      {list.data !== undefined && visible.length === 0 && <Empty>{t(archived ? "series.empty.archived" : "series.empty.active")}</Empty>}
      <ul className="cards" data-testid="series-list">
        {visible.map((s) => (
          <SeriesRow key={s.id} orgId={orgId} series={s} />
        ))}
      </ul>
    </section>
  );
}
