package marketing

// import.go — the writes of the Brevo / Elementor import (spec 62 section
// 10.1, P-18; cmd/arena-marketing-import). Both land in the SAME tables the
// widget uses, so a suppression written here is honoured by the consent code
// (a later widget grant of a withdrawn address becomes `pending` and needs the
// double opt-in confirmation again) and by the organizer export.
//
//   - A SUPPRESSION is a subscription in status `withdrawn`: it is created
//     when the address is unknown, and an existing pending/confirmed one is
//     withdrawn. It is never undone by an import.
//   - A SUBSCRIBER is a `confirmed` subscription with the consent evidence of
//     the export: the consent date and source go to granted_at/confirmed_at,
//     the subscription's source and the journal. An address that already has
//     a subscription (in any status) is left exactly as it is.
//
// The journal rows carry source `import` (migration 0209). Everything is
// idempotent: a second run finds every address present and writes nothing.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Import sources written to marketing_subscriptions.source.
const (
	SourceImportBrevo     = "brevo_import"
	SourceImportElementor = "elementor_club_form"
	// SourceImport is the marketing_consents.source of every imported row.
	SourceImport = "import"
	// ImportTextVersionPrefix starts the text_version of an imported consent:
	// the consent text the contact saw is unknown, only its source is.
	ImportTextVersionPrefix = "import:"
)

// ImportOutcome says what ImportSuppressionTx / ImportSubscriberTx did.
type ImportOutcome string

// Outcomes.
const (
	// ImportSuppressionNew: no subscription existed; a withdrawn one was created.
	ImportSuppressionNew ImportOutcome = "suppression_new"
	// ImportSuppressionApplied: an active subscription was withdrawn.
	ImportSuppressionApplied ImportOutcome = "suppression_applied"
	// ImportSuppressionUnchanged: already withdrawn.
	ImportSuppressionUnchanged ImportOutcome = "suppression_unchanged"
	// ImportSubscriberNew: a confirmed subscription was created.
	ImportSubscriberNew ImportOutcome = "subscriber_new"
	// ImportSubscriberPresent: the address already has an active subscription.
	ImportSubscriberPresent ImportOutcome = "subscriber_present"
	// ImportSubscriberSuppressed: the address is withdrawn, so it is NOT subscribed.
	ImportSubscriberSuppressed ImportOutcome = "subscriber_suppressed"
)

// ImportContact is one deduplicated address ready to write.
type ImportContact struct {
	OrgID uuid.UUID
	// EmailNormalized is the output of NormalizeEmail.
	EmailNormalized string
	// Email is the address as exported (trimmed).
	Email string
	Name  string
	// Source is SourceImportBrevo or SourceImportElementor.
	Source string
	// ConsentSource is where the consent was given, as the export says
	// (Brevo's "source" column, "club popup"...). It is kept in text_version
	// next to the import source, because the text the contact saw is unknown.
	ConsentSource string
	// At is the consent date of a subscriber, or the moment the suppression
	// became known (zero: now).
	At time.Time
	// Now stamps the import itself (zero: now).
	Now time.Time
}

// version is the text_version of an imported row.
func (c ImportContact) version() string {
	v := ImportTextVersionPrefix + c.Source
	if cs := strings.TrimSpace(c.ConsentSource); cs != "" {
		v += ":" + capRunes(cs, 100)
	}
	return v
}

func (c ImportContact) times() (at, now time.Time) {
	now = c.Now.UTC()
	if c.Now.IsZero() {
		now = time.Now().UTC()
	}
	at = c.At.UTC()
	if c.At.IsZero() {
		at = now
	}
	return at, now
}

// ImportSuppressionTx makes the address a suppression inside the caller's
// transaction.
func ImportSuppressionTx(ctx context.Context, tx DB, c ImportContact) (ImportOutcome, error) {
	at, now := c.times()
	var (
		id      uuid.UUID
		status  string
		version string
	)
	err := tx.QueryRow(ctx, `
		SELECT id, status, text_version FROM marketing_subscriptions
		WHERE org_id = $1 AND email_normalized = $2 FOR UPDATE`, c.OrgID, c.EmailNormalized).
		Scan(&id, &status, &version)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		token, terr := newToken()
		if terr != nil {
			return "", terr
		}
		version = c.version()
		if err := tx.QueryRow(ctx, `
			INSERT INTO marketing_subscriptions
			    (org_id, email, email_normalized, buyer_name, locale, status, text_version, source,
			     page_url, granted_at, withdrawn_at, unsubscribe_token, created_at, updated_at)
			VALUES ($1, $2, $3, NULLIF($4, ''), '', 'withdrawn', $5, $6, '', $7, $8, $9, $8, $8)
			RETURNING id`,
			c.OrgID, c.Email, c.EmailNormalized, capRunes(strings.TrimSpace(c.Name), maxName),
			version, c.Source, at, now, token).Scan(&id); err != nil {
			return "", err
		}
		if err := journal(ctx, tx, id, c.OrgID, "withdrawn", version, "", SourceImport, nil, "", "", now); err != nil {
			return "", err
		}
		return ImportSuppressionNew, nil
	case err != nil:
		return "", err
	case status == StatusWithdrawn:
		return ImportSuppressionUnchanged, nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE marketing_subscriptions
		SET    status = 'withdrawn', withdrawn_at = $2, confirm_token_hash = NULL, updated_at = $2
		WHERE  id = $1`, id, now); err != nil {
		return "", err
	}
	if err := journal(ctx, tx, id, c.OrgID, "withdrawn", version, "", SourceImport, nil, "", "", now); err != nil {
		return "", err
	}
	return ImportSuppressionApplied, nil
}

// ImportSubscriberTx subscribes the address with the consent evidence of the
// export, unless it already has a subscription of any kind. A withdrawn one
// (this or an earlier import, an unsubscribe) is a suppression and wins.
func ImportSubscriberTx(ctx context.Context, tx DB, c ImportContact) (ImportOutcome, error) {
	at, now := c.times()
	var status string
	err := tx.QueryRow(ctx, `
		SELECT status FROM marketing_subscriptions
		WHERE org_id = $1 AND email_normalized = $2 FOR UPDATE`, c.OrgID, c.EmailNormalized).Scan(&status)
	switch {
	case err == nil && status == StatusWithdrawn:
		return ImportSubscriberSuppressed, nil
	case err == nil:
		return ImportSubscriberPresent, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return "", err
	}
	token, terr := newToken()
	if terr != nil {
		return "", terr
	}
	version := c.version()
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO marketing_subscriptions
		    (org_id, email, email_normalized, buyer_name, locale, status, text_version, source,
		     page_url, granted_at, confirmed_at, unsubscribe_token, created_at, updated_at)
		VALUES ($1, $2, $3, NULLIF($4, ''), '', 'confirmed', $5, $6, '', $7, $7, $8, $9, $9)
		RETURNING id`,
		c.OrgID, c.Email, c.EmailNormalized, capRunes(strings.TrimSpace(c.Name), maxName),
		version, c.Source, at, token, now).Scan(&id); err != nil {
		return "", err
	}
	// The journal keeps the consent's own date (the evidence), the import's
	// moment is on the subscription row.
	if err := journal(ctx, tx, id, c.OrgID, "granted", version, "", SourceImport, nil, "", "", at); err != nil {
		return "", err
	}
	if err := journal(ctx, tx, id, c.OrgID, "confirmed", version, "", SourceImport, nil, "", "", at); err != nil {
		return "", err
	}
	return ImportSubscriberNew, nil
}
