package marketing

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Retention is how long a consent is kept after it stopped being in force
// (owner decision В-7: while it is in force, plus 3 years after withdrawal).
const Retention = 3 * 365 * 24 * time.Hour

const (
	purgeBatch    = 1000
	maxPurgeBatch = 50
)

// Purge deletes the subscriptions whose evidence is no longer kept, with their
// consent journal (the foreign key cascades):
//
//   - withdrawn ones, 3 years after the withdrawal (a GDPR erasure is a
//     withdrawal too);
//   - pending ones never confirmed, 3 years after the tick: the consent never
//     came into force, but the tick may have earned a discount on an order,
//     and the journal is the proof of it.
//
// A confirmed subscription is in force and is never deleted here. It runs from
// worker_jobs.cleanup (cmd/arena-worker/job_retention.go).
func Purge(ctx context.Context, db DB, now time.Time) (int64, error) {
	return purge(ctx, db, now, nil)
}

// purge is Purge limited to some organizations when orgs is not nil (tests
// share one database with other packages).
func purge(ctx context.Context, db DB, now time.Time, orgs []uuid.UUID) (int64, error) {
	cutoff := now.Add(-Retention)
	var total int64
	for i := 0; i < maxPurgeBatch; i++ {
		tag, err := db.Exec(ctx, `
DELETE FROM marketing_subscriptions
 WHERE id = ANY(ARRAY(
       SELECT id FROM marketing_subscriptions
        WHERE ((status = 'withdrawn' AND withdrawn_at < $1)
            OR (status = 'pending' AND granted_at < $1))
          AND ($3::uuid[] IS NULL OR org_id = ANY($3::uuid[]))
        ORDER BY id
        LIMIT $2))`, cutoff, purgeBatch, orgs)
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
