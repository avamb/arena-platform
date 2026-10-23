/**
 * "Where else to buy": other ticket sellers of the event (up to 3). Name and
 * link, edit, order, remove. The link is checked here (https only) and again
 * by the server; the public page opens it in a new tab with rel noopener and
 * says plainly that it leaves Arena.
 */
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ConfirmPressDialog } from "@/components/ConfirmPressDialog";
import { Empty, ErrorBox, Loading } from "@/components/ui";
import { URL_PROBLEM_KEY } from "@/components/materials/VideoEditor";
import { ActionNotice, MoveButtons, useAction } from "@/components/materials/shared";
import { addPartner, deletePartner, listPartners, orderPartners, patchPartner, type PartnerItem } from "@/lib/api/materialsApi";
import { useI18n } from "@/lib/i18n";
import { LIMITS, checkHttpsUrl, moved, type UrlProblem } from "@/lib/materials";

interface Draft {
  id: string | null;
  seller: string;
  url: string;
}

export function PartnerEditor({ orgId, eventId, canWrite }: { orgId: string; eventId: string; canWrite: boolean }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const queryKey = ["materials", "partners", orgId, eventId] as const;
  const list = useQuery({ queryKey, queryFn: ({ signal }) => listPartners(orgId, eventId, signal) });
  const action = useAction();
  const [draft, setDraft] = useState<Draft | null>(null);
  const [problem, setProblem] = useState<{ field: "url"; value: UrlProblem } | { field: "seller" } | null>(null);
  const [removing, setRemoving] = useState<PartnerItem | null>(null);
  const items = list.data?.items ?? [];
  const max = LIMITS.partners;
  const full = items.length >= max;
  const reload = () => queryClient.invalidateQueries({ queryKey });

  const save = async () => {
    if (draft === null) return;
    const seller = draft.seller.trim();
    if (seller === "") {
      setProblem({ field: "seller" });
      return;
    }
    const checked = checkHttpsUrl(draft.url);
    if (!checked.ok) {
      setProblem({ field: "url", value: checked.problem });
      return;
    }
    setProblem(null);
    const done = await action.run(async () => {
      try {
        if (draft.id === null) await addPartner(orgId, eventId, checked.url, seller);
        else await patchPartner(orgId, eventId, draft.id, checked.url, seller);
      } finally {
        await reload();
      }
    });
    if (done) setDraft(null);
  };

  return (
    <section className="mat-block" aria-labelledby="mat-partners-title" data-testid="mat-partners">
      <h3 id="mat-partners-title">
        {t("mat.partner.title")} <span className="field-hint">({t("mat.count", { count: items.length, max })})</span>
      </h3>
      <p className="field-hint">{t("mat.partner.hint", { max })}</p>
      {list.isLoading && <Loading />}
      {list.error !== null && <ErrorBox error={list.error} onRetry={() => void list.refetch()} />}
      {list.data !== undefined && items.length === 0 && <Empty>{t("mat.empty")}</Empty>}
      <ul className="cards">
        {items.map((p, index) => (
          <li key={`${p.id}:${p.url}:${p.seller_name}`} className="card mat-row" data-testid="mat-partner-item">
            <p className="mat-seller">
              <strong>{p.seller_name}</strong>
            </p>
            <p className="mat-url">
              <a href={p.url} target="_blank" rel="noopener noreferrer nofollow">
                {p.url}
              </a>
            </p>
            {canWrite && (
              <div className="actions">
                <MoveButtons
                  index={index}
                  count={items.length}
                  disabled={action.busy}
                  onMove={(d) =>
                    void action.run(async () => {
                      queryClient.setQueryData(queryKey, await orderPartners(orgId, eventId, moved(items, index, d)));
                    })
                  }
                  testId={`mat-partner-${index}`}
                />
                <button
                  type="button"
                  className="btn"
                  disabled={action.busy}
                  onClick={() => {
                    setProblem(null);
                    setDraft({ id: p.id, seller: p.seller_name, url: p.url });
                  }}
                  data-testid={`mat-partner-${index}-edit`}
                >
                  {t("mat.edit")}
                </button>
                <button type="button" className="btn" disabled={action.busy} onClick={() => setRemoving(p)} data-testid={`mat-partner-${index}-remove`}>
                  {t("mat.remove")}
                </button>
              </div>
            )}
          </li>
        ))}
      </ul>
      {canWrite && list.data !== undefined && draft === null && !full && (
        <div className="actions">
          <button
            type="button"
            className="btn btn-primary"
            onClick={() => {
              setProblem(null);
              setDraft({ id: null, seller: "", url: "" });
            }}
            data-testid="mat-partner-new"
          >
            {t("mat.partner.add")}
          </button>
        </div>
      )}
      {canWrite && full && draft === null && <p className="field-hint">{t("mat.limit", { max })}</p>}
      {canWrite && draft !== null && (
        <div className="mat-form" data-testid="mat-partner-form">
          <label className="field-label" htmlFor="mat-partner-seller">
            {t("mat.partner.seller")}
          </label>
          <input
            id="mat-partner-seller"
            className="input"
            type="text"
            value={draft.seller}
            maxLength={LIMITS.sellerName}
            onChange={(e) => setDraft({ ...draft, seller: e.target.value })}
            aria-invalid={problem?.field === "seller"}
            data-testid="mat-partner-seller"
          />
          <p className="field-hint">{t("mat.partner.sellerHint", { max: LIMITS.sellerName })}</p>
          <label className="field-label" htmlFor="mat-partner-url">
            {t("mat.partner.url")}
          </label>
          <input
            id="mat-partner-url"
            className="input"
            type="url"
            inputMode="url"
            value={draft.url}
            placeholder="https://"
            onChange={(e) => setDraft({ ...draft, url: e.target.value })}
            aria-invalid={problem?.field === "url"}
            data-testid="mat-partner-url"
          />
          <div className="actions">
            <button type="button" className="btn btn-primary" disabled={action.busy} onClick={() => void save()} data-testid="mat-partner-save">
              {t("mat.save")}
            </button>
            <button type="button" className="btn" disabled={action.busy} onClick={() => setDraft(null)}>
              {t("mat.cancel")}
            </button>
          </div>
        </div>
      )}
      {problem !== null && (
        <p className="field-error" role="alert" data-testid="mat-partner-problem">
          {problem.field === "seller" ? t("mat.partner.sellerEmpty") : t(URL_PROBLEM_KEY[problem.value], { max: LIMITS.url })}
        </p>
      )}
      <ActionNotice action={action} max={max} testId="mat-partners" />
      {removing !== null && (
        <ConfirmPressDialog
          title={t("mat.confirm.title")}
          subject={removing.seller_name}
          explain={t("mat.confirm.explain")}
          confirmLabel={t("mat.remove")}
          testId="mat-partner-confirm"
          onCancel={() => setRemoving(null)}
          onConfirm={async () => {
            await deletePartner(orgId, eventId, removing.id);
            await reload();
            setRemoving(null);
          }}
        />
      )}
    </section>
  );
}
