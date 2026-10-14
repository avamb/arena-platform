/**
 * Turns an API failure into a message key. The message always says what
 * to do next (text standard, section 2.1).
 */
import { ApiError } from "@/lib/api/http";
import type { MessageKey } from "@/lib/i18n/en";

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
