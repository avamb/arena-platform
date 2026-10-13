package gen

import (
	"os"
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

// The contact form's private recipients (the organization's contact_email,
// its owner's account e-mail) are asked for as a boolean only, so the page
// never learns an address.
func TestOrgHasPrivateRecipientQuery_AnswersABooleanOnly(t *testing.T) {
	t.Parallel()
	q := strings.Join(strings.Fields(orgHasPrivateRecipient), " ")
	if !strings.Contains(q, "SELECT COALESCE(btrim(o.contact_email), '') <> '' OR EXISTS (SELECT 1 FROM memberships") {
		t.Fatalf("the query must select a comparison, never an address: %s", q)
	}
	for _, banned := range []string{"SELECT u.email", "SELECT o.contact_email", "SELECT contact_email"} {
		if strings.Contains(q, banned) {
			t.Fatalf("the query selects an address (%s): %s", banned, q)
		}
	}
}

// One rule picks the owner who may receive the contact form's messages: the
// page's boolean, the send route's boolean, the letters' boolean and the
// worker's address all build on OwnerRecipientWhereSQL, and the mirrors in
// queries/*.sql repeat it literally. A drift would let the page offer a form
// whose messages the worker cannot deliver, or the reverse.
func TestOwnerRecipientRule_OneCopyEverywhere(t *testing.T) {
	t.Parallel()
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	rule := norm(OwnerRecipientWhereSQL)
	if !strings.Contains(rule, "r.name = 'platform_superadmin'") || !strings.Contains(rule, "NOT EXISTS") {
		t.Fatalf("the rule must leave out platform superadmins: %s", rule)
	}
	for name, q := range map[string]string{
		"OwnerRecipientSQL":      OwnerRecipientSQL,
		"OrgHasPrivateRecipient": orgHasPrivateRecipient,
		"GetContactFormTarget":   getContactFormTarget,
		"GetTicketContactRoute":  getTicketContactRoute,
	} {
		if !strings.Contains(norm(q), rule) {
			t.Errorf("%s does not use OwnerRecipientWhereSQL", name)
		}
	}
	for file, want := range map[string]int{"../queries/seller_profile.sql": 2, "../queries/orgs.sql": 1} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(norm(string(b)), rule); got != want {
			t.Errorf("%s repeats the owner rule %d times, want %d", file, got, want)
		}
	}
}
