package gen

// sample_tickets.sql.go — hand-written wrappers for queries/sample_tickets.sql
// (migration 0119): the sample e-ticket of a session.

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// SessionSampleBarcodeRow is one session_sample_barcodes row.
type SessionSampleBarcodeRow struct {
	SessionID uuid.UUID `json:"session_id"`
	BarcodeID uuid.UUID `json:"barcode_id"`
	EAN13     string    `json:"ean13"`
	CreatedAt time.Time `json:"created_at"`
}

const getSessionSampleBarcode = `-- name: GetSessionSampleBarcode :one
SELECT session_id, barcode_id, ean13, created_at
FROM   session_sample_barcodes
WHERE  session_id = $1`

// GetSessionSampleBarcode returns the session's sample code, or pgx.ErrNoRows
// when none was minted yet.
func (q *Queries) GetSessionSampleBarcode(ctx context.Context, sessionID uuid.UUID) (SessionSampleBarcodeRow, error) {
	row := q.db.QueryRow(ctx, getSessionSampleBarcode, sessionID)
	var r SessionSampleBarcodeRow
	err := row.Scan(&r.SessionID, &r.BarcodeID, &r.EAN13, &r.CreatedAt)
	return r, err
}

const insertSessionSampleBarcode = `-- name: InsertSessionSampleBarcode :execrows
INSERT INTO session_sample_barcodes (session_id, barcode_id, ean13)
VALUES ($1, $2, $3)
ON CONFLICT (session_id) DO NOTHING`

// InsertSessionSampleBarcode records the session's sample code once and
// reports whether this call won; a loser re-reads the winner's code.
func (q *Queries) InsertSessionSampleBarcode(ctx context.Context, sessionID, barcodeID uuid.UUID, ean13 string) (bool, error) {
	tag, err := q.db.Exec(ctx, insertSessionSampleBarcode, sessionID, barcodeID, ean13)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// SampleTicketPresentationRow is what the sample PDF prints.
type SampleTicketPresentationRow struct {
	SessionID              uuid.UUID  `json:"session_id"`
	EventID                uuid.UUID  `json:"event_id"`
	EventName              string     `json:"event_name"`
	StartAt                time.Time  `json:"start_at"`
	VenueName              *string    `json:"venue_name"`
	VenueAddress           *string    `json:"venue_address"`
	VenueCity              *string    `json:"venue_city"`
	VenueTimezone          *string    `json:"venue_timezone"`
	PosterMediaID          *uuid.UUID `json:"poster_media_id"`
	PromoterName           *string    `json:"promoter_name"`
	OrgName                string     `json:"org_name"`
	OrgLocale              string     `json:"org_locale"`
	WebsiteURL             *string    `json:"website_url"`
	LegalName              *string    `json:"legal_name"`
	LegalAddressLine1      *string    `json:"legal_address_line1"`
	LegalAddressLine2      *string    `json:"legal_address_line2"`
	LegalAddressPostalCode *string    `json:"legal_address_postal_code"`
	LegalAddressCity       *string    `json:"legal_address_city"`
	LegalAddressCountry    *string    `json:"legal_address_country"`
	ContactEmail           *string    `json:"contact_email"`
	LogoMediaID            *uuid.UUID `json:"logo_media_id"`
	// DoorsOpenAt is sessions.doors_open_at (migration 0128).
	DoorsOpenAt *time.Time `json:"doors_open_at"`
}

const getSampleTicketPresentation = `-- name: GetSampleTicketPresentation :one
SELECT s.id                                    AS session_id,
       e.id                                    AS event_id,
       e.name                                  AS event_name,
       s.start_at,
       v.name                                  AS venue_name,
       COALESCE(NULLIF(btrim(v.address_line1), ''),
                NULLIF(btrim(v.address),       '')) AS venue_address,
       COALESCE(t_en.value, ci.slug)           AS venue_city,
       v.timezone                              AS venue_timezone,
       COALESCE(s.poster_media_id, e.poster_media_id) AS poster_media_id,
       epr.name                                AS promoter_name,
       o.name                                  AS org_name,
       o.default_locale                        AS org_locale,
       o.website_url, o.legal_name, o.legal_address_line1, o.legal_address_line2,
       o.legal_address_postal_code, o.legal_address_city, o.legal_address_country,
       o.contact_email, o.logo_media_id,
       s.doors_open_at
FROM      sessions s
JOIN      events        e    ON e.id = s.event_id
JOIN      organizations o    ON o.id = e.org_id
LEFT JOIN venues        v    ON v.id = s.venue_id
LEFT JOIN cities        ci   ON ci.id = v.city_id
LEFT JOIN i18n_text     t_en ON t_en.namespace = 'geo.cities'
       AND t_en.key = ci.slug AND t_en.locale = 'en'
LEFT JOIN event_promoters ep  ON ep.event_id = e.id
LEFT JOIN org_promoters   epr ON epr.id = ep.promoter_id
WHERE  s.id = $1
  AND  e.org_id = $2
  AND  s.deleted_at IS NULL
  AND  e.deleted_at IS NULL`

// GetSampleTicketPresentation returns what the session's sample e-ticket
// prints, scoped to the organization; pgx.ErrNoRows means the session is not
// this organization's (or is gone).
func (q *Queries) GetSampleTicketPresentation(ctx context.Context, sessionID, orgID uuid.UUID) (SampleTicketPresentationRow, error) {
	row := q.db.QueryRow(ctx, getSampleTicketPresentation, sessionID, orgID)
	var r SampleTicketPresentationRow
	err := row.Scan(
		&r.SessionID, &r.EventID, &r.EventName, &r.StartAt,
		&r.VenueName, &r.VenueAddress, &r.VenueCity, &r.VenueTimezone,
		&r.PosterMediaID, &r.PromoterName,
		&r.OrgName, &r.OrgLocale,
		&r.WebsiteURL, &r.LegalName, &r.LegalAddressLine1, &r.LegalAddressLine2,
		&r.LegalAddressPostalCode, &r.LegalAddressCity, &r.LegalAddressCountry,
		&r.ContactEmail, &r.LogoMediaID,
		&r.DoorsOpenAt,
	)
	return r, err
}
