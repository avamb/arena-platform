/**
 * Convenience type aliases sourced from the auto-generated OpenAPI TS
 * definitions at apps/backend/openapi/clients/ts/index.d.ts.
 *
 * Importing through this module keeps the rest of the admin app
 * decoupled from the generator output path -- if the codegen layout
 * changes, only this file needs an update.
 */
import type { components } from "@openapi/index";

export type AuthLoginRequest = components["schemas"]["AuthLoginRequest"];
export type AuthLoginResponse = components["schemas"]["AuthLoginResponse"];
export type AuthRefreshRequest = components["schemas"]["AuthRefreshRequest"];
export type AuthRefreshResponse = components["schemas"]["AuthRefreshResponse"];
export type AuthLogoutRequest = components["schemas"]["AuthLogoutRequest"];
export type MeResponse = components["schemas"]["MeResponse"];
export type MeUser = components["schemas"]["MeUser"];
export type MeAssignedNetwork = components["schemas"]["MeAssignedNetwork"];
export type MeOrganizationMembership = components["schemas"]["MeOrganizationMembership"];
export type ErrorEnvelope = components["schemas"]["ErrorEnvelope"];
