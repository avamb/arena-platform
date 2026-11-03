package disputes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is what the package needs from the pool.
type DB interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// querier is a pool or a transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ErrNotFound: no such dispute in the organization.
var ErrNotFound = errors.New("disputes: not found")

const disputeColumns = `id, org_id, provider, provider_dispute_id, channel, payment_intent_id, order_id,
	config_id, amount_minor, currency, reason, reason_group, provider_status, state, is_inquiry,
	is_charge_refundable, due_by, due_by_source, respond_to, funds_withdrawn_at, funds_reinstated_at,
	outcome, closed_at, low_value, opened_at, submitted_at, submitted_by, accepted_at, accepted_by,
	created_at, updated_at`

func scanDispute(row pgx.Row) (Dispute, error) {
	var d Dispute
	err := row.Scan(&d.ID, &d.OrgID, &d.Provider, &d.ProviderDisputeID, &d.Channel, &d.PaymentIntentID, &d.OrderID,
		&d.ConfigID, &d.AmountMinor, &d.Currency, &d.Reason, &d.ReasonGroup, &d.ProviderStatus, &d.State, &d.IsInquiry,
		&d.IsChargeRefundable, &d.DueBy, &d.DueBySource, &d.RespondTo, &d.FundsWithdrawnAt, &d.FundsReinstatedAt,
		&d.Outcome, &d.ClosedAt, &d.LowValue, &d.OpenedAt, &d.SubmittedAt, &d.SubmittedBy, &d.AcceptedAt, &d.AcceptedBy,
		&d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Dispute{}, ErrNotFound
	}
	return d, err
}

// getDispute loads one dispute of the organization; forUpdate locks it.
func getDispute(ctx context.Context, q querier, orgID, id uuid.UUID, forUpdate bool) (Dispute, error) {
	sql := `SELECT ` + disputeColumns + ` FROM payment_disputes WHERE id = $1 AND org_id = $2`
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	return scanDispute(q.QueryRow(ctx, sql, id, orgID))
}

// getDisputeAnyOrg loads a dispute by id alone (jobs, which carry only the id).
func getDisputeAnyOrg(ctx context.Context, q querier, id uuid.UUID, forUpdate bool) (Dispute, error) {
	sql := `SELECT ` + disputeColumns + ` FROM payment_disputes WHERE id = $1`
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	return scanDispute(q.QueryRow(ctx, sql, id))
}

func getDisputeByProviderID(ctx context.Context, q querier, provider, providerID string) (Dispute, error) {
	return scanDispute(q.QueryRow(ctx, `SELECT `+disputeColumns+` FROM payment_disputes
		WHERE provider = $1 AND provider_dispute_id = $2 FOR UPDATE`, provider, providerID))
}

// ListFilter narrows List.
type ListFilter struct {
	// Group: "open" (needs a response), "review" (answered, waiting), "closed".
	Group   string
	OrderID *uuid.UUID
	Limit   int
	Offset  int
}

func groupStates(group string) []string {
	switch group {
	case "open":
		return []string{StateNeedsResponse, StateDraftReady}
	case "review":
		return []string{StateSubmitted, StateUnderReview}
	case "closed":
		return []string{StateAccepted, StateExpired, StateClosed}
	}
	return nil
}

// List returns the organization's disputes, the most urgent first: open
// ones by deadline, then the rest newest first.
func (s *Service) List(ctx context.Context, orgID uuid.UUID, f ListFilter) ([]Dispute, int, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	states := groupStates(f.Group)
	args := []any{orgID, states, f.OrderID}
	where := `org_id = $1 AND ($2::text[] IS NULL OR state = ANY($2)) AND ($3::uuid IS NULL OR order_id = $3)`
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM payment_disputes WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+disputeColumns+` FROM payment_disputes WHERE `+where+`
		ORDER BY (state IN ('needs_response','draft_ready')) DESC, due_by NULLS LAST, created_at DESC, id
		LIMIT $4 OFFSET $5`, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Dispute
	for rows.Next() {
		d, err := scanDispute(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	return out, total, rows.Err()
}

// CountOpen counts the disputes that still wait for an answer.
func (s *Service) CountOpen(ctx context.Context, orgID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM payment_disputes
		WHERE org_id = $1 AND state IN ('needs_response','draft_ready')`, orgID).Scan(&n)
	return n, err
}

// Get returns one dispute of the organization.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Dispute, error) {
	return getDispute(ctx, s.db, orgID, id, false)
}

// Event is one payment_dispute_events row.
type Event struct {
	ID             uuid.UUID
	Kind           string
	ProviderStatus string
	ActorUserID    *uuid.UUID
	ActorLabel     string
	Payload        json.RawMessage
	CreatedAt      time.Time
}

// Events returns the journal of a dispute, oldest first.
func (s *Service) Events(ctx context.Context, orgID, id uuid.UUID) ([]Event, error) {
	rows, err := s.db.Query(ctx, `SELECT id, kind, provider_status, actor_user_id, actor_label, payload, created_at
		FROM payment_dispute_events WHERE dispute_id = $1 AND org_id = $2 ORDER BY created_at, id`, id, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Kind, &e.ProviderStatus, &e.ActorUserID, &e.ActorLabel, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Actor is the person (or job) behind an action.
type Actor struct {
	UserID *uuid.UUID
	Label  string
}

// insertEvent appends to the journal. A provider event id seen before for
// the dispute is not written twice; inserted reports whether it was new.
func insertEvent(ctx context.Context, q querier, d Dispute, kind, providerEventID, providerStatus string, actor Actor, payload map[string]any) (bool, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	var evID *string
	if providerEventID != "" {
		evID = &providerEventID
	}
	tag, err := q.Exec(ctx, `INSERT INTO payment_dispute_events
		(dispute_id, org_id, kind, provider_event_id, provider_status, actor_user_id, actor_label, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (dispute_id, provider_event_id) WHERE provider_event_id IS NOT NULL DO NOTHING`,
		d.ID, d.OrgID, kind, evID, providerStatus, actor.UserID, actor.Label, body)
	if err != nil {
		return false, fmt.Errorf("disputes: journal %s: %w", kind, err)
	}
	return tag.RowsAffected() == 1, nil
}

// Package is one payment_dispute_packages row with its files (no bytes).
type Package struct {
	ID              uuid.UUID
	Version         int
	Locale          string
	Fields          map[string]string
	Gaps            []string
	ProviderFileIDs map[string]string
	EmailDraft      *EmailDraft
	TotalBytes      int
	TotalPages      int
	BuiltBy         *uuid.UUID
	BuiltAt         time.Time
	Files           []FileInfo
}

// FileInfo describes a stored file.
type FileInfo struct {
	ID          uuid.UUID
	Kind        string
	Filename    string
	Bytes       int
	Pages       int
	SHA256      string
	ForProvider bool
}

// EmailDraft is the letter of an e-mail channel answer.
type EmailDraft struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// LatestPackage returns the newest build of a dispute, nil when none.
func (s *Service) LatestPackage(ctx context.Context, orgID, disputeID uuid.UUID) (*Package, error) {
	return latestPackage(ctx, s.db, orgID, disputeID)
}

func latestPackage(ctx context.Context, q querier, orgID, disputeID uuid.UUID) (*Package, error) {
	var p Package
	var fields, gaps, fileIDs []byte
	var draft []byte
	err := q.QueryRow(ctx, `SELECT id, version, locale, fields, gaps, provider_file_ids, email_draft,
			total_bytes, total_pages, built_by, built_at
		FROM payment_dispute_packages WHERE dispute_id = $1 AND org_id = $2
		ORDER BY version DESC LIMIT 1`, disputeID, orgID).
		Scan(&p.ID, &p.Version, &p.Locale, &fields, &gaps, &fileIDs, &draft, &p.TotalBytes, &p.TotalPages, &p.BuiltBy, &p.BuiltAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(fields, &p.Fields)
	_ = json.Unmarshal(gaps, &p.Gaps)
	_ = json.Unmarshal(fileIDs, &p.ProviderFileIDs)
	if len(draft) > 0 && string(draft) != "null" {
		var d EmailDraft
		if json.Unmarshal(draft, &d) == nil {
			p.EmailDraft = &d
		}
	}
	rows, err := q.Query(ctx, `SELECT id, kind, filename, bytes, pages, sha256, for_provider
		FROM payment_dispute_files WHERE package_id = $1 ORDER BY for_provider DESC, kind`, p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f FileInfo
		if err := rows.Scan(&f.ID, &f.Kind, &f.Filename, &f.Bytes, &f.Pages, &f.SHA256, &f.ForProvider); err != nil {
			return nil, err
		}
		p.Files = append(p.Files, f)
	}
	return &p, rows.Err()
}

// File returns one stored file of a dispute's package, with its bytes.
func (s *Service) File(ctx context.Context, orgID, disputeID, fileID uuid.UUID) (FileInfo, []byte, error) {
	var f FileInfo
	var content []byte
	err := s.db.QueryRow(ctx, `SELECT f.id, f.kind, f.filename, f.bytes, f.pages, f.sha256, f.for_provider, f.content
		FROM payment_dispute_files f JOIN payment_dispute_packages p ON p.id = f.package_id
		WHERE f.id = $1 AND p.dispute_id = $2 AND p.org_id = $3`, fileID, disputeID, orgID).
		Scan(&f.ID, &f.Kind, &f.Filename, &f.Bytes, &f.Pages, &f.SHA256, &f.ForProvider, &content)
	if errors.Is(err, pgx.ErrNoRows) {
		return FileInfo{}, nil, ErrNotFound
	}
	return f, content, err
}

// packageFiles returns the bytes of the provider files of a package.
func packageFiles(ctx context.Context, q querier, packageID uuid.UUID) (map[string]storedFile, error) {
	rows, err := q.Query(ctx, `SELECT kind, filename, content FROM payment_dispute_files
		WHERE package_id = $1 AND for_provider`, packageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]storedFile{}
	for rows.Next() {
		var f storedFile
		if err := rows.Scan(&f.Kind, &f.Filename, &f.Content); err != nil {
			return nil, err
		}
		out[f.Kind] = f
	}
	return out, rows.Err()
}

type storedFile struct {
	Kind     string
	Filename string
	Content  []byte
}

// Settings is the low-value rule of decision R-11 in force for an organization.
type Settings struct {
	Enabled    bool             `json:"low_value_enabled"`
	Thresholds map[string]int64 `json:"low_value_thresholds"`
	// Own: the organization has its own row (else the platform default).
	Own bool `json:"own"`
}

// SettingsFor returns the organization's row, else the platform default.
func (s *Service) SettingsFor(ctx context.Context, orgID uuid.UUID) (Settings, error) {
	return settingsFor(ctx, s.db, orgID)
}

func settingsFor(ctx context.Context, q querier, orgID uuid.UUID) (Settings, error) {
	var st Settings
	var raw []byte
	var own bool
	err := q.QueryRow(ctx, `SELECT low_value_enabled, low_value_thresholds, org_id IS NOT NULL
		FROM dispute_settings WHERE org_id = $1 OR org_id IS NULL
		ORDER BY org_id NULLS LAST LIMIT 1`, orgID).Scan(&st.Enabled, &raw, &own)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{Thresholds: map[string]int64{}}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	st.Own = own
	st.Thresholds = map[string]int64{}
	_ = json.Unmarshal(raw, &st.Thresholds)
	return st, nil
}

// IsLowValue applies the rule to an amount.
func (st Settings) IsLowValue(amountMinor int64, currency string) bool {
	if !st.Enabled {
		return false
	}
	t := st.Threshold(currency)
	return t > 0 && amountMinor < t
}

// Threshold returns the threshold of a currency (0 when none applies).
func (st Settings) Threshold(currency string) int64 {
	if !st.Enabled {
		return 0
	}
	return st.Thresholds[strings.ToUpper(strings.TrimSpace(currency))]
}

// SaveSettings stores the organization's own rule.
func (s *Service) SaveSettings(ctx context.Context, orgID uuid.UUID, enabled bool, thresholds map[string]int64, by *uuid.UUID) (Settings, error) {
	body, err := json.Marshal(thresholds)
	if err != nil {
		return Settings{}, err
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO dispute_settings (org_id, low_value_enabled, low_value_thresholds, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (org_id) WHERE org_id IS NOT NULL
		DO UPDATE SET low_value_enabled = EXCLUDED.low_value_enabled,
		              low_value_thresholds = EXCLUDED.low_value_thresholds,
		              updated_by = EXCLUDED.updated_by, updated_at = now()`, orgID, enabled, body, by); err != nil {
		return Settings{}, err
	}
	return s.SettingsFor(ctx, orgID)
}
