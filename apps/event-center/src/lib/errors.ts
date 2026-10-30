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
  | "refundNothing"
  | "refundUnavailable"
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
  // 4-7
  | "taxIdInvalid"
  | "legalCountryInvalid"
  | "legalFormInvalid"
  | "emailInvalid"
  | "phoneInvalid"
  | "sellerIncomplete"
  | "apiKeyForbidden"
  | "sellerHideOperatorOnly"
  | "kybOperatorOnly"
  | "legalNameRequired"
  | "invitationTooMany"
  | "invitationRecipient"
  | "invitationAlreadyRevoked"
  | "invitationScanned"
  | "invitationSeatsTaken"
  | "invitationNotEnough"
  | "invitationRequestReused"
  | "invitationSessionCancelled"
  | "invitationNotIssued"
  | "invitationTicketScanned"
  | "invitationEmailNeeded"
  | "invitationMyEmail"
  | "invitationEmpty"
  | "invitationUnknownSeat"
  | "invitationTooManyPlaces"
  | "invitationListInvalid"
  | "invitationNoneActive"
  | "invitationSeatedRoute"
  | "invitationGuestName"
  | "invitationGuestEmail"
  | "invitationNoteLong"
  | "invitationCategory"
  // INV-06b: the CSV upload
  | "batchTooBig"
  | "batchCsvBroken"
  | "batchEncoding"
  | "batchEmptyFile"
  | "batchHeader"
  | "batchInvalid"
  | "batchTimeout"
  // 4-5
  | "resendTooSoon"
  | "invitationChannel"
  | "webInvitationOff"
  | "invitationClosed"
  | "memberGone"
  | "roleNotAllowed"
  | "lastOwner"
  // 5-1
  | "eventInvalidTransition"
  | "eventNeedsSession"
  | "eventNeedsTier"
  // 5-3
  | "saleTimeInvalid"
  | "doorsAfterStart"
  // 5-4
  | "tierPriceInvalid"
  | "tierCapacityInvalid"
  | "tierPriceWindows"
  // B3-3/B4: billing
  | "billingCardUnavailable"
  | "billingKeyReused"
  | "billingPackGone"
  | "billingNoAccount"
  | "billingNotCardPayable"
  | "billingPayerInvalid"
  | "billingTerms"
  | "billingFxMissing"
  | "billingClosed"
  // QL-12: sales links
  | "linkTargetInvalid"
  | "linkUrlInvalid"
  | "linkLabelInvalid"
  | "linkUtmInvalid"
  // RT-11: formatted texts
  | "emailNoteEmpty"
  | "emailNoteRateLimited"
  | "textRefused"
  // S2b/S2c
  | "seatNothingToRevert"
  | "seatTooMany"
  | "seatTierGone"
  | "seatStageNeeded"
  | "seatNoPlan"
  // The shop (spec 47)
  | "shopStockChanged"
  | "shopDuplicateSlug"
  | "shopVariantsInvalid"
  | "shopMediaInvalid"
  | "shopNotShippable"
  | "shopNotCancellable"
  | "shopNotRefundable"
  | "shopRefundAmount"
  | "shopCurrency"
  | "shopOperatorOnly"
  | "shopNothingToRefund"
  | "shopOutOfStock"
  | "shopOrderChanged"
  | "shopOrderEmailUnavailable"
  | "shopOrderNoBuyerEmail"
  | "shopDuplicateDomain"
  | "shopVariantsChanged"
  | "shopSettingsChanged"
  | "shopCurrencyLocked"
  | "shopCurrencyUnsupported";

/** Every stem, for the catalog test. */
export const API_ERROR_ALL_STEMS: readonly ApiErrorStem[] = [
  "network", "unauthorized", "forbidden", "stepUpRequired", "stepUpWrong", "notFound", "conflict", "invalid",
  "tooMany", "slow", "server", "generic", "sellerSiteOrder", "sessionChangeSite", "contactMissing",
  "changeMessageTooLong", "refundScanned", "refundInProgress", "refundNotRefundable", "refundNotSupported",
  "refundNotRetryable", "refundKeyReused", "refundNothing", "refundUnavailable", "tierNotOnSale", "tierSoldOut", "tierSeated", "tierBelowUsed",
  "tierSaleWindow", "promoNotApplicable", "promoMinimum", "promoExhausted", "promoPerCustomer",
  "promoRateLimited", "promoDuplicate", "eventHasPaidOrders", "invitationTooMany", "invitationRecipient",
  "invitationAlreadyRevoked", "invitationScanned",
  "promoRateLimited", "promoDuplicate", "eventHasPaidOrders",
  // 4-7
  "taxIdInvalid", "legalCountryInvalid", "legalFormInvalid", "emailInvalid", "phoneInvalid", "sellerIncomplete",
  "apiKeyForbidden", "sellerHideOperatorOnly", "kybOperatorOnly", "legalNameRequired",
  // 4-5
  "resendTooSoon", "invitationChannel", "webInvitationOff", "invitationClosed", "memberGone", "roleNotAllowed", "lastOwner",
  // 5-1
  "eventInvalidTransition", "eventNeedsSession", "eventNeedsTier",
  // 5-3
  "saleTimeInvalid", "doorsAfterStart",
  // 5-4
  "tierPriceInvalid", "tierCapacityInvalid", "tierPriceWindows",
  // B3-3/B4
  "billingCardUnavailable", "billingKeyReused", "billingPackGone", "billingNoAccount", "billingNotCardPayable",
  "billingPayerInvalid", "billingTerms", "billingFxMissing", "billingClosed",
  // QL-12
  "linkTargetInvalid", "linkUrlInvalid", "linkLabelInvalid", "linkUtmInvalid",
  // RT-11
  "emailNoteEmpty", "emailNoteRateLimited", "textRefused",
  // S2b/S2c
  "seatNothingToRevert", "seatTooMany", "seatTierGone", "seatStageNeeded", "seatNoPlan",
  // Spec 46: invitations on chosen seats
  "invitationSeatsTaken", "invitationNotEnough", "invitationRequestReused", "invitationSessionCancelled", "invitationNotIssued", "invitationTicketScanned", "invitationEmailNeeded", "invitationMyEmail", "invitationEmpty",
  "invitationUnknownSeat", "invitationTooManyPlaces", "invitationGuestName", "invitationGuestEmail", "invitationNoteLong", "invitationCategory",
  // INV-04b: the organization list and the old route on a seated date
  "invitationListInvalid", "invitationNoneActive", "invitationSeatedRoute",
  // INV-06b: the CSV upload
  "batchTooBig", "batchCsvBroken", "batchEncoding", "batchEmptyFile", "batchHeader", "batchInvalid", "batchTimeout",
  // The shop (spec 47)
  "shopStockChanged", "shopDuplicateSlug", "shopVariantsInvalid", "shopMediaInvalid", "shopNotShippable", "shopNotCancellable", "shopNotRefundable", "shopRefundAmount", "shopCurrency", "shopOperatorOnly",
  "shopNothingToRefund", "shopOutOfStock", "shopOrderChanged", "shopOrderEmailUnavailable", "shopOrderNoBuyerEmail", "shopDuplicateDomain",
  "shopVariantsChanged", "shopSettingsChanged", "shopCurrencyLocked", "shopCurrencyUnsupported",
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
  "refund.nothing_to_refund": "refundNothing",
  "refund.amount_exceeds_refundable": "refundNothing",
  "refund.engine_unavailable": "refundUnavailable",
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
  // S2b/S2c: the seat manager
  "seat_batch.nothing_to_revert": "seatNothingToRevert",
  "seat_batch.too_many_changes": "seatTooMany",
  "seat_batch.tier_not_in_session": "seatTierGone",
  "seat_layout.annotation_required": "seatStageNeeded",
  "seat_layout.no_seating_plan": "seatNoPlan",
  // 4-7
  "org.invalid_tax_id": "taxIdInvalid",
  "org.invalid_tax_id_scheme": "taxIdInvalid",
  "org.invalid_legal_address_country": "legalCountryInvalid",
  "org.invalid_legal_form": "legalFormInvalid",
  "org.invalid_contact_email": "emailInvalid",
  "seller.invalid_email": "emailInvalid",
  "seller.invalid_phone": "phoneInvalid",
  "seller.incomplete": "sellerIncomplete",
  "seller.api_key_forbidden": "apiKeyForbidden",
  "org.identity_fields_api_key_forbidden": "apiKeyForbidden",
  "seller.hide_superadmin_only": "sellerHideOperatorOnly",
  "org.kyb_status_superadmin_only": "kybOperatorOnly",
  "org.legal_name_required": "legalNameRequired",
  "seller.channel_not_found": "notFound",
  "complimentary.qty_too_large": "invitationTooMany",
  "complimentary.too_many_recipients": "invitationTooMany",
  "complimentary.invalid_recipient": "invitationRecipient",
  "complimentary.invalid_recipient_name": "invitationRecipient",
  "complimentary.already_revoked": "invitationAlreadyRevoked",
  "complimentary.scanned_ticket_requires_manual_review": "invitationScanned",
  "complimentary.capacity_overflow": "tierSoldOut",
  // Spec 46: invitations on chosen seats
  "invitation.seats_taken": "invitationSeatsTaken",
  "invitation.not_enough_places": "invitationNotEnough",
  "invitation.capacity_exceeded": "invitationNotEnough",
  "invitation.request_id_reused": "invitationRequestReused",
  "invitation.session_cancelled": "invitationSessionCancelled",
  "invitation.tickets_not_issued": "invitationNotIssued",
  "invitation.ticket_scanned": "invitationTicketScanned",
  "invitation.guest_email_required": "invitationEmailNeeded",
  "invitation.my_email_requires_user": "invitationMyEmail",
  "invitation.empty": "invitationEmpty",
  "invitation.unknown_seat": "invitationUnknownSeat",
  "invitation.too_many": "invitationTooManyPlaces",
  "invitation.invalid_guest_name": "invitationGuestName",
  "invitation.invalid_guest_email": "invitationGuestEmail",
  "invitation.note_too_long": "invitationNoteLong",
  "invitation.unknown_category": "invitationCategory",
  "invitation.seat_without_category": "invitationCategory",
  "invitation.invalid_cursor": "invitationListInvalid",
  "invitation.invalid_filter": "invitationListInvalid",
  "invitation.invalid_state": "invitationListInvalid",
  "invitation.invalid_limit": "invitationListInvalid",
  "invitation.no_active_tickets": "invitationNoneActive",
  "complimentary.use_invitations_route": "invitationSeatedRoute",
  // INV-06b: the CSV upload (the 409 refusals of the transaction keep their single-invitation codes, the screen reads their report)
  "invitation_batch.too_many_rows": "batchTooBig",
  "invitation_batch.too_many_places": "batchTooBig",
  "invitation_batch.file_too_large": "batchTooBig",
  "invitation_batch.invalid_csv": "batchCsvBroken",
  "invitation_batch.invalid_encoding": "batchEncoding",
  "invitation_batch.empty_file": "batchEmptyFile",
  "invitation_batch.file_required": "batchEmptyFile",
  "invitation_batch.invalid_header": "batchHeader",
  "invitation_batch.invalid": "batchInvalid",
  "invitation_batch.timeout": "batchTimeout",
  "invitation_batch.request_id_reused": "invitationRequestReused",
  "invitation_batch.invalid_request_id": "invalid",
  "invitation_batch.invalid_delivery": "invalid",
  "invitation_batch.invalid_dry_run": "invalid",
  "invitation_batch.invalid_body": "invalid",
  "membership.last_owner": "lastOwner",
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
  // 5-1: the status of an event
  "event.invalid_transition": "eventInvalidTransition",
  "event.publish_requires_session": "eventNeedsSession",
  "event.publish_requires_priced_tier": "eventNeedsTier",
  // 5-3: the sales end and doors-open time
  "session.invalid_sales_end_at": "saleTimeInvalid",
  "session.invalid_doors_open_at": "saleTimeInvalid",
  "session.doors_after_start": "doorsAfterStart",
  // 5-4: PATCH tier and PUT price-schedule
  "tier.invalid_fixed_price": "tierPriceInvalid",
  "tier.invalid_free_price": "tierPriceInvalid",
  "tier.invalid_price_window": "tierPriceInvalid",
  "tier.invalid_prices": "tierPriceInvalid",
  "tier.invalid_capacity": "tierCapacityInvalid",
  "tier.capacity_required": "tierCapacityInvalid",
  "tier.price_windows_overlap": "tierPriceWindows",
  "tier.too_many_price_windows": "tierPriceWindows",
  "tier.schedule_requires_fixed_mode": "tierPriceWindows",
  "tier.invalid_sale_window_start": "tierSaleWindow",
  "tier.invalid_sale_window_end": "tierSaleWindow",
  // B3-3/B4: billing account routes
  "billing.card_payment_unavailable": "billingCardUnavailable",
  "billing.idempotency_key_reused": "billingKeyReused",
  "billing.idempotency_key_required": "invalid",
  "billing.pack_not_found": "billingPackGone",
  "billing.invoice_not_found": "notFound",
  "billing.invoice_not_card_payable": "billingNotCardPayable",
  "billing.invalid_payer": "billingPayerInvalid",
  "billing.account_not_found": "billingNoAccount",
  "billing.terms_unavailable": "billingTerms",
  "billing.quote_unavailable": "billingTerms",
  "billing.fx_rate_missing": "billingFxMissing",
  "billing.invalid_payment_method": "invalid",
  "billing.bad_request": "invalid",
  "billing.organizer_disabled": "billingClosed",
  // QL-12: sales links (spec 43)
  "sales_link.invalid_target": "linkTargetInvalid",
  "sales_link.invalid_url": "linkUrlInvalid",
  "sales_link.invalid_label": "linkLabelInvalid",
  "sales_link.invalid_utm": "linkUtmInvalid",
  "sales_link.invalid_placement": "invalid",
  "sales_link.invalid_status": "invalid",
  "sales_link.invalid_body": "invalid",
  "sales_link.invalid_qr_params": "invalid",
  "sales_link.failed": "server",
  // RT-11: the text in the letter, the description and the message to buyers (spec 44)
  "event.email_note_empty": "emailNoteEmpty",
  "event.email_note_test_rate_limited": "emailNoteRateLimited",
  "event.invalid_email_note": "textRefused",
  "event.invalid_description": "textRefused",
  "import.invalid_description": "textRefused",
  "session.change_message_invalid": "textRefused",
  "text.too_long": "textRefused",
  "text.invalid": "textRefused",
  // The shop (spec 47)
  "shop.stock_changed": "shopStockChanged",
  "shop.duplicate_slug": "shopDuplicateSlug",
  "shop.invalid_variants": "shopVariantsInvalid",
  "shop.invalid_variant": "shopVariantsInvalid",
  "shop.invalid_media": "shopMediaInvalid",
  "shop.order_not_shippable": "shopNotShippable",
  // A manual_review order whose payment is still unsettled (second audit pass, SB-01).
  "shop.payment_unconfirmed": "shopNotShippable",
  "shop.order_not_cancellable": "shopNotCancellable",
  "shop.order_not_refundable": "shopNotRefundable",
  "shop.nothing_to_refund": "shopNothingToRefund",
  "shop.out_of_stock": "shopOutOfStock",
  "shop.order_changed": "shopOrderChanged",
  "shop.order_email_unavailable": "shopOrderEmailUnavailable",
  "shop.order_no_buyer_email": "shopOrderNoBuyerEmail",
  "shop.duplicate_domain": "shopDuplicateDomain",
  "shop.variants_changed": "shopVariantsChanged",
  "shop.settings_changed": "shopSettingsChanged",
  "shop.currency_locked": "shopCurrencyLocked",
  "shop.currency_unsupported": "shopCurrencyUnsupported",
  "shop.invalid_refund_amount": "shopRefundAmount",
  "shop.invalid_currency": "shopCurrency",
  "shop.enabled_superadmin_only": "shopOperatorOnly",
  "shop.domain_superadmin_only": "shopOperatorOnly",
  "shop.invalid_digital_media": "shopMediaInvalid",
  "shop.invalid_body_html": "textRefused",
  "shop.text_too_long": "textRefused",
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
