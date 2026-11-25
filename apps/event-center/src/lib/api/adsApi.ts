/**
 * Calls of the Meta ads report (spec 66, M2). Reads and the refresh need `report.read`.
 */
import { call } from "@/lib/api/http";
import { API } from "@/lib/api/routes";
import type { components } from "@openapi/index";

type S = components["schemas"];
export type MetaAdsEventReport = S["MetaAdsEventReport"];
export type MetaAdsOrgSummary = S["MetaAdsOrgSummary"];
export type MetaAdsRefreshResult = S["MetaAdsRefreshResult"];
export type MetaAdsCampaign = S["MetaAdsCampaign"];
export type MetaAdsSync = S["MetaAdsSync"];

export interface AdsWindow {
  from?: string;
  to?: string;
}

const clean = (v: string | undefined) => (v === undefined || v === "" ? undefined : v);

/** The report of one event. No window means the server's default: the last 30 days. */
export function fetchEventMetaAds(orgId: string, eventId: string, window: AdsWindow = {}): Promise<MetaAdsEventReport> {
  return call<MetaAdsEventReport>({
    route: API.eventMetaAds,
    params: { org_id: orgId, event_id: eventId },
    query: { from: clean(window.from), to: clean(window.to) },
  });
}

export function fetchOrgMetaAds(orgId: string, window: AdsWindow = {}): Promise<MetaAdsOrgSummary> {
  return call<MetaAdsOrgSummary>({
    route: API.orgMetaAdsSummary,
    params: { org_id: orgId },
    query: { from: clean(window.from), to: clean(window.to) },
  });
}

/** Asks for a new read of ad spend. The server accepts one every 10 minutes. */
export function refreshOrgMetaAds(orgId: string): Promise<MetaAdsRefreshResult> {
  return call<MetaAdsRefreshResult>({ route: API.orgMetaAdsRefresh, params: { org_id: orgId } });
}
