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
export type MetaFinish = S["MetaFinishRequest"];
export type MetaPagePreview = S["MetaPagePreview"];

const org = (orgId: string) => ({ org_id: orgId });

export function fetchMetaIntegration(orgId: string): Promise<MetaIntegration> {
  return call<MetaIntegration>({ route: API.metaIntegration, params: org(orgId) });
}

/** The selected Facebook Page as Meta shows it now (read live, nothing stored). */
export function fetchMetaPage(orgId: string): Promise<MetaPagePreview> {
  return call<MetaPagePreview>({ route: API.metaPage, params: org(orgId) });
}

/** Chooses the dataset, the ad account and/or the page (each value is the meta_id of a shared object). */
export function selectMetaAssets(orgId: string, body: MetaSelection): Promise<MetaIntegration> {
  return call<MetaIntegration>({ route: API.metaIntegrationPatch, params: org(orgId), body });
}

export function disconnectMeta(orgId: string): Promise<MetaIntegration> {
  return call<MetaIntegration>({ route: API.metaIntegrationDelete, params: org(orgId) });
}

export function startMetaConnect(orgId: string): Promise<MetaConnectStart> {
  return call<MetaConnectStart>({ route: API.metaConnect, params: org(orgId) });
}

/** Finishes the login with the code and the state of the callback's fragment (once per login). */
export function finishMetaConnect(orgId: string, body: MetaFinish): Promise<MetaIntegration> {
  return call<MetaIntegration>({ route: API.metaFinish, params: org(orgId), body });
}
