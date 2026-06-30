package httpserver

import "time"

// pgUniqueViolation is the PostgreSQL error code for unique-constraint violations.
// https://www.postgresql.org/docs/current/errcodes-appendix.html
const pgUniqueViolation = "23505"

// passwordResetTokenTTL is the lifetime of a password-reset token.
const passwordResetTokenTTL = time.Hour
