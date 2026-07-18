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
export type AdminCreateUserRequest = components["schemas"]["AdminCreateUserRequest"];
export type AdminCreateUserResponse = components["schemas"]["AdminCreateUserResponse"];
export type MeResponse = components["schemas"]["MeResponse"];
export type MeUser = components["schemas"]["MeUser"];
export type MeAssignedNetwork = components["schemas"]["MeAssignedNetwork"];
export type MeOrganizationMembership = components["schemas"]["MeOrganizationMembership"];
export type ErrorEnvelope = components["schemas"]["ErrorEnvelope"];

// ── Organizations, members ─────────────────────────────────────────────────
export type OrganizationItem = components["schemas"]["OrganizationItem"];
export type OrganizationDetail = components["schemas"]["OrganizationDetail"];
export type CreateOrganizationRequest = components["schemas"]["CreateOrganizationRequest"];
export type UpdateOrganizationRequest = components["schemas"]["UpdateOrganizationRequest"];
export type MembershipItem = components["schemas"]["MembershipItem"];

// ── Sales channels & feed tokens ──────────────────────────────────────────
export type Channel = components["schemas"]["Channel"];
export type FeedToken = components["schemas"]["FeedToken"];

// ── Events, sessions, tiers ───────────────────────────────────────────────
export type EventItem = components["schemas"]["EventItem"];
export type CreateEventRequest = components["schemas"]["CreateEventRequest"];
export type UpdateEventRequest = components["schemas"]["UpdateEventRequest"];
export type SessionItem = components["schemas"]["SessionItem"];
export type CreateSessionRequest = components["schemas"]["CreateSessionRequest"];
export type TicketTierItem = components["schemas"]["TicketTierItem"];
export type CreateTicketTierRequest = components["schemas"]["CreateTicketTierRequest"];

// ── Checkout & payments ───────────────────────────────────────────────────
export type CheckoutSessionItem = components["schemas"]["CheckoutSessionItem"];
export type StartCheckoutRequest = components["schemas"]["StartCheckoutRequest"];
export type PricingLineItem = components["schemas"]["PricingLineItem"];
export type PricingBreakdownItem = components["schemas"]["PricingBreakdownItem"];
export type PaymentIntentItem = components["schemas"]["PaymentIntentItem"];
export type RefundItem = components["schemas"]["RefundItem"];
export type CreateRefundRequest = components["schemas"]["CreateRefundRequest"];

// ── Promo codes ───────────────────────────────────────────────────────────
export type PromoCodeItem = components["schemas"]["PromoCodeItem"];
export type CreatePromoCodeRequest = components["schemas"]["CreatePromoCodeRequest"];
export type ValidatePromoCodeRequest = components["schemas"]["ValidatePromoCodeRequest"];
export type ValidatePromoCodeResponse = components["schemas"]["ValidatePromoCodeResponse"];

// ── Barcode management ────────────────────────────────────────────────────
export type BarcodeAuthorityItem = components["schemas"]["BarcodeAuthorityItem"];
export type BarcodeItem = components["schemas"]["BarcodeItem"];
export type BarcodeBatch = components["schemas"]["BarcodeBatch"];

// ── Complimentary & allocations ───────────────────────────────────────────
export type ExternalAllocation = components["schemas"]["ExternalAllocation"];
export type ComplimentaryIssuance = components["schemas"]["ComplimentaryIssuance"];

// ── Reconciliation ────────────────────────────────────────────────────────
export type ReconciliationReportLine = components["schemas"]["PricingLineItem"];  // closest available schema

// ── Tickets & delivery ────────────────────────────────────────────────────
export type TicketItem = components["schemas"]["TicketItem"];
export type AdminTicketDeliveryResponse = components["schemas"]["AdminTicketDeliveryResponse"];

// ── Refunds ───────────────────────────────────────────────────────────────
export type ApproveRefundRequest = components["schemas"]["ApproveRefundRequest"];
