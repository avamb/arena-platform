// Package promoattempts is the journal of promo-code checks
// (promo_code_attempts, migration 0161).
//
// Every surface that judges a code — the Bil24 gateway (ADD_PROMO_CODES,
// CHECK_KDP, CREATE_ORDER_EXT), the widget's checkout/start and the org
// validate endpoint — hands the verdict of the single rule
// (hcheckout.ValidatePromoForLines + CheckPromoLimits) to Recorder.Record.
// Reason maps that rule's error codes onto the journal's machine reasons, so
// no surface keeps its own list.
//
// Recording can never break a purchase: Record logs the attempt, then writes
// the row on its own pool connection in a background goroutine (a bounded
// number at a time, the rest is dropped with a warning), so it never touches
// the caller's transaction, never waits for a pool slot while the caller
// holds a row lock, and a failed insert only produces a warning.
//
// The row carries no e-mail, name or phone; the client address is stored as a
// salted hash only.
package promoattempts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Surfaces, as stored.
const (
	SurfaceGatewayAddPromoCodes = "gateway_add_promo_codes"
	SurfaceGatewayCheckKDP      = "gateway_check_kdp"
	SurfaceGatewayCreateOrder   = "gateway_create_order"
	SurfaceWidgetCheckout       = "widget_checkout"
	SurfaceWidgetQuote          = "widget_quote"
	SurfaceOrgValidate          = "org_validate"
)

// Results, as stored.
const (
	ResultApplied  = "applied"
	ResultRejected = "rejected"
)

// Machine reasons of a rejected attempt, as stored.
const (
	ReasonUnknownCode        = "unknown_code"
	ReasonWrongSession       = "wrong_session"
	ReasonWrongTier          = "wrong_tier"
	ReasonOutsideWindow      = "outside_window"
	ReasonExpired            = "expired"
	ReasonMaxUses            = "max_uses"
	ReasonMaxUsesPerCustomer = "max_uses_per_customer"
	ReasonMinOrder           = "min_order"
	ReasonCurrency           = "currency"
	ReasonPaused             = "paused"
	ReasonOther              = "other"
)

const (
	maxCodeRunes  = 64
	dedupWindow   = time.Minute
	maxInFlight   = 32
	writeTimeout  = 5 * time.Second
	purgeBatch    = 5000
	maxPurgeBatch = 20
)

// Retention is how long attempts are kept.
const Retention = 90 * 24 * time.Hour

// Reason maps an error code of hcheckout's promo rule (or the gateway's
// "promo.not_found" sentinel) onto a journal reason. An empty code is not a
// rejection and yields "".
func Reason(errCode string) string {
	switch errCode {
	case "":
		return ""
	case "promo.not_found", "promo.lookup_failed":
		return ReasonUnknownCode
	case "promo.not_active":
		return ReasonPaused
	case "promo.not_yet_valid":
		return ReasonOutsideWindow
	case "promo.expired":
		return ReasonExpired
	case "promo.session_not_applicable":
		return ReasonWrongSession
	case "promo.tier_not_applicable":
		return ReasonWrongTier
	case "promo.invalid_order_amount":
		return ReasonMinOrder
	case "promo.currency_mismatch":
		return ReasonCurrency
	case "promo.exhausted":
		return ReasonMaxUses
	case "promo.per_customer_limit":
		return ReasonMaxUsesPerCustomer
	default:
		return ReasonOther
	}
}

// Attempt is one check of one code.
type Attempt struct {
	OrgID     uuid.UUID
	ChannelID uuid.UUID // uuid.Nil when unknown
	Surface   string
	// Code is what the buyer typed.
	Code string
	// PromoCodeID is the code's row when it was found, uuid.Nil otherwise.
	PromoCodeID uuid.UUID
	// SessionIDs are the event sessions of the cart (invalid ids are dropped).
	SessionIDs []string
	// ErrCode is the rule's verdict: "" = applied.
	ErrCode string
	// CustomerID / GatewaySessionID are stored when the surface knows them.
	CustomerID       uuid.UUID
	GatewaySessionID uuid.UUID
	// ClientIP is hashed before it is stored or compared; "" = unknown.
	ClientIP string
	// CartKey identifies the cart for the one-minute collapse of repeats
	// (the gateway session id, ...). Empty falls back to the client address.
	CartKey string
}

// Row is an attempt as it is written.
type Row struct {
	Attempt
	Code     string
	Result   string
	Reason   string
	IPHash   string
	DedupKey string
	Sessions []uuid.UUID
}

// Recorder writes attempts. A nil *Recorder is a valid no-op.
type Recorder struct {
	pool   *pgxpool.Pool
	salt   string
	logger *slog.Logger
	sem    chan struct{}
	wg     sync.WaitGroup
	sync   bool
	insert func(ctx context.Context, rw Row) error
}

// New builds a Recorder over pool. salt keys the client-address hash.
func New(pool *pgxpool.Pool, salt string, logger *slog.Logger) *Recorder {
	if logger == nil {
		logger = slog.Default()
	}
	r := &Recorder{pool: pool, salt: salt, logger: logger, sem: make(chan struct{}, maxInFlight)}
	r.insert = r.insertRow
	return r
}

// WithSyncWrites makes Record write before it returns. For tests only.
func (r *Recorder) WithSyncWrites() *Recorder {
	if r != nil {
		r.sync = true
	}
	return r
}

// WithInsert replaces the insert step. For tests only.
func (r *Recorder) WithInsert(f func(ctx context.Context, rw Row) error) *Recorder {
	if r != nil {
		r.insert = f
	}
	return r
}

// Wait blocks until the background writes started so far have finished.
func (r *Recorder) Wait() {
	if r != nil {
		r.wg.Wait()
	}
}

// Record journals one attempt. It never returns an error and never blocks on
// the database.
func (r *Recorder) Record(ctx context.Context, a Attempt) {
	if r == nil || a.OrgID == uuid.Nil || strings.TrimSpace(a.Code) == "" {
		return
	}
	defer func() { _ = recover() }()

	rw := Row{Attempt: a, Code: cutRunes(strings.TrimSpace(a.Code), maxCodeRunes), Result: ResultApplied}
	if a.ErrCode != "" {
		rw.Result, rw.Reason = ResultRejected, Reason(a.ErrCode)
	}
	rw.IPHash = r.HashIP(a.ClientIP)
	rw.Sessions = parseUUIDs(a.SessionIDs)
	rw.DedupKey = a.CartKey
	if rw.DedupKey == "" {
		rw.DedupKey = "ip:" + rw.IPHash
	}

	attrs := []any{
		slog.String("surface", a.Surface),
		slog.String("code", rw.Code),
		slog.String("result", rw.Result),
		slog.String("org_id", a.OrgID.String()),
	}
	if rw.Reason != "" {
		attrs = append(attrs, slog.String("reason", rw.Reason))
	}
	if a.ChannelID != uuid.Nil {
		attrs = append(attrs, slog.String("channel_id", a.ChannelID.String()))
	}
	r.logger.InfoContext(ctx, "promo_code_attempt", attrs...)

	write := func() {
		defer func() {
			if p := recover(); p != nil {
				r.logger.Warn("promo_code_attempt: write panicked", slog.Any("panic", p))
			}
		}()
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
		defer cancel()
		if err := r.insert(wctx, rw); err != nil {
			r.logger.Warn("promo_code_attempt: write failed",
				slog.String("org_id", a.OrgID.String()), slog.String("error", err.Error()))
		}
	}
	if r.sync {
		write()
		return
	}
	select {
	case r.sem <- struct{}{}:
	default:
		r.logger.Warn("promo_code_attempt: journal busy, attempt not stored",
			slog.String("org_id", a.OrgID.String()))
		return
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer func() { <-r.sem }()
		write()
	}()
}

// HashIP is the salted hash stored for a client address; "" stays "".
func (r *Recorder) HashIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if r == nil || ip == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(r.salt + "|promo-attempt|" + ip))
	return hex.EncodeToString(sum[:])
}

func (r *Recorder) insertRow(ctx context.Context, rw Row) error {
	// Repeats of the same check in the same cart within a minute collapse
	// into the first row.
	const q = `
INSERT INTO promo_code_attempts
  (org_id, channel_id, surface, code_entered, promo_code_id, session_ids,
   result, reason, customer_id, gateway_session_id, ip_hash, dedup_key)
SELECT $1::uuid, $2::uuid, $3::text, $4::text, $5::uuid, $6::uuid[],
       $7::text, $8::text, $9::uuid, $10::uuid, $11::text, $12::text
WHERE NOT EXISTS (
  SELECT 1 FROM promo_code_attempts p
   WHERE p.org_id = $1::uuid AND p.dedup_key = $12::text AND p.surface = $3::text
     AND lower(p.code_entered) = lower($4::text) AND p.result = $7::text
     AND p.reason IS NOT DISTINCT FROM $8::text
     AND p.created_at > now() - make_interval(secs => $13::double precision))`
	_, err := r.pool.Exec(ctx, q,
		rw.OrgID, nilUUID(rw.ChannelID), rw.Surface, rw.Code, nilUUID(rw.PromoCodeID), rw.Sessions,
		rw.Result, nilString(rw.Reason), nilUUID(rw.CustomerID), nilUUID(rw.GatewaySessionID),
		nilString(rw.IPHash), rw.DedupKey, dedupWindow.Seconds())
	return err
}

func nilUUID(u uuid.UUID) *uuid.UUID {
	if u == uuid.Nil {
		return nil
	}
	return &u
}

func nilString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func parseUUIDs(in []string) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(in))
	seen := map[uuid.UUID]struct{}{}
	for _, s := range in {
		u, err := uuid.Parse(strings.TrimSpace(s))
		if err != nil || u == uuid.Nil {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}

func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// Purge deletes attempts older than Retention in bounded batches and reports
// how many it removed.
func Purge(ctx context.Context, pool *pgxpool.Pool, now time.Time) (int64, error) {
	var total int64
	for i := 0; i < maxPurgeBatch; i++ {
		tag, err := pool.Exec(ctx, `
DELETE FROM promo_code_attempts
 WHERE id = ANY(ARRAY(SELECT id FROM promo_code_attempts WHERE created_at < $1 ORDER BY created_at LIMIT $2))`,
			now.Add(-Retention), purgeBatch)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < purgeBatch {
			break
		}
	}
	return total, nil
}
