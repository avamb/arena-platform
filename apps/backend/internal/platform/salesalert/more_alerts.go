package salesalert

// more_alerts.go — the alerts added after "Серебряное копытце" (migration
// 0197, owner request 2026-11-05): the main category of a date sold out, one
// add-on place kept the date "on sale", and nobody was told.
//
//   - category sold out: a category has no free place and no unpaid hold
//     left while its date goes on (a sell-out of the whole date is told by
//     the sold-out alert instead). A category that hands its places on to a
//     next one in a chain is skipped: it did not sell out, it passed the
//     baton. The row is deleted when places come back.
//   - sales ended: the date's sales end passed. Told once, with the tally.
//   - a state set by hand: "coming soon" or "open again" set by a person or a
//     site is posted with who did it, so the rest of the team knows (a
//     "sold out" by hand is told by the sold-out alert, which names them).
//
// All three go to the organization's own sales chats only. Without a chat
// the organizer still has the bot's own confirmation and the sold-out e-mail.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Notice kinds of sales_alert_notices.
const (
	noticeCategorySoldOut = "category_sold_out"
	noticeSalesEnded      = "sales_ended"
)

// endedHorizon: a sales end older than this is history, never announced.
const endedHorizon = 24 * time.Hour

// manualHorizon: a manual change older than this is history.
const manualHorizon = 24 * time.Hour

type soldCategory struct {
	tierID, sessionID, orgID uuid.UUID
	name                     string
	places                   int64
	sold                     bool
	sessionTold              bool
}

// categoriesSoldOut tells every category that sold its last place.
func (w *Watcher) categoriesSoldOut(ctx context.Context, now time.Time, silent bool) (int, error) {
	rows, err := w.pool.Query(ctx, `
		SELECT tt.id, tt.session_id, e.org_id, tt.name, count(*),
		       count(*) FILTER (WHERE ss.status IN ('available', 'held')) = 0,
		       EXISTS (SELECT 1 FROM session_soldout_notices n WHERE n.session_id = s.id)
		FROM   ticket_tiers tt
		JOIN   sessions s       ON s.id = tt.session_id
		JOIN   events e         ON e.id = s.event_id
		JOIN   session_seats ss ON ss.tier_id = tt.id AND ss.session_id = tt.session_id
		WHERE  tt.deleted_at IS NULL AND s.deleted_at IS NULL AND s.status <> 'cancelled'
		  AND  s.sales_end_at > $1
		  AND  NOT EXISTS (SELECT 1 FROM ticket_tier_chain c WHERE c.tier_id = tt.id)
		GROUP  BY tt.id, tt.session_id, e.org_id, tt.name, s.id`, now)
	if err != nil {
		return 0, err
	}
	var cats []soldCategory
	for rows.Next() {
		var c soldCategory
		if err := rows.Scan(&c.tierID, &c.sessionID, &c.orgID, &c.name, &c.places, &c.sold, &c.sessionTold); err != nil {
			rows.Close()
			return 0, err
		}
		cats = append(cats, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	noticed, err := w.noticed(ctx, noticeCategorySoldOut)
	if err != nil {
		return 0, err
	}
	told := 0
	for _, c := range cats {
		switch {
		case c.sold && !noticed[c.tierID]:
			inserted, err := w.recordNotice(ctx, noticeCategorySoldOut, c.tierID, c.orgID)
			if err != nil {
				return told, err
			}
			// The whole date sold out: the sold-out alert said it already.
			if !inserted || silent || c.sessionTold {
				continue
			}
			told++
			w.announceCategory(ctx, c)
		case !c.sold && noticed[c.tierID]:
			if _, err := w.pool.Exec(ctx, `
				DELETE FROM sales_alert_notices WHERE kind = $1 AND subject_id = $2`, noticeCategorySoldOut, c.tierID); err != nil {
				return told, err
			}
		}
	}
	return told, nil
}

func (w *Watcher) announceCategory(ctx context.Context, c soldCategory) {
	if w.announcer == nil {
		return
	}
	in, err := LoadSessionInfo(ctx, w.pool, c.orgID, c.sessionID)
	if err != nil {
		w.logger.Warn("sales state watch: session info for a sold-out category", "tier_id", c.tierID.String(), "error", err.Error())
		return
	}
	rest, err := w.categoriesLeft(ctx, c.sessionID)
	if err != nil {
		w.logger.Warn("sales state watch: categories left", "session_id", c.sessionID.String(), "error", err.Error())
	}
	if _, _, err := w.announcer.AnnounceToOrg(ctx, c.orgID.String(), CategorySoldOutText(in, c.name, c.places, rest)); err != nil {
		w.logger.Warn("sales state watch: sold-out category chat message", "tier_id", c.tierID.String(), "error", err.Error())
	}
}

// categoriesLeft are the session's categories with free places, in their
// stored order.
func (w *Watcher) categoriesLeft(ctx context.Context, sessionID uuid.UUID) ([]CategoryLeft, error) {
	rows, err := w.pool.Query(ctx, `
		SELECT tt.name, count(*)
		FROM   ticket_tiers tt
		JOIN   session_seats ss ON ss.tier_id = tt.id AND ss.session_id = tt.session_id
		WHERE  tt.session_id = $1 AND tt.deleted_at IS NULL AND ss.status = 'available'
		GROUP  BY tt.id, tt.name, tt.sort_order
		ORDER  BY tt.sort_order, tt.name`, sessionID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (CategoryLeft, error) {
		var c CategoryLeft
		err := r.Scan(&c.Name, &c.Free)
		return c, err
	})
}

// salesEnded tells every session whose sales end passed since the last pass.
func (w *Watcher) salesEnded(ctx context.Context, now time.Time, silent bool) (int, error) {
	rows, err := w.pool.Query(ctx, `
		SELECT s.id, e.org_id
		FROM   sessions s
		JOIN   events e ON e.id = s.event_id
		WHERE  s.deleted_at IS NULL AND s.status <> 'cancelled'
		  AND  s.sales_end_at <= $1 AND s.sales_end_at > $2
		  AND  NOT EXISTS (SELECT 1 FROM sales_alert_notices n
		                    WHERE n.kind = $3 AND n.subject_id = s.id)`,
		now, now.Add(-endedHorizon), noticeSalesEnded)
	if err != nil {
		return 0, err
	}
	type ended struct{ sessionID, orgID uuid.UUID }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ended, error) {
		var e ended
		err := r.Scan(&e.sessionID, &e.orgID)
		return e, err
	})
	if err != nil {
		return 0, err
	}
	told := 0
	for _, e := range list {
		inserted, err := w.recordNotice(ctx, noticeSalesEnded, e.sessionID, e.orgID)
		if err != nil {
			return told, err
		}
		if !inserted || silent || w.announcer == nil {
			continue
		}
		in, err := LoadSessionInfo(ctx, w.pool, e.orgID, e.sessionID)
		if err != nil {
			w.logger.Warn("sales state watch: session info for a sales end", "session_id", e.sessionID.String(), "error", err.Error())
			continue
		}
		if in.AllTotal == 0 {
			continue // a date without places sold nothing to tally
		}
		told++
		if _, _, err := w.announcer.AnnounceToOrg(ctx, e.orgID.String(), SalesEndedText(in)); err != nil {
			w.logger.Warn("sales state watch: sales end chat message", "session_id", e.sessionID.String(), "error", err.Error())
		}
	}
	return told, nil
}

// manualChanges posts the "coming soon" and "open again" changes of the
// journal that the chat has not heard about yet.
func (w *Watcher) manualChanges(ctx context.Context, now time.Time, silent bool) (int, error) {
	rows, err := w.pool.Query(ctx, `
		UPDATE session_sales_state_changes c SET chat_announced_at = $1
		WHERE  c.chat_announced_at IS NULL AND c.created_at > $2
		RETURNING c.session_id, c.org_id, c.new_state, c.actor_name, c.source, c.created_at`,
		now, now.Add(-manualHorizon))
	if err != nil {
		return 0, err
	}
	type change struct {
		sessionID, orgID     uuid.UUID
		state, actor, source string
		at                   time.Time
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (change, error) {
		var c change
		err := r.Scan(&c.sessionID, &c.orgID, &c.state, &c.actor, &c.source, &c.at)
		return c, err
	})
	if err != nil {
		return 0, err
	}
	// Older history (past the horizon) is marked too, so it never waits.
	if _, err := w.pool.Exec(ctx, `
		UPDATE session_sales_state_changes SET chat_announced_at = $1
		WHERE  chat_announced_at IS NULL AND created_at <= $2`, now, now.Add(-manualHorizon)); err != nil {
		return 0, err
	}
	if silent || w.announcer == nil {
		return 0, nil
	}
	told := 0
	for _, c := range list {
		if c.state == "sold_out" {
			continue // the sold-out alert names who set it
		}
		in, err := LoadSessionInfo(ctx, w.pool, c.orgID, c.sessionID)
		if err != nil {
			w.logger.Warn("sales state watch: session info for a manual change", "session_id", c.sessionID.String(), "error", err.Error())
			continue
		}
		told++
		if _, _, err := w.announcer.AnnounceToOrg(ctx, c.orgID.String(), ManualChangeText(in, c.state, c.actor, c.source)); err != nil {
			w.logger.Warn("sales state watch: manual change chat message", "session_id", c.sessionID.String(), "error", err.Error())
		}
	}
	return told, nil
}

func (w *Watcher) noticed(ctx context.Context, kind string) (map[uuid.UUID]bool, error) {
	rows, err := w.pool.Query(ctx, `SELECT subject_id FROM sales_alert_notices WHERE kind = $1`, kind)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// recordNotice claims one alert; false when another pass already did.
func (w *Watcher) recordNotice(ctx context.Context, kind string, subject, orgID uuid.UUID) (bool, error) {
	tag, err := w.pool.Exec(ctx, `
		INSERT INTO sales_alert_notices (kind, subject_id, org_id) VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING`, kind, subject, orgID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
