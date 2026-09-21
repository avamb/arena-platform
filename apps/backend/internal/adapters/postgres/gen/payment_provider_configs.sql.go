// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: payment_provider_configs.sql

package gen

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// PaymentProviderConfigRow — shared result type for all payment_provider_configs queries
// ─────────────────────────────────────────────────────────────────────────────

// PaymentProviderConfigRow is the result type returned by all
// payment_provider_configs queries.
//
// deleted_at is nil for active rows and non-nil for soft-deleted ones.
// provider_account_id is nil when not set (some providers don't have a
// stable public account identifier — e.g. legacy CloudPayments).
//
// secrets is the RAW secret jsonb. HTTP serializers must NEVER include
// this field in GET/LIST responses.
type PaymentProviderConfigRow struct {
	ID                uuid.UUID       `json:"id"`
	OrgID             uuid.UUID       `json:"org_id"`
	Provider          string          `json:"provider"`
	Mode              string          `json:"mode"`
	ProviderAccountID *string         `json:"provider_account_id"`
	PublicConfig      json.RawMessage `json:"public_config"`
	Secrets           json.RawMessage `json:"secrets"`
	Status            string          `json:"status"`
	IsActive          bool            `json:"is_active"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	DeletedAt         *time.Time      `json:"deleted_at"`
	// VerificationStatus is what the PROVIDER said about this credential the
	// last time we sent it one: 'unverified' (never asked, or the secret has
	// changed since), 'ok', or 'failed'. Status above is only a shape check —
	// "every required field holds something" — and says nothing about whether
	// the something is a usable key.
	VerificationStatus string     `json:"verification_status"`
	VerifiedAt         *time.Time `json:"verified_at"`
	// VerificationError is the provider's own wording of the refusal, kept so
	// an operator reads "Invalid API Key provided" instead of a code of ours.
	// Operator-facing: never expose it on an unauthenticated surface.
	VerificationError *string `json:"verification_error"`
}

// scanPaymentProviderConfigRow scans a single payment_provider_configs row.
func scanPaymentProviderConfigRow(row interface {
	Scan(dest ...any) error
}) (PaymentProviderConfigRow, error) {
	var p PaymentProviderConfigRow
	err := row.Scan(
		&p.ID,
		&p.OrgID,
		&p.Provider,
		&p.Mode,
		&p.ProviderAccountID,
		&p.PublicConfig,
		&p.Secrets,
		&p.Status,
		&p.IsActive,
		&p.CreatedAt,
		&p.UpdatedAt,
		&p.DeletedAt,
		&p.VerificationStatus,
		&p.VerifiedAt,
		&p.VerificationError,
	)
	return p, err
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertPaymentProviderConfig
// ─────────────────────────────────────────────────────────────────────────────

const insertPaymentProviderConfig = `-- name: InsertPaymentProviderConfig :one
INSERT INTO payment_provider_configs (
    org_id, provider, mode, provider_account_id,
    public_config, secrets, status, is_active
)
VALUES (
    $1, $2, $3, $4,
    COALESCE($5::jsonb, '{}'::jsonb),
    COALESCE($6::jsonb, '{}'::jsonb),
    $7, $8
)
RETURNING id, org_id, provider, mode, provider_account_id, public_config, secrets, status, is_active, created_at, updated_at, deleted_at, verification_status, verified_at, verification_error`

// InsertPaymentProviderConfig creates a new active payment_provider_configs row.
// Pass publicConfig = nil or secrets = nil to use the database default '{}'::jsonb.
func (q *Queries) InsertPaymentProviderConfig(
	ctx context.Context,
	orgID uuid.UUID,
	provider, mode string,
	providerAccountID *string,
	publicConfig, secrets json.RawMessage,
	status string,
	isActive bool,
) (PaymentProviderConfigRow, error) {
	row := q.db.QueryRow(ctx, insertPaymentProviderConfig,
		orgID, provider, mode, providerAccountID,
		publicConfig, secrets, status, isActive,
	)
	return scanPaymentProviderConfigRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetPaymentProviderConfigByID
// ─────────────────────────────────────────────────────────────────────────────

const getPaymentProviderConfigByID = `-- name: GetPaymentProviderConfigByID :one
SELECT id, org_id, provider, mode, provider_account_id, public_config, secrets, status, is_active, created_at, updated_at, deleted_at, verification_status, verified_at, verification_error
FROM   payment_provider_configs
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL`

// GetPaymentProviderConfigByID fetches an active config by its UUID PK,
// scoped to the given org. Returns pgx.ErrNoRows when not found or deleted.
func (q *Queries) GetPaymentProviderConfigByID(ctx context.Context, id, orgID uuid.UUID) (PaymentProviderConfigRow, error) {
	row := q.db.QueryRow(ctx, getPaymentProviderConfigByID, id, orgID)
	return scanPaymentProviderConfigRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetPaymentProviderConfigByIDUnscoped
// ─────────────────────────────────────────────────────────────────────────────

const getPaymentProviderConfigByIDUnscoped = `-- name: GetPaymentProviderConfigByIDUnscoped :one
SELECT id, org_id, provider, mode, provider_account_id, public_config, secrets, status, is_active, created_at, updated_at, deleted_at, verification_status, verified_at, verification_error
FROM   payment_provider_configs
WHERE  id = $1
  AND  deleted_at IS NULL`

// GetPaymentProviderConfigByIDUnscoped fetches a config by its UUID primary
// key WITHOUT an org scope. Returns pgx.ErrNoRows when not found or deleted.
//
// It exists for exactly one caller: the per-config payment webhook route
// POST /v1/payment-intents/webhook/{config_id}. The caller there is the
// payment provider, which has no arena identity and cannot supply an org —
// the config id in the path IS the routing key, and the org it belongs to is
// what this read establishes, so the signature can then be checked against
// that org's own signing secret.
//
// A single-row primary-key read, so an unauthenticated caller cannot make it
// expensive. Every other reader must keep using the org-scoped
// GetPaymentProviderConfigByID.
func (q *Queries) GetPaymentProviderConfigByIDUnscoped(ctx context.Context, id uuid.UUID) (PaymentProviderConfigRow, error) {
	row := q.db.QueryRow(ctx, getPaymentProviderConfigByIDUnscoped, id)
	return scanPaymentProviderConfigRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// ListPaymentProviderConfigsByOrg
// ─────────────────────────────────────────────────────────────────────────────

const listPaymentProviderConfigsByOrg = `-- name: ListPaymentProviderConfigsByOrg :many
SELECT id, org_id, provider, mode, provider_account_id, public_config, secrets, status, is_active, created_at, updated_at, deleted_at, verification_status, verified_at, verification_error
FROM   payment_provider_configs
WHERE  org_id = $1
  AND  deleted_at IS NULL
ORDER  BY provider ASC, mode ASC, created_at ASC, id ASC`

// ListPaymentProviderConfigsByOrg returns all active configs for the given
// organization, ordered by provider, mode, then creation time.
func (q *Queries) ListPaymentProviderConfigsByOrg(ctx context.Context, orgID uuid.UUID) ([]PaymentProviderConfigRow, error) {
	rows, err := q.db.Query(ctx, listPaymentProviderConfigsByOrg, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []PaymentProviderConfigRow
	for rows.Next() {
		p, err := scanPaymentProviderConfigRow(rows)
		if err != nil {
			return nil, err
		}
		configs = append(configs, p)
	}
	return configs, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// UpdatePaymentProviderConfig
// ─────────────────────────────────────────────────────────────────────────────

const updatePaymentProviderConfig = `-- name: UpdatePaymentProviderConfig :one
UPDATE payment_provider_configs
SET    provider_account_id = CASE WHEN $3::text  IS NOT NULL THEN $3::text  ELSE provider_account_id END,
       public_config       = CASE WHEN $4::jsonb IS NOT NULL THEN $4::jsonb ELSE public_config END,
       secrets             = CASE WHEN $5::jsonb IS NOT NULL THEN $5::jsonb ELSE secrets END,
       status              = COALESCE(NULLIF($6, ''), status),
       is_active           = COALESCE($7, is_active),
       verification_status = CASE WHEN $5::jsonb IS NOT NULL THEN 'unverified' ELSE verification_status END,
       verified_at         = CASE WHEN $5::jsonb IS NOT NULL THEN NULL         ELSE verified_at         END,
       verification_error  = CASE WHEN $5::jsonb IS NOT NULL THEN NULL         ELSE verification_error  END,
       updated_at          = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, org_id, provider, mode, provider_account_id, public_config, secrets, status, is_active, created_at, updated_at, deleted_at, verification_status, verified_at, verification_error`

// UpdatePaymentProviderConfig applies a partial update to an active config.
// Pass providerAccountID = nil, publicConfig = nil, or secrets = nil to leave
// the existing value untouched. status defaults to existing when empty.
// isActive = nil keeps the existing flag.
//
// Passing a non-nil secrets ALSO clears the verification verdict back to
// 'unverified': what the provider said about the previous credential tells
// us nothing about the new one, and a stale green badge over an untried key
// is the exact failure this column exists to prevent.
func (q *Queries) UpdatePaymentProviderConfig(
	ctx context.Context,
	id, orgID uuid.UUID,
	providerAccountID *string,
	publicConfig, secrets json.RawMessage,
	status string,
	isActive *bool,
) (PaymentProviderConfigRow, error) {
	row := q.db.QueryRow(ctx, updatePaymentProviderConfig,
		id, orgID, providerAccountID, publicConfig, secrets, status, isActive,
	)
	return scanPaymentProviderConfigRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// SoftDeletePaymentProviderConfig
// ─────────────────────────────────────────────────────────────────────────────

const softDeletePaymentProviderConfig = `-- name: SoftDeletePaymentProviderConfig :one
UPDATE payment_provider_configs
SET    deleted_at = now(),
       updated_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, org_id, provider, mode, provider_account_id, public_config, secrets, status, is_active, created_at, updated_at, deleted_at, verification_status, verified_at, verification_error`

// SoftDeletePaymentProviderConfig marks a config as deleted. Returns
// pgx.ErrNoRows when the row does not exist, does not belong to the org,
// or has already been deleted.
func (q *Queries) SoftDeletePaymentProviderConfig(ctx context.Context, id, orgID uuid.UUID) (PaymentProviderConfigRow, error) {
	row := q.db.QueryRow(ctx, softDeletePaymentProviderConfig, id, orgID)
	return scanPaymentProviderConfigRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// SetPaymentProviderConfigVerification
// ─────────────────────────────────────────────────────────────────────────────

const setPaymentProviderConfigVerification = `-- name: SetPaymentProviderConfigVerification :one
UPDATE payment_provider_configs
SET    verification_status = $3,
       verified_at         = CASE WHEN $3 = 'ok' THEN now() ELSE verified_at END,
       verification_error  = NULLIF($4, '')
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, org_id, provider, mode, provider_account_id, public_config, secrets, status, is_active, created_at, updated_at, deleted_at, verification_status, verified_at, verification_error`

// SetPaymentProviderConfigVerification records what the provider answered
// when the credential was last sent to it.
//
// status is 'unverified', 'ok' or 'failed'; detail is the provider's own
// wording of a refusal and is stored as NULL when empty. verified_at moves
// only on 'ok', so a later failure leaves "last known good" readable beside
// the failure itself. updated_at is deliberately untouched — that column
// means an operator edited the row, and a verification is not an edit.
//
// Returns pgx.ErrNoRows when the row does not exist, belongs to another org,
// or has been soft-deleted.
func (q *Queries) SetPaymentProviderConfigVerification(
	ctx context.Context,
	id, orgID uuid.UUID,
	status, detail string,
) (PaymentProviderConfigRow, error) {
	row := q.db.QueryRow(ctx, setPaymentProviderConfigVerification, id, orgID, status, detail)
	return scanPaymentProviderConfigRow(row)
}
