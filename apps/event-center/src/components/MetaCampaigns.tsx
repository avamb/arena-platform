/**
 * "Advertising → Meta", section "Campaigns" (spec 66): the campaigns of the selected ad account with
 * "Pause" (ACTIVE) and "Resume" (PAUSED). Every change asks first (ConfirmPressDialog) and only then
 * posts the status; nothing else of the campaign changes. A broken connection (409
 * meta.reconnect_required) refreshes the connection query, so the screen shows "Needs attention".
 */
import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ConfirmPressDialog } from "@/components/ConfirmPressDialog";
import { ErrorNotice } from "@/components/ErrorNotice";
import { Loading } from "@/components/ui";
import { ApiError } from "@/lib/api/http";
import {
  fetchMetaCampaigns,
  setMetaCampaignStatus,
  type MetaCampaign,
  type MetaCampaignList,
  type MetaCampaignSetStatus,
} from "@/lib/api/metaCampaignsApi";
import type { MessageKey } from "@/lib/i18n/en";
import { useI18n } from "@/lib/i18n";

/** Meta error codes with a text of their own; other failures use the general error text. */
const ERROR_KEY: Record<string, MessageKey> = {
  "meta.reconnect_required": "meta.campaigns.error.reconnect",
  "meta.no_ad_account": "meta.campaigns.error.noAdAccount",
  "meta.campaign_not_found": "meta.campaigns.error.notFound",
  "meta.campaign_not_changeable": "meta.campaigns.error.notChangeable",
  "meta.permission_denied": "meta.campaigns.error.permission",
  "meta.rate_limited": "meta.campaigns.error.rateLimited",
  "meta.graph_error": "meta.campaigns.error.graph",
};

const STATUS_KEY: Record<string, MessageKey> = {
  ACTIVE: "meta.campaigns.status.active",
  PAUSED: "meta.campaigns.status.paused",
  DELETED: "meta.campaigns.status.deleted",
  ARCHIVED: "meta.campaigns.status.archived",
};

const codeOf = (error: unknown): string | null => (error instanceof ApiError ? error.code : null);

function MetaError({ error, testId }: { error: unknown; testId: string }) {
  const { t } = useI18n();
  const code = codeOf(error);
  const key = code !== null ? ERROR_KEY[code] : undefined;
  if (key === undefined) return <ErrorNotice error={error} testId={testId} />;
  return (
    <p className="notice notice-bad" role="alert" data-testid={testId} data-code={code ?? undefined}>
      {t(key)}
    </p>
  );
}

export interface MetaCampaignsProps {
  orgId: string;
  /** The selected ad account (act_…); a change of account reads the list again. */
  adAccountId: string;
}

type Notice = { kind: "paused" | "resumed" | "unchanged"; name: string } | null;

export function MetaCampaigns({ orgId, adAccountId }: MetaCampaignsProps) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const key = ["metaCampaigns", orgId, adAccountId];
  const query = useQuery({ queryKey: key, queryFn: () => fetchMetaCampaigns(orgId), retry: false });
  const [asking, setAsking] = useState<{ campaign: MetaCampaign; target: MetaCampaignSetStatus } | null>(null);
  const [notice, setNotice] = useState<Notice>(null);
  const [failure, setFailure] = useState<unknown>(null);

  const reconnect = () => void queryClient.invalidateQueries({ queryKey: ["metaIntegration", orgId] });

  useEffect(() => {
    if (codeOf(query.error) === "meta.reconnect_required") reconnect();
    // eslint-disable-next-line react-hooks/exhaustive-deps -- only when the list failure changes
  }, [query.error]);

  const nameOf = (c: MetaCampaign) => (c.name !== "" ? c.name : c.id);

  const change = async () => {
    if (asking === null) return;
    const { campaign, target } = asking;
    setNotice(null);
    setFailure(null);
    try {
      const result = await setMetaCampaignStatus(orgId, campaign.id, target);
      queryClient.setQueryData<MetaCampaignList>(key, (old) =>
        old === undefined
          ? old
          : { ...old, campaigns: old.campaigns.map((c) => (c.id === result.campaign.id ? result.campaign : c)) },
      );
      setNotice({
        kind: !result.changed ? "unchanged" : result.campaign.status === "PAUSED" ? "paused" : "resumed",
        name: nameOf(result.campaign),
      });
    } catch (error) {
      setFailure(error);
      const code = codeOf(error);
      if (code === "meta.reconnect_required") reconnect();
      if (code === "meta.campaign_not_found" || code === "meta.campaign_not_changeable") void query.refetch();
    } finally {
      setAsking(null);
    }
  };

  const reload = () => {
    setNotice(null);
    setFailure(null);
    void query.refetch();
  };

  const campaigns = query.data?.campaigns ?? [];
  const statusLabel = (status: string) => (STATUS_KEY[status] !== undefined ? t(STATUS_KEY[status]) : status);

  return (
    <section aria-labelledby="meta-campaigns-title" data-testid="meta-campaigns">
      <h2 id="meta-campaigns-title">{t("meta.campaigns.title")}</h2>
      <p>{t("meta.campaigns.intro")}</p>
      <p className="actions">
        <button type="button" className="btn" disabled={query.isFetching} onClick={reload} data-testid="meta-campaigns-reload">
          {t("meta.campaigns.reload")}
        </button>
      </p>
      {notice !== null && (
        <p className="notice notice-ok" role="status" data-testid="meta-campaigns-notice" data-kind={notice.kind}>
          {t(`meta.campaigns.done.${notice.kind}`, { name: notice.name })}
        </p>
      )}
      {failure !== null && <MetaError error={failure} testId="meta-campaigns-failure" />}
      {query.isLoading && <Loading label={t("meta.campaigns.loading")} />}
      {query.error !== null && <MetaError error={query.error} testId="meta-campaigns-error" />}
      {query.data !== undefined && campaigns.length === 0 && <p data-testid="meta-campaigns-none">{t("meta.campaigns.none")}</p>}
      {campaigns.length > 0 && (
        <ul data-testid="meta-campaigns-list">
          {campaigns.map((c) => {
            const target: MetaCampaignSetStatus | null = c.status === "ACTIVE" ? "PAUSED" : c.status === "PAUSED" ? "ACTIVE" : null;
            return (
              <li key={c.id} data-testid={`meta-campaign-${c.id}`} data-status={c.status}>
                <strong>{c.name !== "" ? c.name : t("meta.campaigns.unnamed")}</strong> ({c.id}){" "}
                <span className={`badge badge-${c.status === "ACTIVE" ? "ok" : c.status === "PAUSED" ? "wait" : "neutral"}`}>
                  {statusLabel(c.status)}
                </span>
                {c.effective_status !== "" && c.effective_status !== c.status && (
                  <> {t("meta.campaigns.delivery", { status: c.effective_status })}</>
                )}
                {target !== null && (
                  <>
                    {" "}
                    <button
                      type="button"
                      className="btn"
                      disabled={asking !== null}
                      onClick={() => {
                        setNotice(null);
                        setFailure(null);
                        setAsking({ campaign: c, target });
                      }}
                      data-testid={target === "PAUSED" ? `meta-campaign-pause-${c.id}` : `meta-campaign-resume-${c.id}`}
                    >
                      {t(target === "PAUSED" ? "meta.campaigns.pause" : "meta.campaigns.resume")}
                    </button>
                  </>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {asking !== null && (
        <ConfirmPressDialog
          title={t(asking.target === "PAUSED" ? "meta.campaigns.pause.title" : "meta.campaigns.resume.title", {
            name: nameOf(asking.campaign),
          })}
          explain={t(asking.target === "PAUSED" ? "meta.campaigns.pause.explain" : "meta.campaigns.resume.explain")}
          confirmLabel={t(asking.target === "PAUSED" ? "meta.campaigns.pause" : "meta.campaigns.resume")}
          testId="meta-campaign-confirm"
          onConfirm={change}
          onCancel={() => setAsking(null)}
        />
      )}
    </section>
  );
}
