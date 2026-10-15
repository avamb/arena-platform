/**
 * Turns an API failure into a message key. The message always says what
 * to do next (text standard, section 2.1).
 */
import { ApiError } from "@/lib/api/http";
import type { MessageKey } from "@/lib/i18n/en";
import type { Params } from "@/lib/i18n";

export function errorKey(err: unknown): MessageKey {
  if (err instanceof ApiError) {
    if (err.status === 0) return "common.error.network";
    if (err.status === 403) return "common.error.forbidden";
    if (err.status === 404) return "common.error.notFound";
    if (err.code === "export.too_many_rows" || err.status === 413) return "common.download.tooMany";
  }
  return "common.error.generic";
}

/** Errors of POST /v1/auth/password-reset/confirm. */
export function setPasswordErrorKey(err: unknown): MessageKey {
  if (err instanceof ApiError) {
    if (err.status === 0) return "common.error.network";
    if (err.code === "auth.token_already_used") return "password.error.linkUsed";
    if (err.code === "auth.token_expired") return "password.error.linkExpired";
    if (err.code === "auth.token_not_found" || err.status === 404 || err.status === 410)
      return "password.error.linkInvalid";
    if (err.code === "validation.password_too_short") return "password.error.tooShort";
    if (err.code === "validation.password_too_long") return "password.error.tooLong";
  }
  return "common.error.generic";
}

/** Errors of POST /v1/auth/web/link. Each one says what to do next. */
export function linkLoginErrorKey(err: unknown): MessageKey {
  if (err instanceof ApiError) {
    if (err.status === 0) return "common.error.network";
    if (err.code === "auth.web_link_superadmin") return "linkLogin.error.superadmin";
    if (err.code === "auth.user_deactivated") return "login.error.deactivated";
    if (err.code === "auth.rate_limited" || err.status === 429) return "login.error.rateLimited";
    if (err.code === "auth.web_link_invalid" || err.status === 400) return "linkLogin.error.invalid";
  }
  return "common.error.generic";
}

export function loginErrorKey(err: unknown): MessageKey {
  if (err instanceof ApiError) {
    if (err.status === 0) return "common.error.network";
    if (err.code === "auth.rate_limited" || err.status === 429) return "login.error.rateLimited";
    if (err.code === "auth.user_deactivated") return "login.error.deactivated";
    if (err.status === 401 || err.status === 400) return "login.error.credentials";
  }
  return "common.error.generic";
}

// ─── describeApiError: a title and what to do next ───────────────────────────

/** A text with a title and the next step. Both say what to do (text standard 2.1). */
export interface ApiErrorText {
  title: string;
  next: string;
}

export type ApiErrorStem =
  | "network"
  | "unauthorized"
  | "forbidden"
  | "stepUpRequired"
  | "stepUpWrong"
  | "notFound"
  | "conflict"
  | "invalid"
  | "tooMany"
  | "slow"
  | "server"
  | "generic"
  | "sellerSiteOrder"
  | "sessionChangeSite"
  | "contactMissing"
  | "changeMessageTooLong"
  | "refundScanned"
  | "refundInProgress"
  | "refundNotRefundable"
  | "refundNotSupported"
  | "refundNotRetryable"
  | "refundKeyReused"
  | "tierNotOnSale"
  | "tierSoldOut"
  | "tierSeated"
  | "tierBelowUsed"
  | "tierSaleWindow"
  | "promoNotApplicable"
  | "promoMinimum"
  | "promoExhausted"
  | "promoPerCustomer"
  | "promoRateLimited"
  | "promoDuplicate"
  | "eventHasPaidOrders"
  // 4-5
  | "resendTooSoon"
  | "invitationChannel"
  | "webInvitationOff"
  | "invitationClosed"
  | "memberGone"
  | "roleNotAllowed";

/** Every stem, for the catalog test. */
export const API_ERROR_ALL_STEMS: readonly ApiErrorStem[] = [
  "network", "unauthorized", "forbidden", "stepUpRequired", "stepUpWrong", "notFound", "conflict", "invalid",
  "tooMany", "slow", "server", "generic", "sellerSiteOrder", "sessionChangeSite", "contactMissing",
  "changeMessageTooLong", "refundScanned", "refundInProgress", "refundNotRefundable", "refundNotSupported",
  "refundNotRetryable", "refundKeyReused", "tierNotOnSale", "tierSoldOut", "tierSeated", "tierBelowUsed",
  "tierSaleWindow", "promoNotApplicable", "promoMinimum", "promoExhausted", "promoPerCustomer",
  "promoRateLimited", "promoDuplicate", "eventHasPaidOrders",
  // 4-5
  "resendTooSoon", "invitationChannel", "webInvitationOff", "invitationClosed", "memberGone", "roleNotAllowed",
];

/**
 * Every API error code the web knows, mapped to the stem of its texts
 * (`apiError.<stem>.title` and `apiError.<stem>.next`). A code that is not
 * here falls back by suffix (`*.not_found`) and then by HTTP status.
 */
export const API_ERROR_STEMS: Record<string, ApiErrorStem> = {
  "order.seller_site_order": "sellerSiteOrder",
  "refund.seller_site_order": "sellerSiteOrder",
  "session.change_site_unsupported": "sessionChangeSite",
  "organization.contact_missing": "contactMissing",
  "session.change_message_too_long": "changeMessageTooLong",
  "refund.ticket_scanned": "refundScanned",
  "refund.in_progress": "refundInProgress",
  "refund.ticket_not_refundable": "refundNotRefundable",
  "refund.provider_not_supported": "refundNotSupported",
  "refund.partial_not_supported": "refundNotSupported",
  "refund.not_retryable": "refundNotRetryable",
  "refund.already_replaced": "refundNotRetryable",
  "refund.idempotency_key_reused": "refundKeyReused",
  "tier.not_on_sale": "tierNotOnSale",
  "tier.sold_out": "tierSoldOut",
  "tier.seated_category": "tierSeated",
  "tier.quantity_below_used": "tierBelowUsed",
  "tier.invalid_sale_window": "tierSaleWindow",
  "promo.not_applicable": "promoNotApplicable",
  "promo.minimum_not_reached": "promoMinimum",
  "promo.exhausted": "promoExhausted",
  "promo.per_customer_limit": "promoPerCustomer",
  "promo.rate_limited": "promoRateLimited",
  "promo.duplicate": "promoDuplicate",
  "event.has_paid_orders": "eventHasPaidOrders",
  // 4-5: the team and its invitations (bot.* routes, DELETE members)
  "bot.invitation_resend_too_soon": "resendTooSoon",
  "bot.invitation_channel_unsupported": "invitationChannel",
  "bot.web_invitation_not_configured": "webInvitationOff",
  "bot.invitation_revoked": "invitationClosed",
  "bot.invitation_accepted": "invitationClosed",
  "bot.invitation_not_found": "memberGone",
  "membership.not_found": "memberGone",
  "membership.role_not_allowed": "roleNotAllowed",
  "bot.invalid_channel": "invalid",
  "bot.invalid_email": "invalid",
  "bot.invalid_role": "invalid",
  "membership.empty_body": "invalid",
  "membership.invalid_role": "invalid",
  "auth.step_up_required": "stepUpRequired",
  "auth.step_up_wrong_password": "stepUpWrong",
  "permissions.denied": "forbidden",
};

/** The stem of the texts for an error: the code first, then the status. */
export function apiErrorStem(err: unknown): ApiErrorStem {
  if (!(err instanceof ApiError)) return "generic";
  const known = Object.prototype.hasOwnProperty.call(API_ERROR_STEMS, err.code) ? API_ERROR_STEMS[err.code] : undefined;
  if (known !== undefined) return known;
  if (err.status === 0) return "network";
  if (err.code.endsWith(".not_found")) return "notFound";
  if (err.status === 401) return "unauthorized";
  if (err.status === 403) return "forbidden";
  if (err.status === 404) return "notFound";
  if (err.status === 409) return "conflict";
  if (err.status === 400 || err.status === 422) return "invalid";
  if (err.status === 429) return "tooMany";
  // Cloudflare cuts a slow answer at 100 s (524) and a gateway can time out (504).
  // The server may still finish the work, so the text says to check first.
  if (err.status === 504 || err.status === 524) return "slow";
  if (err.status >= 500) return "server";
  return "generic";
}

type Translate = (key: MessageKey, params?: Params) => string;

/**
 * Turns an API failure into a title and the next step, in the person's
 * language. `t` is `useI18n().t`.
 */
export function describeApiError(err: unknown, t: Translate, params?: Params): ApiErrorText {
  const stem = apiErrorStem(err);
  return {
    title: t(`apiError.${stem}.title` as MessageKey, params),
    next: t(`apiError.${stem}.next` as MessageKey, params),
  };
}
