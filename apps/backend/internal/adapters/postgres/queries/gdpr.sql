-- gdpr.sql — GDPR data subject request queries (feature #164)
--
-- Covers:
--   - Inserting export / delete requests
--   - Listing a user's own requests
--   - Worker polling (FOR UPDATE SKIP LOCKED)
--   - Status transitions (pending → processing → completed | failed)
--   - User anonymization (delete workflow)
--   - Consent recording (registration + explicit consent update)
--   - User data export (safe fields, no password_hash)

-- name: InsertDataSubjectRequest :one
-- Create a new pending GDPR request (export or delete).
INSERT INTO data_subject_requests (user_id, request_type)
VALUES ($1, $2)
RETURNING id, user_id, request_type, status, payload_url, error_msg, created_at, completed_at;

-- name: GetDataSubjectRequestByID :one
-- Fetch a single request, scoped to the requesting user (prevents cross-user access).
SELECT id, user_id, request_type, status, payload_url, error_msg, created_at, completed_at
FROM data_subject_requests
WHERE id = $1 AND user_id = $2;

-- name: ListDataSubjectRequestsByUser :many
-- List all GDPR requests for a given user, newest first.
SELECT id, user_id, request_type, status, payload_url, error_msg, created_at, completed_at
FROM data_subject_requests
WHERE user_id = $1
ORDER BY created_at DESC;

-- name: GetPendingDataSubjectRequests :many
-- Poll up to $1 pending requests for the worker, locking them for exclusive
-- processing using FOR UPDATE SKIP LOCKED (pg job-queue pattern).
-- The worker must UPDATE the status to 'processing' immediately after fetching.
SELECT id, user_id, request_type, status, payload_url, error_msg, created_at, completed_at
FROM data_subject_requests
WHERE status = 'pending'
ORDER BY created_at
FOR UPDATE SKIP LOCKED
LIMIT $1;

-- name: UpdateDataSubjectRequestStatus :one
-- Transition a request to a new status, and optionally set payload_url / error_msg.
-- completed_at is set to now() when the new status is 'completed' or 'failed'.
UPDATE data_subject_requests
SET
    status       = $2,
    payload_url  = $3,
    error_msg    = $4,
    completed_at = CASE WHEN $2 IN ('completed', 'failed') THEN now() ELSE completed_at END
WHERE id = $1
RETURNING id, user_id, request_type, status, payload_url, error_msg, created_at, completed_at;

-- name: AnonymizeUser :exec
-- Replace PII with deterministic placeholder values. The email becomes
-- 'deleted-<uuid>@arena.invalid' so FK constraints remain intact while
-- making it clear the account has been deleted. Financial records that
-- reference user_id are RETAINED per accounting law — only the users row
-- is anonymized.
UPDATE users
SET
    email          = 'deleted-' || id::text || '@arena.invalid',
    password_hash  = '',
    preferred_locale = 'en',
    anonymized_at  = now()
WHERE id = $1;

-- name: RecordUserConsent :exec
-- Record consent at registration time or after an explicit consent action.
-- marketing_consent defaults to false; pass true if the user opted in.
UPDATE users
SET
    consent_given_at = now(),
    marketing_consent = $2
WHERE id = $1;

-- name: GetUserExportData :one
-- Fetch user data for a GDPR export dump.
-- Intentionally omits the password column (irreversibly hashed; not useful
-- to the data subject) and anonymized_at (internal operational field).
SELECT
    id,
    email,
    preferred_locale,
    created_at,
    email_verified_at,
    consent_given_at,
    marketing_consent
FROM users
WHERE id = $1;
