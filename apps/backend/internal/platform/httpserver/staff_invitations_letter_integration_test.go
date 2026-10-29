//go:build integration

// The letter state on the staff invitation lists (spec 46, section 3.4):
// letter {status, sent_at, last_error_code} on the session list and on the
// organization-wide list, folded from the delivery_jobs of the order's
// tickets, through the real router and the real ticket issuance.
package httpserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// inv07Letters reads the letter of one order from both lists.
func inv07Letters(t *testing.T, f *inv02Fx, orderID uuid.UUID) (session, org map[string]any, rawSession, rawOrg any) {
	t.Helper()
	orgPath := "/v1/organizations/" + f.o.org.String()
	for _, c := range []struct {
		path string
		dst  *map[string]any
		raw  *any
	}{
		{orgPath + "/sessions/" + f.session.String() + "/invitations", &session, &rawSession},
		{orgPath + "/invitations?state=all&session_id=" + f.session.String(), &org, &rawOrg},
	} {
		code, body := f.do(f.manager, http.MethodGet, c.path, "")
		if code != http.StatusOK {
			t.Fatalf("list %s: %d %s", c.path, code, body["_raw"])
		}
		found := false
		for _, it := range body["items"].([]any) {
			m := it.(map[string]any)
			if m["order_id"] == orderID.String() {
				found = true
				v, present := m["letter"]
				if !present {
					t.Fatalf("list %s: item has no letter key: %s", c.path, body["_raw"])
				}
				if v == nil {
					*c.dst = nil
				} else {
					*c.dst = v.(map[string]any)
				}
			}
		}
		if !found {
			t.Fatalf("list %s: order %s missing: %s", c.path, orderID, body["_raw"])
		}
		*c.raw = body["_raw"]
	}
	return session, org, rawSession, rawOrg
}

func TestINV07_ListLetter_FoldsTheDeliveryJobsOfTheOrder(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	guest := "letter-" + uuid.NewString()[:8] + "@example.test"

	// A pdf_only invitation queued no letter: null on both lists.
	pdfOrder, _ := inv03Issue(t, f, []string{"A|1|8"}, "pdf_only", "")
	s, o, _, _ := inv07Letters(t, f, pdfOrder)
	if s != nil || o != nil {
		t.Fatalf("pdf_only letter = %v / %v, want null", s, o)
	}

	// guest_email, two tickets: the freshly issued jobs are pending.
	order, cs := inv03Issue(t, f, []string{"A|1|1", "A|1|5"}, "guest_email", guest)
	s, o, rs, ro := inv07Letters(t, f, order)
	for name, l := range map[string]map[string]any{"session": s, "org": o} {
		if l == nil || l["status"] != "pending" || l["sent_at"] != nil || l["last_error_code"] != nil {
			t.Fatalf("%s list, fresh letter = %v, want pending: %v %v", name, l, rs, ro)
		}
	}

	// One job sent, the other still pending: still pending.
	if _, err := f.pool.Exec(ctx, `
		UPDATE delivery_jobs SET status = 'sent', sent_at = now()
		WHERE id = (SELECT dj.id FROM delivery_jobs dj JOIN tickets t ON t.id = dj.ticket_id
		            WHERE t.checkout_session_id = $1 ORDER BY dj.id LIMIT 1)`, cs); err != nil {
		t.Fatalf("mark one sent: %v", err)
	}
	if s, _, _, _ = inv07Letters(t, f, order); s == nil || s["status"] != "pending" {
		t.Fatalf("half sent letter = %v, want pending", s)
	}

	// The other job failed: failed, with a category and never the raw error.
	if _, err := f.pool.Exec(ctx, `
		UPDATE delivery_jobs SET status = 'failed', last_error = 'smtp 550 mailbox unavailable for secret@example.test'
		WHERE status = 'pending' AND ticket_id IN (SELECT id FROM tickets WHERE checkout_session_id = $1)`, cs); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	s, o, rs, ro = inv07Letters(t, f, order)
	for name, l := range map[string]map[string]any{"session": s, "org": o} {
		if l == nil || l["status"] != "failed" || l["last_error_code"] != "address_rejected" {
			t.Fatalf("%s list, failed letter = %v", name, l)
		}
	}
	for _, raw := range []any{rs, ro} {
		if str, _ := raw.(string); str != "" && (strings.Contains(str, "secret@example.test") || strings.Contains(str, "mailbox unavailable")) {
			t.Fatalf("raw error text leaked: %s", str)
		}
	}

	// A resend that finally went out: every job sent, sent_at is the latest.
	if _, err := f.pool.Exec(ctx, `
		UPDATE delivery_jobs SET status = 'sent', sent_at = now(), last_error = NULL
		WHERE ticket_id IN (SELECT id FROM tickets WHERE checkout_session_id = $1)`, cs); err != nil {
		t.Fatalf("mark all sent: %v", err)
	}
	s, o, _, _ = inv07Letters(t, f, order)
	for name, l := range map[string]map[string]any{"session": s, "org": o} {
		if l == nil || l["status"] != "sent" || l["sent_at"] == nil || l["last_error_code"] != nil {
			t.Fatalf("%s list, sent letter = %v", name, l)
		}
	}

	// Skipped and disabled mirror the job.
	if _, err := f.pool.Exec(ctx, `
		UPDATE delivery_jobs SET status = 'skipped', sent_at = NULL, last_error = 'no email address available at delivery time'
		WHERE ticket_id IN (SELECT id FROM tickets WHERE checkout_session_id = $1)`, cs); err != nil {
		t.Fatalf("mark skipped: %v", err)
	}
	if s, _, _, _ = inv07Letters(t, f, order); s == nil || s["status"] != "skipped" || s["last_error_code"] != "no_recipient" {
		t.Fatalf("skipped letter = %v", s)
	}
	if _, err := f.pool.Exec(ctx, `
		UPDATE delivery_jobs SET status = 'disabled', last_error = 'no production email sender configured'
		WHERE ticket_id IN (SELECT id FROM tickets WHERE checkout_session_id = $1)`, cs); err != nil {
		t.Fatalf("mark disabled: %v", err)
	}
	if s, _, _, _ = inv07Letters(t, f, order); s == nil || s["status"] != "disabled" || s["last_error_code"] != "sender_disabled" {
		t.Fatalf("disabled letter = %v", s)
	}
}
