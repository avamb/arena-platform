/**
 * Campaigns of the ad account selected in the organization's Meta connection (spec 66): the list and
 * pause / resume of one campaign. Both need `org.update`. Only the status is ever changed, never a budget.
 */
import { call } from "@/lib/api/http";
import { API } from "@/lib/api/routes";
import type { components } from "@openapi/index";

type S = components["schemas"];
export type MetaCampaign = S["MetaCampaign"];
export type MetaCampaignList = S["MetaCampaignList"];
export type MetaCampaignSetStatus = S["MetaCampaignSetStatus"];
export type MetaCampaignStatusResult = S["MetaCampaignStatusResult"];

export function fetchMetaCampaigns(orgId: string): Promise<MetaCampaignList> {
  return call<MetaCampaignList>({ route: API.metaCampaigns, params: { org_id: orgId } });
}

/** PAUSED stops the campaign, ACTIVE resumes it. */
export function setMetaCampaignStatus(
  orgId: string,
  campaignId: string,
  status: MetaCampaignSetStatus,
): Promise<MetaCampaignStatusResult> {
  return call<MetaCampaignStatusResult>({
    route: API.metaCampaignStatus,
    params: { org_id: orgId, campaign_id: campaignId },
    body: { status },
  });
}
