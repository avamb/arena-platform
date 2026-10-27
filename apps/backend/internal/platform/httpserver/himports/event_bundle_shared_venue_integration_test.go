//go:build integration

package himports

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// A venue the platform verified is open to every organization: an event center
// of another organization picks it by arenaVenueId and the import uses it as it
// is. Unverified, it is still refused, and taking the verification back stops
// NEW dates only.
func TestEventBundle_ArenaVenueIDOfAVerifiedVenueOfAnotherOrg(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	owner := newBundle525Fixture(t, ctx, pool)
	defer owner.cleanup()
	guest := newBundle525Fixture(t, ctx, pool)
	defer guest.cleanup()

	h := newBundle525Handler(t, pool)
	rec, _ := owner.call(h, owner.payload())
	if rec.Code != http.StatusOK {
		t.Fatalf("owner bundle: %d %s", rec.Code, rec.Body.String())
	}
	var venueID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM venues WHERE org_id = $1`, owner.orgID).Scan(&venueID); err != nil {
		t.Fatalf("read venue: %v", err)
	}
	var address string
	if err := pool.QueryRow(ctx, `SELECT coalesce(address, '') FROM venues WHERE id = $1`, venueID).Scan(&address); err != nil {
		t.Fatal(err)
	}

	pick := func(label string) *httpResult {
		p := guest.payload()
		p.ExternalRef = guest.externalRef + ":" + label
		p.Action.ActionName = "Guest " + label
		p.Venue.ArenaVenueID = venueID.String()
		p.Venue.VenueName = "Typed over the pick"
		p.Venue.Address = "Somewhere else 1"
		p.Venue.Timezone = ""
		rec, res := guest.call(h, p)
		return &httpResult{code: rec.Code, body: rec.Body.String(), compatVenue: res.CompatIDs.VenueID}
	}

	// not verified yet: another organization's venue is refused
	if r := pick("before"); r.code != http.StatusUnprocessableEntity || !strings.Contains(r.body, "import.invalid_venue") {
		t.Fatalf("unverified: %d %s", r.code, r.body)
	}

	if _, err := pool.Exec(ctx, `UPDATE venues SET review_status = 'verified', verified_at = now() WHERE id = $1`, venueID); err != nil {
		t.Fatal(err)
	}
	r := pick("verified")
	if r.code != http.StatusOK {
		t.Fatalf("verified: %d %s", r.code, r.body)
	}
	var sessions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions s JOIN events e ON e.id = s.event_id
		WHERE e.org_id = $1 AND s.venue_id = $2`, guest.orgID, venueID).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("guest sessions at the venue = %d (%v)", sessions, err)
	}
	var after string
	if err := pool.QueryRow(ctx, `SELECT coalesce(address, '') FROM venues WHERE id = $1`, venueID).Scan(&after); err != nil || after != address {
		t.Fatalf("a pick edited the owner's venue: %q -> %q (%v)", address, after, err)
	}

	// verification taken back: no new dates, the existing one stays
	if _, err := pool.Exec(ctx, `UPDATE venues SET review_status = 'unverified', verified_at = NULL WHERE id = $1`, venueID); err != nil {
		t.Fatal(err)
	}
	if r := pick("after"); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("after taking the verification back: %d %s", r.code, r.body)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions s JOIN events e ON e.id = s.event_id
		WHERE e.org_id = $1 AND s.venue_id = $2`, guest.orgID, venueID).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("guest sessions at the venue = %d (%v)", sessions, err)
	}
}

type httpResult struct {
	code        int
	body        string
	compatVenue int64
}
