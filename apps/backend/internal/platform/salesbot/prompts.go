package salesbot

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PromptTTL is how long a question of the bot waits for its reply.
const PromptTTL = 30 * time.Minute

// Prompt is a question the bot asked in a delivery group with force_reply:
// the reply that quotes MessageID is the shipment number (Step handed_over)
// or the text of an "other" reason (Step failed).
type Prompt struct {
	ChatID        string
	MessageID     int64
	CardMessageID int64
	OrderID       uuid.UUID
	Step          string
	Locale        string
	AskedBy       int64
	ExpiresAt     time.Time
}

// PromptStore keeps the questions in sales_bot_prompts, so a bot restart does
// not lose them.
type PromptStore interface {
	Save(ctx context.Context, p Prompt) error
	// Get finds the question a reply quotes; ok is false when there is none.
	Get(ctx context.Context, chatID string, messageID int64) (p Prompt, ok bool, err error)
	// Clear forgets the questions about the order in the chat and answers
	// their message ids (the bot deletes those messages).
	Clear(ctx context.Context, chatID string, orderID uuid.UUID) ([]int64, error)
}

// PGPrompts is the production PromptStore.
type PGPrompts struct{ pool *pgxpool.Pool }

// NewPGPrompts wraps a pool.
func NewPGPrompts(pool *pgxpool.Pool) *PGPrompts { return &PGPrompts{pool: pool} }

// Save implements PromptStore; questions expired a day ago are swept on the way.
func (s *PGPrompts) Save(ctx context.Context, p Prompt) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM sales_bot_prompts WHERE expires_at < now() - interval '1 day'`); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sales_bot_prompts (chat_id, message_id, card_message_id, order_id, step, locale, asked_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (chat_id, message_id) DO UPDATE
		   SET card_message_id = EXCLUDED.card_message_id, order_id = EXCLUDED.order_id, step = EXCLUDED.step,
		       locale = EXCLUDED.locale, asked_by = EXCLUDED.asked_by, expires_at = EXCLUDED.expires_at`,
		p.ChatID, p.MessageID, p.CardMessageID, p.OrderID, p.Step, p.Locale, p.AskedBy, p.ExpiresAt)
	return err
}

// Get implements PromptStore.
func (s *PGPrompts) Get(ctx context.Context, chatID string, messageID int64) (Prompt, bool, error) {
	p := Prompt{ChatID: chatID, MessageID: messageID}
	var asked *int64
	err := s.pool.QueryRow(ctx, `
		SELECT card_message_id, order_id, step, locale, asked_by, expires_at
		  FROM sales_bot_prompts WHERE chat_id = $1 AND message_id = $2`, chatID, messageID).
		Scan(&p.CardMessageID, &p.OrderID, &p.Step, &p.Locale, &asked, &p.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Prompt{}, false, nil
	}
	if err != nil {
		return Prompt{}, false, err
	}
	if asked != nil {
		p.AskedBy = *asked
	}
	return p, true, nil
}

// Clear implements PromptStore.
func (s *PGPrompts) Clear(ctx context.Context, chatID string, orderID uuid.UUID) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `DELETE FROM sales_bot_prompts WHERE chat_id = $1 AND order_id = $2 RETURNING message_id`, chatID, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
