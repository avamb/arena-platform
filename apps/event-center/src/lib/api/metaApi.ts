/**
 * Calls of the Meta advertising connection (spec 66, section 4). Reads and writes need `org.update`.
 * The access token never reaches the browser: the answer holds only the status and the shared objects.
 */
import { call } from "@/lib/api/http";
import { API } from "@/lib/api/routes";
import type { components } from "@openapi/index";

type S = components["schemas"];
export type MetaIntegration = S["MetaIntegration"];
export type MetaConnection = S["MetaConnection"];
export type MetaAsset = S["MetaAsset"];
export type MetaConnectStart = S["MetaConnectStart"];
export type MetaSelection = S["UpdateMetaIntegrationRequest"];

const org = (orgId: string) => ({ org_id: orgId });

export function fetchMetaIntegration(orgId: string): Promise<MetaIntegration> {
  return call<MetaIntegration>({ route: API.metaIntegration, params: org(orgId) });
}

/** Chooses the dataset and/or the ad account (each value is the meta_id of a shared object). */
export function selectMetaAssets(orgId: string, body: MetaSelection): Promise<MetaIntegration> {
  return call<MetaIntegration>({ route: API.metaIntegrationPatch, params: org(orgId), body });
}

export function disconnectMeta(orgId: string): Promise<MetaIntegration> {
  return call<MetaIntegration>({ route: API.metaIntegrationDelete, params: org(orgId) });
}

export function startMetaConnect(orgId: string): Promise<MetaConnectStart> {
  return call<MetaConnectStart>({ route: API.metaConnect, params: org(orgId) });
}
