package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ident is one (kind, value) pair of analytics.identities.
type ident struct{ kind, value string }

// linkReq says that all its identities (and the CRM contact and the user, when
// set) belong to ONE person.
type linkReq struct {
	idents   []ident
	twentyID string
	userID   *uuid.UUID
	at       time.Time
}

type linkResult struct {
	personID uuid.UUID
	created  bool
	merged   int
}

type personRow struct {
	id        uuid.UUID
	twenty    *string
	userID    *uuid.UUID
	first     Touch
	last      Touch
	createdAt time.Time
}

// touchJSON is the stored shape of people.first_touch / last_touch.
type touchJSON struct {
	Source   string `json:"source,omitempty"`
	Medium   string `json:"medium,omitempty"`
	Campaign string `json:"campaign,omitempty"`
	RefHost  string `json:"ref_host,omitempty"`
	Kind     string `json:"kind,omitempty"`
	At       string `json:"at,omitempty"`
}

func encodeTouch(t Touch) []byte {
	if t.Empty() {
		return []byte(`{}`)
	}
	j := touchJSON{Source: t.Source, Medium: t.Medium, Campaign: t.Campaign, RefHost: t.RefHost, Kind: t.Kind}
	if !t.At.IsZero() {
		j.At = t.At.UTC().Truncate(time.Second).Format(time.RFC3339)
	}
	b, _ := json.Marshal(j)
	return b
}

func decodeTouch(raw []byte) Touch {
	var j touchJSON
	if len(raw) == 0 || json.Unmarshal(raw, &j) != nil {
		return Touch{}
	}
	t := Touch{Source: j.Source, Medium: j.Medium, Campaign: j.Campaign, RefHost: j.RefHost, Kind: j.Kind}
	if j.At != "" {
		t.At, _ = time.Parse(time.RFC3339, j.At)
	}
	return t
}

// linkIdentities makes req's identities one person: it finds every person that
// already holds one of the identities, the CRM contact or the user, merges them
// into the OLDEST (identities move to it, the younger rows are deleted), then
// records all identities on it. With no person found, a new one is created.
// Everything runs on tx, so a merge is atomic.
func linkIdentities(ctx context.Context, tx pgx.Tx, req linkReq) (linkResult, error) {
	kinds := make([]string, 0, len(req.idents))
	values := make([]string, 0, len(req.idents))
	for _, id := range req.idents {
		kinds = append(kinds, id.kind)
		values = append(values, id.value)
	}
	var userArg any
	if req.userID != nil {
		userArg = *req.userID
	}
	rows, err := tx.Query(ctx, `
SELECT p.id, p.twenty_person_id, p.user_id, p.first_touch, p.last_touch, p.created_at
  FROM analytics.people p
 WHERE p.id IN (SELECT person_id FROM analytics.identities
                 WHERE (kind, value) IN (SELECT * FROM unnest($1::text[], $2::text[])))
    OR ($3::text <> '' AND p.twenty_person_id = $3::text)
    OR ($4::uuid IS NOT NULL AND p.user_id = $4::uuid)
 ORDER BY p.created_at, p.id`, kinds, values, req.twentyID, userArg)
	if err != nil {
		return linkResult{}, fmt.Errorf("analytics: find people: %w", err)
	}
	var found []personRow
	for rows.Next() {
		var p personRow
		var ft, lt []byte
		if err := rows.Scan(&p.id, &p.twenty, &p.userID, &ft, &lt, &p.createdAt); err != nil {
			rows.Close()
			return linkResult{}, fmt.Errorf("analytics: scan person: %w", err)
		}
		p.first, p.last = decodeTouch(ft), decodeTouch(lt)
		found = append(found, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return linkResult{}, err
	}

	res := linkResult{}
	at := req.at
	if at.IsZero() {
		at = time.Now()
	}
	var target personRow
	if len(found) == 0 {
		target = personRow{id: uuid.New(), createdAt: at}
		if _, err := tx.Exec(ctx,
			`INSERT INTO analytics.people (id, created_at) VALUES ($1, $2)`, target.id, at); err != nil {
			return linkResult{}, fmt.Errorf("analytics: create person: %w", err)
		}
		res.created = true
	} else {
		target = found[0]
	}
	res.personID = target.id

	// Attributes of the final person: its own first, then the younger ones'
	// (the CRM contact and the user are unique, so a younger person's value is
	// moved only after it was released), then the request's.
	twenty, userID := target.twenty, target.userID
	first, last := target.first, target.last
	for _, other := range found[min(1, len(found)):] {
		if twenty == nil && other.twenty != nil {
			twenty = other.twenty
		}
		if userID == nil && other.userID != nil {
			userID = other.userID
		}
		first, last = EarlierTouch(first, other.first), LaterTouch(last, other.last)
		if _, err := tx.Exec(ctx,
			`UPDATE analytics.people SET twenty_person_id = NULL, user_id = NULL WHERE id = $1`, other.id); err != nil {
			return linkResult{}, fmt.Errorf("analytics: release person attributes: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE analytics.identities SET person_id = $1 WHERE person_id = $2`, target.id, other.id); err != nil {
			return linkResult{}, fmt.Errorf("analytics: move identities: %w", err)
		}
		// Sessions and the profile go with the row (ON DELETE CASCADE) and are
		// rebuilt for the survivor in the same run.
		if _, err := tx.Exec(ctx, `DELETE FROM analytics.people WHERE id = $1`, other.id); err != nil {
			return linkResult{}, fmt.Errorf("analytics: delete merged person: %w", err)
		}
		res.merged++
	}
	if twenty == nil && req.twentyID != "" {
		t := req.twentyID
		twenty = &t
	}
	if userID == nil && req.userID != nil {
		userID = req.userID
	}
	var twentyArg, userArg2 any
	if twenty != nil {
		twentyArg = *twenty
	}
	if userID != nil {
		userArg2 = *userID
	}
	if _, err := tx.Exec(ctx, `
UPDATE analytics.people
   SET twenty_person_id = $2, user_id = $3, first_touch = $4, last_touch = $5
 WHERE id = $1`, target.id, twentyArg, userArg2, encodeTouch(first), encodeTouch(last)); err != nil {
		return linkResult{}, fmt.Errorf("analytics: update person: %w", err)
	}
	for _, id := range req.idents {
		if _, err := tx.Exec(ctx, `
INSERT INTO analytics.identities (kind, value, person_id, seen_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (kind, value) DO UPDATE SET seen_at = EXCLUDED.seen_at
 WHERE analytics.identities.seen_at < EXCLUDED.seen_at`, id.kind, id.value, target.id, at); err != nil {
			return linkResult{}, fmt.Errorf("analytics: record identity: %w", err)
		}
	}
	return res, nil
}
