package brevo

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Poller persists Brevo's authoritative sender state. A failure deliberately
// leaves the prior state intact: transient Brevo outages must not revoke mail.
type Poller struct {
	pool     *pgxpool.Pool
	client   *Client
	logger   *slog.Logger
	interval time.Duration
}

func NewPoller(pool *pgxpool.Pool, client *Client, logger *slog.Logger, interval time.Duration) *Poller {
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Poller{pool: pool, client: client, logger: logger, interval: interval}
}
func (p *Poller) Run(ctx context.Context) {
	if p == nil || p.pool == nil || p.client == nil || !p.client.Configured() {
		return
	}
	p.poll(ctx)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}
func (p *Poller) poll(ctx context.Context) {
	rows, err := p.pool.Query(ctx, `SELECT id, sender_email FROM organizations WHERE deleted_at IS NULL AND sender_email IS NOT NULL AND sender_verification_status IN ('pending','failed')`)
	if err != nil {
		p.logger.Warn("sender verification query failed", "error", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, email string
		if err := rows.Scan(&id, &email); err != nil {
			continue
		}
		sender, err := p.client.GetSender(ctx, email)
		if err != nil {
			p.logger.Warn("sender verification check failed", "email", email, "error", err)
			continue
		}
		status := "failed"
		if sender.Active {
			status = "verified"
		}
		_, err = p.pool.Exec(ctx, `UPDATE organizations SET sender_verification_status=$2, sender_verified_at=CASE WHEN $2='verified' THEN COALESCE(sender_verified_at, now()) ELSE NULL END, updated_at=now() WHERE id=$1 AND sender_email=$3`, id, status, email)
		if err != nil {
			p.logger.Warn("sender verification update failed", "email", email, "error", err)
		}
	}
}
