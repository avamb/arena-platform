package metaconnect

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/secretbox"
)

// Connection statuses, as stored in org_meta_connections.status.
const (
	StatusActive  = "active"
	StatusRevoked = "revoked"
	StatusError   = "error"
)

// Querier is what the store needs of a pool or a transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var (
	// ErrNotConnected means the organization has no connection row.
	ErrNotConnected = errors.New("metaconnect: the organization has no Meta connection")
	// ErrAssetNotFound means the meta id is not an asset of this kind of this
	// organization's connection.
	ErrAssetNotFound = errors.New("metaconnect: no such Meta asset in this organization")
)

// tokenAAD binds a sealed token to its organization: a value copied to
// another organization's row does not open.
func tokenAAD(orgID uuid.UUID) []byte { return orgID[:] }

// SealToken seals an access token for orgID.
func SealToken(box *secretbox.Box, orgID uuid.UUID, token string) (string, error) {
	return box.Seal([]byte(token), tokenAAD(orgID))
}

// OpenToken opens a value made by SealToken for the same orgID.
func OpenToken(box *secretbox.Box, orgID uuid.UUID, sealed string) (string, error) {
	pt, err := box.Open(sealed, tokenAAD(orgID))
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// UserCanConnect reports whether userID still holds org.update in orgID
// through an ACTIVE membership of a live organization (the callback has no
// login, so the state's user is checked again in the database). A user who
// is deactivated, or who is not a member, is refused even with a global role.
func UserCanConnect(ctx context.Context, q Querier, userID, orgID uuid.UUID) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1
		    FROM memberships m
		    JOIN users u         ON u.id = m.user_id AND u.deactivated_at IS NULL
		    JOIN organizations o ON o.id = m.org_id AND o.deleted_at IS NULL
		   WHERE m.user_id = $1 AND m.org_id = $2 AND m.status = 'active'
		     AND EXISTS (
		       SELECT 1
		         FROM roles r
		         JOIN role_permissions rp ON rp.role_id = r.id
		         JOIN permissions p       ON p.id = rp.permission_id AND p.name = 'org.update'
		        WHERE r.org_id IS NULL
		          AND (r.name = m.role
		               OR r.id IN (SELECT ur.role_id FROM user_roles ur WHERE ur.user_id = $1 AND ur.org_id IS NULL))))`,
		userID, orgID).Scan(&ok)
	return ok, err
}

// NewConnection is what a finished login stores.
type NewConnection struct {
	OrgID      uuid.UUID
	UserID     uuid.UUID
	BusinessID string
	MetaUserID string
	ConfigID   string
	Token      Token
	Scopes     []string
	Assets     []Asset
}

// SaveConnection upserts the organization's connection as active with the
// token sealed, and replaces its assets. A previous selection is kept when
// its asset is still offered; otherwise a kind with exactly one asset gets it
// selected.
func SaveConnection(ctx context.Context, tx pgx.Tx, box *secretbox.Box, c NewConnection) error {
	sealed, err := SealToken(box, c.OrgID, c.Token.AccessToken)
	if err != nil {
		return err
	}
	scopes := c.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO org_meta_connections
		       (org_id, meta_business_id, meta_user_id, config_id, access_token_enc, token_expires_at,
		        scopes, status, last_error, connected_by, connected_at, checked_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'active', NULL, $8, now(), now(), now())
		ON CONFLICT (org_id) DO UPDATE SET
		       meta_business_id = EXCLUDED.meta_business_id,
		       meta_user_id     = EXCLUDED.meta_user_id,
		       config_id        = EXCLUDED.config_id,
		       access_token_enc = EXCLUDED.access_token_enc,
		       token_expires_at = EXCLUDED.token_expires_at,
		       scopes           = EXCLUDED.scopes,
		       status           = 'active',
		       last_error       = NULL,
		       connected_by     = EXCLUDED.connected_by,
		       connected_at     = now(),
		       checked_at       = now(),
		       updated_at       = now()`,
		c.OrgID, c.BusinessID, c.MetaUserID, c.ConfigID, sealed, c.Token.ExpiresAt, scopes, c.UserID); err != nil {
		return err
	}
	prev := map[string]string{}
	rows, err := tx.Query(ctx, `SELECT kind, meta_id FROM org_meta_assets WHERE org_id = $1 AND selected`, c.OrgID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k, id string
		if err := rows.Scan(&k, &id); err != nil {
			rows.Close()
			return err
		}
		prev[k] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM org_meta_assets WHERE org_id = $1`, c.OrgID); err != nil {
		return err
	}
	count := map[string]int{}
	has := map[string]bool{}
	for _, a := range c.Assets {
		count[a.Kind]++
		has[a.Kind+"\x00"+a.MetaID] = true
	}
	for _, a := range c.Assets {
		selected := false
		if id, ok := prev[a.Kind]; ok && has[a.Kind+"\x00"+id] {
			selected = id == a.MetaID
		} else {
			selected = count[a.Kind] == 1
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO org_meta_assets (org_id, kind, meta_id, name, currency, timezone, selected)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7)
			ON CONFLICT (org_id, kind, meta_id) DO NOTHING`,
			c.OrgID, a.Kind, a.MetaID, a.Name, a.Currency, a.Timezone, selected); err != nil {
			return err
		}
	}
	return nil
}

// Connection is a stored connection without its token.
type Connection struct {
	OrgID          uuid.UUID
	MetaBusinessID string
	MetaUserID     string
	ConfigID       string
	TokenExpiresAt *time.Time
	Scopes         []string
	Status         string
	LastError      string
	ConnectedBy    *uuid.UUID
	ConnectedAt    time.Time
	CheckedAt      *time.Time
	UpdatedAt      time.Time
}

// StoredAsset is an asset with its selection.
type StoredAsset struct {
	Asset
	Selected bool
}

// Load reads the organization's connection and assets (ErrNotConnected when
// there is none). The token is never read.
func Load(ctx context.Context, q Querier, orgID uuid.UUID) (Connection, []StoredAsset, error) {
	c := Connection{OrgID: orgID}
	err := q.QueryRow(ctx, `
		SELECT meta_business_id, meta_user_id, config_id, token_expires_at, scopes, status,
		       COALESCE(last_error, ''), connected_by, connected_at, checked_at, updated_at
		  FROM org_meta_connections WHERE org_id = $1`, orgID).
		Scan(&c.MetaBusinessID, &c.MetaUserID, &c.ConfigID, &c.TokenExpiresAt, &c.Scopes, &c.Status,
			&c.LastError, &c.ConnectedBy, &c.ConnectedAt, &c.CheckedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, nil, ErrNotConnected
	}
	if err != nil {
		return Connection{}, nil, err
	}
	if c.Scopes == nil {
		c.Scopes = []string{}
	}
	rows, err := q.Query(ctx, `
		SELECT kind, meta_id, name, COALESCE(currency, ''), COALESCE(timezone, ''), selected
		  FROM org_meta_assets WHERE org_id = $1
		 ORDER BY kind, selected DESC, name, meta_id`, orgID)
	if err != nil {
		return Connection{}, nil, err
	}
	defer rows.Close()
	assets := []StoredAsset{}
	for rows.Next() {
		var a StoredAsset
		if err := rows.Scan(&a.Kind, &a.MetaID, &a.Name, &a.Currency, &a.Timezone, &a.Selected); err != nil {
			return Connection{}, nil, err
		}
		assets = append(assets, a)
	}
	return c, assets, rows.Err()
}

// Select marks metaID as the selected asset of kind (ErrNotConnected,
// ErrAssetNotFound). Run it in a transaction.
func Select(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, kind, metaID string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM org_meta_connections WHERE org_id = $1)`, orgID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotConnected
	}
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM org_meta_assets WHERE org_id = $1 AND kind = $2 AND meta_id = $3)`,
		orgID, kind, metaID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrAssetNotFound
	}
	if _, err := tx.Exec(ctx, `
		UPDATE org_meta_assets SET selected = false
		 WHERE org_id = $1 AND kind = $2 AND selected AND meta_id <> $3`, orgID, kind, metaID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE org_meta_assets SET selected = true WHERE org_id = $1 AND kind = $2 AND meta_id = $3`,
		orgID, kind, metaID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE org_meta_connections SET updated_at = now() WHERE org_id = $1`, orgID)
	return err
}

// SealedToken reads the sealed token and the status of the organization's
// connection (ErrNotConnected). sealed is "" after a revoke.
func SealedToken(ctx context.Context, q Querier, orgID uuid.UUID) (sealed, status string, err error) {
	var s *string
	err = q.QueryRow(ctx, `SELECT access_token_enc, status FROM org_meta_connections WHERE org_id = $1`, orgID).Scan(&s, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotConnected
	}
	if err != nil {
		return "", "", err
	}
	if s != nil {
		sealed = *s
	}
	return sealed, status, nil
}

// Revoke sets the connection to revoked and wipes its token; deleteAssets
// also removes its assets. It reports whether a row was found.
func Revoke(ctx context.Context, q Querier, orgID uuid.UUID, reason string, deleteAssets bool) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE org_meta_connections
		   SET status = 'revoked', access_token_enc = NULL, last_error = NULLIF($2, ''), updated_at = now()
		 WHERE org_id = $1`, orgID, clip(reason, 200))
	if err != nil {
		return false, err
	}
	if deleteAssets {
		if _, err := q.Exec(ctx, `DELETE FROM org_meta_assets WHERE org_id = $1`, orgID); err != nil {
			return false, err
		}
	}
	return tag.RowsAffected() > 0, nil
}

// Delete removes the organization's connection and assets (data deletion).
func Delete(ctx context.Context, q Querier, orgID uuid.UUID) error {
	if _, err := q.Exec(ctx, `DELETE FROM org_meta_assets WHERE org_id = $1`, orgID); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `DELETE FROM org_meta_connections WHERE org_id = $1`, orgID)
	return err
}

// OrgsByMetaUser lists the organizations whose connection was made by the
// app-scoped Meta user id (the only id of Meta's callbacks).
func OrgsByMetaUser(ctx context.Context, q Querier, metaUserID string) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `SELECT org_id FROM org_meta_connections WHERE meta_user_id = $1 ORDER BY org_id`, metaUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ActiveDataset is what the Conversions API stage reads: the selected dataset
// (pixel) id and the opened token of an ACTIVE connection. ok is false when
// the organization has no active connection, no token or no selected dataset.
func ActiveDataset(ctx context.Context, q Querier, box *secretbox.Box, orgID uuid.UUID) (pixelID, token string, ok bool, err error) {
	var sealed string
	err = q.QueryRow(ctx, `
		SELECT a.meta_id, c.access_token_enc
		  FROM org_meta_connections c
		  JOIN org_meta_assets a ON a.org_id = c.org_id AND a.kind = 'dataset' AND a.selected
		 WHERE c.org_id = $1 AND c.status = 'active' AND c.access_token_enc IS NOT NULL`, orgID).Scan(&pixelID, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	token, err = OpenToken(box, orgID, sealed)
	if err != nil {
		return "", "", false, err
	}
	return pixelID, token, true, nil
}

// MarkTokenInvalid moves an ACTIVE connection to error with reason (a short
// code, never a token) and wipes the dead token. It reports true only for the
// call that changed the row, so the owner is told once.
func MarkTokenInvalid(ctx context.Context, q Querier, orgID uuid.UUID, reason string) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE org_meta_connections
		   SET status = 'error', access_token_enc = NULL, last_error = $2, checked_at = now(), updated_at = now()
		 WHERE org_id = $1 AND status = 'active'`, orgID, clip(reason, 200))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// RecordError stores reason (a short code, never a token) as the last_error
// of an ACTIVE connection without changing its status: a failure that does
// not break the connection (no permission on one pixel, codes 10 and
// 200-299) stays visible in the connection's status.
func RecordError(ctx context.Context, q Querier, orgID uuid.UUID, reason string) error {
	_, err := q.Exec(ctx, `
		UPDATE org_meta_connections
		   SET last_error = $2, checked_at = now(), updated_at = now()
		 WHERE org_id = $1 AND status = 'active'`, orgID, clip(reason, 200))
	return err
}
