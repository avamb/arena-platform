package analytics

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// loadMilestones derives a person's milestones from the business tables.
// It is READ-ONLY on them (spec 50 section 1, decision 3): nothing in a money
// or onboarding path writes analytics, and nothing here writes back.
//
// The person's applications are the ones whose e-mail hashes to one of their
// email_sha256 identities. Their organizations are the organizations an
// approved application created plus those where the user is an active owner
// (membership role org_admin). A manager invited later is not an owner, so a
// sale of the organization is not credited to them.
//
// Every milestone but gmv_30d is a time; gmv_30d maps a currency to the
// minor units paid in the last 30 UTC days.
func loadMilestones(ctx context.Context, tx pgx.Tx, personID uuid.UUID, userID *uuid.UUID,
	now time.Time) (map[string]time.Time, map[string]int64, error) {
	ms := map[string]time.Time{}
	put := func(name string, t *time.Time) {
		if t != nil && !t.IsZero() {
			ms[name] = t.UTC().Truncate(time.Second)
		}
	}

	hashes, err := emailHashes(ctx, tx, personID)
	if err != nil {
		return nil, nil, err
	}
	orgSet := map[uuid.UUID]bool{}

	if len(hashes) > 0 {
		var started, submitted, approved, rejected *time.Time
		var appOrgs []string
		err := tx.QueryRow(ctx, `
SELECT min(created_at), min(submitted_at),
       min(reviewed_at) FILTER (WHERE status = 'approved'),
       min(reviewed_at) FILTER (WHERE status = 'rejected'),
       COALESCE(array_agg(org_id::text) FILTER (WHERE status = 'approved' AND org_id IS NOT NULL), '{}'::text[])
  FROM onboarding_applications
 WHERE purged_at IS NULL
   AND encode(sha256(convert_to(applicant_email, 'UTF8')), 'hex') = ANY($1)`, hashes).
			Scan(&started, &submitted, &approved, &rejected, &appOrgs)
		if err != nil {
			return nil, nil, fmt.Errorf("analytics: application milestones: %w", err)
		}
		put(MsApplicationStarted, started)
		put(MsApplicationSubmitted, submitted)
		put(MsApplicationApproved, approved)
		put(MsApplicationRejected, rejected)
		for _, o := range appOrgs {
			if id, perr := uuid.Parse(o); perr == nil {
				orgSet[id] = true
			}
		}
	}
	if userID != nil {
		rows, err := tx.Query(ctx, `
SELECT org_id FROM memberships WHERE user_id = $1 AND role = 'org_admin' AND status = 'active'`, *userID)
		if err != nil {
			return nil, nil, fmt.Errorf("analytics: owner organizations: %w", err)
		}
		for rows.Next() {
			var o uuid.UUID
			if err := rows.Scan(&o); err != nil {
				rows.Close()
				return nil, nil, err
			}
			orgSet[o] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
	}
	if len(orgSet) == 0 {
		return ms, nil, nil
	}
	orgs := make([]string, 0, len(orgSet))
	for o := range orgSet {
		orgs = append(orgs, o.String())
	}

	var orgCreated, payment, evCreated, evPublished, firstSale, sales100, invited *time.Time
	var owner any
	if userID != nil {
		owner = *userID
	}
	err = tx.QueryRow(ctx, `
SELECT
  (SELECT min(created_at) FROM organizations WHERE id = ANY($1::uuid[])),
  (SELECT min(COALESCE(verified_at, updated_at)) FROM payment_provider_configs
    WHERE org_id = ANY($1::uuid[]) AND is_active AND deleted_at IS NULL AND verification_status = 'ok'),
  (SELECT min(created_at) FROM events WHERE org_id = ANY($1::uuid[]) AND deleted_at IS NULL),
  (SELECT COALESCE(
      (SELECT min(p.published_at) FROM event_publications p JOIN events ev ON ev.id = p.event_id
        WHERE ev.org_id = ANY($1::uuid[]) AND ev.deleted_at IS NULL),
      (SELECT min(updated_at) FROM events
        WHERE org_id = ANY($1::uuid[]) AND deleted_at IS NULL AND status = 'published'))),
  (SELECT min(paid_at) FROM orders
    WHERE org_id = ANY($1::uuid[]) AND paid_at IS NOT NULL AND total > 0 AND source <> 'complimentary'
      AND status IN ('paid', 'partially_refunded', 'refunded')),
  (SELECT paid_at FROM orders
    WHERE org_id = ANY($1::uuid[]) AND paid_at IS NOT NULL AND total > 0 AND source <> 'complimentary'
      AND status IN ('paid', 'partially_refunded', 'refunded')
    ORDER BY paid_at, id OFFSET 99 LIMIT 1),
  LEAST(
    (SELECT min(created_at) FROM bot_invitations
      WHERE org_id = ANY($1::uuid[]) AND revoked_at IS NULL AND ($2::uuid IS NULL OR user_id <> $2::uuid)),
    (SELECT min(joined_at) FROM memberships
      WHERE org_id = ANY($1::uuid[]) AND status = 'active' AND role IN ('organizer', 'agent', 'org_admin')
        AND ($2::uuid IS NULL OR user_id <> $2::uuid)))`, orgs, owner).
		Scan(&orgCreated, &payment, &evCreated, &evPublished, &firstSale, &sales100, &invited)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, fmt.Errorf("analytics: organization milestones: %w", err)
	}
	put(MsOrgCreated, orgCreated)
	put(MsPaymentConnected, payment)
	put(MsFirstEventCreated, evCreated)
	put(MsFirstEventPublished, evPublished)
	put(MsFirstSale, firstSale)
	put(MsSales100, sales100)
	put(MsMemberInvited, invited)

	// gmv_30d: gross paid in the last 30 UTC days, per currency, minor units.
	cut := TruncateDay(now).AddDate(0, 0, -30)
	rows, err := tx.Query(ctx, `
SELECT currency, sum(total)::bigint FROM orders
 WHERE org_id = ANY($1::uuid[]) AND paid_at >= $2 AND total > 0 AND source <> 'complimentary'
   AND status IN ('paid', 'partially_refunded', 'refunded')
 GROUP BY currency`, orgs, cut)
	if err != nil {
		return nil, nil, fmt.Errorf("analytics: gmv: %w", err)
	}
	defer rows.Close()
	gmv := map[string]int64{}
	for rows.Next() {
		var cur string
		var sum int64
		if err := rows.Scan(&cur, &sum); err != nil {
			return nil, nil, err
		}
		gmv[cur] = sum
	}
	return ms, gmv, rows.Err()
}

func emailHashes(ctx context.Context, tx pgx.Tx, personID uuid.UUID) ([]string, error) {
	rows, err := tx.Query(ctx,
		`SELECT value FROM analytics.identities WHERE person_id = $1 AND kind = $2`, personID, IdentityEmailSHA256)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
