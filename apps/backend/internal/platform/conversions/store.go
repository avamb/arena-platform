package conversions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/metaconnect"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/secretbox"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/trackingconsent"
)

// Settings are one organization's server-side conversion credentials. The two
// secrets never leave the process: no route returns them and no log line
// carries them.
type Settings struct {
	MetaPixelID   string
	MetaCAPIToken string
	// MetaTokenSource says where MetaPixelID and MetaCAPIToken come from:
	// TokenSourceConnection (the organization's Meta connection, spec 66
	// section 4.4), TokenSourceManual (org_conversion_settings) or "" (none).
	MetaTokenSource   string
	MetaTestEventCode string
	GA4MeasurementID  string
	GA4APISecret      string
}

// Meta token sources of Settings.MetaTokenSource.
const (
	TokenSourceConnection = "connection"
	TokenSourceManual     = "manual"
)

// MetaReady reports whether Meta CAPI can be used.
func (s Settings) MetaReady() bool { return s.MetaPixelID != "" && s.MetaCAPIToken != "" }

// GA4Ready reports whether GA4 Measurement Protocol can be used.
func (s Settings) GA4Ready() bool { return s.GA4MeasurementID != "" && s.GA4APISecret != "" }

// OrderData is what a send needs about the paid order.
type OrderData struct {
	Number   string
	Total    int64 // minor units
	Currency string
	IsTest   bool
	Email    string
	Phone    string
	Context  BuyerContext
}

// Delivery is one claimed conversion_deliveries row.
type Delivery struct {
	ID        uuid.UUID
	OrderID   uuid.UUID
	OrgID     uuid.UUID
	Provider  string
	Attempts  int
	CreatedAt time.Time
}

// Store is the persistence the dispatcher and the sweep need.
type Store interface {
	Enqueue(ctx context.Context, orderID uuid.UUID) error
	ClaimDue(ctx context.Context, limit int) ([]Delivery, error)
	Settings(ctx context.Context, orgID uuid.UUID) (Settings, bool, error)
	Order(ctx context.Context, orderID uuid.UUID) (OrderData, error)
	// ConsentForOrder is the ONE place the tracking consent is read from. It is
	// asked at SEND time (spec 62 section 8.1): a refusal after the payment
	// started but before the send stops the conversion. It reads the
	// checkout_sessions columns of P-20 (trackingconsent.ForOrder), which
	// checkout/start and PATCH /v1/public/checkout/{token}/consent write.
	ConsentForOrder(ctx context.Context, orderID uuid.UUID) (bool, error)
	MarkSent(ctx context.Context, id uuid.UUID) error
	MarkSkipped(ctx context.Context, id uuid.UUID, reason string) error
	MarkRetry(ctx context.Context, id uuid.UUID, next time.Time, errMsg string) error
	MarkFailed(ctx context.Context, id uuid.UUID, errMsg string) error
}

// PGStore is the PostgreSQL Store.
type PGStore struct {
	pool *pgxpool.Pool
	// box opens the token of the organization's Meta connection. nil (or a
	// disabled Box) means the connection is ignored and only
	// org_conversion_settings is read.
	box *secretbox.Box
}

// NewPGStore wraps a pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

// WithMetaBox lets the store use the organization's Meta connection (spec 66
// section 4.4) as the first source of the Meta pixel and token. A nil or
// disabled box leaves the manual path alone.
func (s *PGStore) WithMetaBox(box *secretbox.Box) *PGStore {
	s.box = box
	return s
}

// connectionOn reports whether the Meta connection is read at all.
func (s *PGStore) connectionOn() bool { return s.box != nil && s.box.CurrentKeyID() != "" }

// Enqueue queues one delivery per ready provider of the order's organization,
// only for an order that has a stored buyer context (a site checkout). Meta is
// ready with a manual pixel and token, or (when the connection is on) with an
// active Meta connection that has a token and a selected dataset.
// Idempotent: an outbox retry or a repeated event queues nothing twice.
func (s *PGStore) Enqueue(ctx context.Context, orderID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO conversion_deliveries (order_id, org_id, provider)
		SELECT o.id, o.org_id, p.provider
		  FROM orders o
		  JOIN checkout_buyer_contexts bc ON bc.order_id = o.id
		  LEFT JOIN org_conversion_settings st ON st.org_id = o.org_id
		 CROSS JOIN LATERAL (VALUES
		        ('meta'::text, (COALESCE(st.meta_pixel_id, '') <> '' AND COALESCE(st.meta_capi_token, '') <> '')
		                       OR ($2::boolean AND EXISTS (
		                             SELECT 1
		                               FROM org_meta_connections c
		                               JOIN org_meta_assets a ON a.org_id = c.org_id AND a.kind = 'dataset' AND a.selected
		                              WHERE c.org_id = o.org_id AND c.status = 'active' AND c.access_token_enc IS NOT NULL))),
		        ('ga4'::text,  COALESCE(st.ga4_measurement_id, '') <> '' AND COALESCE(st.ga4_api_secret, '') <> '')
		       ) AS p(provider, ready)
		 WHERE o.id = $1 AND p.ready
		ON CONFLICT (order_id, provider, kind) DO NOTHING`, orderID, s.connectionOn())
	return err
}

// leaseFor keeps a claimed row away from other workers while it is sent.
const leaseFor = 5 * time.Minute

// ClaimDue returns up to limit pending rows that are due and leases them.
func (s *PGStore) ClaimDue(ctx context.Context, limit int) ([]Delivery, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE conversion_deliveries d
		   SET next_attempt_at = now() + $2::interval, updated_at = now()
		 WHERE d.id IN (SELECT id FROM conversion_deliveries
		                 WHERE status = 'pending' AND next_attempt_at <= now()
		                 ORDER BY next_attempt_at
		                 LIMIT $1
		                 FOR UPDATE SKIP LOCKED)
		RETURNING d.id, d.order_id, d.org_id, d.provider, d.attempts, d.created_at`,
		limit, fmt.Sprintf("%d seconds", int(leaseFor.Seconds())))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.OrderID, &d.OrgID, &d.Provider, &d.Attempts, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Settings loads the organization's credentials; ok is false when none are
// stored. The Meta pixel and token come from the active Meta connection with
// a selected dataset first (when the connection is on), else from
// org_conversion_settings; the test event code and GA4 always come from
// org_conversion_settings.
func (s *PGStore) Settings(ctx context.Context, orgID uuid.UUID) (Settings, bool, error) {
	var st Settings
	manual := true
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(meta_pixel_id, ''), COALESCE(meta_capi_token, ''), COALESCE(meta_test_event_code, ''),
		       COALESCE(ga4_measurement_id, ''), COALESCE(ga4_api_secret, '')
		  FROM org_conversion_settings WHERE org_id = $1`, orgID).
		Scan(&st.MetaPixelID, &st.MetaCAPIToken, &st.MetaTestEventCode, &st.GA4MeasurementID, &st.GA4APISecret)
	if errors.Is(err, pgx.ErrNoRows) {
		st, manual = Settings{}, false
	} else if err != nil {
		return Settings{}, false, err
	}
	var pixelID, token string
	conn := false
	if s.connectionOn() {
		pixelID, token, conn, err = metaconnect.ActiveDataset(ctx, s.pool, s.box, orgID)
		if err != nil {
			// A database or key error: never fall back to another pixel
			// silently, the delivery retries. secretbox errors carry no
			// secret.
			return Settings{}, false, errors.New("meta connection: " + err.Error())
		}
	}
	st, ok := chooseMeta(st, manual, pixelID, token, conn)
	return st, ok, nil
}

// chooseMeta picks the Meta source: the connection's pixel and token when
// conn, else the manual ones of org_conversion_settings when they are both
// set, else none. The test event code and GA4 stay those of manual.
func chooseMeta(manual Settings, manualOK bool, connPixel, connToken string, conn bool) (Settings, bool) {
	st := manual
	st.MetaTokenSource = ""
	if conn && connPixel != "" && connToken != "" {
		st.MetaPixelID, st.MetaCAPIToken, st.MetaTokenSource = connPixel, connToken, TokenSourceConnection
		return st, true
	}
	if st.MetaReady() {
		st.MetaTokenSource = TokenSourceManual
	}
	return st, manualOK
}

// MarkMetaTokenInvalid moves the organization's active Meta connection to
// error (metaconnect.MarkTokenInvalid); changed is true only once.
func (s *PGStore) MarkMetaTokenInvalid(ctx context.Context, orgID uuid.UUID, reason string) (bool, error) {
	return metaconnect.MarkTokenInvalid(ctx, s.pool, orgID, reason)
}

// RecordMetaError stores reason as the last_error of the organization's
// active Meta connection without changing its status.
func (s *PGStore) RecordMetaError(ctx context.Context, orgID uuid.UUID, reason string) error {
	return metaconnect.RecordError(ctx, s.pool, orgID, reason)
}

// Order loads the paid order and its stored buyer context.
func (s *PGStore) Order(ctx context.Context, orderID uuid.UUID) (OrderData, error) {
	var od OrderData
	var raw []byte
	err := s.pool.QueryRow(ctx, `
		SELECT o.system_id::text, o.total, o.currency, o.is_test,
		       COALESCE(o.buyer_email, ''), COALESCE(o.buyer_phone, ''),
		       COALESCE(bc.context, '{}'::jsonb)
		  FROM orders o
		  LEFT JOIN checkout_buyer_contexts bc ON bc.order_id = o.id
		 WHERE o.id = $1`, orderID).
		Scan(&od.Number, &od.Total, &od.Currency, &od.IsTest, &od.Email, &od.Phone, &raw)
	if err != nil {
		return OrderData{}, err
	}
	_ = json.Unmarshal(raw, &od.Context)
	return od, nil
}

// ConsentForOrder reads the buyer's tracking consent the order's checkout
// carries now (trackingconsent.ForOrder, then Allows: only an explicit yes).
// An unknown order, or one without a checkout, reads as no.
func (s *PGStore) ConsentForOrder(ctx context.Context, orderID uuid.UUID) (bool, error) {
	st, err := trackingconsent.ForOrder(ctx, s.pool, orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return st.Allows(), nil
}

// markTerminal moves one delivery to a final state and, in the same
// transaction, forgets the buyer context of its checkout once no delivery of
// that order is still pending (forgetContextSQL).
func (s *PGStore) markTerminal(ctx context.Context, id uuid.UUID, sql string, args ...any) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, sql, append([]any{id}, args...)...); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, forgetContextSQL, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// forgetContextSQL empties the stored buyer context (IP, User-Agent, fbp,
// fbc, gclid, client id, page URL) of the delivery's order when every
// delivery of that order has reached sent, failed or skipped: nothing will
// read it again. The row itself stays, Enqueue keys on it, so a repeated
// order.paid still queues nothing twice. The column is NOT NULL, so
// "forgotten" is the empty object.
const forgetContextSQL = `
	UPDATE checkout_buyer_contexts bc
	   SET context = '{}'::jsonb
	 WHERE bc.order_id = (SELECT order_id FROM conversion_deliveries WHERE id = $1)
	   AND bc.context <> '{}'::jsonb
	   AND NOT EXISTS (SELECT 1 FROM conversion_deliveries d
	                    WHERE d.order_id = bc.order_id AND d.status = 'pending')`

// ContextRetention is how long a buyer context is kept at most, whatever
// happened to its conversions (an unpaid checkout, an organization without
// conversion settings).
const ContextRetention = 30 * 24 * time.Hour

// purgeBatch bounds one purge statement.
const purgeBatch = 1000

// PurgeContexts empties the buyer contexts written before olderThan and
// returns how many it emptied (at most purgeBatch per call).
func (s *PGStore) PurgeContexts(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE checkout_buyer_contexts SET context = '{}'::jsonb
		 WHERE checkout_session_id IN (
		       SELECT checkout_session_id FROM checkout_buyer_contexts
		        WHERE created_at < $1 AND context <> '{}'::jsonb
		        LIMIT $2)`, olderThan, purgeBatch)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// MarkSent records a delivered conversion.
func (s *PGStore) MarkSent(ctx context.Context, id uuid.UUID) error {
	return s.markTerminal(ctx, id, `
		UPDATE conversion_deliveries
		   SET status = 'sent', attempts = attempts + 1, sent_at = now(), last_error = NULL, updated_at = now()
		 WHERE id = $1`)
}

// MarkSkipped records a conversion that is deliberately not sent.
func (s *PGStore) MarkSkipped(ctx context.Context, id uuid.UUID, reason string) error {
	return s.markTerminal(ctx, id, `
		UPDATE conversion_deliveries SET status = 'skipped', last_error = $2, updated_at = now() WHERE id = $1`, reason)
}

// MarkRetry schedules the next attempt.
func (s *PGStore) MarkRetry(ctx context.Context, id uuid.UUID, next time.Time, errMsg string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE conversion_deliveries
		   SET attempts = attempts + 1, next_attempt_at = $2, last_error = $3, updated_at = now()
		 WHERE id = $1`, id, next, errMsg)
	return err
}

// MarkFailed gives the row up for good.
func (s *PGStore) MarkFailed(ctx context.Context, id uuid.UUID, errMsg string) error {
	return s.markTerminal(ctx, id, `
		UPDATE conversion_deliveries
		   SET status = 'failed', attempts = attempts + 1, last_error = $2, updated_at = now()
		 WHERE id = $1`, errMsg)
}
