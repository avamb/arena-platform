package sessionchange

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Record is one session_changes row as the letter worker reads it.
type Record struct {
	ID        uuid.UUID
	SessionID uuid.UUID
	EventID   uuid.UUID
	OrgID     uuid.UUID
	Kinds     []string
	Old       State
	New       State
	Message   string
	CreatedAt time.Time
}

const loadChangeSQL = `
SELECT id, session_id, event_id, org_id, kinds, old_state, new_state, message, created_at
FROM   session_changes
WHERE  id = $1`

// LoadChange reads a journal row.
func LoadChange(ctx context.Context, db DB, changeID uuid.UUID) (Record, error) {
	var (
		r      Record
		oldRaw []byte
		newRaw []byte
	)
	if err := db.QueryRow(ctx, loadChangeSQL, changeID).Scan(&r.ID, &r.SessionID, &r.EventID, &r.OrgID,
		&r.Kinds, &oldRaw, &newRaw, &r.Message, &r.CreatedAt); err != nil {
		return Record{}, err
	}
	if err := json.Unmarshal(oldRaw, &r.Old); err != nil {
		return Record{}, fmt.Errorf("sessionchange: decode old state: %w", err)
	}
	if err := json.Unmarshal(newRaw, &r.New); err != nil {
		return Record{}, fmt.Errorf("sessionchange: decode new state: %w", err)
	}
	return r, nil
}

const loadNoticeStateSQL = `
SELECT state FROM session_change_notices WHERE change_id = $1 AND order_id = $2`

// NoticeState returns the state of one notice (pgx.ErrNoRows when there is none).
func NoticeState(ctx context.Context, db DB, changeID, orderID uuid.UUID) (string, error) {
	var state string
	err := db.QueryRow(ctx, loadNoticeStateSQL, changeID, orderID).Scan(&state)
	return state, err
}

const setNoticeStateSQL = `
UPDATE session_change_notices
SET    state = $3, last_error = $4, updated_at = now()
WHERE  change_id = $1 AND order_id = $2`

// maxStoredError bounds what a failed send leaves in last_error.
const maxStoredError = 200

// SetNoticeState records the outcome of a notice. lastError is scrubbed of
// anything that looks like an e-mail address and truncated before it is
// stored, so a provider's complaint never leaves a buyer's address in the
// journal.
func SetNoticeState(ctx context.Context, db DB, changeID, orderID uuid.UUID, state, lastError string) error {
	var errText *string
	if s := ScrubError(lastError); s != "" {
		errText = &s
	}
	_, err := db.Exec(ctx, setNoticeStateSQL, changeID, orderID, state, errText)
	return err
}

// ScrubError removes e-mail addresses from an error text and bounds its length.
func ScrubError(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	words := strings.Split(s, " ")
	for i, w := range words {
		if strings.Contains(w, "@") {
			words[i] = "[address]"
		}
	}
	s = strings.Join(words, " ")
	if r := []rune(s); len(r) > maxStoredError {
		s = string(r[:maxStoredError])
	}
	return s
}

// OrderRecipient is what the letter worker needs to address one order.
type OrderRecipient struct {
	Email      string
	Name       string
	LocaleHint string
	OrderID    uuid.UUID
	SystemID   int64
	SessionID  uuid.UUID
	Status     string
}

const orderRecipientSQL = `
SELECT o.id, o.system_id, o.session_id, o.status,
       COALESCE(NULLIF(btrim(o.buyer_email), ''),
                (SELECT btrim(t2.holder_email) FROM tickets t2
                  WHERE t2.order_id = o.id AND t2.holder_email IS NOT NULL
                    AND btrim(t2.holder_email) <> '' LIMIT 1),
                ''),
       COALESCE(btrim(o.buyer_name), ''),
       COALESCE(cs.buyer_locale, '')
FROM   orders o
LEFT   JOIN checkout_sessions cs ON cs.id = o.checkout_session_id
WHERE  o.id = $1`

// LoadOrderRecipient reads the address, name and language of an order's buyer.
func LoadOrderRecipient(ctx context.Context, db DB, orderID uuid.UUID) (OrderRecipient, error) {
	var r OrderRecipient
	err := db.QueryRow(ctx, orderRecipientSQL, orderID).
		Scan(&r.OrderID, &r.SystemID, &r.SessionID, &r.Status, &r.Email, &r.Name, &r.LocaleHint)
	return r, err
}

const orderActiveTicketsSQL = `
SELECT t.id
FROM   tickets t
WHERE  t.order_id = $1 AND t.session_id = $2 AND t.status = 'active'
ORDER  BY t.issued_at, t.id`

// ActiveTickets lists the order's tickets for the session that are still valid.
func ActiveTickets(ctx context.Context, db DB, orderID, sessionID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := db.Query(ctx, orderActiveTicketsSQL, orderID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("sessionchange: active tickets: %w", err)
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
