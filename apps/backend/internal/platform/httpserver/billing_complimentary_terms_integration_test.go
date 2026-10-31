//go:build integration

// The superadmin switch "invitations free / standard rate" of spec 46
// (owner decision R-7, task INV-07): complimentary_billing on
// POST /v1/admin/billing/accounts/{account_id}/terms. Like every terms row it
// starts in the future, so invitations already issued keep their charge.
package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

func TestBillingAdmin_ComplimentaryBillingSwitch(t *testing.T) {
	e := newBillingAdminEnv(t)
	ctx := context.Background()
	acc := e.account(t)
	path := "/v1/admin/billing/accounts/" + acc.ID.String() + "/terms"
	body := func(from time.Time, comp string) string {
		extra := ""
		if comp != "" {
			extra = fmt.Sprintf(`,"complimentary_billing":%q`, comp)
		}
		return fmt.Sprintf(`{"effective_from":%q,"price_list_code":%q,"plan_kind":"standard","billing_cycle":"monthly","shortfall_policy":"invoice_postpaid"%s}`,
			rfc(from), billing.StandardPriceListCode, extra)
	}
	day := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)

	if c := billingErrCode(e.expect(t, http.StatusUnprocessableEntity, http.MethodPost, path, e.super, "inv07", body(day, "sometimes"))); c != "billing.invalid_terms" {
		t.Errorf("unknown value: %q", c)
	}
	// Only a superadmin with a reason (the route's guard, unchanged).
	e.expect(t, http.StatusForbidden, http.MethodPost, path, e.owner, "inv07", body(day, "standard"))

	out := e.expect(t, http.StatusCreated, http.MethodPost, path, e.super, "inv07 standard", body(day, "standard"))
	if jsonField(t, out, "complimentary_billing") != "standard" {
		t.Fatalf("terms = %s", out)
	}
	// A later row for another reason does not silently switch it back.
	out = e.expect(t, http.StatusCreated, http.MethodPost, path, e.super, "inv07 keep", body(day.Add(24*time.Hour), ""))
	if jsonField(t, out, "complimentary_billing") != "standard" {
		t.Fatalf("terms without the key = %s, want standard kept", out)
	}
	out = e.expect(t, http.StatusCreated, http.MethodPost, path, e.super, "inv07 free", body(day.Add(48*time.Hour), "free"))
	if jsonField(t, out, "complimentary_billing") != "free" {
		t.Fatalf("terms = %s", out)
	}

	// The rule follows the dates: free today, standard from the first new
	// row, free again from the third.
	for _, c := range []struct {
		at   time.Time
		want string
	}{{time.Now().UTC(), "free"}, {day, "standard"}, {day.Add(30 * time.Hour), "standard"}, {day.Add(48 * time.Hour), "free"}} {
		terms, err := billing.TermsAt(ctx, e.q, acc.ID, c.at)
		if err != nil || terms.ComplimentaryBilling() != c.want {
			t.Errorf("at %s: %q %v, want %s", rfc(c.at), terms.ComplimentaryBilling(), err, c.want)
		}
	}
	if n, _, reason := e.auditRows(t, "v1.admin.billing.account_terms.create", acc.ID.String()); n < 3 || reason == "" {
		t.Errorf("audit rows = %d (%q)", n, reason)
	}
}
