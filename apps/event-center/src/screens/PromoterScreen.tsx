/**
 * One promoter (task 4-6, the web twin of the bot's promoter card). One field
 * per form section: a person changes one thing at a time, and the API's
 * refusals are shown in words. A changed page address asks "old -> new" first,
 * because links already handed out with the old address stop working. Archive
 * says what stops opening and what stays. The public page link has a copy
 * button.
 */
import { useId, useState, type FormEvent } from "react";
import { Link, useParams } from "@tanstack/react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { updatePromoter, type PromoterPatch } from "@/lib/api/endpoints";
import type { Promoter } from "@/lib/api/types";
import { useI18n, type MessageKey } from "@/lib/i18n";
import {
  PROMOTER_FIELDS,
  canClear,
  fieldValue,
  parseField,
  promoterErrorKey,
  slugChangeNeedsConfirm,
  type PromoterField,
} from "@/lib/promoters";
import { Badge, ErrorBox, Loading } from "@/components/ui";
import { ErrorNotice } from "@/components/ErrorNotice";
import { useOrg } from "@/screens/OrgLayout";
import { usePromoters } from "@/screens/PromotersScreen";

/** Saves a change and keeps the list cache in step with the answer. */
function usePromoterUpdate(orgId: string, promoterId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (patch: PromoterPatch) => updatePromoter(orgId, promoterId, patch),
    onSuccess: (res) => {
      queryClient.setQueryData<{ promoters: Promoter[] }>(["promoters", orgId], (old) =>
        old === undefined ? old : { ...old, promoters: old.promoters.map((p) => (p.id === res.promoter.id ? res.promoter : p)) },
      );
      void queryClient.invalidateQueries({ queryKey: ["promoters", orgId] });
    },
  });
}

type Update = ReturnType<typeof usePromoterUpdate>;

function ApiProblem({ error, testId }: { error: unknown; testId: string }) {
  const { t } = useI18n();
  const key = promoterErrorKey(error);
  if (key !== null) {
    return (
      <p className="bad-text" role="alert" data-testid={testId}>
        {t(key)}
      </p>
    );
  }
  return <ErrorNotice error={error} testId={testId} />;
}

function FieldSection({
  promoter,
  field,
  canEdit,
  editing,
  update,
  onEdit,
  onSaved,
}: {
  promoter: Promoter;
  field: PromoterField;
  canEdit: boolean;
  editing: boolean;
  update: Update;
  onEdit: (field: PromoterField | null) => void;
  onSaved: (message: string) => void;
}) {
  const { t } = useI18n();
  const ids = useId();
  const current = fieldValue(promoter, field);
  const [draft, setDraft] = useState(current);
  const [shapeError, setShapeError] = useState<MessageKey | null>(null);
  const [pendingSlug, setPendingSlug] = useState<string | null>(null);
  const label = t(`promoters.field.${field}` as MessageKey);

  const open = () => {
    setDraft(current);
    setShapeError(null);
    setPendingSlug(null);
    update.reset();
    onEdit(field);
  };
  const close = () => {
    setPendingSlug(null);
    setShapeError(null);
    update.reset();
    onEdit(null);
  };

  const save = (value: string | null) => {
    const patch: PromoterPatch = { [field]: value };
    update.mutate(patch, {
      onSuccess: (res) => {
        onEdit(null);
        setPendingSlug(null);
        if (value === null) onSaved(t("promoters.cleared"));
        else if (field === "slug" && res.promoter.public_url !== "") onSaved(t("promoters.savedPage", { url: res.promoter.public_url }));
        else onSaved(t("promoters.saved"));
      },
    });
  };

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (update.isPending) return;
    const parsed = parseField(field, draft);
    if (!parsed.ok) {
      setShapeError(parsed.reason);
      return;
    }
    setShapeError(null);
    // A working page address is replaced only after the person has seen old and new.
    if (field === "slug" && slugChangeNeedsConfirm(promoter.slug, parsed.value)) {
      update.reset();
      setPendingSlug(parsed.value);
      return;
    }
    save(field === "website" ? draft.trim() : parsed.value);
  };

  return (
    <section className="field-section" aria-labelledby={`${ids}-label`} data-testid={`field-${field}`}>
      <h2 id={`${ids}-label`} className="field-label">
        {label}
      </h2>
      {!editing && (
        <>
          <p className="field-value" data-testid={`field-value-${field}`}>
            {current !== "" ? current : <span className="muted">{t("promoters.none")}</span>}
          </p>
          {canEdit && (
            <p>
              <button type="button" className="btn" onClick={open} data-testid={`edit-${field}`}>
                {t("promoters.edit")}
                <span className="visually-hidden">: {label}</span>
              </button>
            </p>
          )}
        </>
      )}
      {editing && pendingSlug === null && (
        <form onSubmit={onSubmit} noValidate>
          <label htmlFor={`${ids}-input`}>{label}</label>
          <input
            id={`${ids}-input`}
            type={field === "phone" ? "tel" : "text"}
            autoComplete="off"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            aria-invalid={shapeError !== null || update.error !== null}
            aria-describedby={`${ids}-hint`}
            data-testid={`input-${field}`}
          />
          <p id={`${ids}-hint`} className="muted field-hint">
            {t(`promoters.hint.${field}` as MessageKey)}
          </p>
          {shapeError !== null && (
            <p className="bad-text" role="alert" data-testid="field-error">
              {t(shapeError)}
            </p>
          )}
          {update.error !== null && <ApiProblem error={update.error} testId="field-error" />}
          <div className="dialog-actions">
            <button type="submit" className="btn btn-primary" disabled={update.isPending} data-testid="field-save">
              {update.isPending ? t("promoters.saving") : t("promoters.save")}
            </button>
            {canClear(field) && current !== "" && (
              <button type="button" className="btn" disabled={update.isPending} onClick={() => save(null)} data-testid="field-clear">
                {t("promoters.clear")}
              </button>
            )}
            <button type="button" className="btn" disabled={update.isPending} onClick={close} data-testid="field-cancel">
              {t("promoters.cancel")}
            </button>
          </div>
        </form>
      )}
      {editing && pendingSlug !== null && (
        <div className="notice" role="group" aria-label={t("promoters.slug.confirm", { old: current, new: pendingSlug })} data-testid="slug-confirm">
          <p>{t("promoters.slug.confirm", { old: current, new: pendingSlug })}</p>
          <dl className="facts-grid">
            <dt>{t("promoters.slug.old")}</dt>
            <dd className="mono" data-testid="slug-confirm-old">
              {current}
            </dd>
            <dt>{t("promoters.slug.new")}</dt>
            <dd className="mono" data-testid="slug-confirm-new">
              {pendingSlug}
            </dd>
          </dl>
          <p>{t("promoters.slug.warning")}</p>
          {update.error !== null && <ApiProblem error={update.error} testId="field-error" />}
          <div className="dialog-actions">
            <button type="button" className="btn btn-primary" disabled={update.isPending} onClick={() => save(pendingSlug)} data-testid="slug-confirm-save">
              {update.isPending ? t("promoters.saving") : t("promoters.slug.save")}
            </button>
            <button type="button" className="btn" disabled={update.isPending} onClick={close} data-testid="slug-confirm-back">
              {t("promoters.slug.back")}
            </button>
          </div>
        </div>
      )}
    </section>
  );
}

function PageLink({ promoter }: { promoter: Promoter }) {
  const { t } = useI18n();
  const [note, setNote] = useState<{ text: string; bad: boolean } | null>(null);
  const url = promoter.public_url;

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(url);
      setNote({ text: t("promoters.page.copied"), bad: false });
    } catch {
      setNote({ text: t("promoters.page.copyFailed"), bad: true });
    }
  };

  return (
    <section aria-labelledby="page-title" data-testid="promoter-page">
      <h2 id="page-title">{t("promoters.page.title")}</h2>
      {url === "" ? (
        <p className="muted" data-testid="page-none">
          {promoter.slug === null ? t("promoters.noPage") : t("promoters.page.noBase")}
        </p>
      ) : (
        <>
          <p className="mono page-url" data-testid="page-url">
            {url}
          </p>
          {promoter.archived && (
            <p className="notice" role="status" data-testid="page-archived">
              {t("promoters.page.archived")}
            </p>
          )}
          <div className="actions">
            <button type="button" className="btn" onClick={() => void copy()} data-testid="page-copy">
              {t("promoters.page.copy")}
            </button>
            {!promoter.archived && (
              <a className="btn" href={url} target="_blank" rel="noopener noreferrer" data-testid="page-open">
                {t("promoters.page.open")}
              </a>
            )}
          </div>
          <p className={note?.bad === true ? "bad-text" : "muted"} role={note?.bad === true ? "alert" : "status"} aria-live="polite" data-testid="page-copy-note">
            {note?.text ?? ""}
          </p>
        </>
      )}
    </section>
  );
}

function ArchiveSection({ promoter, update, onDone }: { promoter: Promoter; update: Update; onDone: (message: string) => void }) {
  const { t } = useI18n();
  const [asking, setAsking] = useState(false);

  if (promoter.archived) {
    return (
      <section aria-labelledby="archive-title">
        <h2 id="archive-title">{t("promoters.archivedBadge")}</h2>
        {update.error !== null && <ApiProblem error={update.error} testId="archive-error" />}
        <p>
          <button
            type="button"
            className="btn btn-primary"
            disabled={update.isPending}
            onClick={() => update.mutate({ archived: false }, { onSuccess: () => onDone(t("promoters.restore.done", { name: promoter.name })) })}
            data-testid="restore"
          >
            {t("promoters.restore.btn")}
          </button>
        </p>
      </section>
    );
  }
  return (
    <section aria-labelledby="archive-title">
      <h2 id="archive-title">{t("promoters.archive.btn")}</h2>
      {!asking ? (
        <p>
          <button type="button" className="btn" onClick={() => { update.reset(); setAsking(true); }} data-testid="archive-open">
            {t("promoters.archive.btn")}
          </button>
        </p>
      ) : (
        <div className="notice" role="group" aria-label={t("promoters.archive.title", { name: promoter.name })} data-testid="archive-ask">
          <p>
            <strong>{t("promoters.archive.title", { name: promoter.name })}</strong>
          </p>
          <p>{t("promoters.archive.explain")}</p>
          {update.error !== null && <ApiProblem error={update.error} testId="archive-error" />}
          <div className="dialog-actions">
            <button
              type="button"
              className="btn btn-primary"
              disabled={update.isPending}
              onClick={() =>
                update.mutate(
                  { archived: true },
                  {
                    onSuccess: () => {
                      setAsking(false);
                      onDone(t("promoters.archive.done", { name: promoter.name }));
                    },
                  },
                )
              }
              data-testid="archive-confirm"
            >
              {t("promoters.archive.yes")}
            </button>
            <button type="button" className="btn" disabled={update.isPending} onClick={() => setAsking(false)} data-testid="archive-cancel">
              {t("promoters.archive.no")}
            </button>
          </div>
        </div>
      )}
    </section>
  );
}

export function PromoterScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const { promoterId } = useParams({ from: "/o/$orgId/promoters/$promoterId" });
  const canRead = access.can("promoter.read");
  const canManage = access.can("promoter.manage");
  const promoters = usePromoters(orgId, canRead);
  const update = usePromoterUpdate(orgId, promoterId);
  const [editing, setEditing] = useState<PromoterField | null>(null);
  const [status, setStatus] = useState<string | null>(null);

  const back = (
    <p>
      <Link to="/o/$orgId/promoters" params={{ orgId }} data-testid="back-to-promoters">
        {t("promoters.backToList")}
      </Link>
    </p>
  );

  if (!canRead) {
    return (
      <section>
        <h1>{t("promoters.title")}</h1>
        <p className="notice notice-bad" role="status" data-testid="promoters-no-access">
          {t("promoters.noAccess")}
        </p>
        {back}
      </section>
    );
  }
  if (promoters.isLoading) return <Loading />;
  if (promoters.error !== null) return <ErrorBox error={promoters.error} onRetry={() => void promoters.refetch()} />;
  const promoter = promoters.data?.promoters.find((p) => p.id === promoterId);
  if (promoter === undefined) {
    return (
      <section>
        {back}
        <p className="notice" role="status" data-testid="promoter-gone">
          {t("promoters.gone")}
        </p>
      </section>
    );
  }

  const say = (message: string) => setStatus(message);

  return (
    <section aria-labelledby="promoter-title">
      {back}
      <h1 id="promoter-title" data-testid="promoter-name">
        {promoter.name}
      </h1>
      {promoter.archived && <Badge tone="neutral">{t("promoters.archivedBadge")}</Badge>}
      {!canManage && (
        <p className="notice" role="status" data-testid="promoters-read-only">
          {t("promoters.readOnly")}
        </p>
      )}
      <p className="notice notice-ok" role="status" aria-live="polite" data-testid="promoter-status" hidden={status === null}>
        {status ?? ""}
      </p>
      <PageLink promoter={promoter} />
      {PROMOTER_FIELDS.map((field) => (
        <FieldSection
          key={`${promoter.id}-${field}-${fieldValue(promoter, field)}`}
          promoter={promoter}
          field={field}
          canEdit={canManage && !promoter.archived}
          editing={editing === field}
          update={update}
          onEdit={setEditing}
          onSaved={say}
        />
      ))}
      {canManage && <ArchiveSection promoter={promoter} update={update} onDone={say} />}
    </section>
  );
}
