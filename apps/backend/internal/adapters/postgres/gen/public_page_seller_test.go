package gen

import (
	"strings"
	"testing"
)

// The hosted page publishes a contact only from the channel's public_contact.
// organizations.contact_email / contact_phone may hold a private address, so
// the seller query must never select them.
func TestGetHostedPageSellerQuery_NeverReadsOrganizationContact(t *testing.T) {
	t.Parallel()
	for _, banned := range []string{"contact_email", "contact_phone", "sender_email", "website_url"} {
		if strings.Contains(getHostedPageSeller, banned) {
			t.Fatalf("seller query selects %s", banned)
		}
	}
	if !strings.Contains(getHostedPageSeller, "public_contact") {
		t.Fatal("seller query must read the channel's public_contact")
	}
	if !strings.Contains(getHostedPageSeller, "sc.org_id = o.id") {
		t.Fatal("seller query must join the channel of the SAME organization")
	}
}

// The contact form's fallback recipient is asked for as a boolean only, so
// the page never learns the organization's address.
func TestOrgHasContactEmailQuery_AnswersABooleanOnly(t *testing.T) {
	t.Parallel()
	q := strings.Join(strings.Fields(orgHasContactEmail), " ")
	if !strings.Contains(q, "SELECT COALESCE(btrim(contact_email), '') <> '' FROM organizations") {
		t.Fatalf("the query must select a comparison, never the column: %s", q)
	}
}
